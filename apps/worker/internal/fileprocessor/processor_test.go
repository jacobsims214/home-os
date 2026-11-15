package fileprocessor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"home-os/worker/internal/files"
	"home-os/worker/internal/tika"
)

// fakeRow is one files row in the in-memory store.
type fakeRow struct {
	householdID   uuid.UUID
	contentType   string
	blob          []byte
	noBlob        bool
	status        string
	extractedText *string
	ocrError      *string
}

// fakeStore implements store in memory. ListRetryable mirrors the SQL
// due check in files.Repo.ListRetryable by parsing the retry marker.
type fakeStore struct {
	rows  map[uuid.UUID]*fakeRow
	order []uuid.UUID
	now   func() time.Time
}

func newFakeStore(now func() time.Time) *fakeStore {
	return &fakeStore{rows: map[uuid.UUID]*fakeRow{}, now: now}
}

func (s *fakeStore) add(contentType string, blob []byte) uuid.UUID {
	id := uuid.New()
	s.rows[id] = &fakeRow{householdID: uuid.New(), contentType: contentType, blob: blob, status: files.OCRStatusPending}
	s.order = append(s.order, id)
	return id
}

func (s *fakeStore) ListPending(_ context.Context, limit int) ([]files.PendingFile, error) {
	out := []files.PendingFile{}
	for _, id := range s.order {
		r := s.rows[id]
		if r.status == files.OCRStatusPending && len(out) < limit {
			out = append(out, files.PendingFile{ID: id, HouseholdID: r.householdID, ContentType: r.contentType})
		}
	}
	return out, nil
}

func (s *fakeStore) ListRetryable(_ context.Context, limit int) ([]files.RetryableFile, error) {
	out := []files.RetryableFile{}
	for _, id := range s.order {
		r := s.rows[id]
		if r.status != files.OCRStatusFailed || r.ocrError == nil || len(out) >= limit {
			continue
		}
		m := retryMarker.FindStringSubmatch(*r.ocrError)
		if m == nil {
			continue
		}
		next, err := time.Parse(time.RFC3339, m[3])
		if err != nil {
			return nil, err
		}
		if next.After(s.now()) {
			continue
		}
		out = append(out, files.RetryableFile{
			PendingFile: files.PendingFile{ID: id, HouseholdID: r.householdID, ContentType: r.contentType},
			OCRError:    *r.ocrError,
		})
	}
	return out, nil
}

func (s *fakeStore) GetBlob(_ context.Context, id uuid.UUID) ([]byte, error) {
	r, ok := s.rows[id]
	if !ok || r.noBlob {
		return nil, fmt.Errorf("files: blob for file %s missing: %w", id, pgx.ErrNoRows)
	}
	return r.blob, nil
}

func (s *fakeStore) UpdateOCRStatus(_ context.Context, id uuid.UUID, status string, text *string, ocrErr *string) error {
	r, ok := s.rows[id]
	if !ok {
		return pgx.ErrNoRows
	}
	r.status = status
	if text != nil {
		r.extractedText = text
	}
	r.ocrError = ocrErr
	return nil
}

func (s *fakeStore) GetForIndex(context.Context, uuid.UUID) (*files.IndexableFile, error) {
	return nil, nil
}

func (s *fakeStore) GetEntityName(context.Context, string, uuid.UUID) (string, error) {
	return "", nil
}

// tikaStub is an httptest Tika server whose responses can be switched
// between failing (503) and succeeding, counting calls.
type tikaStub struct {
	srv   *httptest.Server
	fail  atomic.Bool
	calls atomic.Int32
}

func newTikaStub(t *testing.T) *tikaStub {
	t.Helper()
	ts := &tikaStub{}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		if ts.fail.Load() {
			http.Error(w, "tika down", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "extracted by tika")
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

// clock is a settable test clock.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock                   { return &clock{t: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)} }
func strOrNil(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func newTestProcessor(st *fakeStore, ts *tikaStub, c *clock, opts ...Option) *Processor {
	p := New(nil, tika.NewClient(tika.WithBaseURL(ts.srv.URL)), opts...)
	p.repo = st
	p.now = c.now
	return p
}

func TestProcessOnce_RetriesWithBackoffThenGivesUp(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	st := newFakeStore(c.now)
	ts := newTikaStub(t)
	ts.fail.Store(true)
	p := newTestProcessor(st, ts, c, WithMaxRetries(3), WithRetryBaseDelay(30*time.Second))
	id := st.add("application/pdf", []byte("%PDF"))

	// Attempt 1 fails: failed with retry 1/3, next in 30s.
	if err := p.processOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row := st.rows[id]
	want := "[retry 1/3 next=2026-11-15T12:00:30Z] tika extraction failed"
	if row.status != files.OCRStatusFailed || !strings.HasPrefix(strOrNil(row.ocrError), want) {
		t.Fatalf("after attempt 1: status=%s error=%q, want failed with prefix %q", row.status, strOrNil(row.ocrError), want)
	}

	// Not due yet: no Tika call.
	c.advance(29 * time.Second)
	if err := p.processOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ts.calls.Load(); got != 1 {
		t.Fatalf("tika calls before backoff elapsed = %d, want 1", got)
	}

	// Backoff doubles: 30s, 60s, 120s between attempts.
	steps := []struct {
		wait   time.Duration
		prefix string
	}{
		{1 * time.Second, "[retry 2/3 next=2026-11-15T12:01:30Z] "},
		{60 * time.Second, "[retry 3/3 next=2026-11-15T12:03:30Z] "},
	}
	for i, s := range steps {
		c.advance(s.wait)
		if err := p.processOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if row.status != files.OCRStatusFailed || !strings.HasPrefix(strOrNil(row.ocrError), s.prefix) {
			t.Fatalf("step %d: status=%s error=%q, want prefix %q", i, row.status, strOrNil(row.ocrError), s.prefix)
		}
	}

	// Final attempt (4th = 1 + 3 retries) fails: stays failed with the error, no marker.
	c.advance(120 * time.Second)
	if err := p.processOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if row.status != files.OCRStatusFailed {
		t.Fatalf("final status = %s, want failed", row.status)
	}
	if got := strOrNil(row.ocrError); !strings.HasPrefix(got, "failed after 4 attempts: tika extraction failed") {
		t.Fatalf("final error = %q", got)
	}
	if got := ts.calls.Load(); got != 4 {
		t.Fatalf("tika calls = %d, want 4", got)
	}

	// Never picked up again, however long we wait.
	c.advance(24 * time.Hour)
	if err := p.processOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ts.calls.Load(); got != 4 {
		t.Fatalf("tika calls after giving up = %d, want 4", got)
	}
}

func TestProcessOnce_RetrySucceedsAndClearsError(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	st := newFakeStore(c.now)
	ts := newTikaStub(t)
	ts.fail.Store(true)
	p := newTestProcessor(st, ts, c)
	id := st.add("image/png", []byte("png"))

	if err := p.processOnce(ctx); err != nil {
		t.Fatal(err)
	}
	ts.fail.Store(false)
	c.advance(DefaultRetryBaseDelay)
	if err := p.processOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row := st.rows[id]
	if row.status != files.OCRStatusDone || strOrNil(row.extractedText) != "extracted by tika" || row.ocrError != nil {
		t.Fatalf("status=%s text=%q error=%q, want done/extracted/nil", row.status, strOrNil(row.extractedText), strOrNil(row.ocrError))
	}
}

func TestProcessOnce_MissingBlobNotRetried(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	st := newFakeStore(c.now)
	ts := newTikaStub(t)
	p := newTestProcessor(st, ts, c)
	id := st.add("application/pdf", nil)
	st.rows[id].noBlob = true

	if err := p.processOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row := st.rows[id]
	if row.status != files.OCRStatusFailed || !strings.HasPrefix(strOrNil(row.ocrError), "failed to fetch file blob") {
		t.Fatalf("status=%s error=%q, want failed without retry marker", row.status, strOrNil(row.ocrError))
	}
}

func TestProcessOnce_ZeroRetriesFailsImmediately(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	st := newFakeStore(c.now)
	ts := newTikaStub(t)
	ts.fail.Store(true)
	p := newTestProcessor(st, ts, c, WithMaxRetries(0))
	id := st.add("application/pdf", []byte("%PDF"))

	if err := p.processOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strOrNil(st.rows[id].ocrError); !strings.HasPrefix(got, "tika extraction failed") {
		t.Fatalf("error = %q, want plain tika error", got)
	}
}

func TestProcessOnce_PlainTextDoneWithoutTika(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		blob        []byte
		want        string
	}{
		{"plain", "text/plain", []byte("grocery list\nmilk"), "grocery list\nmilk"},
		{"charset param and case", "Text/Plain; charset=utf-8", []byte("héllo"), "héllo"},
		{"invalid utf8 and NUL cleaned", "text/plain", []byte("a\x00b\xffc"), "ab�c"},
		{"empty", "text/plain", []byte{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClock()
			st := newFakeStore(c.now)
			ts := newTikaStub(t)
			p := newTestProcessor(st, ts, c)
			id := st.add(tt.contentType, tt.blob)

			if err := p.processOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			row := st.rows[id]
			if row.status != files.OCRStatusDone {
				t.Fatalf("status = %s, want done", row.status)
			}
			if got := strOrNil(row.extractedText); got != tt.want {
				t.Fatalf("extracted_text = %q, want %q", got, tt.want)
			}
			if ts.calls.Load() != 0 {
				t.Fatalf("tika was called %d times, want 0", ts.calls.Load())
			}
		})
	}
}

func TestBackoff(t *testing.T) {
	p := &Processor{retryBaseDelay: 30 * time.Second}
	tests := []struct {
		failures int
		want     time.Duration
	}{
		{1, 30 * time.Second},
		{2, time.Minute},
		{3, 2 * time.Minute},
		{6, 16 * time.Minute},
		{7, MaxRetryDelay},
		{50, MaxRetryDelay},
	}
	for _, tt := range tests {
		if got := p.backoff(tt.failures); got != tt.want {
			t.Errorf("backoff(%d) = %s, want %s", tt.failures, got, tt.want)
		}
	}
}

func TestParseRetryAttempts(t *testing.T) {
	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{formatRetryMarker(2, 3, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) + "boom", 2, true},
		{"[retry 1/3 next=2026-11-15T12:00:30Z] tika extraction failed: x", 1, true},
		{"failed after 4 attempts: boom", 0, false},
		{"tika extraction failed: boom", 0, false},
		{"[retry 0/3 next=2026-11-15T12:00:30Z] x", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseRetryAttempts(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseRetryAttempts(%q) = %d,%v want %d,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}
