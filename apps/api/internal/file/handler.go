// Handler implementations for the file storage module. These are the REST API
// endpoints the UI calls for file upload, listing, viewing, and deletion.
//
// All handlers sit behind the RequireAuth middleware (registered in main.go by
// task #1202) and extract the caller's household_id from the JWT claims via
// middleware.ClaimsFromContext — every query is household-scoped so one tenant
// can never read another tenant's files.
//
// The handler depends on the Repo defined in repo.go (task #1200). The Repo
// method signatures the handlers call:
//
//	CreateFile(ctx, householdID uuid.UUID, f *File, data []byte) (*File, error)
//	    Inserts a file_blobs row (data) and a files row (f) in one transaction.
//	    f.Name/ContentType/Size/EntityType/EntityID are set by the handler; the
//	    household_id comes from the JWT. Returns the fully-populated File.
//	GetFile(ctx, householdID, id uuid.UUID) (*File, error)
//	    Metadata only — never loads the BYTEA payload. Returns nil if not found.
//	GetFileContent(ctx, householdID, id uuid.UUID) ([]byte, error)
//	    Raw blob bytes for the given FILE id (the repo resolves blob_id from
//	    the household-scoped files row internally). Returns (nil,nil) if absent.
//	ListFiles(ctx, householdID uuid.UUID, entityType string, entityID uuid.UUID) ([]*File, error)
//	    When entityType == "" / entityID == uuid.Nil the entity filter is
//	    ignored and all household files are returned.
//	UpdateFile(ctx, householdID, id uuid.UUID, p Patch) (*File, error)
//	    PATCH for name, tags and the 048 document metadata. Returns nil if
//	    not found.
//
// Document metadata (#4478, migration 048): upload takes optional form
// fields title, category, document_date, expires_on, description and
// person_id; PATCH takes the same keys as JSON, where null clears a field
// (category cannot be cleared). Dates are "YYYY-MM-DD". Each create/PATCH
// also upserts the file's metadata document into household_search (see
// search.go) so hybrid search matches on the metadata; delete removes it.
//
// Role rules (#4689): the route policy already limits who reaches these
// handlers (files are hidden from housesitter and vendor). On top of that a
// role that cannot read credentials (viewer) never sees a file attached to
// a secret or linked from one (document_links, #4775; the MCP's rule): it
// is omitted from lists and 404 on get/content/patch, the same answer as a
// file that does not exist (Repo.GetVisibleFile, secretFileWhere).
package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
	"home-os/api/pkg/apierr"
	"home-os/api/pkg/jsonopt"
)

// maxUploadBytes caps the size of a single upload request body. The product
// spec is 25 MB; http.MaxBytesReader enforces this before the body is parsed,
// so an oversized upload is rejected early without buffering the whole stream.
const maxUploadBytes = 25 << 20 // 25 MB

// searchDeleteTimeout bounds the immediate Typesense delete after a file
// delete. It is short because the delete is queued anyway (migration 053) and
// the worker retries it; a slow Typesense must not slow the request.
const searchDeleteTimeout = 5 * time.Second

// searchIndexTimeout bounds the metadata document upsert after a create or
// PATCH. Indexing failures are logged, never fail the request; cmd/reindex
// rebuilds every document.
const searchIndexTimeout = 10 * time.Second

// SearchIndex keeps search in step with files: the worker-owned files
// collection on delete, and the file's metadata document in
// household_search on create/PATCH/delete. *search.Client satisfies it.
type SearchIndex interface {
	DeleteFileDocuments(ctx context.Context, fileID string) error
	IndexDocument(ctx context.Context, doc search.SearchDocument) error
	DeleteDocument(ctx context.Context, id string) error
}

// Handler holds dependencies for file HTTP handlers.
type Handler struct {
	repo   *Repo
	search SearchIndex // nil: no immediate search delete (the worker still purges)
}

// NewHandler creates a new file handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

// WithSearch makes DeleteFile remove the file from Typesense right away.
// Without it a deleted file leaves search on the worker's next tick.
func (h *Handler) WithSearch(idx SearchIndex) *Handler {
	h.search = idx
	return h
}

// --- request / response types ---

// updateFileRequest is the JSON body for PATCH /api/v1/files/{id}. Every
// field is optional; omitted fields are left unchanged. The metadata fields
// accept null to clear (except category).
type updateFileRequest struct {
	Name         *string                  `json:"name,omitempty"`
	Tags         []string                 `json:"tags,omitempty"`
	Title        jsonopt.Field[string]    `json:"title"`
	Category     jsonopt.Field[string]    `json:"category"`
	DocumentDate jsonopt.Field[string]    `json:"document_date"`
	ExpiresOn    jsonopt.Field[string]    `json:"expires_on"`
	Description  jsonopt.Field[string]    `json:"description"`
	PersonID     jsonopt.Field[uuid.UUID] `json:"person_id"`
}

// canSeeSecretFiles reports whether the caller may see files attached to a
// credential: only roles with secrets read (not viewer).
func canSeeSecretFiles(r *http.Request) bool {
	claims := middleware.ClaimsFromContext(r.Context())
	return claims != nil && auth.Can(claims.Role, auth.ResSecrets, auth.ActRead)
}

// validDate checks a "YYYY-MM-DD" date.
func validDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

// normText trims s; blank becomes nil (048 rejects a blank title, and
// "no description" has one spelling too).
func normText(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	if t == "" {
		return nil
	}
	return &t
}

// validateMeta checks the metadata a client sent. Empty strings mean "not
// given" for upload form fields.
func validateMeta(category string, docDate, expires *string) string {
	if category != "" && !ValidCategory(category) {
		return "invalid category: must be one of " + strings.Join(Categories, ", ")
	}
	if docDate != nil && !validDate(*docDate) {
		return "invalid document_date: use YYYY-MM-DD"
	}
	if expires != nil && !validDate(*expires) {
		return "invalid expires_on: use YYYY-MM-DD"
	}
	if docDate != nil && expires != nil && *expires < *docDate {
		return "expires_on must not be before document_date"
	}
	return ""
}

// formOpt returns a pointer to the trimmed form value, or nil when blank.
func formOpt(r *http.Request, key string) *string {
	v := r.FormValue(key)
	return normText(&v)
}

// indexDocument upserts f's metadata document into household_search. It
// is detached from the request (a client hang-up must not cancel it) and
// bounded; a failure is logged and the request still succeeds.
func (h *Handler) indexDocument(r *http.Request, f *File) {
	if h.search == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), searchIndexTimeout)
	defer cancel()
	person, err := h.repo.PersonName(ctx, f.HouseholdID, f.PersonID)
	if err != nil {
		slog.Warn("file saved but its person could not be read for search", "file_id", f.ID, "error", err)
	}
	if err := h.search.IndexDocument(ctx, SearchDocument(f, person)); err != nil {
		slog.Warn("file saved but its metadata is not yet searchable; run reindex",
			"file_id", f.ID, "error", err)
	}
}

// householdID extracts the household UUID from the JWT claims in the request
// context. RequireAuth must have run on the route; if claims are missing the
// request is rejected as forbidden.
func householdID(r *http.Request) (uuid.UUID, error) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		return uuid.Nil, apierr.ErrForbidden
	}
	return uuid.Parse(claims.HouseholdID)
}

// UploadFile accepts a multipart/form-data upload containing a "file" part
// plus "entity_type" and "entity_id" form fields, stores the blob + metadata,
// and returns the created file record.
//
// POST /api/v1/files/upload
func (h *Handler) UploadFile(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	// Cap the request body before parsing so an oversized upload is rejected
	// early. MaxBytesReader also causes ParseMultipartForm / FormFile to return
	// an error once the limit is crossed.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		apierr.BadRequest(w, "upload too large or invalid multipart form (max 25MB)")
		return
	}

	// With no entity the file belongs to the family: ('household', hid).
	entityType := r.FormValue("entity_type")
	entityIDStr := r.FormValue("entity_id")
	if entityType == "" && entityIDStr == "" {
		entityType = EntityTypeHousehold
	}
	if entityType == EntityTypeHousehold && entityIDStr == "" {
		entityIDStr = hid.String()
	}
	if entityType == "" {
		apierr.BadRequest(w, "entity_type is required")
		return
	}
	entityID, err := uuid.Parse(entityIDStr)
	if err != nil {
		apierr.BadRequest(w, "invalid entity_id")
		return
	}

	f, fh, err := r.FormFile("file")
	if err != nil {
		apierr.BadRequest(w, "file field is required")
		return
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if len(data) == 0 {
		apierr.BadRequest(w, "uploaded file is empty")
		return
	}

	// ContentType: prefer the multipart header's declared type; fall back to
	// octet-stream so the content endpoint always sends a usable Content-Type.
	contentType := fh.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	rec := &File{
		HouseholdID:  hid,
		Name:         fh.Filename,
		ContentType:  contentType,
		Size:         int64(len(data)),
		EntityType:   entityType,
		EntityID:     entityID,
		Title:        formOpt(r, "title"),
		Category:     strings.TrimSpace(r.FormValue("category")),
		DocumentDate: formOpt(r, "document_date"),
		ExpiresOn:    formOpt(r, "expires_on"),
		Description:  formOpt(r, "description"),
	}
	if msg := validateMeta(rec.Category, rec.DocumentDate, rec.ExpiresOn); msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	if p := formOpt(r, "person_id"); p != nil {
		pid, err := uuid.Parse(*p)
		if err != nil {
			apierr.BadRequest(w, "invalid person_id")
			return
		}
		rec.PersonID = &pid
	}

	// A person from another family fails 048's same-family FK (23503) and
	// answers 404 via apierr.DB, like any cross-family reference.
	created, err := h.repo.CreateFile(r.Context(), hid, rec, data)
	if err != nil {
		apierr.DB(w, err)
		return
	}
	h.indexDocument(r, created)

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": created,
	})
}

// ListFiles returns files for the authenticated household. Optional
// entity_type + entity_id query params filter to a specific entity;
// category and person_id narrow further. Without them every file in the
// household is returned (minus files attached to credentials for roles that
// cannot read credentials).
//
// GET /api/v1/files?entity_type=property&entity_id=<uuid>&category=tax&person_id=<uuid>
func (h *Handler) ListFiles(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	entityType := r.URL.Query().Get("entity_type")
	entityIDStr := r.URL.Query().Get("entity_id")
	var entityID uuid.UUID
	if entityType == EntityTypeHousehold && entityIDStr == "" {
		entityIDStr = hid.String()
	}
	if entityType != "" {
		if entityIDStr == "" {
			apierr.BadRequest(w, "entity_id is required when entity_type is provided")
			return
		}
		entityID, err = uuid.Parse(entityIDStr)
		if err != nil {
			apierr.BadRequest(w, "invalid entity_id")
			return
		}
	}
	// When entityType == "" the repo ignores the entity filter and returns all
	// household files (entityID is uuid.Nil / disregarded).
	flt := ListFilter{EntityType: entityType, EntityID: entityID}
	if c := r.URL.Query().Get("category"); c != "" {
		if !ValidCategory(c) {
			apierr.BadRequest(w, "invalid category: must be one of "+strings.Join(Categories, ", "))
			return
		}
		flt.Category = c
	}
	if p := r.URL.Query().Get("person_id"); p != "" {
		if flt.PersonID, err = uuid.Parse(p); err != nil {
			apierr.BadRequest(w, "invalid person_id")
			return
		}
	}
	if !canSeeSecretFiles(r) {
		if entityType == EntityTypeSecret {
			apierr.JSON(w, http.StatusOK, map[string]any{"data": []*File{}})
			return
		}
		flt.HideSecretFiles = true
	}

	files, err := h.repo.ListFiles(r.Context(), hid, flt)
	if err != nil {
		apierr.DB(w, err)
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": files,
	})
}

// GetFile returns a single file's metadata by ID (no blob bytes).
//
// GET /api/v1/files/{id}
func (h *Handler) GetFile(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid file id")
		return
	}

	fl, err := h.repo.GetVisibleFile(r.Context(), hid, id, canSeeSecretFiles(r))
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if fl == nil {
		apierr.NotFound(w, "file not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": fl,
	})
}

// GetFileContent streams the raw file bytes inline with the stored
// Content-Type so browsers can render images / PDFs / text directly. The
// metadata lookup (household-scoped) is done first to both authorize the
// request and resolve the stored content_type + filename; the blob bytes are
// then fetched via GetFileContent (which re-checks household ownership on the
// files row before reading the blob, so the auth check is enforced on both
// calls).
//
// GET /api/v1/files/{id}/content
func (h *Handler) GetFileContent(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid file id")
		return
	}

	fl, err := h.repo.GetVisibleFile(r.Context(), hid, id, canSeeSecretFiles(r))
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if fl == nil {
		apierr.NotFound(w, "file not found")
		return
	}

	// GetFileContent resolves blob_id from the household-scoped files row, so
	// pass the file id (not the blob id).
	data, err := h.repo.GetFileContent(r.Context(), hid, id)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if data == nil {
		// GetFileContent returns (nil, nil) when the files row is gone; we
		// already confirmed it exists above, so this indicates a race or a
		// missing blob — surface as 404 rather than leaking internals.
		apierr.NotFound(w, "file content not available")
		return
	}

	contentType := fl.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", contentType)
	// Defense-in-depth against stored XSS via the API: even though the
	// endpoint is bearer-token authed (no cookies to leak, no Authorization
	// header auto-attached on a browser navigation), the intended UI pattern
	// is fetch() -> response.blob() -> URL.createObjectURL -> render in an
	// <img>/<iframe>. A text/html upload rendered in an iframe would execute
	// JS in the app origin, and MIME sniffing can also turn a text/plain
	// upload into HTML. Two mitigations:
	//
	//   1. X-Content-Type-Options: nosniff — unconditionally, so the browser
	//      never second-guesses the declared Content-Type.
	//   2. Content-Disposition: attachment for everything except safe inline
	//      types (image/*, application/pdf). The browser will download those
	//      rather than rendering them in-page, blocking the iframe-render XSS
	//      vector even if a caller still manages to set text/html.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Sanitise the filename before placing it in the Content-Disposition
	// header. fl.Name is sourced from the multipart upload's fh.Filename,
	// which is fully attacker-controlled. A literal " or ; in the value
	// would break the HTTP quoted-string / disposition parameter grammar
	// (Go's net/http already strips CR/LF, so this is not header-splitting
	// — but it can confuse browser parsing). Strip quotes, semicolons, and
	// control characters; fall back to a generic name when nothing usable
	// remains.
	name := strings.Map(func(r rune) rune {
		if r == '"' || r == ';' || r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, fl.Name)
	if name == "" {
		name = "file"
	}
	disposition := "inline"
	if !strings.HasPrefix(contentType, "image/") && contentType != "application/pdf" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disposition, name))
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(data)), 10))
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// UpdateFile modifies an existing file's name and/or tags. Fields not present
// in the request body are left unchanged. Returns the updated metadata.
//
// PATCH /api/v1/files/{id}
func (h *Handler) UpdateFile(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid file id")
		return
	}

	var req updateFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	p := Patch{
		Name: req.Name, Tags: req.Tags,
		Title: req.Title, Category: req.Category, DocumentDate: req.DocumentDate,
		ExpiresOn: req.ExpiresOn, Description: req.Description, PersonID: req.PersonID,
	}
	// Reject an empty patch so the endpoint doesn't return a no-op 200 that
	// hides caller mistakes.
	if p.Empty() {
		apierr.BadRequest(w, "nothing to update: provide name, tags or document metadata")
		return
	}
	if p.Category.Set && p.Category.Value == nil {
		apierr.BadRequest(w, "category cannot be null; use \"other\"")
		return
	}
	p.Title.Value = normText(p.Title.Value)
	p.Description.Value = normText(p.Description.Value)
	var category string
	if p.Category.Value != nil {
		category = *p.Category.Value
		if category == "" {
			apierr.BadRequest(w, "invalid category: must be one of "+strings.Join(Categories, ", "))
			return
		}
	}
	// Only the dates sent are checked here; expires_on before an already
	// stored document_date is caught by 048's CHECK (23514 -> 400).
	if msg := validateMeta(category, p.DocumentDate.Value, p.ExpiresOn.Value); msg != "" {
		apierr.BadRequest(w, msg)
		return
	}

	// A file the caller may not see is absent: check before writing.
	cur, err := h.repo.GetVisibleFile(r.Context(), hid, id, canSeeSecretFiles(r))
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if cur == nil {
		apierr.NotFound(w, "file not found")
		return
	}

	updated, err := h.repo.UpdateFile(r.Context(), hid, id, p)
	if err != nil {
		apierr.DB(w, err)
		return
	}
	if updated == nil {
		apierr.NotFound(w, "file not found")
		return
	}
	h.indexDocument(r, updated)

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": updated,
	})
}

// DeleteFile removes a file and its underlying blob. Returns 204 on success.
//
// Deleted files must never stay searchable. Deleting the row queues the file
// in file_search_deletions (migration 053's trigger), which the worker drains
// into Typesense; after the commit the handler also deletes the file's
// documents directly, so it usually leaves search before the response. If
// Typesense is unreachable that is logged and the delete still succeeds; the
// queued row is retried by the worker until Typesense confirms.
//
// DELETE /api/v1/files/{id}
func (h *Handler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid file id")
		return
	}

	if err := h.repo.DeleteFile(r.Context(), hid, id); err != nil {
		// The repo contract is: returns pgx.ErrNoRows only when the file
		// does not exist or belongs to a different household. Any other
		// error is a wrapped DB failure (pool closed, ctx deadline,
		// connection lost) and must surface as a 500 so it is logged and
		// not silently dropped.
		if errors.Is(err, pgx.ErrNoRows) {
			apierr.NotFound(w, "file not found")
			return
		}
		apierr.InternalError(w, err)
		return
	}

	if h.search != nil {
		// Detached from the request so a client hang-up doesn't cancel it.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), searchDeleteTimeout)
		if err := h.search.DeleteFileDocuments(ctx, id.String()); err != nil {
			slog.Warn("file deleted but not yet removed from search; the worker will retry",
				"file_id", id, "error", err)
		}
		_, docIDs := SearchDocIDs(&File{ID: id})
		for _, docID := range docIDs {
			if err := h.search.DeleteDocument(ctx, docID); err != nil {
				slog.Warn("file deleted but its metadata document is still in search; run reindex",
					"file_id", id, "doc_id", docID, "error", err)
			}
		}
		cancel()
	}

	w.WriteHeader(http.StatusNoContent)
}
