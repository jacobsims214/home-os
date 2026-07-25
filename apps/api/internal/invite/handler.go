package invite

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"home-os/api/internal/config"
	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

type Handler struct {
	repo *Repo
	cfg  *config.Config
}

func NewHandler(repo *Repo, cfg *config.Config) *Handler {
	return &Handler{repo: repo, cfg: cfg}
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

	apierr.JSON(w, http.StatusCreated, map[string]any{"data": inv})
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

func (h *Handler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	inv, err := h.repo.GetByToken(r.Context(), req.Token)
	if err != nil {
		apierr.NotFound(w, "invalid or expired invitation")
		return
	}

	if inv.AcceptedAt != nil {
		apierr.Conflict(w, "invitation already accepted")
		return
	}

	if time.Now().After(inv.ExpiresAt) {
		apierr.BadRequest(w, "invitation has expired")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{"data": inv})
}
