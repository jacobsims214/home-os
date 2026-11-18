package note

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
	"home-os/api/internal/searchqueue"
	"home-os/api/pkg/apierr"
)

type searchIndexer interface {
	IndexDocument(ctx context.Context, doc search.SearchDocument) error
	DeleteDocument(ctx context.Context, id string) error
}

// FileChecker answers which of a household's file ids the caller may see;
// *file.Repo satisfies it (VisibleFileIDs, the credential rule shared with
// the files API and the MCP).
type FileChecker interface {
	VisibleFileIDs(ctx context.Context, householdID string, ids []string, includeSecretFiles bool) (map[string]bool, error)
}

// Handler holds dependencies for note HTTP handlers.
type Handler struct {
	repo   *Repo
	search searchIndexer
	files  FileChecker // nil: notes on files are hidden from roles without secrets read
}

// WithFileChecker lets roles without secrets read see notes on the files
// they may see; without it they see no notes on files at all (fail closed).
func (h *Handler) WithFileChecker(fc FileChecker) *Handler {
	h.files = fc
	return h
}

// hideSecretFileNotes drops, for a role without secrets read (viewer),
// notes on a file attached to or linked from a credential (#4775): the file
// is hidden from that role, so are its notes. A failed check drops every
// note on a file (fail closed).
func (h *Handler) hideSecretFileNotes(r *http.Request, hid uuid.UUID, notes []*Note) []*Note {
	if middleware.Can(r, auth.ResSecrets, auth.ActRead) {
		return notes
	}
	var ids []string
	for _, n := range notes {
		if n.EntityType == "file" {
			ids = append(ids, n.EntityID.String())
		}
	}
	if len(ids) == 0 {
		return notes
	}
	ok := map[string]bool{}
	if h.files != nil {
		var err error
		if ok, err = h.files.VisibleFileIDs(r.Context(), hid.String(), ids, false); err != nil {
			slog.Warn("notes: file visibility check failed, omitting notes on files", "error", err)
			ok = map[string]bool{}
		}
	}
	out := notes[:0:0]
	for _, n := range notes {
		if n.EntityType != "file" || ok[n.EntityID.String()] {
			out = append(out, n)
		}
	}
	return out
}

// noteVisible is CanSeeEntity plus the credential-file rule for one note.
func (h *Handler) noteVisible(r *http.Request, hid uuid.UUID, n *Note) bool {
	return auth.CanSeeEntity(middleware.Role(r), n.EntityType) &&
		len(h.hideSecretFileNotes(r, hid, []*Note{n})) == 1
}

// NewHandler creates a new note handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

func (h *Handler) WithSearchClient(sc *search.Client) *Handler {
	h.search = sc
	return h
}

func (h *Handler) indexNote(ctx context.Context, n *Note) {
	// Delegates to the one document builder (#5314). searchqueue.Document
	// reads the row and produces the same document the index queue's drain
	// writes, so a write made here and the same write made through an MCP tool
	// cannot index differently — and there is no second copy of the document
	// rules to drift. A failure is only logged: the trigger already queued the
	// row, so the drain retries it.
	if h.search == nil {
		return
	}
	if err := searchqueue.IndexNow(ctx, h.repo.pool, h.search, "note", n.ID.String()); err != nil {
		slog.Warn("note: indexed later by the queue", "id", n.ID, "error", err)
	}
}

func (h *Handler) deleteNoteIndex(ctx context.Context, id string) {
	if h.search == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("note: panic during search deletion", "panic", r)
		}
	}()
	if err := h.search.DeleteDocument(context.Background(), "note-"+id); err != nil {
		slog.Warn("note: failed to delete from search", "id", id, "error", err)
	}
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

// noteWriteResponse is what a note write returns: the note, plus the existing
// notes its title may be a rewrite of (#4564, the API half of #4945). The
// note is always written; possible_duplicates is a prompt, never a gate.
// `data` is the note itself, unchanged, so a client that only reads `data`
// keeps working.
type noteWriteResponse struct {
	Data               noteResponse         `json:"data"`
	PossibleDuplicates []DuplicateCandidate `json:"possible_duplicates"`
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
		// A record the role may not see has no notes it can see (#4689).
		if !auth.CanSeeEntity(middleware.Role(r), entityType) {
			apierr.NotFound(w, "entity not found")
			return
		}
		notes, err := h.repo.ListByEntity(r.Context(), hid, entityType, entityID)
		if err != nil {
			apierr.DB(w, err)
			return
		}
		notes = h.hideSecretFileNotes(r, hid, notes)
		apierr.JSON(w, http.StatusOK, map[string]any{"data": toNoteResponses(notes)})
		return
	}

	notes, err := h.repo.ListByHousehold(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	notes = h.hideSecretFileNotes(r, hid, visibleNotes(middleware.Role(r), notes))

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
	if note == nil || !h.noteVisible(r, hid, note) {
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

	// A family-level note is about the family itself: ('household', <its id>).
	if req.EntityType == "household" && req.EntityID == "" {
		req.EntityID = hid.String()
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

	// Household role policy (#4689): family roles note anything they can
	// see; a housesitter only pets. A record the role cannot see is 404.
	role := middleware.Role(r)
	if !auth.CanSeeEntity(role, req.EntityType) {
		apierr.NotFound(w, "entity not found")
		return
	}
	if !auth.CanAnnotateEntity(role, req.EntityType) {
		apierr.Forbidden(w, "the "+role+" role cannot add notes on "+req.EntityType+" records")
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

	// The owner trigger (migration 038) rejects an entity that is missing or
	// belongs to another family; apierr.DB turns that into a 404.
	created, err := h.repo.Create(r.Context(), n)
	if err != nil {
		apierr.DB(w, err)
		return
	}

	h.indexNote(r.Context(), created)

	// #4564: a note whose title is close to an existing one says so, so the
	// person writing it can make it an update instead of a contradictory twin
	// (#4945's war-game failure, now visible in the web UI too). The scan
	// follows the role policy and never fails the write.
	// The scan runs after the insert, so the note must exclude itself: it is
	// already in the table and would otherwise match its own title at 1.0.
	duplicates := []DuplicateCandidate{}
	if created.Title != nil && strings.TrimSpace(*created.Title) != "" {
		duplicates = h.repo.NearDuplicates(r.Context(), hid, role, *created.Title, created.ID)
	}

	apierr.JSON(w, http.StatusCreated, noteWriteResponse{
		Data:               toNoteResponse(created),
		PossibleDuplicates: duplicates,
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
	if existing == nil || !h.noteVisible(r, hid, existing) {
		apierr.NotFound(w, "note not found")
		return
	}
	if !middleware.Can(r, auth.ResNotes, auth.ActWrite) {
		apierr.Forbidden(w, middleware.DenyMessage(middleware.Role(r), auth.ResNotes, auth.ActWrite))
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

	// Only a supplied title is checked (#4945): a rename is the only change
	// that can introduce a new collision, and re-warning on every body edit is
	// the noise that teaches a reader to skip the prompt.
	duplicates := []DuplicateCandidate{}
	if req.Title != nil && updated.Title != nil && strings.TrimSpace(*updated.Title) != "" {
		duplicates = h.repo.NearDuplicates(r.Context(), hid, middleware.Role(r), *updated.Title, id)
	}

	apierr.JSON(w, http.StatusOK, noteWriteResponse{
		Data:               toNoteResponse(updated),
		PossibleDuplicates: duplicates,
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

	h.deleteNoteIndex(r.Context(), id.String())

	w.WriteHeader(http.StatusNoContent)
}

// visibleNotes drops notes on records the role may not see (#4689): a
// viewer sees no notes on secrets, a housesitter only notes on pets,
// properties, rooms, assets, vendors, calendar, lists, meals and guides.
func visibleNotes(role string, notes []*Note) []*Note {
	out := notes[:0:0]
	for _, n := range notes {
		if auth.CanSeeEntity(role, n.EntityType) {
			out = append(out, n)
		}
	}
	return out
}
