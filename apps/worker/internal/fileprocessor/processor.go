// Package fileprocessor runs the asynchronous OCR job: a polling loop
// that every FilePollInterval queries the files table for ocr_status=
// 'pending', fetches each pending file's blob, calls the Tika sidecar
// to extract text, and writes the result back to the files row.
//
// The processor is designed to be the only writer of ocr_status for
// pending files. Upload handlers insert files with ocr_status='pending'
// and return immediately to the UI; the processor picks them up on the
// next tick and transitions them to done/failed/skipped. It never
// crashes the worker on a per-file error — a bad file, a missing blob,
// or a Tika outage for one content type is logged and the row is marked
// failed so the next tick can move on to other files.
//
// Graceful shutdown: Run blocks until ctx is cancelled (SIGINT/SIGTERM).
// An in-flight processFile call is allowed to complete; the next tick
// is not started. This bounds shutdown latency to one Tika call
// (DefaultTimeout = 60s).
package fileprocessor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"home-os/worker/internal/files"
	"home-os/worker/internal/search"
	"home-os/worker/internal/tika"
)

// DefaultInterval is how often the processor polls for pending files
// when no override is supplied. The task spec calls for 10s; this keeps
// end-to-end OCR latency (upload -> extracted text visible in the UI)
// bounded to roughly interval + one Tika call.
const DefaultInterval = 10 * time.Second

// DefaultBatchLimit is the max number of pending files processed per
// tick. Bounded so a single tick can never monopolise the worker for
// minutes if thousands of files are uploaded at once — each tick makes
// progress and yields back to the scheduler.
const DefaultBatchLimit = 50

// Processor runs the file OCR polling loop. Construct one with New and
// run it with Run(ctx). A Processor is safe to run on a single goroutine
// only; do not call Run concurrently on the same instance.
type Processor struct {
	repo         *files.Repo
	tikaClient   *tika.Client
	searchClient *search.Client // nil = indexing disabled (graceful degradation)
	interval     time.Duration
	batchLimit   int
}

// Option configures a Processor at construction time.
type Option func(*Processor)

// WithInterval overrides the polling interval (default 10s).
func WithInterval(d time.Duration) Option {
	return func(p *Processor) {
		if d > 0 {
			p.interval = d
		}
	}
}

// WithBatchLimit overrides the max files processed per tick.
func WithBatchLimit(n int) Option {
	return func(p *Processor) {
		if n > 0 {
			p.batchLimit = n
		}
	}
}

// WithSearchClient enables Typesense indexing of files after OCR completes.
// If not set, the processor simply skips indexing — files are still OCR'd and
// their extracted_text is written to Postgres, they just won't be searchable
// via Typesense until a re-index runs. This keeps OCR working when Typesense
// is unavailable or not yet configured.
func WithSearchClient(c *search.Client) Option {
	return func(p *Processor) {
		p.searchClient = c
	}
}

// New returns a Processor that uses the given files Repo and Tika client.
// Both are required and must be non-nil; the processor does not own
// either (the caller closes the pool / lets the HTTP client go idle on
// shutdown).
func New(repo *files.Repo, tikaClient *tika.Client, opts ...Option) *Processor {
	p := &Processor{
		repo:       repo,
		tikaClient: tikaClient,
		interval:   DefaultInterval,
		batchLimit: DefaultBatchLimit,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Run polls the files table for pending files until ctx is cancelled.
// Each tick processes up to batchLimit files sequentially. A per-file
// error never stops the loop — it is logged and the file is marked
// failed so the next tick proceeds. Run returns nil on graceful
// shutdown, or the ctx.Err() if the context was cancelled.
func (p *Processor) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	slog.Info("file processor started", "interval", p.interval, "batch_limit", p.batchLimit)

	for {
		select {
		case <-ctx.Done():
			slog.Info("file processor stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := p.processOnce(ctx); err != nil {
				// processOnce returns only errors that prevent the whole
				// batch from running (e.g. DB unreachable). Per-file
				// errors are handled inside processFile and never reach
				// here. Log and keep ticking — the next tick will retry.
				slog.Error("file processor tick failed", "error", err)
			}
		}
	}
}

// processOnce runs a single polling tick: list pending files, process
// each one. Returns an error only if listing pending files fails (a DB
// problem); per-file failures are logged and the file is marked failed
// but do not propagate.
func (p *Processor) processOnce(ctx context.Context) error {
	pending, err := p.repo.ListPending(ctx, p.batchLimit)
	if err != nil {
		return fmt.Errorf("list pending files: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}

	slog.Info("processing pending files", "count", len(pending))
	for _, f := range pending {
		// Respect shutdown between files: if the worker is stopping,
		// bail out of the batch rather than starting another Tika call.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.processFile(ctx, f)
	}
	return nil
}

// processFile processes a single pending file. It never returns an
// error — every failure path updates the file's ocr_status and is
// logged, so one bad file can never abort the batch.
func (p *Processor) processFile(ctx context.Context, f files.PendingFile) {
	logger := slog.With("file_id", f.ID, "household_id", f.HouseholdID, "content_type", f.ContentType)

	// Skip content types that have no extractable text without calling
	// Tika. This avoids a 60s-timeout network call and a useless blob
	// read for every uploaded video. The list is prefix-based so
	// "video/mp4", "video/quicktime", "audio/mpeg" etc. all match.
	if isSkippableContentType(f.ContentType) {
		if err := p.repo.UpdateOCRStatus(ctx, f.ID, files.OCRStatusSkipped, ptrString(""), nil); err != nil {
			logger.Error("failed to mark file skipped", "error", err)
			return
		}
		logger.Info("file skipped (non-text content type)")
		return
	}

	// Fetch the blob bytes. A missing blob is a data-integrity problem
	// for this one row — mark it failed and continue, do not crash.
	blob, err := p.repo.GetBlob(ctx, f.ID)
	if err != nil {
		logger.Error("failed to fetch blob; marking file failed", "error", err)
		p.markFailed(ctx, f.ID, "failed to fetch file blob: "+err.Error(), logger)
		return
	}

	// Call Tika. An error here covers Tika being down (connection
	// refused), timing out (60s), or returning a non-2xx status — all
	// map to ocr_status=failed so the next tick can retry. Tika being
	// unavailable does NOT crash the worker; each affected file is
	// marked failed and the loop continues.
	text, err := p.tikaClient.ExtractText(ctx, blob, f.ContentType)
	if err != nil {
		logger.Error("tika extraction failed; marking file failed", "error", err)
		p.markFailed(ctx, f.ID, "tika extraction failed: "+err.Error(), logger)
		return
	}

	// Tika succeeded (2xx). Per the task acceptance criteria, empty text
	// maps to done with an empty extracted_text — not skipped. (Skipped
	// is reserved for content-type-based pre-filtering above.)
	if err := p.repo.UpdateOCRStatus(ctx, f.ID, files.OCRStatusDone, ptrString(text), nil); err != nil {
		logger.Error("failed to mark file done", "error", err)
		return
	}
	logger.Info("file processed", "text_bytes", len(text))

	// Index the file in Typesense now that OCR is done. Indexing is a
	// best-effort side-effect of OCR completion: a Typesense failure does
	// NOT roll back the OCR done status (the file's extracted_text is the
	// source of truth in Postgres and a re-index job can catch up later).
	// We log and move on so one bad index call can't stall the batch.
	if p.searchClient == nil {
		return
	}
	if err := p.indexFile(ctx, f.ID, text, logger); err != nil {
		logger.Error("failed to index file in typesense (ocr_status stays done)", "error", err)
	}
}

// indexFile builds a FileDocument from the file row plus the just-extracted
// text and upserts it into Typesense. It performs two extra reads: one to
// fetch the file's metadata (name, entity_type, entity_id, tags, created_at)
// and one to denormalize the attached entity's name. Both reads are
// per-file; the cost is acceptable because OCR (a 60s Tika call) dominates.
func (p *Processor) indexFile(ctx context.Context, fileID uuid.UUID, extractedText string, logger *slog.Logger) error {
	meta, err := p.repo.GetForIndex(ctx, fileID)
	if err != nil {
		return fmt.Errorf("fetch file metadata: %w", err)
	}
	if meta == nil {
		// File was deleted between OCR and indexing — nothing to index.
		// The delete path should have already removed it from Typesense.
		logger.Info("file disappeared before typesense indexing; skipping")
		return nil
	}

	entityName, err := p.repo.GetEntityName(ctx, meta.EntityType, meta.EntityID)
	if err != nil {
		// GetEntityName never returns an error for unknown/deleted entities
		// (it returns "" in those cases), so an error here is a real DB
		// problem. Log it but still index — the file is searchable by name
		// and extracted_text without entity_name.
		logger.Error("failed to resolve entity_name; indexing without it", "error", err)
		entityName = ""
	}

	doc := search.FileDocument{
		ID:            meta.ID.String(),
		HouseholdID:   meta.HouseholdID.String(),
		Name:          meta.Name,
		ExtractedText: extractedText,
		EntityType:    meta.EntityType,
		EntityID:      meta.EntityID.String(),
		EntityName:    entityName,
		Tags:          meta.Tags,
		CreatedAt:     meta.CreatedAt.UnixMilli(),
	}
	// An empty EntityType / EntityID should not appear in the JSON payload
	// so Typesense stores them as missing rather than empty strings — the
	// schema marks both optional, and an empty entity_id would be a
	// misleading "00000…" uuid.Nil string.
	if meta.EntityType == "" {
		doc.EntityType = ""
		doc.EntityID = ""
	}

	if err := p.searchClient.IndexFile(ctx, doc); err != nil {
		return err
	}
	logger.Info("file indexed in typesense", "entity_type", meta.EntityType, "entity_name", entityName)
	return nil
}

// markFailed sets ocr_status=failed with the given error message. It
// logs (but does not propagate) any DB error from the update itself —
// the processor cannot do better than try once, and a failed
// status-update is not a reason to abort the batch.
func (p *Processor) markFailed(ctx context.Context, fileID uuid.UUID, errMsg string, logger *slog.Logger) {
	if err := p.repo.UpdateOCRStatus(ctx, fileID, files.OCRStatusFailed, nil, ptrString(errMsg)); err != nil {
		if err == pgx.ErrNoRows {
			// File was deleted between ListPending and the update —
			// nothing to mark, and that's fine. Also remove it from
			// Typesense so stale search results don't outlive the file.
			// (The API delete path doesn't emit an outbox event yet, so
			// this is the worker's best delete-signal today; see
			// architecture/search-platform.md for the planned outbox flow.)
			logger.Info("file disappeared before status update", "error", err)
			p.removeFromSearch(ctx, fileID, logger)
			return
		}
		logger.Error("failed to persist ocr_status=failed", "error", err)
	}
}

// removeFromSearch deletes a file from the Typesense `files` collection.
// Best-effort: a Typesense failure is logged but never blocks the processor.
// Called when the worker observes that a file has been deleted out from under
// it (the only delete signal available to the OCR polling loop today).
func (p *Processor) removeFromSearch(ctx context.Context, fileID uuid.UUID, logger *slog.Logger) {
	if p.searchClient == nil {
		return
	}
	if err := p.searchClient.DeleteFile(ctx, fileID.String()); err != nil {
		logger.Error("failed to delete file from typesense", "error", err)
	}
}

// isSkippableContentType reports whether a content type has no
// extractable text and should be skipped without calling Tika. Video
// and audio are the obvious cases; we also skip empty content types
// that Tika would have to sniff (the upload handler always sets a
// content type, so an empty value here is a bug worth skipping rather
// than sending garbage to Tika).
//
// The check is prefix-based and case-insensitive so "video/mp4",
// "Video/QuickTime", and "audio/mpeg" all match.
func isSkippableContentType(contentType string) bool {
	if contentType == "" {
		return true
	}
	ct := strings.ToLower(contentType)
	for _, prefix := range []string{"video/", "audio/"} {
		if strings.HasPrefix(ct, prefix) {
			return true
		}
	}
	return false
}

// ptrString returns a pointer to s. Used so callers can pass a *string
// to UpdateOCRStatus's COALESCE'd extracted_text column (nil = leave
// unchanged, &"" = set to empty string explicitly).
func ptrString(s string) *string {
	return &s
}
