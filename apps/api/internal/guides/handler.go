package guides

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
	"home-os/api/internal/searchqueue"
	"home-os/api/internal/subject"
	"home-os/api/pkg/apierr"
)

// EntityType is the guide's entity_type in household_search (and the entity
// registry kind from migration 049).
const EntityType = "guide"

// Limits on what a caller may save.
const (
	maxTitle      = 255
	maxSummary    = 2000
	maxChangeNote = 500
	maxBody       = 256 << 10 // 256 KiB of markdown
	maxTags       = 30
	maxTagLen     = 50
	maxRequest    = 1 << 20
	searchTimeout = 5 * time.Second
)

// Action is what a request does to guides, for the role policy.
type Action string

const (
	// ActionRead: list and read guides and their revisions.
	ActionRead Action = "read"
	// ActionWrite: create, edit, restore a revision, delete.
	ActionWrite Action = "write"
)

// Policy decides whether a member with role (their role in the request's
// X-Household-ID household, as resolved by middleware.RequireAuth) may
// perform action. Role restrictions (#4689, viewers read-only) plug in here
// with Handler.WithPolicy.
type Policy func(role string, action Action) bool

// AllowAll lets every household member do everything. It is the default
// until role restrictions land.
func AllowAll(string, Action) bool { return true }

// SearchIndex keeps household_search in step with guides. *search.Client
// satisfies it.
type SearchIndex interface {
	IndexDocument(ctx context.Context, doc search.SearchDocument) error
	DeleteDocument(ctx context.Context, id string) error
}

// Handler serves the guides endpoints.
//
// Guides are indexed into household_search (id "guide-<uuid>", title, summary
// + body, tags, property_id), where Typesense embeds them with the local model
// so "the wifi is down" finds "Reset the router". Indexing is best effort and
// detached from the request: a Typesense outage is logged, never fails the
// save, and the reindex tool (cmd/reindex) repairs the index.
type Handler struct {
	repo   *Repo
	policy Policy
	search SearchIndex
}

// NewHandler creates a guides handler with the AllowAll policy and no search
// index.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo, policy: AllowAll}
}

// WithPolicy replaces the role policy.
func (h *Handler) WithPolicy(p Policy) *Handler {
	h.policy = p
	return h
}

// WithSearch sets the search index guides are written to.
func (h *Handler) WithSearch(s SearchIndex) *Handler {
	h.search = s
	return h
}

// RegisterRoutes mounts the guides routes. The router must already run
// middleware.RequireAuth.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/guides", h.List)
	r.Post("/api/v1/guides", h.Create)
	r.Get("/api/v1/guides/{id}", h.Get)
	r.Put("/api/v1/guides/{id}", h.Update)
	r.Delete("/api/v1/guides/{id}", h.Delete)
	r.Get("/api/v1/guides/{id}/revisions", h.ListRevisions)
	r.Get("/api/v1/guides/{id}/revisions/{revisionId}", h.GetRevision)
	r.Post("/api/v1/guides/{id}/revisions/{revisionId}/restore", h.Restore)
}

// --- search ---

// DocumentID is the guide's household_search document id.
func DocumentID(id uuid.UUID) string { return EntityType + "-" + id.String() }

// The guide document builder used to live here. It is now the single shared
// builder in packages/searchindex (#5314): migration 075 queues guides through
// that builder, so having a second copy here meant the index could hold one
// guide two different ways. Callers use searchqueue.IndexNow (the handler) or
// searchindex.Build (cmd/reindex).

// searchCtx is detached from the request so a client hang-up doesn't cancel
// the index write, and bounded so an unreachable Typesense can't hold the
// response.
func searchCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), searchTimeout)
}

func (h *Handler) index(r *http.Request, g *Guide) {
	// Same builder the index queue's drain uses, so a guide saved here and one
	// saved through an MCP tool produce identical documents (#5314). A failure
	// is only logged: the trigger already queued the row.
	if h.search == nil {
		return
	}
	ctx, cancel := searchCtx(r)
	defer cancel()
	if err := searchqueue.IndexNow(ctx, h.repo.pool, h.search, EntityType, g.ID.String()); err != nil {
		slog.Warn("guides: indexed later by the queue", "guide_id", g.ID, "error", err)
	}
}

func (h *Handler) unindex(r *http.Request, id uuid.UUID) {
	if h.search == nil {
		return
	}
	ctx, cancel := searchCtx(r)
	defer cancel()
	if err := h.search.DeleteDocument(ctx, DocumentID(id)); err != nil {
		slog.Warn("guides: deleted but still in search; run reindex", "guide_id", id, "error", err)
	}
}

// --- request / response types ---

type createRequest struct {
	subject.Fields
	Title   string   `json:"title"`
	Summary *string  `json:"summary"`
	Body    string   `json:"body"`
	Tags    []string `json:"tags"`
}

// updateRequest: omitted fields are unchanged; summary "" clears it; subject
// fields follow subject.Fields.Apply ("" clears one). change_note is why it
// changed, stored on the new revision (trimmed; "" or omitted stores none).
type updateRequest struct {
	subject.Fields
	Title      *string   `json:"title"`
	Summary    *string   `json:"summary"`
	Body       *string   `json:"body"`
	Tags       *[]string `json:"tags"`
	ChangeNote *string   `json:"change_note"`
}

type guideResponse struct {
	ID          string   `json:"id"`
	HouseholdID string   `json:"household_id"`
	Title       string   `json:"title"`
	Summary     *string  `json:"summary"`
	Body        string   `json:"body"`
	Tags        []string `json:"tags"`
	PropertyID  *string  `json:"property_id"`
	AssetID     *string  `json:"asset_id"`
	VehicleID   *string  `json:"vehicle_id"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

type revisionResponse struct {
	ID           string  `json:"id"`
	GuideID      string  `json:"guide_id"`
	Body         *string `json:"body,omitempty"` // GetRevision only
	BodyLength   int     `json:"body_length"`
	EditedBy     *string `json:"edited_by"`
	EditedByName *string `json:"edited_by_name"`
	ChangeNote   *string `json:"change_note"`
	CreatedAt    string  `json:"created_at"`
}

func toGuideResponse(g *Guide) guideResponse {
	return guideResponse{
		ID:          g.ID.String(),
		HouseholdID: g.HouseholdID.String(),
		Title:       g.Title,
		Summary:     g.Summary,
		Body:        g.Body,
		Tags:        g.Tags,
		PropertyID:  subject.String(g.PropertyID),
		AssetID:     subject.String(g.AssetID),
		VehicleID:   subject.String(g.VehicleID),
		CreatedAt:   g.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   g.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func toRevisionResponse(v *Revision, withBody bool) revisionResponse {
	out := revisionResponse{
		ID:           v.ID.String(),
		GuideID:      v.GuideID.String(),
		BodyLength:   utf8.RuneCountInString(v.Body),
		EditedBy:     subject.String(v.EditedBy),
		EditedByName: v.EditedByName,
		ChangeNote:   v.ChangeNote,
		CreatedAt:    v.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if withBody {
		b := v.Body
		out.Body = &b
	}
	return out
}

// --- validation ---

// validationError is a message safe to show the caller (400).
type validationError string

func (e validationError) Error() string { return string(e) }

func cleanTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", validationError("title is required")
	}
	if utf8.RuneCountInString(s) > maxTitle {
		return "", validationError("title must be at most 255 characters")
	}
	return s, nil
}

// cleanSummary trims s; blank means no summary.
func cleanSummary(s *string) (*string, error) {
	if s == nil {
		return nil, nil
	}
	v := strings.TrimSpace(*s)
	if v == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(v) > maxSummary {
		return nil, validationError("summary must be at most 2000 characters")
	}
	return &v, nil
}

// cleanChangeNote trims a revision's change note; blank is none.
func cleanChangeNote(s *string) (*string, error) {
	if s == nil {
		return nil, nil
	}
	v := strings.TrimSpace(*s)
	if v == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(v) > maxChangeNote {
		return nil, validationError("change_note must be at most 500 characters")
	}
	return &v, nil
}

func checkBody(s string) error {
	if len(s) > maxBody {
		return validationError("body must be at most 256 KiB")
	}
	return nil
}

// CleanTags trims and lower-cases tags, drops blanks and duplicates (keeping
// the first occurrence's order) and enforces the limits.
func CleanTags(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		if utf8.RuneCountInString(t) > maxTagLen {
			return nil, validationError("each tag must be at most 50 characters")
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > maxTags {
		return nil, validationError("at most 30 tags")
	}
	return out, nil
}

// --- helpers ---

type caller struct {
	householdID uuid.UUID
	// userID is nil when the identity is not a users row id (local dev).
	userID *uuid.UUID
}

// authorize returns the request's household (X-Household-ID aware, resolved
// by RequireAuth) after the role policy allows action. It writes the error
// response and returns false otherwise.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, action Action) (caller, bool) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing household context")
		return caller{}, false
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return caller{}, false
	}
	if !h.policy(claims.Role, action) {
		apierr.Forbidden(w, "your role cannot do this")
		return caller{}, false
	}
	c := caller{householdID: hid}
	if uid, err := uuid.Parse(claims.UserID); err == nil {
		c.userID = &uid
	}
	return c, true
}

// pathID parses a UUID path parameter. A malformed id is a 404: it can't
// name a row of this family.
func pathID(w http.ResponseWriter, r *http.Request, name, what string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		apierr.NotFound(w, what+" not found")
		return uuid.Nil, false
	}
	return id, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequest)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return false
	}
	return true
}

// writeErr maps repo/validation errors to responses. A subject from another
// family is a 23503 from derive_subject_property, which apierr.DB turns into
// 404 "referenced record not found".
func writeErr(w http.ResponseWriter, err error, what string) {
	var ve validationError
	switch {
	case errors.As(err, &ve):
		apierr.BadRequest(w, ve.Error())
	case errors.Is(err, ErrNotFound):
		apierr.NotFound(w, what+" not found")
	default:
		apierr.DB(w, err)
	}
}

func queryUUID(w http.ResponseWriter, r *http.Request, name string) (*uuid.UUID, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, true
	}
	id, err := uuid.Parse(v)
	if err != nil {
		apierr.BadRequest(w, "invalid "+name)
		return nil, false
	}
	return &id, true
}

// --- handlers ---

// List returns the household's guides, alphabetically.
// GET /api/v1/guides?property_id=&asset_id=&vehicle_id=&tag=&subject=family
//
// property_id includes guides about the property's assets. subject=family
// returns only guides with no subject.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authorize(w, r, ActionRead)
	if !ok {
		return
	}
	var f ListFilter
	if f.PropertyID, ok = queryUUID(w, r, "property_id"); !ok {
		return
	}
	if f.AssetID, ok = queryUUID(w, r, "asset_id"); !ok {
		return
	}
	if f.VehicleID, ok = queryUUID(w, r, "vehicle_id"); !ok {
		return
	}
	f.Tag = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tag")))
	switch r.URL.Query().Get("subject") {
	case "":
	case "family":
		f.FamilyOnly = true
	default:
		apierr.BadRequest(w, "subject must be family")
		return
	}

	guides, err := h.repo.List(r.Context(), c.householdID, f)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	out := make([]guideResponse, 0, len(guides))
	for _, g := range guides {
		out = append(out, toGuideResponse(g))
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": out})
}

// Create adds a guide and its first revision.
// POST /api/v1/guides
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authorize(w, r, ActionWrite)
	if !ok {
		return
	}
	var req createRequest
	if !decodeBody(w, r, &req) {
		return
	}
	g := &Guide{HouseholdID: c.householdID, Body: req.Body}
	var err error
	if g.Title, err = cleanTitle(req.Title); err != nil {
		writeErr(w, err, "guide")
		return
	}
	if g.Summary, err = cleanSummary(req.Summary); err != nil {
		writeErr(w, err, "guide")
		return
	}
	if err = checkBody(g.Body); err != nil {
		writeErr(w, err, "guide")
		return
	}
	if g.Tags, err = CleanTags(req.Tags); err != nil {
		writeErr(w, err, "guide")
		return
	}
	if err := req.Fields.Apply(&g.Subject); err != nil {
		apierr.BadRequest(w, err.Error())
		return
	}

	created, err := h.repo.Create(r.Context(), g, c.userID)
	if err != nil {
		writeErr(w, err, "guide")
		return
	}
	h.index(r, created)
	apierr.JSON(w, http.StatusCreated, map[string]any{"data": toGuideResponse(created)})
}

// Get returns one guide.
// GET /api/v1/guides/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authorize(w, r, ActionRead)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id", "guide")
	if !ok {
		return
	}
	g, err := h.repo.Get(r.Context(), c.householdID, id)
	if err != nil {
		writeErr(w, err, "guide")
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toGuideResponse(g)})
}

// Update changes a guide. Every save writes a revision in the same
// transaction.
// PUT /api/v1/guides/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authorize(w, r, ActionWrite)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id", "guide")
	if !ok {
		return
	}
	var req updateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	note, err := cleanChangeNote(req.ChangeNote)
	if err != nil {
		writeErr(w, err, "guide")
		return
	}

	saved, err := h.repo.Update(r.Context(), c.householdID, id, c.userID, note, func(g *Guide) error {
		var err error
		if req.Title != nil {
			if g.Title, err = cleanTitle(*req.Title); err != nil {
				return err
			}
		}
		if req.Summary != nil {
			if g.Summary, err = cleanSummary(req.Summary); err != nil {
				return err
			}
		}
		if req.Body != nil {
			if err = checkBody(*req.Body); err != nil {
				return err
			}
			g.Body = *req.Body
		}
		if req.Tags != nil {
			if g.Tags, err = CleanTags(*req.Tags); err != nil {
				return err
			}
		}
		if err := req.Fields.Apply(&g.Subject); err != nil {
			return validationError(err.Error())
		}
		return nil
	})
	if err != nil {
		writeErr(w, err, "guide")
		return
	}
	h.index(r, saved)
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toGuideResponse(saved)})
}

// Delete removes a guide and its search document.
// DELETE /api/v1/guides/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authorize(w, r, ActionWrite)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id", "guide")
	if !ok {
		return
	}
	if err := h.repo.Delete(r.Context(), c.householdID, id); err != nil {
		writeErr(w, err, "guide")
		return
	}
	h.unindex(r, id)
	w.WriteHeader(http.StatusNoContent)
}

// ListRevisions returns a guide's revisions, newest first, without bodies.
// GET /api/v1/guides/{id}/revisions
func (h *Handler) ListRevisions(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authorize(w, r, ActionRead)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id", "guide")
	if !ok {
		return
	}
	revs, err := h.repo.ListRevisions(r.Context(), c.householdID, id)
	if err != nil {
		writeErr(w, err, "guide")
		return
	}
	out := make([]revisionResponse, 0, len(revs))
	for _, v := range revs {
		out = append(out, toRevisionResponse(v, false))
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": out})
}

// GetRevision returns one revision with its body.
// GET /api/v1/guides/{id}/revisions/{revisionId}
func (h *Handler) GetRevision(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authorize(w, r, ActionRead)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id", "guide")
	if !ok {
		return
	}
	revID, ok := pathID(w, r, "revisionId", "revision")
	if !ok {
		return
	}
	v, err := h.repo.GetRevision(r.Context(), c.householdID, id, revID)
	if err != nil {
		writeErr(w, err, "revision")
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toRevisionResponse(v, true)})
}

// Restore makes an old revision the guide's body again, as a new revision.
// POST /api/v1/guides/{id}/revisions/{revisionId}/restore
func (h *Handler) Restore(w http.ResponseWriter, r *http.Request) {
	c, ok := h.authorize(w, r, ActionWrite)
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id", "guide")
	if !ok {
		return
	}
	revID, ok := pathID(w, r, "revisionId", "revision")
	if !ok {
		return
	}
	saved, err := h.repo.Restore(r.Context(), c.householdID, id, revID, c.userID)
	if err != nil {
		writeErr(w, err, "revision")
		return
	}
	h.index(r, saved)
	apierr.JSON(w, http.StatusOK, map[string]any{"data": toGuideResponse(saved)})
}
