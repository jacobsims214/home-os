package note

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/testutil"
)

// TestListNotes_Smoke runs list_notes against a real, fully migrated
// Postgres and checks household scoping and the entity filter.
func TestListNotes_Smoke(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, other := testutil.SeedHousehold(t, pool)

	propertyID := uuid.New()
	insert := func(householdID uuid.UUID, entityType string, entityID uuid.UUID, title, body string) {
		t.Helper()
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, $2, $3, $4, $5)`,
			householdID, entityType, entityID, title, body,
		); err != nil {
			t.Fatalf("insert note: %v", err)
		}
	}
	insert(hh.HouseholdID, "property", propertyID, "Roof", "Replace shingles in spring")
	insert(hh.HouseholdID, "vehicle", uuid.New(), "Oil", "Change oil at 5k miles")
	insert(other.HouseholdID, "property", propertyID, "Not mine", "Belongs to another household")

	_, _, handler := NewListNotesTool(pool)

	type note struct {
		ID             uuid.UUID `json:"id"`
		Title          *string   `json:"title"`
		ContentPreview string    `json:"content_preview"`
	}
	call := func(t *testing.T, ctx context.Context, args map[string]any) []note {
		t.Helper()
		var req mcp.CallToolRequest
		req.Params.Name = "list_notes"
		req.Params.Arguments = args
		res, err := handler(ctx, req)
		if err != nil {
			t.Fatalf("list_notes: %v", err)
		}
		if len(res.Content) != 1 {
			t.Fatalf("want 1 content item, got %d", len(res.Content))
		}
		text, ok := res.Content[0].(mcp.TextContent)
		if !ok {
			t.Fatalf("want TextContent, got %T", res.Content[0])
		}
		var notes []note
		if err := json.Unmarshal([]byte(text.Text), &notes); err != nil {
			t.Fatalf("decode %q: %v", text.Text, err)
		}
		return notes
	}

	tests := []struct {
		name       string
		args       map[string]any
		wantTitles []string
	}{
		{name: "all notes in household, newest first", args: nil, wantTitles: []string{"Oil", "Roof"}},
		{
			name:       "filtered by entity",
			args:       map[string]any{"entity_type": "property", "entity_id": propertyID.String()},
			wantTitles: []string{"Roof"},
		},
		{
			name:       "no match",
			args:       map[string]any{"entity_type": "pet", "entity_id": uuid.NewString()},
			wantTitles: []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := call(t, ctx, tc.args)
			if len(got) != len(tc.wantTitles) {
				t.Fatalf("want %d notes, got %d: %+v", len(tc.wantTitles), len(got), got)
			}
			for i, want := range tc.wantTitles {
				if got[i].Title == nil || *got[i].Title != want {
					t.Errorf("note %d: want title %q, got %v", i, want, got[i].Title)
				}
			}
		})
	}

	t.Run("no claims is unauthorized", func(t *testing.T) {
		var req mcp.CallToolRequest
		res, err := handler(context.Background(), req)
		if err != nil {
			t.Fatalf("list_notes: %v", err)
		}
		text := res.Content[0].(mcp.TextContent).Text
		if text != `{"error":"unauthorized"}` {
			t.Errorf("want unauthorized, got %s", text)
		}
	})
}
