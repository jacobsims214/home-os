package search

import (
	"net/http"
	"strings"

	"home-os/api/internal/auth"
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
	results = VisibleResults(claims.Role, results)

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"results": results,
			"total":   len(results),
			"query":   query,
		},
	})
}

// VisibleResults drops hits the role may not see under the household role
// policy (#4689): records of hidden kinds, and credential documents (indexed
// as "secret-<uuid>" under their parent record's type) for roles without
// secrets read, so a viewer never sees credential names in search.
func VisibleResults(role string, results []SearchResult) []SearchResult {
	canSecrets := auth.Can(role, auth.ResSecrets, auth.ActRead)
	out := make([]SearchResult, 0, len(results))
	for _, res := range results {
		if !canSecrets && strings.HasPrefix(res.DocID, "secret-") {
			continue
		}
		if res.EntityType != "" && !auth.CanSeeEntity(role, res.EntityType) {
			continue
		}
		out = append(out, res)
	}
	return out
}
