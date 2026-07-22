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
//	UpdateFile(ctx, householdID, id uuid.UUID, name *string, tags []string) (*File, error)
//	    PATCH for name and/or tags. nil name leaves name unchanged; a non-nil
//	    tags slice replaces the tags array. Returns nil if not found.
package file

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// maxUploadBytes caps the size of a single upload request body. The product
// spec is 25 MB; http.MaxBytesReader enforces this before the body is parsed,
// so an oversized upload is rejected early without buffering the whole stream.
const maxUploadBytes = 25 << 20 // 25 MB

// Handler holds dependencies for file HTTP handlers.
type Handler struct {
	repo *Repo
}

// NewHandler creates a new file handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

// --- request / response types ---

// updateFileRequest is the JSON body for PATCH /api/v1/files/{id}. Both fields
// are optional (pointer / nil-tagged) so a caller can update just tags, just
// name, or both. Omitted fields are left unchanged.
type updateFileRequest struct {
	Name *string  `json:"name,omitempty"`
	Tags []string `json:"tags,omitempty"`
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

	entityType := r.FormValue("entity_type")
	entityIDStr := r.FormValue("entity_id")
	if entityType == "" {
		apierr.BadRequest(w, "entity_type is required")
		return
	}
	if entityIDStr == "" {
		apierr.BadRequest(w, "entity_id is required")
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
		HouseholdID: hid,
		Name:        fh.Filename,
		ContentType: contentType,
		Size:        int64(len(data)),
		EntityType:  entityType,
		EntityID:    entityID,
	}

	created, err := h.repo.CreateFile(r.Context(), hid, rec, data)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": created,
	})
}

// ListFiles returns files for the authenticated household. Optional
// entity_type + entity_id query params filter to a specific entity; without
// them every file in the household is returned.
//
// GET /api/v1/files?entity_type=property&entity_id=<uuid>
func (h *Handler) ListFiles(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	entityType := r.URL.Query().Get("entity_type")
	entityIDStr := r.URL.Query().Get("entity_id")
	var entityID uuid.UUID
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

	files, err := h.repo.ListFiles(r.Context(), hid, entityType, entityID)
	if err != nil {
		apierr.InternalError(w, err)
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

	fl, err := h.repo.GetFile(r.Context(), hid, id)
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

	fl, err := h.repo.GetFile(r.Context(), hid, id)
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

	// Reject an empty patch so the endpoint doesn't return a no-op 200 that
	// hides caller mistakes.
	if req.Name == nil && req.Tags == nil {
		apierr.BadRequest(w, "nothing to update: provide name and/or tags")
		return
	}

	updated, err := h.repo.UpdateFile(r.Context(), hid, id, req.Name, req.Tags)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if updated == nil {
		apierr.NotFound(w, "file not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": updated,
	})
}

// DeleteFile removes a file and its underlying blob. Returns 204 on success.
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

	w.WriteHeader(http.StatusNoContent)
}
