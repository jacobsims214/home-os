// Package file manages native file storage for the Home OS. File blobs (raw
// bytes) are stored separately from file metadata to keep query performance
// high — file_blobs holds the BYTEA payload while files holds the metadata,
// polymorphic entity association, OCR status, and tags. Both tables live in a
// single Postgres store so that writes, reads, and cleanup share one
// transactional boundary.
package file

import (
	"bytes"
	"crypto/sha256"
	"time"

	"github.com/google/uuid"

	"home-os/api/pkg/jsonopt"
)

// OCRStatus constants track the lifecycle of text extraction (OCR) for a file.
// Stored as plain text in the files.ocr_status column. The DB default is
// 'pending' on insert; the API updates the value as extraction progresses.
const (
	// OCRStatusPending means extraction has not yet been attempted.
	OCRStatusPending = "pending"
	// OCRStatusProcessing means extraction is currently in flight.
	OCRStatusProcessing = "processing"
	// OCRStatusDone means extraction completed and extracted_text is populated.
	OCRStatusDone = "done"
	// OCRStatusFailed means extraction errored; see ocr_error for details.
	OCRStatusFailed = "failed"
	// OCRStatusSkipped means extraction was skipped (e.g. file type not
	// OCR-able like an image with no text, or an unsupported content type).
	OCRStatusSkipped = "skipped"
)

// EntityType constants enumerate the polymorphic entity kinds that a file can
// be attached to via files.entity_type + files.entity_id. These mirror the
// values of the entity_kind domain (migration 038), which the database
// enforces; the owner trigger also rejects another family's entity.
const (
	EntityTypeProperty    = "property"
	EntityTypeVehicle     = "vehicle"
	EntityTypePet         = "pet"
	EntityTypeBill        = "bill"
	EntityTypeNote        = "note"
	EntityTypeAsset       = "asset"
	EntityTypeMaintenance = "maintenance_task"
	EntityTypeVendor      = "vendor"
	// EntityTypeHousehold marks a family-level file: entity_id is the
	// household's own id. It is the default when no entity is given.
	EntityTypeHousehold = "household"
)

// File represents a row from the files table — metadata for a stored blob
// plus its polymorphic entity association, OCR state, and tags.
// Columns match migration 023_create_files.up.sql.
type File struct {
	ID            uuid.UUID `json:"id"`
	HouseholdID   uuid.UUID `json:"household_id"`
	BlobID        uuid.UUID `json:"blob_id"`
	Name          string    `json:"name"`
	ContentType   string    `json:"content_type"`
	Size          int64     `json:"size"`
	EntityType    string    `json:"entity_type"`
	EntityID      uuid.UUID `json:"entity_id"`
	ExtractedText *string   `json:"extracted_text,omitempty"`
	OCRStatus     string    `json:"ocr_status"`
	OCRError      *string   `json:"ocr_error,omitempty"`
	Tags          []string  `json:"tags"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`

	// Document metadata (migration 048). Dates are "YYYY-MM-DD" strings:
	// the columns are DATE and the repo selects them with to_char, so no
	// time zone ever shifts a receipt to the previous day.
	Title        *string    `json:"title"`
	Category     string     `json:"category"`
	DocumentDate *string    `json:"document_date"`
	ExpiresOn    *string    `json:"expires_on"`
	Description  *string    `json:"description"`
	PersonID     *uuid.UUID `json:"person_id"`

	// Provenance of ExtractedText (migration 072, #4941): "raw" when the body
	// is the text that was pasted, stored unaltered (the source a recorded
	// amount can be checked against), "extracted" when the worker read it
	// from the blob. TextSHA256 is the SHA-256 of a raw body as filed.
	TextOrigin string `json:"text_origin"`
	TextSHA256 []byte `json:"-"`

	// TextVerified is set for a "raw" body whose stored text still hashes to
	// TextSHA256: the body is byte-for-byte what was filed. False means the
	// body changed after filing (a re-typed or edited body), which is the
	// signal the war game's false "verified" lacked. Nil for derived text,
	// which carries no fingerprint and so can claim nothing (#4941).
	TextVerified *bool `json:"text_verified,omitempty"`

	// UploadedByName is the display name of the member who uploaded the file
	// (files.created_by, migration 050). Nil when the uploader's account is
	// gone or the row predates authorship tracking. Read-only: filled by the
	// repo's select list, never written.
	UploadedByName *string `json:"uploaded_by_name,omitempty"`
}

// TextOrigin values (migration 072's files_text_origin_check).
const (
	// TextOriginRaw is the caller's own pasted text, stored unaltered.
	TextOriginRaw = "raw"
	// TextOriginExtracted is text the worker read from the blob: derived.
	TextOriginExtracted = "extracted"
)

// finalize fills the derived, read-only fields of a file: whether a raw body
// still matches its fingerprint. Every repo read calls it, so the API never
// reports a body as filed when it is not (#4941's false "verified").
func (f *File) finalize() {
	if f == nil {
		return
	}
	if f.TextOrigin != TextOriginRaw || len(f.TextSHA256) == 0 {
		// Derived text carries no fingerprint, so it can claim nothing.
		f.TextVerified = nil
		return
	}
	body := ""
	if f.ExtractedText != nil {
		body = *f.ExtractedText
	}
	sum := sha256.Sum256([]byte(body))
	ok := bytes.Equal(sum[:], f.TextSHA256)
	f.TextVerified = &ok
}

// finalizeAll is finalize for a list.
func finalizeAll(files []*File) []*File {
	for _, f := range files {
		f.finalize()
	}
	return files
}

// Categories is the allowed set of files.category values (048's
// files_category_check). CategoryOther is the column default.
var Categories = []string{
	"receipt", "tax", "insurance", "medical", "manual", "warranty",
	"school", "legal", "vehicle", "home", CategoryOther,
}

// CategoryOther is the default category for a file uploaded without one.
const CategoryOther = "other"

// ValidCategory reports whether c is one of Categories.
func ValidCategory(c string) bool {
	for _, v := range Categories {
		if v == c {
			return true
		}
	}
	return false
}

// EntityTypeSecret is a file attached to a credential. Such files are
// hidden from roles that cannot read credentials (viewer, #4689).
const EntityTypeSecret = "secret"

// DisplayTitle is the title the UI and search show: the metadata title, or
// the uploaded file name when there is none.
func (f *File) DisplayTitle() string {
	if f.Title != nil && *f.Title != "" {
		return *f.Title
	}
	return f.Name
}

// FileBlob represents a row from the file_blobs table — the raw byte payload
// for a stored file, kept separate from files metadata for query performance.
// Columns match migration 023_create_files.up.sql.
type FileBlob struct {
	ID        uuid.UUID `json:"id"`
	Data      []byte    `json:"-"` // raw bytes; never serialized to JSON
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// Patch is a PATCH /files/{id} update. Name and Tags keep their original
// semantics (nil = unchanged, tags replace wholesale); the metadata fields
// use jsonopt.Field so a client can clear them with null. Category cannot be cleared
// (the column is NOT NULL): null is rejected by the handler.
type Patch struct {
	Name         *string
	Tags         []string
	Title        jsonopt.Field[string]
	Category     jsonopt.Field[string]
	DocumentDate jsonopt.Field[string]
	ExpiresOn    jsonopt.Field[string]
	Description  jsonopt.Field[string]
	PersonID     jsonopt.Field[uuid.UUID]
}

// Empty reports whether the patch changes nothing.
func (p *Patch) Empty() bool {
	return p.Name == nil && p.Tags == nil && !p.Title.Set && !p.Category.Set &&
		!p.DocumentDate.Set && !p.ExpiresOn.Set && !p.Description.Set && !p.PersonID.Set
}

// ListFilter narrows ListFiles. Zero values mean "no filter".
type ListFilter struct {
	EntityType string
	EntityID   uuid.UUID
	Category   string
	PersonID   uuid.UUID
	// HideSecretFiles leaves out files attached to or linked from a
	// credential (for roles without secrets read; see secretFileWhere).
	HideSecretFiles bool
}
