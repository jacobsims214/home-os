package link

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// Handler holds the dependencies needed by link HTTP handlers.
type Handler struct {
	repo *Repo
}

// NewHandler creates a new link handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

// --- request / response types ---

type createLinkRequest struct {
	EntityType string  `json:"entity_type"`
	EntityID   string  `json:"entity_id"`
	LinkType   string  `json:"link_type"`
	LinkID     string  `json:"link_id"`
	Title      string  `json:"title"`
	URL        *string `json:"url,omitempty"`
}

type linkResponse struct {
	ID         string  `json:"id"`
	EntityType string  `json:"entity_type"`
	EntityID   string  `json:"entity_id"`
	LinkType   string  `json:"link_type"`
	LinkID     string  `json:"link_id"`
	Title      string  `json:"title"`
	URL        *string `json:"url,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

type linksGroupedResponse struct {
	Other []*linkResponse `json:"other,omitempty"`
}

// Create adds a new link between an entity and an external resource.
// POST /api/v1/links
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "no claims in context")
		return
	}

	var req createLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	req.EntityType = strings.TrimSpace(req.EntityType)
	req.LinkType = strings.TrimSpace(req.LinkType)
	req.LinkID = strings.TrimSpace(req.LinkID)
	req.Title = strings.TrimSpace(req.Title)

	if req.EntityType == "" {
		apierr.BadRequest(w, "entity_type is required")
		return
	}
	if req.EntityID == "" {
		apierr.BadRequest(w, "entity_id is required")
		return
	}
	entityID, err := uuid.Parse(req.EntityID)
	if err != nil {
		apierr.BadRequest(w, "invalid entity_id")
		return
	}
	if req.LinkType == "" {
		apierr.BadRequest(w, "link_type is required")
		return
	}
	if req.LinkID == "" {
		apierr.BadRequest(w, "link_id is required")
		return
	}
	if req.Title == "" {
		apierr.BadRequest(w, "title is required")
		return
	}

	// Verify the referenced entity exists and belongs to the caller's household
	// before performing any write. A 404 is returned on mismatch (rather than
	// 403) so that callers cannot probe for the existence of entities owned by
	// other households (cross-tenant IDOR).
	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.Forbidden(w, "invalid household claim")
		return
	}
	owned, err := h.repo.EntityOwnedByHousehold(r.Context(), req.EntityType, entityID, householdID)
	if err != nil {
		if errors.Is(err, ErrUnknownEntityType) {
			apierr.BadRequest(w, "invalid entity_type")
			return
		}
		apierr.InternalError(w, err)
		return
	}
	if !owned {
		apierr.NotFound(w, "entity not found")
		return
	}

	link := &Link{
		EntityType: req.EntityType,
		EntityID:   entityID,
		LinkType:   req.LinkType,
		LinkID:     req.LinkID,
		Title:      req.Title,
		URL:        req.URL,
	}

	created, err := h.repo.CreateLink(r.Context(), link)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusCreated, linkToResponse(created))
}

// List returns all links for a given entity, grouped by link_type.
// GET /api/v1/links?entity_type=asset&entity_id=uuid
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "no claims in context")
		return
	}

	entityType := strings.TrimSpace(r.URL.Query().Get("entity_type"))
	entityIDStr := strings.TrimSpace(r.URL.Query().Get("entity_id"))

	if entityType == "" {
		apierr.BadRequest(w, "entity_type query parameter is required")
		return
	}
	if entityIDStr == "" {
		apierr.BadRequest(w, "entity_id query parameter is required")
		return
	}

	entityID, err := uuid.Parse(entityIDStr)
	if err != nil {
		apierr.BadRequest(w, "invalid entity_id")
		return
	}

	// Verify the referenced entity exists and belongs to the caller's household
	// before returning any links. Returns 404 on mismatch to avoid leaking
	// existence of other households' entities (cross-tenant IDOR).
	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.Forbidden(w, "invalid household claim")
		return
	}
	owned, err := h.repo.EntityOwnedByHousehold(r.Context(), entityType, entityID, householdID)
	if err != nil {
		if errors.Is(err, ErrUnknownEntityType) {
			apierr.BadRequest(w, "invalid entity_type")
			return
		}
		apierr.InternalError(w, err)
		return
	}
	if !owned {
		apierr.NotFound(w, "entity not found")
		return
	}

	links, err := h.repo.GetLinks(r.Context(), entityType, entityID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	// All links are returned in a single generic "other" bucket. The link
	// package no longer maintains typed buckets for specific integration
	// types — link_type is a free-form string. Legacy integration link rows
	// whose link_type was once a first-class integration type are still
	// stored and surfaced here so they remain visible to the caller instead
	// of being silently dropped.
	grouped := linksGroupedResponse{}
	for _, l := range links {
		resp := linkToResponse(l)
		grouped.Other = append(grouped.Other, resp)
	}

	apierr.JSON(w, http.StatusOK, map[string]any{"data": grouped})
}

// Delete removes a link by its ID.
// DELETE /api/v1/links/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "no claims in context")
		return
	}

	linkID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid link id")
		return
	}

	// Fetch the link first so we can verify its referenced entity belongs to the
	// caller's household before deleting. We do NOT delete by id alone — that
	// would let a user destroy another household's link by guessing its UUID
	// (cross-tenant IDOR). Returns 404 on any failure path (link missing or
	// entity not owned) to avoid leaking existence.
	existing, err := h.repo.GetLink(r.Context(), linkID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if existing == nil {
		apierr.NotFound(w, "link not found")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.Forbidden(w, "invalid household claim")
		return
	}
	owned, err := h.repo.EntityOwnedByHousehold(r.Context(), existing.EntityType, existing.EntityID, householdID)
	if err != nil {
		if errors.Is(err, ErrUnknownEntityType) {
			apierr.NotFound(w, "link not found")
			return
		}
		apierr.InternalError(w, err)
		return
	}
	if !owned {
		apierr.NotFound(w, "link not found")
		return
	}

	if err := h.repo.DeleteLink(r.Context(), linkID); err != nil {
		apierr.InternalError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

func linkToResponse(l *Link) *linkResponse {
	return &linkResponse{
		ID:         l.ID.String(),
		EntityType: l.EntityType,
		EntityID:   l.EntityID.String(),
		LinkType:   l.LinkType,
		LinkID:     l.LinkID,
		Title:      l.Title,
		URL:        l.URL,
		CreatedAt:  l.CreatedAt.Format(time.RFC3339),
	}
}