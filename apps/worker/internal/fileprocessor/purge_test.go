package fileprocessor

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"home-os/worker/internal/files"
	"home-os/worker/internal/search"
)

// remove deletes a row the way any SQL delete does once migration 053 is
// applied: the row is gone and its id is queued for a search delete.
func (s *fakeStore) remove(id uuid.UUID) {
	r := s.rows[id]
	delete(s.rows, id)
	for i, o := range s.order {
		if o == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.queue = append(s.queue, files.SearchDeletion{FileID: id, HouseholdID: r.householdID, DeletedAt: s.now()})
}

func (s *fakeStore) ListSearchDeletions(_ context.Context, limit int) ([]files.SearchDeletion, error) {
	return append([]files.SearchDeletion{}, s.queue[:min(limit, len(s.queue))]...), nil
}

func (s *fakeStore) AckSearchDeletion(_ context.Context, d files.SearchDeletion) error {
	for i, q := range s.queue {
		if q == d {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			return nil
		}
	}
	return nil
}

func (s *fakeStore) MissingFileIDs(_ context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	out := []uuid.UUID{}
	for _, id := range ids {
		if _, ok := s.rows[id]; !ok {
			out = append(out, id)
		}
	}
	return out, nil
}

// A deleted file is removed from the index in the next tick and its queue
// row is acknowledged.
func TestPurge_DeletedFileLeavesIndex(t *testing.T) {
	p, st, idx, _ := newIndexingProcessor(t)
	keep := st.add("text/plain", []byte("keep me"))
	gone := st.add("text/plain", []byte("delete me"))
	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(idx.docs) != 2 {
		t.Fatalf("indexed %d files, want 2", len(idx.docs))
	}

	st.remove(gone)
	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.docs[gone.String()]; ok {
		t.Error("deleted file still indexed")
	}
	if _, ok := idx.docs[keep.String()]; !ok {
		t.Error("kept file was removed from the index")
	}
	if len(st.queue) != 0 {
		t.Errorf("queue = %v, want empty", st.queue)
	}
}

// While Typesense is down the queue row stays, and the delete happens once
// it is back. Deleting a file never depends on Typesense being up.
func TestPurge_TypesenseDownKeepsQueue(t *testing.T) {
	p, st, idx, _ := newIndexingProcessor(t)
	id := st.add("text/plain", []byte("private"))
	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	st.remove(id)
	idx.deleteErr = errors.New("connection refused")
	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.queue) != 1 {
		t.Fatalf("queue = %v, want the file still queued", st.queue)
	}

	idx.deleteErr = nil
	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.docs[id.String()]; ok || len(st.queue) != 0 {
		t.Fatalf("after recovery indexed=%v queue=%v; want removed and acked", ok, st.queue)
	}
}

// Documents of files deleted before the queue existed are removed by the
// once-per-start orphan sweep.
func TestPurge_OrphanSweepOnStart(t *testing.T) {
	p, st, idx, _ := newIndexingProcessor(t)
	live := st.add("text/plain", []byte("live"))
	orphan := uuid.New().String()
	idx.docs[orphan] = search.FileDocument{ID: orphan}

	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.docs[orphan]; ok {
		t.Error("orphan document survived the start-up sweep")
	}
	if _, ok := idx.docs[live.String()]; !ok {
		t.Error("live file missing from the index")
	}
	if !p.orphansSwept {
		t.Error("orphansSwept not set")
	}
}

// Without a search client the queue is left for a later configured client.
func TestPurge_DisabledLeavesQueue(t *testing.T) {
	c := newClock()
	st := newFakeStore(c.now)
	p := newTestProcessor(st, newTikaStub(t), c)
	id := st.add("text/plain", []byte("a"))
	st.remove(id)
	if err := p.processOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.queue) != 1 {
		t.Fatalf("queue = %v, want it left alone", st.queue)
	}
}
