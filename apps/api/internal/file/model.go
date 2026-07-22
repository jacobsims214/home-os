// Package file manages native file storage for the Home OS. File blobs (raw
// bytes) are stored separately from file metadata to keep query performance
// high — file_blobs holds the BYTEA payload while files holds the metadata,
// polymorphic entity association, OCR status, and tags. Both tables live in a
// single Postgres store so that writes, reads, and cleanup share one
// transactional boundary.
package file

import (
	"time"

	"github.com/google/uuid"
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
// values used by the notes system and other polymorphic modules — keep them
// in sync with apps/api/internal/seed/demo.go and the note/calendar repos.
const (
	EntityTypeProperty    = "property"
	EntityTypeVehicle     = "vehicle"
	EntityTypePet         = "pet"
	EntityTypeBill        = "bill"
	EntityTypeNote        = "note"
	EntityTypeAsset       = "asset"
	EntityTypeMaintenance = "maintenance"
	EntityTypeVendor      = "vendor"
)

// File represents a row from the files table — metadata for a stored blob
// plus its polymorphic entity association, OCR state, and tags.
// Columns match migration 023_create_files.up.sql.
type File struct {
	ID            uuid.UUID  `json:"id"`
	HouseholdID   uuid.UUID  `json:"household_id"`
	BlobID        uuid.UUID  `json:"blob_id"`
	Name          string     `json:"name"`
	ContentType   string     `json:"content_type"`
	Size          int64      `json:"size"`
	EntityType    string     `json:"entity_type"`
	EntityID      uuid.UUID  `json:"entity_id"`
	ExtractedText *string    `json:"extracted_text,omitempty"`
	OCRStatus     string     `json:"ocr_status"`
	OCRError      *string    `json:"ocr_error,omitempty"`
	Tags          []string   `json:"tags"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
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
