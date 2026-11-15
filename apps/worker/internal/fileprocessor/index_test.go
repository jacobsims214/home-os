package fileprocessor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"home-os/worker/internal/files"
	"home-os/worker/internal/search"
)

// fakeIndexer records IndexFile calls and keeps the latest document per file.
type fakeIndexer struct {
	initCalls int
	wm        search.Watermark
	wmErr     error
	failNext  []error // errors returned by the next IndexFile calls, in order
	calls     []string
	docs      map[string]search.FileDocument
	chunks    map[string][]string
}

func newFakeIndexer() *fakeIndexer {
	return &fakeIndexer{docs: map[string]search.FileDocument{}, chunks: map[string][]string{}}
}

func (f *fakeIndexer) InitCollection(context.Context) error { f.initCalls++; return nil }
func (f *fakeIndexer) IndexWatermark(context.Context) (search.Watermark, error) {
	return f.wm, f.wmErr
}
func (f *fakeIndexer) DeleteFile(_ context.Context, id string) error { delete(f.docs, id); return nil }
func (f *fakeIndexer) IndexFile(_ context.Context, doc search.FileDocument, chunks []string) error {
	f.calls = append(f.calls, doc.ID)
	if len(f.failNext) > 0 {
		err := f.failNext[0]
		f.failNext = f.failNext[1:]
		if err != nil {
			return err
		}
	}
	f.docs[doc.ID] = doc
	f.chunks[doc.ID] = chunks
	return nil
}

func newIndexingProcessor(t *testing.T) (*Processor, *fakeStore, *fakeIndexer, *clock) {
	t.Helper()
	c := newClock()
	st := newFakeStore(c.now)
	ts := newTikaStub(t)
	p := newTestProcessor(st, ts, c)
	idx := newFakeIndexer()
	p.searchClient = idx
	return p, st, idx, c
}

// A file OCR'd in a tick is indexed in the same tick, chunked, with its
// row version.
func TestIndex_AfterOCRSameTick(t *testing.T) {
	p, st, idx, _ := newIndexingProcessor(t)
	long := strings.Repeat("The boiler warranty covers parts and labour for ten years. ", 60)
	id := st.add("text/plain", []byte(long))

	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	doc, ok := idx.docs[id.String()]
	if !ok {
		t.Fatalf("file not indexed; calls = %v", idx.calls)
	}
	if doc.RowVersion != st.rows[id].updatedAt.UnixMicro() {
		t.Errorf("row version = %d, want updated_at %d", doc.RowVersion, st.rows[id].updatedAt.UnixMicro())
	}
	if n := len(idx.chunks[id.String()]); n < 2 {
		t.Errorf("long text indexed as %d chunk(s), want several", n)
	}
	if doc.EntityType != "" || doc.EntityID != "" {
		t.Errorf("unattached file has entity %q/%q", doc.EntityType, doc.EntityID)
	}

	// Nothing changed: the next tick indexes nothing.
	idx.calls = nil
	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(idx.calls) != 0 {
		t.Errorf("unchanged file re-indexed: %v", idx.calls)
	}
}

// A metadata change (rename, tags) bumps updated_at and re-indexes the file.
func TestIndex_MetadataChangeReindexes(t *testing.T) {
	p, st, idx, _ := newIndexingProcessor(t)
	id := st.add("text/plain", []byte("Dog vaccination record"))
	other := st.add("text/plain", []byte("Car title"))
	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	r := st.rows[id]
	r.name = "Rex shots.pdf"
	r.tags = []string{"pets", "vet"}
	st.touch(r)
	idx.calls = nil
	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(idx.calls) != 1 || idx.calls[0] != id.String() {
		t.Fatalf("calls = %v, want only the edited file", idx.calls)
	}
	got := idx.docs[id.String()]
	if got.Name != "Rex shots.pdf" || len(got.Tags) != 2 {
		t.Errorf("re-indexed doc = %+v", got)
	}
	if _, ok := idx.docs[other.String()]; !ok {
		t.Error("other file missing")
	}
}

// A transient Typesense error stops the sweep; the next tick resumes at the
// failed file. A rejected document is skipped.
func TestIndex_TransientStopsPermanentSkips(t *testing.T) {
	p, st, idx, _ := newIndexingProcessor(t)
	a := st.add("text/plain", []byte("a"))
	b := st.add("text/plain", []byte("b"))
	c := st.add("text/plain", []byte("c"))
	if err := p.ocrOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	idx.failNext = []error{nil, errors.New("connection refused")}
	if err := p.indexOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(idx.docs) != 1 || idx.docs[a.String()].ID == "" {
		t.Fatalf("after outage: docs = %v, want only a", idx.docs)
	}

	idx.failNext = []error{&search.DocumentError{DocID: b.String(), Message: "bad"}}
	if err := p.indexOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.docs[c.String()]; !ok {
		t.Fatalf("c not indexed after b was rejected; calls = %v", idx.calls)
	}
	want := []string{a.String(), b.String(), b.String(), c.String()}
	if strings.Join(idx.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", idx.calls, want)
	}
}

// A new processor (worker restart) resumes from the Typesense watermark:
// files at or before it are not re-indexed, later ones are.
func TestIndex_ResumesFromWatermark(t *testing.T) {
	p, st, idx, c := newIndexingProcessor(t)
	a := st.add("text/plain", []byte("a"))
	b := st.add("text/plain", []byte("b"))
	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The newest indexed version is b's (b was OCR'd last).
	wm := search.Watermark{RowVersion: idx.docs[b.String()].RowVersion, FileID: b.String()}

	// While the worker is down, a is renamed.
	st.rows[a].name = "renamed"
	st.touch(st.rows[a])

	p2 := newTestProcessor(st, newTikaStub(t), c)
	idx2 := newFakeIndexer()
	idx2.wm = wm
	p2.searchClient = idx2
	if err := p2.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if idx2.initCalls != 1 {
		t.Errorf("InitCollection called %d times, want 1", idx2.initCalls)
	}
	if len(idx2.calls) != 1 || idx2.calls[0] != a.String() || idx2.docs[a.String()].Name != "renamed" {
		t.Errorf("after restart calls = %v, want only the renamed file", idx2.calls)
	}
}

// If the watermark cannot be read (Typesense down) nothing is indexed and
// the processor retries on the next tick.
func TestIndex_WatermarkUnavailable(t *testing.T) {
	p, st, idx, _ := newIndexingProcessor(t)
	id := st.add("text/plain", []byte("a"))
	idx.wmErr = errors.New("connection refused")
	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(idx.calls) != 0 || st.rows[id].status != files.OCRStatusDone {
		t.Fatalf("calls = %v status = %s; want no indexing, OCR done", idx.calls, st.rows[id].status)
	}
	idx.wmErr = nil
	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(idx.calls) != 1 {
		t.Fatalf("after recovery calls = %v, want 1", idx.calls)
	}
}

func TestCursorFromWatermark(t *testing.T) {
	if got := cursorFromWatermark(search.Watermark{}); got != (files.IndexCursor{}) {
		t.Errorf("zero watermark -> %+v, want zero cursor", got)
	}
	ts := time.Date(2026, 11, 15, 12, 30, 0, 123456000, time.UTC)
	id := uuid.New()
	got := cursorFromWatermark(search.Watermark{RowVersion: ts.UnixMicro(), FileID: id.String()})
	if !got.UpdatedAt.Equal(ts) || got.ID != id {
		t.Errorf("cursor = %+v, want %v/%v", got, ts, id)
	}
}

// Without a search client the processor still OCRs and never indexes.
func TestIndex_DisabledWithoutSearchClient(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	p := newTestProcessor(st, newTikaStub(t), c, WithSearchClient(nil))
	id := st.add("text/plain", []byte("a"))
	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.searchClient != nil || st.rows[id].status != files.OCRStatusDone {
		t.Fatalf("searchClient = %v, status = %s", p.searchClient, st.rows[id].status)
	}
}
