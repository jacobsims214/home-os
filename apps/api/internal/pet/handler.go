package pet

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// Handler holds dependencies for pet HTTP handlers.
type Handler struct {
	repo *Repo
}

// NewHandler creates a new pet handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

// --- request / response types ---

type createPetRequest struct {
	Name        string     `json:"name"`
	Species     *string    `json:"species"`
	Breed       *string    `json:"breed"`
	DateOfBirth *time.Time `json:"date_of_birth"`
	VetName     *string    `json:"vet_name"`
	VetPhone    *string    `json:"vet_phone"`
	Notes       *string    `json:"notes"`
}

type updatePetRequest struct {
	Name        string     `json:"name"`
	Species     *string    `json:"species"`
	Breed       *string    `json:"breed"`
	DateOfBirth *time.Time `json:"date_of_birth"`
	VetName     *string    `json:"vet_name"`
	VetPhone    *string    `json:"vet_phone"`
	Notes       *string    `json:"notes"`
}

type petResponse struct {
	ID          string     `json:"id"`
	HouseholdID string     `json:"household_id"`
	Name        string     `json:"name"`
	Species     *string    `json:"species,omitempty"`
	Breed       *string    `json:"breed,omitempty"`
	DateOfBirth *time.Time `json:"date_of_birth,omitempty"`
	VetName     *string    `json:"vet_name,omitempty"`
	VetPhone    *string    `json:"vet_phone,omitempty"`
	Notes       *string    `json:"notes,omitempty"`
	CreatedAt   string     `json:"created_at"`
	UpdatedAt   string     `json:"updated_at"`
}

func toPetResponse(p *Pet) petResponse {
	return petResponse{
		ID:          p.ID.String(),
		HouseholdID: p.HouseholdID.String(),
		Name:        p.Name,
		Species:     p.Species,
		Breed:       p.Breed,
		DateOfBirth: p.DateOfBirth,
		VetName:     p.VetName,
		VetPhone:    p.VetPhone,
		Notes:       p.Notes,
		CreatedAt:   p.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   p.UpdatedAt.Format(time.RFC3339),
	}
}

func toPetResponses(pets []*Pet) []petResponse {
	out := make([]petResponse, 0, len(pets))
	for _, p := range pets {
		out = append(out, toPetResponse(p))
	}
	return out
}

// householdID extracts the household UUID from the JWT claims in the request context.
func householdID(r *http.Request) (uuid.UUID, error) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		return uuid.Nil, apierr.ErrForbidden
	}
	return uuid.Parse(claims.HouseholdID)
}

// List returns all pets for the authenticated household.
// GET /api/v1/pets
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	pets, err := h.repo.List(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toPetResponses(pets),
	})
}

// Get returns a single pet by ID.
// GET /api/v1/pets/:id
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid pet id")
		return
	}

	pet, err := h.repo.Get(r.Context(), hid, id)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if pet == nil {
		apierr.NotFound(w, "pet not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toPetResponse(pet),
	})
}

// Create inserts a new pet for the authenticated household.
// POST /api/v1/pets
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	var req createPetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}

	p := &Pet{
		Name:        req.Name,
		Species:     req.Species,
		Breed:       req.Breed,
		DateOfBirth: req.DateOfBirth,
		VetName:     req.VetName,
		VetPhone:    req.VetPhone,
		Notes:       req.Notes,
	}

	created, err := h.repo.Create(r.Context(), hid, p)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": toPetResponse(created),
	})
}

// Update modifies an existing pet.
// PUT /api/v1/pets/:id
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid pet id")
		return
	}

	var req updatePetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}

	p := &Pet{
		Name:        req.Name,
		Species:     req.Species,
		Breed:       req.Breed,
		DateOfBirth: req.DateOfBirth,
		VetName:     req.VetName,
		VetPhone:    req.VetPhone,
		Notes:       req.Notes,
	}

	updated, err := h.repo.Update(r.Context(), hid, id, p)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if updated == nil {
		apierr.NotFound(w, "pet not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toPetResponse(updated),
	})
}

// Delete removes a pet.
// DELETE /api/v1/pets/:id
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid pet id")
		return
	}

	if err := h.repo.Delete(r.Context(), hid, id); err != nil {
		apierr.NotFound(w, "pet not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
