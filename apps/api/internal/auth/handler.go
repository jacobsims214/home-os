package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"home-os/api/internal/config"
	"home-os/api/internal/household"
	"home-os/api/pkg/apierr"
)

// Handler holds the dependencies needed by auth HTTP handlers.
type Handler struct {
	repo          *Repo
	householdRepo *household.Repo
	cfg           *config.Config
}

// NewHandler creates a new auth handler.
func NewHandler(repo *Repo, householdRepo *household.Repo, cfg *config.Config) *Handler {
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
	hh, err := h.householdRepo.CreateHousehold(r.Context(), req.Name+"'s Home")
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Create membership as owner.
	if err := h.householdRepo.CreateMembership(r.Context(), hh.ID, user.ID, RoleOwner); err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Sign JWT.
	claims := Claims{
		UserID:      user.ID.String(),
		HouseholdID: hh.ID.String(),
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
