// Package files provides the worker's direct read/write access to the
// files and file_blobs tables for the asynchronous OCR processing job.
//
// This package is intentionally separate from apps/api/internal/file
// (the API's file repository). Go's internal-package rule means the
// worker — rooted at apps/worker, not apps/api — cannot import that
// package, and the two services have different access patterns anyway:
// the API is household-scoped and tenant-isolating (every query filters
// by household_id to prevent cross-household reads), while the worker
// is a trusted internal service that processes pending files across
// ALL households on every tick. Sharing the API repo would either leak
// the cross-household scan into a tenant-isolated package or require
// a householdID parameter that the worker has no business enforcing.
//
// The schema (migration 023_create_files.up.sql) is shared, so the OCR
// status string constants here mirror apps/api/internal/file/model.go
// exactly. If you change one, change the other.
package files

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EntityName lookups. When a file is attached to an entity (entity_type /
// entity_id), the worker denormalizes that entity's human-readable name into
// Typesense so search results can say "attached to <entity>" without the
// search API joining against every possible entity table. The map below
// defines, per entity_type, the SELECT used to resolve the name and the
// column it returns. The query must take exactly one $1 (the entity_id).
//
// Mirrors the EntityType constants in apps/api/internal/file/model.go. If you
// add a new attachable entity type there, add a query here too.
var entityNameQueries = map[string]string{
	"property":    "SELECT name FROM properties WHERE id = $1",
	"vehicle":     "SELECT CONCAT_WS(' ', NULLIF(year::text, ''), make, model) FROM vehicles WHERE id = $1",
	"pet":         "SELECT name FROM pets WHERE id = $1",
	"bill":        "SELECT name FROM bills WHERE id = $1",
	"maintenance": "SELECT name FROM maintenance_tasks WHERE id = $1",
	"asset":       "SELECT name FROM assets WHERE id = $1",
	"vendor":      "SELECT name FROM vendors WHERE id = $1",
}

// OCR status constants. These mirror apps/api/internal/file/model.go
// exactly — the same string values are stored in files.ocr_status. The
// DB default on insert is OCRStatusPending.
const (
	OCRStatusPending    = "pending"
	OCRStatusProcessing = "processing"
	OCRStatusDone       = "done"
	OCRStatusFailed     = "failed"
	OCRStatusSkipped    = "skipped"
)

// PendingFile is the minimal row the worker needs from the files table
// to process a file: the file id, the household that owns it (passed
// through for logging and any future per-household throttling), and the
// MIME type used to decide whether to call Tika at all (video/audio are
// skipped without a network round-trip).
type PendingFile struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	ContentType string
}

// Repo is the worker's read/write handle on the files and file_blobs
// tables. It wraps a *pgxpool.Pool (the same pool the rest of the worker
// uses) and is safe for concurrent use — pgxpool connections are pooled
// and each Query/Exec borrows one.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo returns a files Repo backed by the given pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// ListPending returns up to limit files whose ocr_status is 'pending',
// across ALL households, ordered by created_at so older uploads are
// processed first (a simple FIFO that bounds worst-case latency under
// load). The worker is a trusted cross-household service; it is the
// only caller that should scan pending files without a household filter.
//
// An empty (non-nil) slice is returned when no files are pending.
func (r *Repo) ListPending(ctx context.Context, limit int) ([]PendingFile, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, COALESCE(content_type, '')
		 FROM files
		 WHERE ocr_status = $1
		 ORDER BY created_at ASC
		 LIMIT $2`,
		OCRStatusPending, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("files: list pending: %w", err)
	}
	defer rows.Close()

	out := make([]PendingFile, 0, limit)
	for rows.Next() {
		var pf PendingFile
		if err := rows.Scan(&pf.ID, &pf.HouseholdID, &pf.ContentType); err != nil {
			return nil, fmt.Errorf("files: scan pending row: %w", err)
		}
		out = append(out, pf)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("files: iterate pending rows: %w", err)
	}
	return out, nil
}

// GetBlob returns the raw bytes of the blob backing the given file.
// It joins files -> file_blobs on blob_id so the worker can fetch bytes
// by file id alone (it already knows the file id from ListPending).
//
// Returns (nil, pgx.ErrNoRows) if the file or its blob is missing — the
// processor treats that as a per-file failure (marks the file failed)
// rather than crashing the loop, since a missing blob is a data
// integrity problem for that one row, not a reason to stop processing
// the rest of the batch.
func (r *Repo) GetBlob(ctx context.Context, fileID uuid.UUID) ([]byte, error) {
	var data []byte
	err := r.pool.QueryRow(ctx,
		`SELECT b.data
		 FROM files f
		 JOIN file_blobs b ON b.id = f.blob_id
		 WHERE f.id = $1`,
		fileID,
	).Scan(&data)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("files: blob for file %s missing: %w", fileID, err)
		}
		return nil, fmt.Errorf("files: get blob: %w", err)
	}
	return data, nil
}

// UpdateOCRStatus sets ocr_status for a single file, optionally writing
// the extracted text and/or an error message. Pass nil for extractedText
// to leave the column unchanged (useful when transitioning to
// 'processing' before any text exists); pass a *string to set/overwrite.
// Pass nil for ocrError to clear any previously recorded error.
//
// Unlike apps/api/internal/file.Repo.UpdateOCRStatus this method is NOT
// scoped by household_id — the worker processes files across all
// households and trusts the file id it got from ListPending.
//
// Returns pgx.ErrNoRows if the file no longer exists (e.g. it was
// deleted between the ListPending tick and this update). The processor
// logs and drops such files rather than treating them as failures.
func (r *Repo) UpdateOCRStatus(ctx context.Context, fileID uuid.UUID, ocrStatus string, extractedText *string, ocrError *string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE files
		 SET ocr_status = $1,
		     extracted_text = COALESCE($2, extracted_text),
		     ocr_error = $3,
		     updated_at = NOW()
		 WHERE id = $4`,
		ocrStatus, extractedText, ocrError, fileID,
	)
	if err != nil {
		return fmt.Errorf("files: update ocr status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// IndexableFile is the slice of a files row the worker needs to build a
// Typesense FileDocument after OCR completes: the file's own identity plus the
// polymorphic attach target and tags. extracted_text is intentionally absent —
// the processor already has it in memory from Tika and passes it through to
// the indexer separately, avoiding a second read of a potentially-large TEXT
// column that the processor already wrote.
type IndexableFile struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	Name        string
	EntityType  string
	EntityID    uuid.UUID
	Tags        []string
	CreatedAt   time.Time
}

// GetForIndex returns the metadata needed to index a file in Typesense after
// OCR. It is NOT scoped by household_id — the worker already proved it owns
// the row via ListPending (which scans across all households by design), and
// re-checking household ownership here would just duplicate that trust
// boundary.
//
// Returns (nil, nil) — not pgx.ErrNoRows — if the file no longer exists, so
// the processor can treat a vanished file as "nothing to index" without a
// special-case error branch. This mirrors the not-found contract of the API's
// GetFile rather than the UpdateOCRStatus ErrNoRows contract, because the
// caller is a read path, not a write path.
func (r *Repo) GetForIndex(ctx context.Context, fileID uuid.UUID) (*IndexableFile, error) {
	var (
		f          IndexableFile
		entityType *string
		entityID   *uuid.UUID
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, household_id, name, entity_type, entity_id, tags, created_at
		 FROM files
		 WHERE id = $1`,
		fileID,
	).Scan(&f.ID, &f.HouseholdID, &f.Name, &entityType, &entityID, &f.Tags, &f.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("files: get for index: %w", err)
	}
	if entityType != nil {
		f.EntityType = *entityType
	}
	if entityID != nil {
		f.EntityID = *entityID
	}
	if f.Tags == nil {
		f.Tags = []string{}
	}
	return &f, nil
}

// GetEntityName resolves the human-readable name of the entity a file is
// attached to, used to populate the denormalized entity_name field in the
// Typesense document. Returns ("", nil) when entityType is empty/unknown or
// entityID is nil — a file may legitimately have no attach target, and an
// unknown entity type is treated the same way (the file is still indexed,
// just without an entity_name). A NULL result from a known table (the entity
// was deleted) is also treated as empty rather than an error.
//
// The worker is a trusted cross-household service; the entity_id was read
// from a household-scoped files row, so no household filter is needed here.
func (r *Repo) GetEntityName(ctx context.Context, entityType string, entityID uuid.UUID) (string, error) {
	if entityType == "" || entityID == uuid.Nil {
		return "", nil
	}
	query, ok := entityNameQueries[entityType]
	if !ok {
		// Unknown attach type — index the file without an entity_name rather
		// than failing the whole indexing step. The file is still searchable
		// by name and extracted_text.
		return "", nil
	}

	var name *string
	err := r.pool.QueryRow(ctx, query, entityID).Scan(&name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The entity was deleted but the files row still references it —
			// a referential-integrity gap. Index with an empty entity_name;
			// the dangling file is a data problem for another day.
			return "", nil
		}
		return "", fmt.Errorf("files: get entity name (%s): %w", entityType, err)
	}
	if name == nil {
		return "", nil
	}
	return *name, nil
}
