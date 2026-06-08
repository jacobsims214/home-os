package search

import (
	"net/http"
	"strings"

	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// Handler holds the dependencies needed by search HTTP handlers.
type Handler struct {
	client *Client
}

// NewHandler creates a new search handler.
func NewHandler(client *Client) *Handler {
	return &Handler{client: client}
}

// Search performs a search across all indexed entities for the authenticated
// household. The query string is passed via the q parameter. Results can be
// further filtered by entity type (comma-separated) and property ID.
//
// GET /api/v1/search?q=<query>&type=<types>&property_id=<id>
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	query := r.URL.Query().Get("q")
	if query == "" {
		apierr.BadRequest(w, "query parameter 'q' is required")
		return
	}

	var filters SearchFilters
	if typeParam := r.URL.Query().Get("type"); typeParam != "" {
		filters.EntityTypes = strings.Split(typeParam, ",")
	}
	if propID := r.URL.Query().Get("property_id"); propID != "" {
		filters.PropertyID = propID
	}

	results, err := h.client.Search(r.Context(), claims.HouseholdID, query, filters)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"results": results,
			"total":   len(results),
			"query":   query,
		},
	})
}