package note

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// Handler holds dependencies for note HTTP handlers.
type Handler struct {
	repo *Repo
}

// NewHandler creates a new note handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

// --- request / response types ---

type createNoteRequest struct {
	EntityType string  `json:"entity_type"`
	EntityID   string  `json:"entity_id"`
	Title      *string `json:"title,omitempty"`
	Body       string  `json:"body"`
}

type updateNoteRequest struct {
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
}

type noteResponse struct {
	ID          string  `json:"id"`
	HouseholdID string  `json:"household_id"`
	EntityType  string  `json:"entity_type"`
	EntityID    string  `json:"entity_id"`
	Title       *string `json:"title,omitempty"`
	Body        string  `json:"body"`
	AuthorID    *string `json:"author_id,omitempty"`
	AuthorName  *string `json:"author_name,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func toNoteResponse(n *Note) noteResponse {
	resp := noteResponse{
		ID:          n.ID.String(),
		HouseholdID: n.HouseholdID.String(),
		EntityType:  n.EntityType,
		EntityID:    n.EntityID.String(),
		Title:       n.Title,
		Body:        n.Body,
		CreatedAt:   n.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   n.UpdatedAt.Format(time.RFC3339),
	}
	if n.AuthorID != nil {
		s := n.AuthorID.String()
		resp.AuthorID = &s
	}
	if n.AuthorName != nil {
		resp.AuthorName = n.AuthorName
	}
	return resp
}

func toNoteResponses(notes []*Note) []noteResponse {
	out := make([]noteResponse, 0, len(notes))
	for _, n := range notes {
		out = append(out, toNoteResponse(n))
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

// List returns all notes for the authenticated household.
// GET /api/v1/notes
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	// If entity_type + entity_id are provided, filter by entity
	entityType := r.URL.Query().Get("entity_type")
	entityIDStr := r.URL.Query().Get("entity_id")
	if entityType != "" && entityIDStr != "" {
		entityID, err := uuid.Parse(entityIDStr)
		if err != nil {
			apierr.BadRequest(w, "invalid entity_id")
			return
		}
		notes, err := h.repo.ListByEntity(r.Context(), hid, entityType, entityID)
		if err != nil {
			apierr.InternalError(w, err)
			return
		}
		apierr.JSON(w, http.StatusOK, map[string]any{"data": toNoteResponses(notes)})
		return
	}

	notes, err := h.repo.ListByHousehold(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toNoteResponses(notes),
	})
}

// Get returns a single note by ID.
// GET /api/v1/notes/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid note id")
		return
	}

	note, err := h.repo.Get(r.Context(), hid, id)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if note == nil {
		apierr.NotFound(w, "note not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toNoteResponse(note),
	})
}

// Create inserts a new note for the authenticated household.
// POST /api/v1/notes
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	var req createNoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.EntityType == "" {
		apierr.BadRequest(w, "entity_type is required")
		return
	}
	if req.EntityID == "" {
		apierr.BadRequest(w, "entity_id is required")
		return
	}
	if req.Body == "" {
		apierr.BadRequest(w, "body is required")
		return
	}

	entityID, err := uuid.Parse(req.EntityID)
	if err != nil {
		apierr.BadRequest(w, "invalid entity_id")
		return
	}

	// Extract author_id from JWT claims if available
	var authorID *uuid.UUID
	if claims := middleware.ClaimsFromContext(r.Context()); claims != nil {
		if uid, err := uuid.Parse(claims.UserID); err == nil {
			authorID = &uid
		}
	}

	n := &Note{
		HouseholdID: hid,
		EntityType:  req.EntityType,
		EntityID:    entityID,
		Title:       req.Title,
		Body:        req.Body,
		AuthorID:    authorID,
	}

	created, err := h.repo.Create(r.Context(), n)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": toNoteResponse(created),
	})
}

// Update modifies an existing note.
// PUT /api/v1/notes/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid note id")
		return
	}

	var req updateNoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	// Fetch existing to verify it exists and belongs to household
	existing, err := h.repo.Get(r.Context(), hid, id)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if existing == nil {
		apierr.NotFound(w, "note not found")
		return
	}

	// Merge fields
	if req.Title != nil {
		existing.Title = req.Title
	}
	if req.Body != nil {
		existing.Body = *req.Body
	}

	updated, err := h.repo.Update(r.Context(), hid, id, existing)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if updated == nil {
		apierr.NotFound(w, "note not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": toNoteResponse(updated),
	})
}

// Delete removes a note.
// DELETE /api/v1/notes/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid note id")
		return
	}

	if err := h.repo.Delete(r.Context(), hid, id); err != nil {
		apierr.NotFound(w, "note not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}