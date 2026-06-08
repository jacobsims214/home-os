package property

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// Handler holds the dependencies needed by property HTTP handlers.
type Handler struct {
	repo *Repo
}

// NewHandler creates a new property handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

// --- request / response types ---

type propertyRequest struct {
	Name         *string `json:"name"`
	Address      *string `json:"address"`
	PropertyType *string `json:"property_type"`
	Notes        *string `json:"notes"`
}

type propertyResponse struct {
	ID           string  `json:"id"`
	HouseholdID  string  `json:"household_id"`
	Name         string  `json:"name"`
	Address      *string `json:"address,omitempty"`
	PropertyType *string `json:"property_type,omitempty"`
	Notes        *string `json:"notes,omitempty"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

type roomRequest struct {
	Name  *string `json:"name"`
	Floor *int    `json:"floor"`
	Notes *string `json:"notes"`
}

type roomResponse struct {
	ID         string  `json:"id"`
	PropertyID string  `json:"property_id"`
	Name       string  `json:"name"`
	Floor      *int    `json:"floor,omitempty"`
	Notes      *string `json:"notes,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

// --- helpers ---

// householdID extracts the household UUID from JWT claims injected by the
// RequireAuth middleware. Writes a 401 and returns uuid.Nil if not found.
func householdID(w http.ResponseWriter, r *http.Request) uuid.UUID {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{
				Code:    "UNAUTHORIZED",
				Message: "missing claims",
			},
		})
		return uuid.Nil
	}

	id, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{
				Code:    "UNAUTHORIZED",
				Message: "invalid household_id in token",
			},
		})
		return uuid.Nil
	}
	return id
}

func toPropertyResponse(p *Property) propertyResponse {
	return propertyResponse{
		ID:           p.ID.String(),
		HouseholdID:  p.HouseholdID.String(),
		Name:         p.Name,
		Address:      p.Address,
		PropertyType: p.PropertyType,
		Notes:        p.Notes,
		CreatedAt:    p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:    p.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func toRoomResponse(r *Room) roomResponse {
	return roomResponse{
		ID:         r.ID.String(),
		PropertyID: r.PropertyID.String(),
		Name:       r.Name,
		Floor:      r.Floor,
		Notes:      r.Notes,
		CreatedAt:  r.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// --- property handlers ---

// ListProperties handles GET /api/v1/properties.
func (h *Handler) ListProperties(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}

	properties, err := h.repo.ListProperties(r.Context(), hhID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	resp := make([]propertyResponse, len(properties))
	for i, p := range properties {
		resp[i] = toPropertyResponse(p)
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": resp,
	})
}

// CreateProperty handles POST /api/v1/properties.
func (h *Handler) CreateProperty(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}

	var req propertyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Name == nil || *req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}

	property, err := h.repo.CreateProperty(r.Context(), hhID, *req.Name, req.Address, req.PropertyType, req.Notes)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": toPropertyResponse(property),
	})
}

// GetProperty handles GET /api/v1/properties/{id}.
func (h *Handler) GetProperty(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}

	propertyID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid property id")
		return
	}

	property, err := h.repo.GetProperty(r.Context(), propertyID, hhID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if property == nil {
		apierr.NotFound(w, "property not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toPropertyResponse(property),
	})
}

// UpdateProperty handles PUT /api/v1/properties/{id}.
func (h *Handler) UpdateProperty(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}

	propertyID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid property id")
		return
	}

	var req propertyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	property, err := h.repo.UpdateProperty(r.Context(), propertyID, hhID, req.Name, req.Address, req.PropertyType, req.Notes)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if property == nil {
		apierr.NotFound(w, "property not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toPropertyResponse(property),
	})
}

// DeleteProperty handles DELETE /api/v1/properties/{id}.
func (h *Handler) DeleteProperty(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}

	propertyID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid property id")
		return
	}

	deleted, err := h.repo.DeleteProperty(r.Context(), propertyID, hhID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if !deleted {
		apierr.NotFound(w, "property not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- room handlers ---

// ListRooms handles GET /api/v1/properties/{id}/rooms.
func (h *Handler) ListRooms(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}

	propertyID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid property id")
		return
	}

	// Verify the property exists and belongs to this household.
	property, err := h.repo.GetProperty(r.Context(), propertyID, hhID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if property == nil {
		apierr.NotFound(w, "property not found")
		return
	}

	rooms, err := h.repo.ListRooms(r.Context(), propertyID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	resp := make([]roomResponse, len(rooms))
	for i, rm := range rooms {
		resp[i] = toRoomResponse(rm)
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": resp,
	})
}

// CreateRoom handles POST /api/v1/properties/{id}/rooms.
func (h *Handler) CreateRoom(w http.ResponseWriter, r *http.Request) {
	hhID := householdID(w, r)
	if hhID == uuid.Nil {
		return
	}

	propertyID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid property id")
		return
	}

	// Verify the property exists and belongs to this household.
	property, err := h.repo.GetProperty(r.Context(), propertyID, hhID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if property == nil {
		apierr.NotFound(w, "property not found")
		return
	}

	var req roomRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Name == nil || *req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}

	room, err := h.repo.CreateRoom(r.Context(), propertyID, *req.Name, req.Floor, req.Notes)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": toRoomResponse(room),
	})
}
