package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

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

// --- request / response types ---

type registerRequest struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type tokenResponse struct {
	Token string `json:"token"`
}

type userResponse struct {
	ID        string  `json:"id"`
	Email     string  `json:"email"`
	Name      string  `json:"name"`
	AvatarURL *string `json:"avatar_url,omitempty"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// Register creates a new user, a default household, an owner membership,
// and returns a signed JWT. POST /api/v1/auth/register.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.AllowRegistration {
		apierr.JSON(w, http.StatusForbidden, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{Code: "REGISTRATION_DISABLED", Message: "Registration is currently disabled"},
		})
		return
	}

	var req registerRequest
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
	// This enables Dex OIDC authentication for the new user.
	if h.dexClient != nil {
		if err := h.dexClient.CreatePassword(r.Context(), req.Email, hash, user.ID.String()); err != nil {
			slog.Warn("dex: failed to create password, continuing registration", "email", req.Email, "error", err)
		}
	} else {
		slog.Info("dex: no client configured, skipping password creation", "email", req.Email)
	}

	// Create default household.
	hhID, err := h.householdRepo.CreateHousehold(r.Context(), req.Name+"'s Home")
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Create membership as owner.
	if err := h.householdRepo.CreateMembership(r.Context(), hhID, user.ID.String(), RoleOwner); err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Sign JWT.
	claims := Claims{
		UserID:      user.ID.String(),
		HouseholdID: hhID,
		Role:        RoleOwner,
		Email:       req.Email,
	}
	token, err := SignToken(h.cfg, claims)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusCreated, tokenResponse{Token: token})
}

// Login validates email/password, looks up the user's primary membership,
// and returns a signed JWT. POST /api/v1/auth/login.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	req.Email = strings.TrimSpace(req.Email)

	if req.Email == "" || req.Password == "" {
		apierr.BadRequest(w, "email and password are required")
		return
	}

	user, err := h.repo.GetUserByEmail(r.Context(), req.Email)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if user == nil {
		apierr.NotFound(w, "user not found")
		return
	}

	if err := CheckPassword(user.PasswordHash, req.Password); err != nil {
		apierr.Forbidden(w, "invalid password")
		return
	}

	memberships, err := h.repo.GetMemberships(r.Context(), user.ID.String())
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if len(memberships) == 0 {
		apierr.Forbidden(w, "no household memberships found")
		return
	}

	claims := Claims{
		UserID:      user.ID.String(),
		HouseholdID: memberships[0].HouseholdID.String(),
		Role:        memberships[0].Role,
		Email:       req.Email,
	}
	token, err := SignToken(h.cfg, claims)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusOK, tokenResponse{Token: token})
}

// Me returns the currently authenticated user based on the Bearer token.
// GET /api/v1/auth/me.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		apierr.Forbidden(w, "missing authorization header")
		return
	}

	tokenStr, ok := strings.CutPrefix(authHeader, "Bearer ")
	if !ok {
		apierr.Forbidden(w, "invalid authorization header format")
		return
	}

	claims, err := h.verifier.VerifyToken(r.Context(), tokenStr)
	if err != nil {
		apierr.Forbidden(w, "invalid or expired token")
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

// GenerateCaldavPassword generates a random 32-character CalDAV app password,
// hashes it with bcrypt, stores it in the database, and returns the plaintext
// password to the user. POST /api/v1/auth/caldav-password.
func (h *Handler) GenerateCaldavPassword(w http.ResponseWriter, r *http.Request) {
	// Extract and verify JWT from Authorization header directly
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{Code: "UNAUTHORIZED", Message: "Not authenticated"},
		})
		return
	}
	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
	claims, err := h.verifier.VerifyToken(r.Context(), tokenStr)
	if err != nil {
		apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{Code: "UNAUTHORIZED", Message: "Invalid or expired token"},
		})
		return
	}

	// Generate a random 32-char password
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%"
	buf := make([]byte, 32)
	for i := range buf {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		buf[i] = chars[n.Int64()]
	}
	plaintext := string(buf)

	// Hash with bcrypt
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), 12)
	if err != nil {
		apierr.JSON(w, http.StatusInternalServerError, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{Code: "HASH_ERROR", Message: "Failed to hash password"},
		})
		return
	}

	// Store in database
	userUUID, err := uuid.Parse(claims.UserID)
	if err != nil {
		apierr.JSON(w, http.StatusBadRequest, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{Code: "INVALID_USER_ID", Message: "Invalid user ID format"},
		})
		return
	}
	if err := h.repo.UpdateCaldavPasswordHash(r.Context(), userUUID, string(hash)); err != nil {
		apierr.JSON(w, http.StatusInternalServerError, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{Code: "DB_ERROR", Message: "Failed to store password"},
		})
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]string{"password": plaintext})
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
