package search

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// FileChecker answers which of a household's file ids still exist and are
// visible to the caller. The file repo implements it from Postgres, the
// source of truth, so a file deleted while Typesense was down, or a
// credential's file asked for by a viewer, never comes back from search
// even if the index is stale.
type FileChecker interface {
	VisibleFileIDs(ctx context.Context, householdID string, ids []string, includeSecretFiles bool) (map[string]bool, error)
}

// Handler holds the dependencies needed by search HTTP handlers.
type Handler struct {
	client *Client
	files  FileChecker // nil: file hits are not re-checked against Postgres
	notes  NoteChecker // nil: every note hit is dropped (fail closed)
}

// WithFileChecker makes Search drop file hits whose file is gone or hidden
// from the caller.
func (h *Handler) WithFileChecker(fc FileChecker) *Handler {
	h.files = fc
	return h
}

// WithNoteChecker makes Search re-check note hits against Postgres by
// their parent (#4798): a note on a record the role may not see, such as a
// credential or a credential's file for a viewer, is dropped. Without it
// Search returns no note hits at all.
func (h *Handler) WithNoteChecker(nc NoteChecker) *Handler {
	h.notes = nc
	return h
}

// NewHandler creates a new search handler.
func NewHandler(client *Client) *Handler {
	return &Handler{client: client}
}

// Search performs a search across all indexed entities for the authenticated
// household. The query string is passed via the q parameter. Results can be
// further filtered by entity type (comma-separated; "document" selects file
// metadata documents) and property ID.
//
// Every collection is queried in hybrid mode: keyword plus semantic match
// using Typesense's built-in local embedding model (ts/all-MiniLM-L12-v2,
// computed inside Typesense; nothing leaves the cluster), fused by rank.
// The household scope comes from the caller's claims and is applied as a
// Typesense filter_by server-side; no client parameter can widen it.
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
	results = CollapseFiles(results)
	if h.files != nil {
		results = h.checkFiles(r.Context(), claims.HouseholdID, claims.Role, results)
	}
	results = h.checkNotes(r.Context(), claims.HouseholdID, claims.Role, results)

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
		if res.FileID != "" && !auth.Can(role, auth.ResFiles, auth.ActRead) {
			continue
		}
		out = append(out, res)
	}
	return out
}

// CollapseFiles keeps one hit per file: a file can match both as OCR text
// (files collection) and as its metadata document (household_search). The
// higher-scored hit stays, in the position of the first one; it takes the
// metadata title when the other hit had it, so the UI shows the document's
// title rather than its upload file name.
func CollapseFiles(results []SearchResult) []SearchResult {
	pos := map[string]int{}
	out := make([]SearchResult, 0, len(results))
	for _, res := range results {
		if res.FileID == "" {
			out = append(out, res)
			continue
		}
		i, seen := pos[res.FileID]
		if !seen {
			pos[res.FileID] = len(out)
			out = append(out, res)
			continue
		}
		prev := out[i]
		keep, other := prev, res
		if res.Score > prev.Score {
			keep, other = res, prev
		}
		if other.EntityType == documentEntityType {
			keep.Title = other.Title
		}
		out[i] = keep
	}
	return out
}

// checkFiles drops file hits whose file no longer exists in the household
// or is attached to a credential the role may not read. If the check fails
// every file hit is dropped (fail closed) and the rest are returned.
func (h *Handler) checkFiles(ctx context.Context, householdID, role string, results []SearchResult) []SearchResult {
	var ids []string
	for _, res := range results {
		if res.FileID != "" {
			ids = append(ids, res.FileID)
		}
	}
	if len(ids) == 0 {
		return results
	}
	ok, err := h.files.VisibleFileIDs(ctx, householdID, ids, auth.Can(role, auth.ResSecrets, auth.ActRead))
	if err != nil {
		slog.Warn("search: file visibility check failed, omitting file hits", "error", err)
		ok = map[string]bool{}
	}
	out := results[:0]
	for _, res := range results {
		if res.FileID == "" || ok[res.FileID] {
			out = append(out, res)
		}
	}
	return out
}
