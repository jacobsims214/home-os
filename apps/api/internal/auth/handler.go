package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"home-os/api/internal/config"
	"home-os/api/pkg/apierr"
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
}

// NewHandler creates a new auth handler.
func NewHandler(repo *Repo, householdRepo HouseholdCreator, cfg *config.Config) *Handler {
	return &Handler{repo: repo, householdRepo: householdRepo, cfg: cfg}
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

	claims, err := VerifyToken(h.cfg, tokenStr)
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
	claims, err := VerifyToken(h.cfg, tokenStr)
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
