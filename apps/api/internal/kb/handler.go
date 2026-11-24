package kb

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
	"home-os/api/pkg/apierr"
)

// searchTimeout bounds the detached index write a handler may do for
// freshness. The index is reconciled from Postgres anyway (searchrecon), so a
// failure here only costs a few minutes of staleness.
const searchTimeout = 5 * time.Second

// searchHits is how many search hits a search request asks for before the
// role-visible ones are cut down to the requested limit.
const searchHits = 40

// SearchIndex keeps household_search in step with a doc on write. It is
// optional: the reconciler (searchrecon) is what makes the index converge, so
// a handler without one still produces findable docs on the next sweep.
// *search.Client satisfies it.
type SearchIndex interface {
	IndexDocument(ctx context.Context, doc search.SearchDocument) error
	DeleteDocument(ctx context.Context, id string) error
}

// Handler serves the KB endpoints.
//
//	GET    /api/v1/kb?prefix=how-to/networking   → paths and titles (list)
//	GET    /api/v1/kb?q=dog+sick+at+night        → search (path, title, excerpt)
//	GET    /api/v1/kb/how-to/networking/router-reset   → one doc, full body
//	PUT    /api/v1/kb/how-to/networking/router-reset   → create or REPLACE by path
//	DELETE /api/v1/kb/how-to/networking/router-reset   → soft delete
//
// The doc path is a chi wildcard ("/api/v1/kb/*"), so its slashes need no
// escaping. The Vary/what counts as a path is CleanPath's job.
//
// A path is a namespaced slug ('how-to/networking/router-reset'); chi's
// wildcard captures it whole, so no URL escaping is needed for the slashes.
type Handler struct {
	repo   *Repo
	search *search.Client
	idx    SearchIndex
}

// NewHandler serves the KB endpoints over repo.
func NewHandler(repo *Repo, client *search.Client) *Handler {
	return &Handler{repo: repo, search: client}
}

// WithIndex attaches a write-path index. Optional: searchrecon is the source
// of truth for the index.
func (h *Handler) WithIndex(s SearchIndex) *Handler {
	h.idx = s
	return h
}

// RegisterRoutes mounts the KB routes. The router must already run
// middleware.RequireAuth and middleware.RequirePolicy.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/kb", h.ListOrSearch)
	r.Get("/api/v1/kb/*", h.Get)
	r.Put("/api/v1/kb/*", h.Upsert)
	r.Delete("/api/v1/kb/*", h.Delete)
}

// --- request and response types ---------------------------------------------

type docResponse struct {
	Path        string    `json:"path"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Tags        []string  `json:"tags"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	CreatedBy   *string   `json:"created_by,omitempty"`
	UpdatedBy   *string   `json:"updated_by,omitempty"`
	CreatedByID *string   `json:"created_by_id,omitempty"`
	UpdatedByID *string   `json:"updated_by_id,omitempty"`
}

type summaryResponse struct {
	Path      string    `json:"path"`
	Title     string    `json:"title"`
	Tags      []string  `json:"tags"`
	Excerpt   string    `json:"excerpt"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy *string   `json:"updated_by,omitempty"`
	// Score is present on search hits only; higher is a better match.
	Score *float64 `json:"score,omitempty"`
}

type upsertRequest struct {
	Title string   `json:"title"`
	Body  string   `json:"body"`
	Tags  []string `json:"tags"`
}

type upsertResponse struct {
	Doc     docResponse `json:"doc"`
	Created bool        `json:"created"`
	// Note tells the caller, in words, that an update replaced the doc.
	Note string `json:"note"`
}

func toDocResponse(d *Doc) docResponse {
	out := docResponse{
		Path: d.Path, Title: d.Title, Body: d.Body, Tags: d.Tags,
		CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(),
		CreatedBy: d.CreatedName, UpdatedBy: d.UpdatedName,
	}
	if d.CreatedBy != nil {
		s := d.CreatedBy.String()
		out.CreatedByID = &s
	}
	if d.UpdatedBy != nil {
		s := d.UpdatedBy.String()
		out.UpdatedByID = &s
	}
	return out
}

func toSummaryResponse(s Summary) summaryResponse {
	return summaryResponse{
		Path: s.Path, Title: s.Title, Tags: s.Tags, Excerpt: s.Excerpt,
		UpdatedAt: s.UpdatedAt.UTC(), UpdatedBy: s.UpdatedName,
	}
}

// --- handlers ---------------------------------------------------------------

// ListOrSearch serves GET /api/v1/kb: with q it searches the KB by meaning and
// keyword; with prefix (or neither) it lists paths and titles.
//
// GET /api/v1/kb?q=what+do+we+do+if+the+dog+is+sick+at+night
// GET /api/v1/kb?prefix=how-to
func (h *Handler) ListOrSearch(w http.ResponseWriter, r *http.Request) {
	claims, ok := authorize(w, r)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	prefix := strings.TrimSpace(r.URL.Query().Get("prefix"))

	if q != "" {
		h.searchDocs(w, r, claims, q)
		return
	}
	if prefix != "" {
		clean, err := CleanPath(prefix)
		if err != nil {
			apierr.BadRequest(w, err.Error())
			return
		}
		prefix = clean
	}
	docs, err := h.repo.List(r.Context(), claims.HouseholdID, prefix)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	out := make([]summaryResponse, 0, len(docs))
	for _, d := range docs {
		out = append(out, toSummaryResponse(d))
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": out, "total": len(out)})
}

// searchDocs runs the query against household_search, keeps only KB hits the
// caller's role may see, and returns path, title, excerpt and author for each.
//
// The index is queried rather than Postgres because the point of the feature
// is finding a doc BY MEANING: "what do we do if the dog is sick at night"
// matches how-to/pets/vet-after-hours without sharing a word with its path.
// Every hit is then re-read from Postgres, so a doc deleted or soft-deleted
// while the index was stale never comes back, and the excerpt and author come
// from the source of truth.
func (h *Handler) searchDocs(w http.ResponseWriter, r *http.Request, claims claims, q string) {
	if len(q) > MaxQuery {
		apierr.BadRequest(w, "q must be at most 500 characters")
		return
	}
	if h.search == nil {
		// No Typesense configured: fall back to a plain word match over the
		// docs' paths and titles, so the endpoint still works.
		h.listByWord(w, r, claims, q)
		return
	}
	results, err := h.search.Search(r.Context(), claims.HouseholdID.String(), q, search.SearchFilters{
		EntityTypes: []string{EntityType},
	})
	if err != nil {
		slog.Warn("kb: search failed, falling back to path/title match", "error", err)
		h.listByWord(w, r, claims, q)
		return
	}
	// VisibleResults drops records of kinds the role cannot see (#4689). The
	// KB is readable by every role, so in practice this is a no-op — but it
	// is the one place that decision lives, and a future role that cannot
	// read the KB is filtered here without another handler remembering to.
	results = search.VisibleResults(claims.Role, results)

	out := make([]summaryResponse, 0, len(results))
	seen := map[string]bool{}
	for _, hit := range results {
		if hit.EntityType != EntityType {
			continue
		}
		var doc *Doc
		if id, err := uuid.Parse(hit.EntityID); err == nil {
			doc, _ = h.repo.GetByID(r.Context(), claims.HouseholdID, id)
		}
		if doc == nil {
			// The index knows a doc Postgres no longer has as live. Skip it;
			// the reconciler prunes it on the next sweep.
			continue
		}
		if seen[doc.Path] {
			continue
		}
		seen[doc.Path] = true
		s := toSummaryResponse(Summary{
			Path: doc.Path, Title: doc.Title, Tags: doc.Tags,
			UpdatedAt: doc.UpdatedAt, UpdatedName: doc.UpdatedName,
			Excerpt: Excerpt(doc.Body),
		})
		score := hit.Score
		s.Score = &score
		out = append(out, s)
		if len(out) >= 20 {
			break
		}
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": out, "total": len(out), "query": q})
}

// listByWord is the no-index fallback: a substring match over path and title.
func (h *Handler) listByWord(w http.ResponseWriter, r *http.Request, claims claims, q string) {
	docs, err := h.repo.SearchByWord(r.Context(), claims.HouseholdID, q)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	out := make([]summaryResponse, 0, len(docs))
	for _, d := range docs {
		out = append(out, toSummaryResponse(d))
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": out, "total": len(out), "query": q})
}

// Get returns one doc, full body, by path.
// GET /api/v1/kb/how-to/networking/router-reset
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	claims, ok := authorize(w, r)
	if !ok {
		return
	}
	path, ok := pathParam(w, r)
	if !ok {
		return
	}
	doc, err := h.repo.Get(r.Context(), claims.HouseholdID, path)
	if err != nil {
		writeErr(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toDocResponse(doc)})
}

// Upsert creates or REPLACES the doc at path.
// PUT /api/v1/kb/how-to/networking/router-reset
//
// The request replaces the whole doc: a field left out is cleared. That is
// deliberate — a doc has one address and one body, so a partial write would
// silently merge two people's knowledge into neither.
func (h *Handler) Upsert(w http.ResponseWriter, r *http.Request) {
	claims, ok := authorize(w, r)
	if !ok {
		return
	}
	path, ok := pathParam(w, r)
	if !ok {
		return
	}
	var req upsertRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid JSON body")
		return
	}
	title, err := CleanTitle(req.Title)
	if err != nil {
		apierr.BadRequest(w, err.Error())
		return
	}
	if err := CheckBody(req.Body); err != nil {
		apierr.BadRequest(w, err.Error())
		return
	}
	tags, err := CleanTags(req.Tags)
	if err != nil {
		apierr.BadRequest(w, err.Error())
		return
	}
	doc, created, err := h.repo.Upsert(r.Context(), claims.HouseholdID, &Doc{
		Path: path, Title: title, Body: req.Body, Tags: tags,
	}, claims.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.index(r, doc)
	note := "Doc created at " + doc.Path + "."
	if !created {
		note = "Doc at " + doc.Path + " was REPLACED: its previous title, body and tags are gone."
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	apierr.JSON(w, status, map[string]any{"data": upsertResponse{
		Doc: toDocResponse(doc), Created: created, Note: note,
	}})
}

// Delete soft-deletes the doc at path.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	claims, ok := authorize(w, r)
	if !ok {
		return
	}
	path, ok := pathParam(w, r)
	if !ok {
		return
	}
	doc, err := h.repo.Get(r.Context(), claims.HouseholdID, path)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.repo.Delete(r.Context(), claims.HouseholdID, path, claims.UserID); err != nil {
		writeErr(w, err)
		return
	}
	h.unindex(r, doc)
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ----------------------------------------------------------------

// claims is what a request's role and household come from.
type claims struct {
	HouseholdID uuid.UUID
	UserID      *uuid.UUID
	Role        string
}

// authorize reads the request's claims and applies the KB role policy. Every
// household role reads; only the family roles write (auth.ResKB).
func authorize(w http.ResponseWriter, r *http.Request) (claims, bool) {
	c := middleware.ClaimsFromContext(r.Context())
	if c == nil {
		apierr.Forbidden(w, "no household in this request")
		return claims{}, false
	}
	hid, err := uuid.Parse(c.HouseholdID)
	if err != nil {
		apierr.Forbidden(w, "no household in this request")
		return claims{}, false
	}
	out := claims{HouseholdID: hid, Role: c.Role}
	if uid, err := uuid.Parse(c.UserID); err == nil {
		out.UserID = &uid
	}
	return out, true
}

// pathParam reads and validates the wildcard path from the URL.
func pathParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := chi.URLParam(r, "*")
	path, err := CleanPath(raw)
	if err != nil {
		apierr.BadRequest(w, err.Error())
		return "", false
	}
	return path, true
}

// writeErr maps repo errors to HTTP responses.
func writeErr(w http.ResponseWriter, err error) {
	var ve ValidationError
	if errors.As(err, &ve) {
		apierr.BadRequest(w, ve.Error())
		return
	}
	if errors.Is(err, ErrNotFound) {
		apierr.NotFound(w, "no doc at that path")
		return
	}
	apierr.InternalError(w, err)
}

// index refreshes the search document on write. Best effort and detached from
// the request: the reconciler is what guarantees the index converges, so a
// Typesense outage must never fail a save.
func (h *Handler) index(r *http.Request, d *Doc) {
	if h.idx == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), searchTimeout)
	defer cancel()
	if err := h.idx.IndexDocument(ctx, Document(d)); err != nil {
		slog.Warn("kb: doc saved but not indexed; the reconciler will pick it up", "path", d.Path, "error", err)
	}
}

func (h *Handler) unindex(r *http.Request, d *Doc) {
	if h.idx == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), searchTimeout)
	defer cancel()
	if err := h.idx.DeleteDocument(ctx, DocumentID(d)); err != nil {
		slog.Warn("kb: doc deleted but still indexed; the reconciler will prune it", "path", d.Path, "error", err)
	}
}

// The role policy is applied by middleware.RequirePolicy on the routes
// (auth.ResKB), so handlers do not restate it. Keep the import honest: the
// policy table is the authority this package follows.
var _ = auth.ResKB
