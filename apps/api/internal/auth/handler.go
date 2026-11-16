package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"home-os/api/internal/config"
	"home-os/api/internal/dex"
	"home-os/api/pkg/apierr"
	"home-os/api/pkg/smtp"
)

// Handler holds the dependencies needed by auth HTTP handlers.
type Handler struct {
	repo       *Repo
	cfg        *config.Config
	smtpClient *smtp.Client
	dexClient  *dex.Client
	verifier   *Verifier

	// passwords verifies and replaces Dex passwords for ChangePassword. It
	// defaults to dexClient; NewHandler leaves it nil when Dex is not
	// configured, which makes the endpoint answer 503.
	passwords PasswordService
	// changeLimiter bounds change-password attempts per account. Nil means
	// only the per-IP limiter wired in cmd/api applies.
	changeLimiter ChangePasswordLimiter
}

// NewHandler creates a new auth handler. Instance-admin user provisioning
// lives in the admin package (it replaced AdminCreateUser here).
func NewHandler(repo *Repo, cfg *config.Config, smtpClient *smtp.Client, dexClient *dex.Client, verifier *Verifier) *Handler {
	h := &Handler{repo: repo, cfg: cfg, smtpClient: smtpClient, dexClient: dexClient, verifier: verifier}
	if dexClient != nil { // a nil *dex.Client must not become a non-nil interface
		h.passwords = dexClient
	}
	return h
}

// WithPasswordService replaces the Dex password backend (tests use a fake).
func (h *Handler) WithPasswordService(p PasswordService) *Handler {
	h.passwords = p
	return h
}

// WithChangePasswordLimiter adds the per-account change-password budget.
func (h *Handler) WithChangePasswordLimiter(l ChangePasswordLimiter) *Handler {
	h.changeLimiter = l
	return h
}

// --- token verification for handlers outside RequireAuth ---

// WriteVerifyError writes the response for a failed Verifier.VerifyToken. It
// is the one mapping shared by middleware.RequireAuth and the handlers that
// verify tokens themselves, so clients see the same status everywhere:
//   - ErrNoMembership: 403 FORBIDDEN (authenticated, not a household member)
//   - ErrUserDeactivated: 403 FORBIDDEN (authenticated, account deactivated)
//   - ErrNotHouseholdMember: 403 FORBIDDEN (X-Household-ID names a household
//     the caller does not belong to)
//   - ErrMemberLookup: 500 INTERNAL_ERROR (database failure, not a bad token)
//   - anything else (bad signature, wrong audience, expired): 401 UNAUTHORIZED
func WriteVerifyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoMembership):
		slog.Warn("auth: authenticated identity has no household membership", "error", err)
		writeAuthError(w, http.StatusForbidden, "FORBIDDEN",
			"authenticated, but this account is not a member of any household")
	case errors.Is(err, ErrNotHouseholdMember):
		slog.Warn("auth: requested household is not one of the caller's", "error", err)
		writeAuthError(w, http.StatusForbidden, "FORBIDDEN",
			"this account is not a member of the requested household")
	case errors.Is(err, ErrUserDeactivated):
		slog.Warn("auth: authenticated identity is deactivated", "error", err)
		writeAuthError(w, http.StatusForbidden, "FORBIDDEN", "this account has been deactivated")
	case errors.Is(err, ErrMemberLookup):
		slog.Error("auth: membership lookup failed", "error", err)
		writeAuthError(w, http.StatusInternalServerError, "INTERNAL_ERROR",
			"could not resolve household membership")
	default:
		slog.Warn("auth: token verification failed", "error", err)
		WriteUnauthorized(w, "invalid or expired token")
	}
}

// WriteUnauthorized writes a 401 UNAUTHORIZED error with msg.
func WriteUnauthorized(w http.ResponseWriter, msg string) {
	writeAuthError(w, http.StatusUnauthorized, "UNAUTHORIZED", msg)
}

func writeAuthError(w http.ResponseWriter, status int, code, msg string) {
	apierr.JSON(w, status, apierr.ErrorResponse{
		Error: apierr.ErrorDetail{Code: code, Message: msg},
	})
}

// BearerToken extracts the token from an "Authorization: Bearer <token>"
// header (scheme matched case-insensitively). On failure it returns the 401
// message to send.
func BearerToken(r *http.Request) (token, errMsg string) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", "missing authorization header"
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", "invalid authorization header format"
	}
	return parts[1], ""
}

// authenticate verifies the request's Bearer token for handlers registered
// outside RequireAuth. On failure it has already written the response
// (400/401/403/500, as RequireAuth would) and returns nil. It honours the
// X-Household-ID header like RequireAuth.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) *Claims {
	tokenStr, errMsg := BearerToken(r)
	if errMsg != "" {
		WriteUnauthorized(w, errMsg)
		return nil
	}
	ctx, errMsg := ContextWithHouseholdHeader(r)
	if errMsg != "" {
		writeAuthError(w, http.StatusBadRequest, "BAD_REQUEST", errMsg)
		return nil
	}
	claims, err := h.verifier.VerifyToken(ctx, tokenStr)
	if err != nil {
		WriteVerifyError(w, err)
		return nil
	}
	return claims
}

// --- request / response types ---

type userResponse struct {
	ID        string  `json:"id"`
	Email     string  `json:"email"`
	Name      string  `json:"name"`
	AvatarURL *string `json:"avatar_url,omitempty"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	// IsInstanceAdmin is users.is_instance_admin, read from the caller's own
	// users row on every request (never from the token). It is the
	// instance-wide admin flag, a different axis from the household role:
	// the UI uses it only to show the instance-admin entry, and it says
	// nothing about any household. Always present, so a client can tell
	// "not an admin" from "field missing".
	IsInstanceAdmin bool `json:"is_instance_admin"`
}

// Me returns the currently authenticated user based on the Bearer token.
// GET /api/v1/auth/me.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	claims := h.authenticate(w, r)
	if claims == nil {
		return
	}

	user, err := h.repo.GetUserByID(r.Context(), claims.UserID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if user == nil {
		apierr.NotFound(w, "user not found")
		return
	}

	apierr.JSON(w, http.StatusOK, userResponse{
		ID:              user.ID.String(),
		Email:           user.Email,
		Name:            user.Name,
		AvatarURL:       user.AvatarURL,
		CreatedAt:       user.CreatedAt.Format(time.RFC3339),
		UpdatedAt:       user.UpdatedAt.Format(time.RFC3339),
		IsInstanceAdmin: user.IsInstanceAdmin,
	})
}

// caldavPasswordAlphabet is the character set for CalDAV app passwords.
// Alphanumeric only: app passwords get typed into phone settings screens and
// pasted through shells, so punctuation buys little and costs usability.
const caldavPasswordAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// caldavPasswordLength gives log2(62^32) ≈ 190 bits of entropy.
const caldavPasswordLength = 32

// generateCaldavPassword returns a uniformly random app password drawn from
// crypto/rand. It fails rather than returning a weak password if the system
// RNG errors.
func generateCaldavPassword() (string, error) {
	max := big.NewInt(int64(len(caldavPasswordAlphabet)))
	buf := make([]byte, caldavPasswordLength)
	for i := range buf {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate caldav password: %w", err)
		}
		buf[i] = caldavPasswordAlphabet[n.Int64()]
	}
	return string(buf), nil
}

// caldavPasswordResponse is returned once by GenerateCaldavPassword. The
// plaintext password is never stored or shown again.
type caldavPasswordResponse struct {
	Password  string `json:"password"`
	Username  string `json:"username"`
	ServerURL string `json:"server_url"`
}

// caldavServerURL is the public CalDAV endpoint: the calendar service is
// served under /dav/ on the same public origin (envoy routes /dav/ to it).
func (h *Handler) caldavServerURL() string {
	base := ""
	if h.cfg != nil {
		base = strings.TrimRight(h.cfg.PublicURL, "/")
	}
	return base + "/dav/"
}

// GenerateCaldavPassword generates a fresh CalDAV app password for the calling
// member, stores its bcrypt hash in users.caldav_password_hash (replacing, and
// so invalidating, any previous app password) and returns the plaintext once
// together with the CalDAV server URL and username (the member's email, which
// the calendar service's Basic auth matches on). POST /api/v1/auth/caldav-password.
func (h *Handler) GenerateCaldavPassword(w http.ResponseWriter, r *http.Request) {
	// Extract and verify the Dex token. The verifier's member resolver maps
	// the Dex identity to the Home OS user, so claims.UserID is users.id.
	claims := h.authenticate(w, r)
	if claims == nil {
		return
	}

	userUUID, err := uuid.Parse(claims.UserID)
	if err != nil {
		apierr.JSON(w, http.StatusBadRequest, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{Code: "INVALID_USER_ID", Message: "Invalid user ID format"},
		})
		return
	}
	user, err := h.repo.GetUserByID(r.Context(), userUUID.String())
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if user == nil {
		apierr.NotFound(w, "user not found")
		return
	}

	plaintext, err := generateCaldavPassword()
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	hash, err := HashPassword(plaintext)
	if err != nil {
		apierr.JSON(w, http.StatusInternalServerError, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{Code: "HASH_ERROR", Message: "Failed to hash password"},
		})
		return
	}

	if err := h.repo.UpdateCaldavPasswordHash(r.Context(), user.ID, hash); err != nil {
		slog.Error("caldav: failed to store app password hash", "user_id", user.ID, "error", err)
		apierr.JSON(w, http.StatusInternalServerError, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{Code: "DB_ERROR", Message: "Failed to store password"},
		})
		return
	}

	// The plaintext is a credential: keep it out of caches.
	w.Header().Set("Cache-Control", "no-store")
	apierr.JSON(w, http.StatusOK, caldavPasswordResponse{
		Password:  plaintext,
		Username:  user.Email,
		ServerURL: h.caldavServerURL(),
	})
}

// ForgotPassword handles password reset requests. Always returns 200 to prevent email enumeration.
func (h *Handler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	// Always return 200 to prevent email enumeration
	user, err := h.repo.GetUserByEmail(r.Context(), req.Email)
	if err != nil || user == nil {
		apierr.JSON(w, http.StatusOK, map[string]string{"message": "If the email exists, a reset link has been sent"})
		return
	}

	// Generate token and store in DB
	token := uuid.New().String()
	if err := h.repo.CreatePasswordResetToken(r.Context(), user.ID, token); err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Send password reset email asynchronously (best-effort — log warning on failure, don't return error).
	// The handler returns immediately; the email is sent in a timeout-bounded goroutine.
	if h.smtpClient != nil {
		resetLink := fmt.Sprintf("%s/reset-password?token=%s", h.cfg.PublicURL, token)
		subject := "Reset your Home OS password"
		htmlBody := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head><meta charset="UTF-8"></head>
<body style="font-family: sans-serif; padding: 24px;">
	<h2>Password Reset</h2>
	<p>We received a request to reset your password. Click the link below to set a new password:</p>
	<p><a href="%s">Reset Password</a></p>
	<p>If you didn't request this, you can safely ignore this email.</p>
	<hr>
	<p style="color: #666; font-size: 12px;">Home OS</p>
</body>
</html>`, resetLink)
		go func() {
			// Detach from request context so request cancellation doesn't kill the send,
			// but bound the total lifetime to 10 seconds.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
			defer cancel()

			errCh := make(chan error, 1)
			go func() {
				errCh <- h.smtpClient.SendHTMLEmail(user.Email, subject, htmlBody)
			}()

			select {
			case err := <-errCh:
				if err != nil {
					slog.Warn("failed to send password reset email", "email", user.Email, "error", err)
				}
			case <-ctx.Done():
				slog.Warn("password reset email timed out", "email", user.Email)
			}
		}()
	} else {
		slog.Info("SMTP not configured; skipping password reset email", "email", user.Email)
	}

	apierr.JSON(w, http.StatusOK, map[string]string{"message": "If the email exists, a reset link has been sent"})
}

// ResetPassword handles password reset completion.
func (h *Handler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if err := ValidatePasswordLength(req.Password); err != nil {
		apierr.BadRequest(w, err.Error())
		return
	}

	userID, err := h.repo.GetPasswordResetToken(r.Context(), req.Token)
	if err != nil {
		apierr.BadRequest(w, "invalid or expired reset token")
		return
	}

	hash, err := HashPassword(req.Password)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	if err := h.repo.UpdatePassword(r.Context(), userID, hash); err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Sync the new password hash to Dex's password database. CreatePassword
	// never overwrites, so dex.SetPassword updates the existing entry: the
	// reset token proves this user owns the email.
	if h.dexClient != nil {
		user, err := h.repo.GetUserByID(r.Context(), userID)
		if err == nil && user != nil {
			if err := dex.SetPassword(r.Context(), h.dexClient, user.Email, hash, userID); err != nil {
				slog.Warn("dex: failed to sync password after reset", "user_id", userID, "error", err)
			}
		}
	} else {
		slog.Info("dex: no client configured, skipping password sync after reset", "user_id", userID)
	}

	if err := h.repo.MarkResetTokenUsed(r.Context(), req.Token); err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]string{"message": "Password reset successfully"})
}

// --- change password (signed in) ---

// PasswordService is the part of Dex's admin API that ChangePassword needs.
// *dex.Client implements it; tests substitute a fake.
//
// The current password is verified by Dex rather than by comparing
// users.password_hash: Dex is the identity store that signs people in, and
// users.password_hash survives only as a legacy column.
type PasswordService interface {
	VerifyPassword(ctx context.Context, email, plaintext string) error
	UpdatePassword(ctx context.Context, email, hash string) error
	RefreshClientIDs(ctx context.Context, sub string) ([]string, error)
	RevokeRefresh(ctx context.Context, sub, clientID string) error
}

// ChangePasswordLimiter spends a budget keyed by an arbitrary string; it is
// *middleware.RateLimiter, which cannot be imported here (middleware imports
// auth). Allow reports whether the call may proceed and, when it may not, how
// long the caller must wait.
type ChangePasswordLimiter interface {
	Allow(key string) (bool, time.Duration)
}

// changePasswordBodyLimit caps the request body: two passwords and nothing
// else fits in well under 1 KiB.
const changePasswordBodyLimit = 16 << 10

// changePasswordRevokeTimeout bounds the Dex calls that revoke the caller's
// refresh tokens after the new password is stored. They run on a context
// detached from the request (see revokeRefreshTokens), so they need a deadline
// of their own.
const changePasswordRevokeTimeout = 10 * time.Second

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// changePasswordResponse is the 200 body. It says only that the password
// changed; what happens to sessions is in the completion notes and the KB
// (architecture/change-password.md), because it is a property of Dex's tokens,
// not something the caller acts on.
type changePasswordResponse struct {
	Message string `json:"message"`
}

// ChangePassword changes the signed-in caller's own password.
// POST /api/v1/auth/change-password with {current_password, new_password}.
//
// The current password is verified against Dex first and nothing is written
// unless it matches: a change-password screen that skipped that check would be
// an account-takeover primitive for anyone holding a live session.
//
// Every failure to verify the current password answers the same 401 with the
// same body and takes the same time, whether the password was wrong or Dex had
// no password for the caller's email: Dex's lookup path skips bcrypt, so the
// handler spends one bcrypt comparison itself when that happens
// (passwordCheckCost). Failures are logged and rate limited per user and per
// client IP, because a stolen session makes this a guessing surface.
//
// On success the new bcrypt hash is written to Dex with UpdatePassword (never
// CreatePassword: the entry the current password proved exists must be the one
// that changes), the legacy users.password_hash column is kept in step,
// and every Dex refresh token the caller holds is revoked. Id tokens already
// issued keep working until they expire, which is a property of Dex's JWTs and
// not something this endpoint can change.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	claims := h.authenticate(w, r)
	if claims == nil {
		return
	}
	// Per-user budget. The per-IP budget was spent by RateLimiter.Middleware
	// before the token was even verified; this one is keyed by the account so
	// a guessing attacker behind many addresses still runs out.
	if !h.allowChangePassword(w, claims) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, changePasswordBodyLimit)
	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			apierr.JSON(w, http.StatusRequestEntityTooLarge, apierr.ErrorResponse{Error: apierr.ErrorDetail{
				Code: "PAYLOAD_TOO_LARGE", Message: "request body too large",
			}})
			return
		}
		apierr.BadRequest(w, "invalid request body")
		return
	}
	if req.CurrentPassword == "" {
		apierr.BadRequest(w, "current_password is required")
		return
	}
	// The same rule signup and reset apply (auth.ValidatePasswordLength); a
	// change-password endpoint with its own idea of a strong password would be
	// a second rule to keep in step.
	if err := ValidatePasswordLength(req.NewPassword); err != nil {
		apierr.BadRequest(w, err.Error())
		return
	}

	if h.passwords == nil {
		// Without Dex the caller could never sign in with the new password.
		apierr.JSON(w, http.StatusServiceUnavailable, apierr.ErrorResponse{Error: apierr.ErrorDetail{
			Code: "SERVICE_UNAVAILABLE", Message: "changing your password is not available",
		}})
		return
	}

	// The account comes from the verified token (claims.UserID, which
	// VerifyToken resolved from the Dex subject), and so does the email: it is
	// users.email, the column every Dex password entry is created under
	// (invite signup and admin create both pass it to CreatePassword). Taking
	// the email from the token instead would mean trusting a claim whose value
	// is only a hint about which row the resolver already picked, and the two
	// can differ after an email change in Dex.
	user, err := h.repo.GetUserByID(r.Context(), claims.UserID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if user == nil {
		// The token is valid but its user is gone (deleted between the
		// resolver's lookup and this one). Nothing to change.
		apierr.NotFound(w, "user not found")
		return
	}
	email := user.Email

	if !h.verifyCurrentPassword(w, r, claims, email, req.CurrentPassword) {
		return
	}

	hash, err := HashPassword(req.NewPassword)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if err := h.passwords.UpdatePassword(r.Context(), email, hash); err != nil {
		// Nothing changed, so the old password still works. Say so rather
		// than claim a change that did not happen.
		slog.Error("auth: change password: dex update failed", "user_id", claims.UserID, "error", err)
		apierr.JSON(w, http.StatusBadGateway, apierr.ErrorResponse{Error: apierr.ErrorDetail{
			Code: "BAD_GATEWAY", Message: "could not change your password; try again",
		}})
		return
	}
	slog.Info("auth: password changed", "user_id", claims.UserID)

	// Keep the legacy column in step with the credential that now signs in.
	// Dex is authoritative from here on, so a failure is logged, not fatal.
	if err := h.repo.UpdatePassword(r.Context(), claims.UserID, hash); err != nil {
		slog.Error("auth: change password: users.password_hash not updated", "user_id", claims.UserID, "error", err)
	}

	revoked, err := h.revokeRefreshTokens(r, claims)
	if err != nil {
		slog.Error("auth: change password: refresh tokens not revoked", "user_id", claims.UserID, "error", err)
	} else {
		slog.Info("auth: change password: revoked refresh tokens", "user_id", claims.UserID, "clients", revoked)
	}

	w.Header().Set("Cache-Control", "no-store")
	apierr.JSON(w, http.StatusOK, changePasswordResponse{Message: "password changed"})
}

// allowChangePassword spends the caller's per-user budget, answering 429 with
// Retry-After when it is gone.
func (h *Handler) allowChangePassword(w http.ResponseWriter, claims *Claims) bool {
	if h.changeLimiter == nil {
		return true
	}
	ok, wait := h.changeLimiter.Allow("user:" + claims.UserID)
	if ok {
		return true
	}
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	slog.Warn("auth: change password rate limited", "user_id", claims.UserID, "retry_after_s", secs)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	apierr.JSON(w, http.StatusTooManyRequests, apierr.ErrorResponse{Error: apierr.ErrorDetail{
		Code: "RATE_LIMITED", Message: "too many requests, try again later",
	}})
	return false
}

// verifyCurrentPassword checks plaintext against the caller's Dex password. It
// answers the request itself and returns false when the check failed or could
// not be run, so ChangePassword can return immediately.
//
// A wrong password and an email Dex has no password for answer exactly the same
// 401 with the same body, and both take about as long: Dex's NotFound path does
// no bcrypt work, so this one does it instead.
func (h *Handler) verifyCurrentPassword(w http.ResponseWriter, r *http.Request, claims *Claims, email, plaintext string) bool {
	err := h.passwords.VerifyPassword(r.Context(), email, plaintext)
	if err == nil {
		return true
	}
	failed := false
	switch {
	case errors.Is(err, dex.ErrPasswordLookup):
		// No password entry to compare against, so Dex spent nothing.
		passwordCheckCost(plaintext)
		failed = true
	case errors.Is(err, dex.ErrPasswordNotVerified):
		failed = true
	}
	if !failed {
		// Dex is unreachable or refused the call: not a wrong password, and
		// not something the caller can fix.
		slog.Error("auth: change password: dex verify failed", "user_id", claims.UserID, "error", err)
		apierr.JSON(w, http.StatusBadGateway, apierr.ErrorResponse{Error: apierr.ErrorDetail{
			Code: "BAD_GATEWAY", Message: "could not verify your current password; try again",
		}})
		return false
	}
	// One line per failed attempt: with a stolen session this is the signal
	// that someone is guessing, and the rate limiter alone only shows 429s.
	slog.Warn("auth: change password refused: current password did not verify",
		"user_id", claims.UserID, "remote_addr", r.RemoteAddr)
	writeAuthError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "current password is incorrect")
	return false
}

// revokeRefreshTokens revokes every Dex refresh token the caller holds, one per
// client that has one, and returns how many. An empty Dex subject (an account
// that has never signed in through Dex) leaves nothing to revoke.
//
// It runs on a context detached from the request, like the reset email: the
// password has already changed by now, and a browser that hung up must not stop
// the revocation half-way and leave a token minting id tokens.
func (h *Handler) revokeRefreshTokens(r *http.Request, claims *Claims) (int, error) {
	if claims.DexSub == "" {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), changePasswordRevokeTimeout)
	defer cancel()

	clients, err := h.passwords.RefreshClientIDs(ctx, claims.DexSub)
	if err != nil {
		return 0, err
	}
	revoked := 0
	for _, clientID := range clients {
		switch err := h.passwords.RevokeRefresh(ctx, claims.DexSub, clientID); {
		case err == nil:
			revoked++
		case errors.Is(err, dex.ErrRefreshNotFound):
			// Rotated or already gone between the list and the revoke.
		default:
			return revoked, fmt.Errorf("revoke %s: %w", clientID, err)
		}
	}
	return revoked, nil
}
