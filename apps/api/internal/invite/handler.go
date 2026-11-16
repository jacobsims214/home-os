package invite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/auth"
	"home-os/api/internal/config"
	"home-os/api/internal/dex"
	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
	"home-os/api/pkg/smtp"
)

type Handler struct {
	repo     *Repo
	cfg      *config.Config
	smtp     *smtp.Client
	verifier *auth.Verifier
	dex      PasswordStore
	// hashPassword is auth.HashPassword (bcrypt). WithPasswordHasher swaps
	// it so tests can prove invalid signups never reach bcrypt.
	hashPassword func(password string) (string, error)
}

// PasswordStore creates a password entry in Dex's local password database.
// *dex.Client implements it. The hash is bcrypt; Dex does not hash. It must
// return dex.ErrPasswordExists, never overwrite, when the email already has a
// password.
type PasswordStore interface {
	CreatePassword(ctx context.Context, email, hash, userID string) error
}

func NewHandler(repo *Repo, cfg *config.Config, smtpClient *smtp.Client) *Handler {
	return &Handler{repo: repo, cfg: cfg, smtp: smtpClient, hashPassword: auth.HashPassword}
}

// WithPasswordHasher replaces the bcrypt hasher signup uses. It exists for
// tests (counting calls, or skipping bcrypt's cost); production keeps the
// default auth.HashPassword.
func (h *Handler) WithPasswordHasher(hash func(password string) (string, error)) *Handler {
	h.hashPassword = hash
	return h
}

// WithVerifier lets AcceptInvite verify the caller's Dex token itself, so the
// accept route can be served outside RequireAuth to users who are not yet
// members of any household.
func (h *Handler) WithVerifier(v *auth.Verifier) *Handler {
	h.verifier = v
	return h
}

// WithDex enables new-user signup through an invite link: the invitee's
// password is stored in Dex so they can sign in there afterwards. Without it,
// signup answers 503 and only people who already have a Dex login can accept.
// Pass a nil interface (not a nil *dex.Client) when Dex is unavailable.
func (h *Handler) WithDex(d PasswordStore) *Handler {
	h.dex = d
	return h
}

// inviteView is an invitation as the API returns it to household managers:
// the stored fields plus accept_url, the shareable link the inviter copies
// and sends however they like (Home OS does not need SMTP to invite).
type inviteView struct {
	Invitation
	AcceptURL string `json:"accept_url,omitempty"`
}

// acceptURL is the UI page that accepts the invitation with token.
func (h *Handler) acceptURL(token string) string {
	return strings.TrimRight(h.cfg.PublicURL, "/") + "/invites/accept?token=" + url.QueryEscape(token)
}

// validRoles reports whether role is a membership_role enum value
// (migration 001). It is checked before the invitation is stored.
func validRoles(role string) bool { return auth.ValidRole(role) }

// canInvite reports whether a member with inviterRole may invite someone as
// invitedRole: only owner and family_manager invite, and a family_manager
// may not invite an owner (see auth.CanGrantRole).
func canInvite(inviterRole, invitedRole string) bool {
	return auth.CanGrantRole(inviterRole, invitedRole)
}

// callerManagerRole returns the caller's user ID and role in the household
// the request resolved to (X-Household-ID aware), re-read from memberships.
// It writes 400/403/500 and returns ok=false unless the caller is an owner or
// family_manager there.
func (h *Handler) callerManagerRole(w http.ResponseWriter, r *http.Request, claims *auth.Claims) (uuid.UUID, string, bool) {
	userID, err := uuid.Parse(claims.UserID)
	if err != nil {
		apierr.Forbidden(w, "only owners and family managers can manage invitations")
		return uuid.Nil, "", false
	}
	if _, err := uuid.Parse(claims.HouseholdID); err != nil {
		apierr.Forbidden(w, "only owners and family managers can manage invitations")
		return uuid.Nil, "", false
	}
	role, err := h.repo.MemberRole(r.Context(), claims.HouseholdID, userID)
	if err != nil {
		apierr.InternalError(w, err)
		return uuid.Nil, "", false
	}
	if !auth.CanManageMembers(role) {
		slog.Warn("invite: refused non-manager", "user_id", userID, "household_id", claims.HouseholdID, "role", role)
		apierr.Forbidden(w, "only owners and family managers can manage invitations")
		return uuid.Nil, "", false
	}
	return userID, role, true
}

func (h *Handler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Unauthorized(w, "not authenticated")
		return
	}

	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	userID, inviterRole, ok := h.callerManagerRole(w, r, claims)
	if !ok {
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		apierr.BadRequest(w, "a valid email is required")
		return
	}
	if req.Role == "" {
		req.Role = "family_member"
	}
	if !validRoles(req.Role) {
		apierr.BadRequest(w, "invalid role")
		return
	}
	if !canInvite(inviterRole, req.Role) {
		slog.Warn("invite: refused role elevation", "user_id", userID, "household_id", claims.HouseholdID,
			"inviter_role", inviterRole, "invited_role", req.Role)
		apierr.Forbidden(w, "you cannot invite someone with a role above your own")
		return
	}

	token := uuid.New().String()
	expiresAt := time.Now().Add(7 * 24 * time.Hour)

	inv, err := h.repo.Create(r.Context(), claims.HouseholdID, req.Email, token, req.Role, userID, expiresAt)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Send invitation email best-effort, off the request path. Without SMTP
	// (the normal self-hosted case) the inviter shares accept_url instead.
	go h.sendInviteEmail(context.WithoutCancel(r.Context()), inv, req.Email)

	apierr.JSON(w, http.StatusCreated, map[string]any{"data": inviteView{Invitation: *inv, AcceptURL: h.acceptURL(inv.Token)}})
}

// sendInviteEmail sends an HTML invitation email if SMTP is configured.
// This is best-effort: failures are logged but never returned to the caller.
func (h *Handler) sendInviteEmail(ctx context.Context, inv *Invitation, toEmail string) {
	if h.smtp == nil {
		slog.Info("invite: SMTP not configured, skipping email", "email", toEmail)
		return
	}

	// Bound the goroutine lifetime so it never leaks.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	householdName, err := h.repo.GetHouseholdName(ctx, inv.HouseholdID)
	if err != nil {
		slog.Warn("invite: failed to get household name for email", "error", err, "household_id", inv.HouseholdID)
		householdName = "your household"
	}

	// Prevent SMTP header injection in the subject line.
	subjectName := strings.NewReplacer("\r", " ", "\n", " ").Replace(householdName)

	// Prevent HTML injection in the body.
	escapedName := html.EscapeString(householdName)

	acceptLink := h.acceptURL(inv.Token)

	subject := "You're invited to " + subjectName
	htmlBody := `<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"></head>
<body style="font-family: sans-serif; padding: 20px;">
	<h2>You're invited to ` + escapedName + `</h2>
	<p>You have been invited to join <strong>` + escapedName + `</strong> on Home OS.</p>
	<p>
		<a href="` + acceptLink + `"
		   style="display: inline-block; padding: 12px 24px; background-color: #4F46E5; color: #fff; text-decoration: none; border-radius: 6px;">
			Accept Invitation
		</a>
	</p>
	<p>Or copy this link into your browser:</p>
	<p><a href="` + acceptLink + `">` + acceptLink + `</a></p>
	<p>This invitation expires in 7 days.</p>
</body>
</html>`

	if err := h.smtp.SendHTMLEmail(toEmail, subject, htmlBody); err != nil {
		slog.Warn("invite: failed to send email", "error", err, "email", toEmail)
		return
	}
	slog.Info("invite: invitation email sent", "email", toEmail)
}

func (h *Handler) ListInvites(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Unauthorized(w, "not authenticated")
		return
	}
	// The list carries live accept_urls (an owner invite's link would let
	// anyone holding it sign up as owner), so only managers may read it.
	if _, _, ok := h.callerManagerRole(w, r, claims); !ok {
		return
	}

	invs, err := h.repo.ListByHousehold(r.Context(), claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	// Pending, unexpired invitations carry their link again so the inviter
	// can re-copy it; used or expired ones do not.
	now := time.Now()
	views := make([]inviteView, 0, len(invs))
	for _, inv := range invs {
		v := inviteView{Invitation: inv}
		if inv.AcceptedAt == nil && now.Before(inv.ExpiresAt) {
			v.AcceptURL = h.acceptURL(inv.Token)
		}
		views = append(views, v)
	}

	apierr.JSON(w, http.StatusOK, map[string]any{"data": views})
}

// RevokeInvite DELETE /api/v1/invites/{id}
//
// Revokes a pending invitation of the request's household (X-Household-ID
// aware). Only an owner or family_manager there may revoke, and a
// family_manager may not revoke an owner invitation (the same rule as
// creating one, auth.CanGrantRole). The invitation row is deleted, so its
// accept link stops working at once: accept and signup then answer 404, as
// for an unknown token.
//
// Responses: 204 revoked; 403 not a manager, or a family_manager and an
// owner invitation; 404 unknown id, malformed id, or another household's
// invitation; 409 already accepted.
func (h *Handler) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Unauthorized(w, "not authenticated")
		return
	}
	userID, _, ok := h.callerManagerRole(w, r, claims)
	if !ok {
		return
	}
	inviteID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.NotFound(w, "invitation not found")
		return
	}

	switch err := h.repo.Revoke(r.Context(), claims.HouseholdID, inviteID, userID); {
	case err == nil:
		slog.Info("invite: revoked", "invitation_id", inviteID, "household_id", claims.HouseholdID, "user_id", userID)
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrInviteNotFound):
		apierr.NotFound(w, "invitation not found")
	case errors.Is(err, ErrInviteAccepted):
		apierr.Conflict(w, "invitation already accepted")
	case errors.Is(err, ErrRevokeForbidden):
		slog.Warn("invite: refused revoke", "invitation_id", inviteID, "household_id", claims.HouseholdID, "user_id", userID)
		apierr.Forbidden(w, "only owners can revoke an owner invitation")
	default:
		apierr.InternalError(w, err)
	}
}

// Signup password limits. bcrypt ignores (newer versions reject) input past
// 72 bytes, so longer passwords are refused rather than silently truncated.
const (
	minPasswordLen = 8
	maxPasswordLen = 72
	maxNameLen     = 100

	// maxAcceptBody caps the POST /api/v1/invites/accept body. The route is
	// unauthenticated and public; a real body ({"token","name","password"})
	// is well under 1 KiB.
	maxAcceptBody = 16 << 10
)

// errPasswordStore marks a failure to create the Dex password on signup.
var errPasswordStore = errors.New("password store")

// AcceptInvite POST /api/v1/invites/accept
//
// The caller is usually not a household member yet, so this route is
// registered OUTSIDE the RequireAuth group (which answers 403 for verified
// non-members). It has two modes, chosen by the Authorization header:
//
//   - Existing user (Bearer Dex token, body {"token"}): the handler verifies
//     the token itself with Verifier.Verify (signature, issuer, audience,
//     expiry; no membership needed) and Repo.Accept finds or creates the
//     Home OS user, adds the membership with the invitation's role and marks
//     the invitation accepted. The token's email must equal the invited email
//     (case-insensitive): holding the link alone is not enough.
//   - New person (no Authorization header, body {"token","name","password"}):
//     Repo.Signup creates the user with the invitation's email, the
//     membership and the Dex password entry in one transaction. The person
//     then signs in through Dex with the invited email and that password.
//
// Responses: 200 (accept) or 201 (signup) with the membership; 413 body over
// 16 KiB; 400 bad body,
// expired, or a weak password/name; 401 missing or invalid token (and no
// signup fields); 403 email differs from the invited email, or the account is
// deactivated; 404 unknown token; 409 already accepted, already a member, or
// (signup) an account with the invited email already exists; 502 Dex refused
// the password; 503 signup without Dex configured.
func (h *Handler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	// Cap the body before reading anything: this route needs no auth.
	r.Body = http.MaxBytesReader(w, r.Body, maxAcceptBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeDecodeError(w, err)
		return
	}
	req.Token = strings.TrimSpace(req.Token)

	if r.Header.Get("Authorization") == "" && req.Password != "" {
		h.signup(w, r, req.Token, req.Name, req.Password)
		return
	}

	ident, ok := h.acceptingIdentity(w, r)
	if !ok {
		return
	}
	if req.Token == "" {
		apierr.BadRequest(w, "token is required")
		return
	}

	res, err := h.repo.Accept(r.Context(), req.Token, ident, time.Now())
	if err != nil {
		writeAcceptError(w, err)
		return
	}
	h.writeAccepted(w, http.StatusOK, res)
}

// signup is AcceptInvite for a person with no account: it validates the
// name and password, checks the invitation read-only, hashes the password
// before the transaction (bcrypt is slow; the invitation row stays locked
// only for the writes) and runs Repo.Signup with a callback that stores the
// hash in Dex.
//
// The route is unauthenticated, so bcrypt (cost 12, ~250ms of CPU) must only
// run for a pending invitation: an unknown, used or expired token is refused
// after one indexed lookup, with the same responses Repo.Signup gives. That
// pre-check is only an early exit; Repo.Signup re-checks under FOR UPDATE and
// stays authoritative (an invite accepted between the two still fails there).
func (h *Handler) signup(w http.ResponseWriter, r *http.Request, token, name, password string) {
	if token == "" {
		apierr.BadRequest(w, "token is required")
		return
	}
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		apierr.BadRequest(w, "name is required")
		return
	case utf8.RuneCountInString(name) > maxNameLen:
		apierr.BadRequest(w, "name is too long")
		return
	case len(password) < minPasswordLen:
		apierr.BadRequest(w, "password must be at least 8 characters")
		return
	case len(password) > maxPasswordLen:
		apierr.BadRequest(w, "password must be at most 72 bytes")
		return
	}
	if h.dex == nil {
		// Without Dex the new user could never sign in.
		apierr.JSON(w, http.StatusServiceUnavailable, apierr.ErrorResponse{Error: apierr.ErrorDetail{
			Code: "SERVICE_UNAVAILABLE", Message: "signing up through an invitation is not available",
		}})
		return
	}

	now := time.Now()
	inv, err := h.repo.GetByToken(r.Context(), token)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if err := checkPending(inv, now); err != nil {
		writeAcceptError(w, err)
		return
	}

	hash, err := h.hashPassword(password)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	res, err := h.repo.Signup(r.Context(), token, name, hash, now,
		func(ctx context.Context, email, userID string) error {
			err := h.dex.CreatePassword(ctx, email, hash, userID)
			if errors.Is(err, dex.ErrPasswordExists) {
				// Dex already has a password for the invited email (a stale
				// entry, or one someone else set). Fail closed with the same
				// 409 as an existing Home OS account, so the response says
				// nothing the invitation flow does not already say.
				slog.Warn("invite signup: dex already has a password for the invited email; nothing written")
				return ErrAccountExists
			}
			if err != nil {
				return fmt.Errorf("%w: %w", errPasswordStore, err)
			}
			return nil
		})
	if err != nil {
		writeAcceptError(w, err)
		return
	}
	h.writeAccepted(w, http.StatusCreated, res)
}

// checkPending applies lockPendingInvitation's rules to an invitation read
// without a lock (nil when the token is unknown), returning the same errors.
func checkPending(inv *Invitation, now time.Time) error {
	switch {
	case inv == nil:
		return ErrInviteNotFound
	case inv.AcceptedAt != nil:
		return ErrInviteAccepted
	case now.After(inv.ExpiresAt):
		return ErrInviteExpired
	}
	return nil
}

// writeDecodeError answers a failed request body decode: 413 when the body
// went past its http.MaxBytesReader cap, 400 otherwise.
func writeDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		apierr.JSON(w, http.StatusRequestEntityTooLarge, apierr.ErrorResponse{Error: apierr.ErrorDetail{
			Code: "PAYLOAD_TOO_LARGE", Message: "request body too large",
		}})
		return
	}
	apierr.BadRequest(w, "invalid request body")
}

// writeAcceptError maps Repo.Accept / Repo.Signup errors to HTTP responses.
func writeAcceptError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInviteNotFound):
		apierr.NotFound(w, "invalid or expired invitation")
	case errors.Is(err, ErrInviteAccepted):
		apierr.Conflict(w, "invitation already accepted")
	case errors.Is(err, ErrAlreadyMember):
		apierr.Conflict(w, "already a member of this household")
	case errors.Is(err, ErrAccountExists):
		apierr.Conflict(w, "an account with this email already exists; sign in to accept the invitation")
	case errors.Is(err, ErrInviteExpired):
		apierr.BadRequest(w, "invitation has expired")
	case errors.Is(err, ErrInviteEmailMismatch):
		apierr.Forbidden(w, "this invitation was sent to a different email address")
	case errors.Is(err, ErrUserInactive):
		apierr.Forbidden(w, "this account has been deactivated")
	case errors.Is(err, errPasswordStore):
		slog.Error("invite signup: dex create password failed", "error", err)
		apierr.JSON(w, http.StatusBadGateway, apierr.ErrorResponse{Error: apierr.ErrorDetail{
			Code: "BAD_GATEWAY", Message: "could not set up sign-in; try again",
		}})
	default:
		apierr.InternalError(w, err)
	}
}

func (h *Handler) writeAccepted(w http.ResponseWriter, status int, res *AcceptResult) {
	slog.Info("invite: accepted", "invitation_id", res.Invitation.ID,
		"household_id", res.HouseholdID, "user_id", res.UserID, "role", res.Role, "user_created", res.UserCreated)
	apierr.JSON(w, status, map[string]any{"data": map[string]any{
		"invitation":   res.Invitation,
		"user_id":      res.UserID,
		"email":        res.Email,
		"household_id": res.HouseholdID,
		"role":         res.Role,
		"user_created": res.UserCreated,
	}})
}

// acceptingIdentity returns the verified Dex identity of the caller. It writes
// a 401 and returns false when the token is missing or invalid.
func (h *Handler) acceptingIdentity(w http.ResponseWriter, r *http.Request) (AcceptIdentity, bool) {
	if h.verifier == nil {
		// Mounted behind RequireAuth (no verifier wired): fall back to claims.
		if c := middleware.ClaimsFromContext(r.Context()); c != nil {
			return AcceptIdentity{Email: c.Email, Name: c.Name}, true
		}
		apierr.Unauthorized(w, "not authenticated")
		return AcceptIdentity{}, false
	}

	parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		apierr.Unauthorized(w, "missing or invalid authorization header")
		return AcceptIdentity{}, false
	}
	id, err := h.verifier.Verify(r.Context(), strings.TrimSpace(parts[1]))
	if err != nil {
		slog.Warn("invite accept: token verification failed", "error", err)
		apierr.Unauthorized(w, "invalid or expired token")
		return AcceptIdentity{}, false
	}
	return AcceptIdentity{Subject: id.Subject, Email: id.Email, Name: id.Name}, true
}
