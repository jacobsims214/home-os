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
// Retries: a failure that may be transient (Tika down or timing out, a DB
// error while reading the blob) is retried up to maxRetries times with
// exponential backoff. No columns were added for this. The retry state lives
// in an ocr_error prefix, "[retry <k>/<n> next=<RFC3339 UTC>] <error>": k is
// the number of failed attempts so far and next is the earliest time to try
// again. files.Repo.ListRetryable returns failed rows whose next time has
// passed. When the retries run out, the error is rewritten as
// "failed after <n> attempts: <error>" with no prefix, so the row stays
// failed with its error and is never picked up again. A missing blob is
// permanent and is never retried.
//
// text/plain files skip Tika: the blob is already text, so it is copied
// into extracted_text and the file is marked done on its first tick.
//
// Search indexing: after the OCR work, each tick runs an index sweep
// (indexOnce). It walks done files in (updated_at, id) order from a cursor
// and (re)indexes each one into the Typesense `files` collection, split into
// chunks small enough for the embedding model (search.Chunk). Every write to
// a files row bumps updated_at, so the same sweep covers OCR completion and
// metadata edits (rename, tags via PATCH /api/v1/files/{id}); there are no
// file outbox events to listen to. The cursor starts from the newest
// row_version already in Typesense (search.Client.IndexWatermark), so a
// restart neither re-embeds everything nor misses edits made while the
// worker was down. A Typesense outage stops the sweep at the first file that
// fails and the next tick resumes there; a file Typesense rejects outright
// is logged and skipped.
//
// Search deletes: deleted files must never stay searchable. Every delete of
// a files row (API, household cascade, any later path) queues the file id in
// file_search_deletions via an AFTER DELETE trigger (migration 053). Each
// tick ends with purgeOnce, which deletes the queued files' documents by
// file_id and removes a queue row only after Typesense confirms; an outage
// leaves the rows for the next tick. Once per start, sweepOrphans also
// removes documents of files that were deleted before 053.
//
// Graceful shutdown: Run blocks until ctx is cancelled (SIGINT/SIGTERM).
// An in-flight processFile call is allowed to complete; the next tick
// is not started. This bounds shutdown latency to one Tika call
// (DefaultTimeout = 60s).
package fileprocessor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
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

// DefaultMaxRetries is how many times a failed file is retried after its
// first failed attempt before it is left failed for good.
const DefaultMaxRetries = 3

// DefaultRetryBaseDelay is the wait before the first retry. Each later
// retry doubles it (30s, 1m, 2m, ...), capped at MaxRetryDelay.
const DefaultRetryBaseDelay = 30 * time.Second

// MaxRetryDelay caps the backoff between retries.
const MaxRetryDelay = 30 * time.Minute

// retryMarker matches the retry-state prefix the processor writes to
// ocr_error. Keep it in step with the SQL in files.Repo.ListRetryable.
var retryMarker = regexp.MustCompile(`^\[retry (\d+)/(\d+) next=([^\]]+)\] `)

// store is the slice of files.Repo the processor uses. *files.Repo
// satisfies it; tests supply an in-memory fake.
type store interface {
	ListPending(ctx context.Context, limit int) ([]files.PendingFile, error)
	ListRetryable(ctx context.Context, limit int) ([]files.RetryableFile, error)
	GetBlob(ctx context.Context, fileID uuid.UUID) ([]byte, error)
	UpdateOCRStatus(ctx context.Context, fileID uuid.UUID, ocrStatus string, extractedText *string, ocrError *string) error
	ListIndexable(ctx context.Context, after files.IndexCursor, limit int) ([]files.IndexableFile, error)
	GetEntityName(ctx context.Context, entityType string, entityID uuid.UUID) (string, error)
	ListSearchDeletions(ctx context.Context, limit int) ([]files.SearchDeletion, error)
	AckSearchDeletion(ctx context.Context, d files.SearchDeletion) error
	MissingFileIDs(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error)
}

// indexer is the slice of search.Client the processor uses. Tests supply a
// fake.
type indexer interface {
	InitCollection(ctx context.Context) error
	IndexWatermark(ctx context.Context) (search.Watermark, error)
	IndexFile(ctx context.Context, doc search.FileDocument, chunks []string) error
	DeleteFile(ctx context.Context, fileID string) error
	IndexedFileIDs(ctx context.Context) ([]string, error)
}

// DefaultIndexBatchLimit is the max number of files (re)indexed per tick.
// Each file can be up to search.MaxChunks embeddings, so this is smaller
// than the OCR batch.
const DefaultIndexBatchLimit = 20

// DefaultPurgeBatchLimit is the max queued search deletions handled per
// tick. A delete is two small Typesense calls, so this can be large.
const DefaultPurgeBatchLimit = 200

// orphanCheckBatch is how many indexed file ids are checked against
// Postgres per query in the start-up orphan sweep.
const orphanCheckBatch = 1000

// Processor runs the file OCR polling loop. Construct one with New and
// run it with Run(ctx). A Processor is safe to run on a single goroutine
// only; do not call Run concurrently on the same instance.
type Processor struct {
	repo         store
	tikaClient   *tika.Client
	searchClient indexer // nil = indexing disabled (graceful degradation)
	interval     time.Duration
	batchLimit   int
	indexBatch   int
	// indexCursor is where the next index sweep starts; indexReady reports
	// whether it has been loaded from Typesense yet.
	indexCursor files.IndexCursor
	indexReady  bool
	purgeBatch  int
	// orphansSwept reports whether the once-per-start sweep for documents
	// of files deleted before migration 053 has completed.
	orphansSwept   bool
	maxRetries     int
	retryBaseDelay time.Duration
	now            func() time.Time
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

// WithMaxRetries overrides how many times a failed file is retried
// (default DefaultMaxRetries). 0 disables retries.
func WithMaxRetries(n int) Option {
	return func(p *Processor) {
		if n >= 0 {
			p.maxRetries = n
		}
	}
}

// WithRetryBaseDelay overrides the wait before the first retry (default
// DefaultRetryBaseDelay). Later retries double it, capped at MaxRetryDelay.
func WithRetryBaseDelay(d time.Duration) Option {
	return func(p *Processor) {
		if d > 0 {
			p.retryBaseDelay = d
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
		if c != nil {
			p.searchClient = c
		}
	}
}

// WithIndexBatchLimit overrides the max files indexed per tick.
func WithIndexBatchLimit(n int) Option {
	return func(p *Processor) {
		if n > 0 {
			p.indexBatch = n
		}
	}
}

// New returns a Processor that uses the given files Repo and Tika client.
// Both are required and must be non-nil; the processor does not own
// either (the caller closes the pool / lets the HTTP client go idle on
// shutdown).
func New(repo *files.Repo, tikaClient *tika.Client, opts ...Option) *Processor {
	p := &Processor{
		repo:           repo,
		tikaClient:     tikaClient,
		interval:       DefaultInterval,
		batchLimit:     DefaultBatchLimit,
		indexBatch:     DefaultIndexBatchLimit,
		purgeBatch:     DefaultPurgeBatchLimit,
		maxRetries:     DefaultMaxRetries,
		retryBaseDelay: DefaultRetryBaseDelay,
		now:            time.Now,
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

// processOnce runs a single polling tick: the OCR work (ocrOnce), then
// the search index sweep (indexOnce), so a file OCR'd in this tick is
// searchable by the end of it, then the search delete queue (purgeOnce).
// Purging runs last, on the same goroutine as indexing, so a file deleted
// while it was being indexed is removed again in the same tick.
func (p *Processor) processOnce(ctx context.Context) error {
	ocrErr := p.ocrOnce(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	indexErr := p.indexOnce(ctx)
	if ctx.Err() != nil {
		return errors.Join(ocrErr, indexErr, ctx.Err())
	}
	return errors.Join(ocrErr, indexErr, p.purgeOnce(ctx))
}

// ocrOnce processes pending files first, then failed files whose retry is
// due, up to batchLimit files in all. Returns an error only if a listing
// query fails (a DB problem); per-file failures are logged and recorded
// on the row but do not propagate.
func (p *Processor) ocrOnce(ctx context.Context) error {
	pending, err := p.repo.ListPending(ctx, p.batchLimit)
	if err != nil {
		return fmt.Errorf("list pending files: %w", err)
	}
	if len(pending) > 0 {
		slog.Info("processing pending files", "count", len(pending))
	}
	for _, f := range pending {
		// Respect shutdown between files: if the worker is stopping,
		// bail out of the batch rather than starting another Tika call.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.processFile(ctx, f, 0)
	}

	if p.maxRetries == 0 {
		return nil
	}
	remaining := p.batchLimit - len(pending)
	if remaining <= 0 {
		return nil
	}
	retryable, err := p.repo.ListRetryable(ctx, remaining)
	if err != nil {
		return fmt.Errorf("list retryable files: %w", err)
	}
	if len(retryable) > 0 {
		slog.Info("retrying failed files", "count", len(retryable))
	}
	for _, f := range retryable {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		attempts, ok := parseRetryAttempts(f.OCRError)
		if !ok {
			// ListRetryable only returns marked rows, so this is a row
			// whose marker we cannot read. Leave it alone.
			slog.Warn("skipping failed file with unreadable retry marker", "file_id", f.ID)
			continue
		}
		p.processFile(ctx, f.PendingFile, attempts)
	}
	return nil
}

// processFile processes a single file. priorFailures is how many
// attempts have already failed (0 for a pending file). It never returns
// an error — every failure path updates the file's ocr_status and is
// logged, so one bad file can never abort the batch.
func (p *Processor) processFile(ctx context.Context, f files.PendingFile, priorFailures int) {
	logger := slog.With("file_id", f.ID, "household_id", f.HouseholdID, "content_type", f.ContentType)
	if priorFailures > 0 {
		logger = logger.With("attempt", priorFailures+1)
	}

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
		msg := "failed to fetch file blob: " + err.Error()
		if errors.Is(err, pgx.ErrNoRows) {
			// The blob row is gone; retrying cannot bring it back.
			p.markFailed(ctx, f.ID, msg, logger)
			return
		}
		p.recordFailure(ctx, f.ID, priorFailures, msg, logger)
		return
	}

	// text/plain needs no extraction: the bytes are the text. Postgres
	// TEXT rejects invalid UTF-8 and NUL bytes, so clean both first.
	if isPlainText(f.ContentType) {
		text := strings.ReplaceAll(strings.ToValidUTF8(string(blob), "\uFFFD"), "\x00", "")
		p.markDone(ctx, f.ID, text, logger)
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
		p.recordFailure(ctx, f.ID, priorFailures, "tika extraction failed: "+err.Error(), logger)
		return
	}

	// Tika succeeded (2xx). Per the task acceptance criteria, empty text
	// maps to done with an empty extracted_text — not skipped. (Skipped
	// is reserved for content-type-based pre-filtering above.)
	p.markDone(ctx, f.ID, text, logger)
}

// markDone writes extracted_text and sets ocr_status=done (clearing any
// earlier error or retry marker). The write bumps updated_at, so the index
// sweep at the end of this tick indexes the file.
func (p *Processor) markDone(ctx context.Context, fileID uuid.UUID, text string, logger *slog.Logger) {
	if err := p.repo.UpdateOCRStatus(ctx, fileID, files.OCRStatusDone, ptrString(text), nil); err != nil {
		logger.Error("failed to mark file done", "error", err)
		return
	}
	logger.Info("file processed", "text_bytes", len(text))
}

// indexOnce runs one index sweep: it (re)indexes up to indexBatch done
// files changed since the cursor. Indexing is a best-effort side effect:
// Postgres stays the source of truth, and a Typesense failure never
// changes a file's OCR status. It returns an error only if listing files
// fails.
func (p *Processor) indexOnce(ctx context.Context) error {
	if p.searchClient == nil {
		return nil
	}
	if !p.indexReady {
		// Create or migrate the collection first: it may not exist if
		// Typesense was down when the worker started.
		if err := p.searchClient.InitCollection(ctx); err != nil {
			slog.Warn("search index sweep: collection init failed; will retry", "error", err)
			return nil
		}
		wm, err := p.searchClient.IndexWatermark(ctx)
		if err != nil {
			slog.Warn("search index sweep: cannot read index watermark; will retry", "error", err)
			return nil
		}
		p.indexCursor = cursorFromWatermark(wm)
		p.indexReady = true
		slog.Info("search index sweep: resuming", "after_updated_at", p.indexCursor.UpdatedAt, "after_file_id", p.indexCursor.ID)
	}

	batch, err := p.repo.ListIndexable(ctx, p.indexCursor, p.indexBatch)
	if err != nil {
		return fmt.Errorf("list files to index: %w", err)
	}
	for _, f := range batch {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logger := slog.With("file_id", f.ID, "household_id", f.HouseholdID)
		if err := p.indexFile(ctx, f, logger); err != nil {
			if !search.IsPermanent(err) {
				// Typesense down or busy: stop here and resume from this
				// file on the next tick.
				logger.Error("failed to index file in typesense; will retry next tick", "error", err)
				return nil
			}
			logger.Error("typesense rejected file; skipping it until it changes", "error", err)
		}
		p.indexCursor = files.IndexCursor{UpdatedAt: f.UpdatedAt, ID: f.ID}
	}
	return nil
}

// purgeOnce removes deleted files from Typesense. Deleted files must never
// stay searchable, and every delete of a files row queues the file in
// file_search_deletions (migration 053's AFTER DELETE trigger), whichever
// path deleted it. Each queued file's documents are deleted by file_id
// (search.Client.DeleteFile) and the queue row is removed only after
// Typesense confirms. A Typesense failure stops the purge; the next tick
// retries from the same row. It returns an error only if the queue cannot be
// read or acknowledged (a DB problem).
//
// With indexing disabled (no search client) the queue is left alone, so a
// search client configured later still removes those files.
func (p *Processor) purgeOnce(ctx context.Context) error {
	if p.searchClient == nil {
		return nil
	}
	if !p.orphansSwept {
		if err := p.sweepOrphans(ctx); err != nil {
			slog.Warn("search purge: orphan sweep failed; will retry next tick", "error", err)
		} else {
			p.orphansSwept = true
		}
	}

	queued, err := p.repo.ListSearchDeletions(ctx, p.purgeBatch)
	if err != nil {
		return fmt.Errorf("list search deletions: %w", err)
	}
	for _, d := range queued {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		logger := slog.With("file_id", d.FileID, "household_id", d.HouseholdID)
		if err := p.searchClient.DeleteFile(ctx, d.FileID.String()); err != nil {
			logger.Error("failed to delete file from typesense; will retry next tick", "error", err)
			return nil
		}
		if err := p.repo.AckSearchDeletion(ctx, d); err != nil {
			return err
		}
		logger.Info("deleted file removed from typesense")
	}
	return nil
}

// sweepOrphans deletes the documents of every indexed file that no longer
// has a files row. It runs once per worker start, for files deleted before
// migration 053 queued deletes (and as a backstop if the queue was ever
// lost, e.g. 053 rolled back and re-applied).
func (p *Processor) sweepOrphans(ctx context.Context) error {
	indexed, err := p.searchClient.IndexedFileIDs(ctx)
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, 0, len(indexed))
	for _, s := range indexed {
		if id, err := uuid.Parse(s); err == nil {
			ids = append(ids, id)
		}
	}
	removed := 0
	for start := 0; start < len(ids); start += orphanCheckBatch {
		missing, err := p.repo.MissingFileIDs(ctx, ids[start:min(start+orphanCheckBatch, len(ids))])
		if err != nil {
			return err
		}
		for _, id := range missing {
			if err := p.searchClient.DeleteFile(ctx, id.String()); err != nil {
				return err
			}
			removed++
		}
	}
	if removed > 0 {
		slog.Info("search purge: removed documents of files deleted earlier", "files", removed)
	}
	return nil
}

// cursorFromWatermark converts the index watermark (row_version in unix
// micros) back to a files cursor. Postgres timestamps have microsecond
// precision, so the round trip is exact.
func cursorFromWatermark(wm search.Watermark) files.IndexCursor {
	if wm.RowVersion <= 0 {
		return files.IndexCursor{}
	}
	id, _ := uuid.Parse(wm.FileID) // a bad id leaves uuid.Nil: re-index ties
	return files.IndexCursor{UpdatedAt: time.UnixMicro(wm.RowVersion).UTC(), ID: id}
}

// indexFile chunks one file's text and upserts it into Typesense, with the
// attached entity's name denormalized onto every chunk.
func (p *Processor) indexFile(ctx context.Context, f files.IndexableFile, logger *slog.Logger) error {
	entityName, err := p.repo.GetEntityName(ctx, f.EntityType, f.EntityID)
	if err != nil {
		// GetEntityName returns "" (not an error) for unknown or deleted
		// entities, so this is a real DB problem. Index without it; the
		// file is still searchable by name and text.
		logger.Error("failed to resolve entity_name; indexing without it", "error", err)
		entityName = ""
	}

	doc := search.FileDocument{
		ID:          f.ID.String(),
		HouseholdID: f.HouseholdID.String(),
		Name:        f.Name,
		EntityName:  entityName,
		Tags:        f.Tags,
		CreatedAt:   f.CreatedAt.UnixMilli(),
		RowVersion:  f.UpdatedAt.UnixMicro(),
	}
	// An unattached file has no entity_type/entity_id in Typesense (both
	// optional) rather than "" and a misleading uuid.Nil string.
	if f.EntityType != "" {
		doc.EntityType = f.EntityType
		doc.EntityID = f.EntityID.String()
	}

	chunks, truncated := search.Chunk(f.ExtractedText)
	if truncated {
		logger.Warn("extracted text exceeds the chunk cap; indexing only the beginning",
			"text_bytes", len(f.ExtractedText), "max_chunks", search.MaxChunks)
	}
	if err := p.searchClient.IndexFile(ctx, doc, chunks); err != nil {
		return err
	}
	logger.Info("file indexed in typesense", "chunks", max(len(chunks), 1), "entity_type", f.EntityType)
	return nil
}

// recordFailure handles a failure that may be transient. If retries are
// left, the file is marked failed with a retry marker naming the attempt
// count and the backoff deadline; otherwise it is marked failed for good
// with the last error.
func (p *Processor) recordFailure(ctx context.Context, fileID uuid.UUID, priorFailures int, errMsg string, logger *slog.Logger) {
	failures := priorFailures + 1
	if failures > p.maxRetries {
		if p.maxRetries > 0 {
			errMsg = fmt.Sprintf("failed after %d attempts: %s", failures, errMsg)
		}
		logger.Warn("file failed; no retries left", "attempts", failures)
		p.markFailed(ctx, fileID, errMsg, logger)
		return
	}
	next := p.now().Add(p.backoff(failures)).UTC()
	logger.Info("file failed; retry scheduled", "attempts", failures, "next_attempt", next)
	p.markFailed(ctx, fileID, formatRetryMarker(failures, p.maxRetries, next)+errMsg, logger)
}

// backoff returns the wait after the given number of failed attempts:
// retryBaseDelay doubled for each failure after the first, capped at
// MaxRetryDelay.
func (p *Processor) backoff(failures int) time.Duration {
	d := p.retryBaseDelay
	for i := 1; i < failures; i++ {
		d *= 2
		if d >= MaxRetryDelay {
			return MaxRetryDelay
		}
	}
	return min(d, MaxRetryDelay)
}

// formatRetryMarker renders the ocr_error prefix that carries retry
// state. next is written as RFC3339 UTC so Postgres can cast it to
// timestamptz in files.Repo.ListRetryable.
func formatRetryMarker(failures, maxRetries int, next time.Time) string {
	return fmt.Sprintf("[retry %d/%d next=%s] ", failures, maxRetries, next.UTC().Format(time.RFC3339))
}

// parseRetryAttempts returns the failed-attempt count from an ocr_error
// retry marker, or false if ocrError has no readable marker.
func parseRetryAttempts(ocrError string) (int, bool) {
	m := retryMarker.FindStringSubmatch(ocrError)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
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
			// Typesense right away; the delete is queued in
			// file_search_deletions too and purgeOnce drains it.
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
// it. The reliable delete signal is the file_search_deletions queue
// (purgeOnce); this only makes the removal a tick sooner.
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

// isPlainText reports whether a content type is text/plain (any case,
// with or without parameters such as "; charset=utf-8").
func isPlainText(contentType string) bool {
	mt, _, _ := strings.Cut(contentType, ";")
	return strings.EqualFold(strings.TrimSpace(mt), "text/plain")
}

// ptrString returns a pointer to s. Used so callers can pass a *string
// to UpdateOCRStatus's COALESCE'd extracted_text column (nil = leave
// unchanged, &"" = set to empty string explicitly).
func ptrString(s string) *string {
	return &s
}
