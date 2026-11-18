package search

import (
	"testing"

	"home-os/api/internal/auth"
)

// TestVisibleResults: roles without secrets read never get credential
// documents, which are indexed under their parent record's type (#4689).
func TestVisibleResults(t *testing.T) {
	results := []SearchResult{
		{EntityType: "pet", DocID: "pet-1"},
		{EntityType: "pet", DocID: "secret-2"}, // a credential attached to a pet
		{EntityType: "secret", DocID: "secret-3"},
		{EntityType: "bill", DocID: "bill-4"},
	}
	ids := func(rs []SearchResult) []string {
		out := []string{}
		for _, r := range rs {
			out = append(out, r.DocID)
		}
		return out
	}
	if got := ids(VisibleResults(auth.RoleOwner, results)); len(got) != 4 {
		t.Errorf("owner sees %v, want all 4", got)
	}
	got := ids(VisibleResults(auth.RoleViewer, results))
	if len(got) != 2 || got[0] != "pet-1" || got[1] != "bill-4" {
		t.Errorf("viewer sees %v, want [pet-1 bill-4]", got)
	}
}

// TestCollapseFiles: one hit per file; the higher score wins and keeps the
// metadata document's title. Non-file hits are untouched.
func TestCollapseFiles(t *testing.T) {
	got := CollapseFiles([]SearchResult{
		{EntityType: "pet", DocID: "pet-1", Score: 0.9},
		{EntityType: "document", FileID: "f1", Title: "Rex vaccination record", Score: 0.5},
		{EntityType: "pet", FileID: "f1", Title: "IMG_1.jpg", Body: "rabies", Score: 0.8},
		{EntityType: "document", FileID: "f2", Title: "Roof invoice", Score: 0.4},
	})
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3: %+v", len(got), got)
	}
	if got[1].FileID != "f1" || got[1].Body != "rabies" || got[1].Title != "Rex vaccination record" {
		t.Errorf("f1 collapsed to %+v, want the OCR hit with the document title", got[1])
	}
	if got[2].FileID != "f2" {
		t.Errorf("got[2] = %+v, want f2", got[2])
	}
}

// TestVisibleResultsFileHitsNeedFilesRead: a file hit is dropped for a role
// without files read even when its attached entity is visible.
func TestVisibleResultsFileHitsNeedFilesRead(t *testing.T) {
	rs := []SearchResult{{EntityType: "property", FileID: "f1", DocID: "f1"}, {EntityType: "property", DocID: "property-1"}}
	if got := VisibleResults(auth.RoleVendor, rs); len(got) != 1 || got[0].FileID != "" {
		t.Errorf("vendor sees %+v, want only the property", got)
	}
}
