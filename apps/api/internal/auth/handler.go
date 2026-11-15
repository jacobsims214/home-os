package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"home-os/api/internal/config"
	"home-os/api/internal/dex"
	"home-os/api/pkg/apierr"
	"home-os/api/pkg/smtp"
)

// HouseholdCreator is the subset of household.Repo that auth needs.
// Defined as an interface to avoid an import cycle (auth → household → auth).
type HouseholdCreator interface {
	CreateHousehold(ctx context.Context, name string) (id string, err error)
	CreateMembership(ctx context.Context, householdID, userID, role string) error
}

// Handler holds the dependencies needed by auth HTTP handlers.
type Handler struct {
	repo          *Repo
	householdRepo HouseholdCreator
	cfg           *config.Config
	smtpClient    *smtp.Client
	dexClient     *dex.Client
	verifier      *Verifier
}

// NewHandler creates a new auth handler.
func NewHandler(repo *Repo, householdRepo HouseholdCreator, cfg *config.Config, smtpClient *smtp.Client, dexClient *dex.Client, verifier *Verifier) *Handler {
	return &Handler{repo: repo, householdRepo: householdRepo, cfg: cfg, smtpClient: smtpClient, dexClient: dexClient, verifier: verifier}
}

// --- token verification for handlers outside RequireAuth ---

// WriteVerifyError writes the response for a failed Verifier.VerifyToken. It
// is the one mapping shared by middleware.RequireAuth and the handlers that
// verify tokens themselves, so clients see the same status everywhere:
//   - ErrNoMembership: 403 FORBIDDEN (authenticated, not a household member)
//   - ErrUserDeactivated: 403 FORBIDDEN (authenticated, account deactivated)
//   - ErrMemberLookup: 500 INTERNAL_ERROR (database failure, not a bad token)
//   - anything else (bad signature, wrong audience, expired): 401 UNAUTHORIZED
func WriteVerifyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoMembership):
		slog.Warn("auth: authenticated identity has no household membership", "error", err)
		writeAuthError(w, http.StatusForbidden, "FORBIDDEN",
			"authenticated, but this account is not a member of any household")
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
// (401/403/500, as RequireAuth would) and returns nil.
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) *Claims {
	tokenStr, errMsg := BearerToken(r)
	if errMsg != "" {
		WriteUnauthorized(w, errMsg)
		return nil
	}
	claims, err := h.verifier.VerifyToken(r.Context(), tokenStr)
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
}

// adminCreateUserRequest is the request body for AdminCreateUser.
type adminCreateUserRequest struct {
	Email         string `json:"email"`
	Name          string `json:"name"`
	Password      string `json:"password"`
	HouseholdName string `json:"household_name"`
}

// adminCreateUserResponse is the response body for AdminCreateUser.
type adminCreateUserResponse struct {
	UserID      string `json:"user_id"`
	HouseholdID string `json:"household_id"`
}

// AdminCreateUser creates a new user, a household, and an owner membership.
// Requires a valid JWT with role "owner". POST /api/v1/admin/users.
func (h *Handler) AdminCreateUser(w http.ResponseWriter, r *http.Request) {
	claims := h.authenticate(w, r)
	if claims == nil {
		return
	}

	// Only users with the owner role can create new admin users.
	if claims.Role != RoleOwner {
		apierr.JSON(w, http.StatusForbidden, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{Code: "FORBIDDEN", Message: "Only owners can create users"},
		})
		return
	}

	var req adminCreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	req.Email = strings.TrimSpace(req.Email)
	req.Name = strings.TrimSpace(req.Name)

	if req.Email == "" || req.Name == "" || req.Password == "" {
		apierr.BadRequest(w, "email, name, and password are required")
		return
	}

	if len(req.Password) < 8 {
		apierr.BadRequest(w, "password must be at least 8 characters")
		return
	}

	// Check for existing user.
	existing, err := h.repo.GetUserByEmail(r.Context(), req.Email)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if existing != nil {
		apierr.Conflict(w, "email already registered")
		return
	}

	// Hash password.
	hash, err := HashPassword(req.Password)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Create user.
	user, err := h.repo.CreateUser(r.Context(), req.Email, req.Name, hash)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Register password in Dex's local password database.
	if h.dexClient != nil {
		if err := h.dexClient.CreatePassword(r.Context(), req.Email, hash, user.ID.String()); err != nil {
			slog.Warn("dex: failed to create password for admin-created user", "email", req.Email, "error", err)
		}
	} else {
		slog.Info("dex: no client configured, skipping password creation", "email", req.Email)
	}

	// Create household.
	householdName := req.HouseholdName
	if householdName == "" {
		householdName = req.Name + "'s Home"
	}
	hhID, err := h.householdRepo.CreateHousehold(r.Context(), householdName)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Create membership as owner.
	if err := h.householdRepo.CreateMembership(r.Context(), hhID, user.ID.String(), RoleOwner); err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusCreated, adminCreateUserResponse{
		UserID:      user.ID.String(),
		HouseholdID: hhID,
	})
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
		ID:        user.ID.String(),
		Email:     user.Email,
		Name:      user.Name,
		AvatarURL: user.AvatarURL,
		CreatedAt: user.CreatedAt.Format(time.RFC3339),
		UpdatedAt: user.UpdatedAt.Format(time.RFC3339),
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

	if req.Password == "" || len(req.Password) < 8 {
		apierr.BadRequest(w, "password must be at least 8 characters")
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

	// Sync the new password hash to Dex's password database.
	// CreatePassword is an upsert — it creates or updates the entry.
	if h.dexClient != nil {
		user, err := h.repo.GetUserByID(r.Context(), userID)
		if err == nil && user != nil {
			if err := h.dexClient.CreatePassword(r.Context(), user.Email, hash, userID); err != nil {
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
