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
