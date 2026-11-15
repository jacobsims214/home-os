package invite

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"home-os/api/internal/auth"
	"home-os/api/internal/config"
	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
	"home-os/api/pkg/smtp"
)

type Handler struct {
	repo     *Repo
	cfg      *config.Config
	smtp     *smtp.Client
	verifier *auth.Verifier
}

func NewHandler(repo *Repo, cfg *config.Config, smtpClient *smtp.Client) *Handler {
	return &Handler{repo: repo, cfg: cfg, smtp: smtpClient}
}

// WithVerifier lets AcceptInvite verify the caller's Dex token itself, so the
// accept route can be served outside RequireAuth to users who are not yet
// members of any household.
func (h *Handler) WithVerifier(v *auth.Verifier) *Handler {
	h.verifier = v
	return h
}

// validRoles mirrors the membership_role enum (migration 001). Invitations
// store role as TEXT, so it is checked here before it is cast on accept.
var validRoles = map[string]bool{
	"owner": true, "family_manager": true, "family_member": true,
	"viewer": true, "housesitter": true, "vendor": true,
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	if req.Email == "" {
		apierr.BadRequest(w, "email is required")
		return
	}
	if req.Role == "" {
		req.Role = "family_member"
	}
	if !validRoles[req.Role] {
		apierr.BadRequest(w, "invalid role")
		return
	}

	token := uuid.New().String()
	expiresAt := time.Now().Add(7 * 24 * time.Hour)

	userID, err := uuid.Parse(claims.UserID)
	if err != nil {
		apierr.BadRequest(w, "invalid user ID")
		return
	}
	inv, err := h.repo.Create(r.Context(), claims.HouseholdID, req.Email, token, req.Role, userID, expiresAt)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Send invitation email best-effort, off the request path.
	go h.sendInviteEmail(context.WithoutCancel(r.Context()), inv, req.Email)

	apierr.JSON(w, http.StatusCreated, map[string]any{"data": inv})
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

	acceptLink := h.cfg.PublicURL + "/invites/accept?token=" + inv.Token

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

	invs, err := h.repo.ListByHousehold(r.Context(), claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if invs == nil {
		invs = []Invitation{}
	}

	apierr.JSON(w, http.StatusOK, map[string]any{"data": invs})
}

// AcceptInvite POST /api/v1/invites/accept
//
// The caller is usually not a household member yet, so this route is
// registered OUTSIDE the RequireAuth group (which answers 403 for verified
// non-members). The handler verifies the Dex bearer token itself with
// Verifier.Verify (signature, issuer, audience, expiry; no membership needed)
// and then, in one transaction, finds or creates the Home OS user, adds the
// membership with the invitation's role and marks the invitation accepted.
//
// Responses: 200 with the membership; 400 bad body / expired; 401 missing or
// invalid token; 403 token email differs from the invited email; 404 unknown
// token; 409 already accepted or already a member.
func (h *Handler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	ident, ok := h.acceptingIdentity(w, r)
	if !ok {
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		apierr.BadRequest(w, "token is required")
		return
	}

	res, err := h.repo.Accept(r.Context(), req.Token, ident, time.Now())
	switch {
	case err == nil:
	case errors.Is(err, ErrInviteNotFound):
		apierr.NotFound(w, "invalid or expired invitation")
		return
	case errors.Is(err, ErrInviteAccepted):
		apierr.Conflict(w, "invitation already accepted")
		return
	case errors.Is(err, ErrAlreadyMember):
		apierr.Conflict(w, "already a member of this household")
		return
	case errors.Is(err, ErrInviteExpired):
		apierr.BadRequest(w, "invitation has expired")
		return
	case errors.Is(err, ErrInviteEmailMismatch):
		apierr.Forbidden(w, "this invitation was sent to a different email address")
		return
	default:
		apierr.InternalError(w, err)
		return
	}

	slog.Info("invite: accepted", "invitation_id", res.Invitation.ID,
		"household_id", res.HouseholdID, "user_id", res.UserID, "role", res.Role, "user_created", res.UserCreated)
	apierr.JSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"invitation":   res.Invitation,
		"user_id":      res.UserID,
		"household_id": res.HouseholdID,
		"role":         res.Role,
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
