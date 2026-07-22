package household

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

type Handler struct {
	repo *Repo
}

func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

// GetMe GET /api/v1/households/me — returns the current user's household.
func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	hh, err := h.repo.GetHousehold(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if hh == nil {
		apierr.NotFound(w, "household not found")
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": hh})
}

// ListMembers GET /api/v1/households/me/members
func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	members, err := h.repo.ListMembers(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": members})
}

// UpdateMemberRole PATCH /api/v1/households/me/members/{userId}
func (h *Handler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userId"))
	if err != nil {
		apierr.BadRequest(w, "invalid user id")
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid body")
		return
	}
	if req.Role == "" {
		apierr.BadRequest(w, "role is required")
		return
	}
	if err := h.repo.UpdateMemberRole(r.Context(), hid, userID, req.Role); err != nil {
		apierr.InternalError(w, err)
		return
	}
	members, err := h.repo.ListMembers(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": members})
}

// RemoveMember DELETE /api/v1/households/me/members/{userId}
func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userId"))
	if err != nil {
		apierr.BadRequest(w, "invalid user id")
		return
	}
	if err := h.repo.RemoveMember(r.Context(), hid, userID); err != nil {
		apierr.InternalError(w, err)
		return
	}
	members, err := h.repo.ListMembers(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": members})
}
