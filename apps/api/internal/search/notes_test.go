package search

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

type fakeNotes struct {
	ok   map[string]bool
	err  error
	got  []string
	role string
}

func (f *fakeNotes) VisibleNoteIDs(_ context.Context, _, role string, ids []string) (map[string]bool, error) {
	f.got, f.role = ids, role
	return f.ok, f.err
}

// checkNotes keeps only the note hits the checker allows, never touches
// other hits, and drops every note hit when there is no checker or it fails
// (#4798).
func TestCheckNotes(t *testing.T) {
	results := func() []SearchResult {
		return []SearchResult{
			{EntityType: "note", EntityID: "n1"},
			{EntityType: "pet", EntityID: "p1"},
			{EntityType: "note", EntityID: "n2"},
			{EntityType: "document", EntityID: "f1", FileID: "f1"},
		}
	}
	ids := func(rs []SearchResult) []string {
		out := []string{}
		for _, r := range rs {
			out = append(out, r.EntityID)
		}
		return out
	}

	fc := &fakeNotes{ok: map[string]bool{"n2": true}}
	h := (&Handler{}).WithNoteChecker(fc)
	assert.Equal(t, []string{"p1", "n2", "f1"}, ids(h.checkNotes(context.Background(), "h", "viewer", results())))
	assert.Equal(t, []string{"n1", "n2"}, fc.got, "only note hits are checked")
	assert.Equal(t, "viewer", fc.role)

	failing := (&Handler{}).WithNoteChecker(&fakeNotes{err: errors.New("db down")})
	assert.Equal(t, []string{"p1", "f1"}, ids(failing.checkNotes(context.Background(), "h", "owner", results())), "fail closed")

	none := &Handler{}
	assert.Equal(t, []string{"p1", "f1"}, ids(none.checkNotes(context.Background(), "h", "owner", results())), "no checker: no notes")

	noNotes := []SearchResult{{EntityType: "pet", EntityID: "p1"}}
	fc.got = nil
	assert.Equal(t, []string{"p1"}, ids(h.checkNotes(context.Background(), "h", "owner", noNotes)))
	assert.Nil(t, fc.got, "no note hits: no query")
}
