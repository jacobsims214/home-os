package note

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
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

// callJSON runs a tool handler with args and decodes its single text result
// into out.
func callJSON(t *testing.T, ctx context.Context, handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error), args map[string]any, out any) string {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	res, err := handler(ctx, req)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("want 1 content item, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	if out != nil {
		if err := json.Unmarshal([]byte(text.Text), out); err != nil {
			t.Fatalf("decode %q: %v", text.Text, err)
		}
	}
	return text.Text
}

type authoredNote struct {
	ID             uuid.UUID  `json:"id"`
	EntityType     string     `json:"entity_type"`
	EntityID       uuid.UUID  `json:"entity_id"`
	Title          *string    `json:"title"`
	Body           string     `json:"body"`
	ContentPreview string     `json:"content_preview"`
	AuthorID       *uuid.UUID `json:"author_id"`
	AuthorName     *string    `json:"author_name"`
}

// TestCreateNote_RecordsAuthor checks that create_note stores the caller as
// author, attaches entity-less notes to the household, and that both
// create_note and list_notes return the author and the full body.
func TestCreateNote_RecordsAuthor(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)

	_, _, create := NewCreateNoteTool(pool)
	_, _, list := NewListNotesTool(pool)

	longBody := strings.Repeat("x", 150)
	vehicleID := uuid.New()

	tests := []struct {
		name           string
		args           map[string]any
		wantEntityType string
		wantEntityID   uuid.UUID
	}{
		{
			name:           "no entity is attached to the household",
			args:           map[string]any{"title": "Standalone", "content": longBody},
			wantEntityType: "household",
			wantEntityID:   hh.HouseholdID,
		},
		{
			name:           "with entity",
			args:           map[string]any{"title": "Tires", "content": "Rotate tires", "entity_type": "vehicle", "entity_id": vehicleID.String()},
			wantEntityType: "vehicle",
			wantEntityID:   vehicleID,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got authoredNote
			callJSON(t, ctx, create, tc.args, &got)

			if got.AuthorID == nil || *got.AuthorID != hh.UserID {
				t.Errorf("response author_id: want %s, got %v", hh.UserID, got.AuthorID)
			}
			if got.AuthorName == nil || *got.AuthorName != "Test Owner" {
				t.Errorf("response author_name: want %q, got %v", "Test Owner", got.AuthorName)
			}
			if got.Body != tc.args["content"] {
				t.Errorf("response body: want full content, got %q", got.Body)
			}
			if got.EntityType != tc.wantEntityType || got.EntityID != tc.wantEntityID {
				t.Errorf("entity: want %s/%s, got %s/%s", tc.wantEntityType, tc.wantEntityID, got.EntityType, got.EntityID)
			}

			var stored struct {
				authorID   *uuid.UUID
				entityType string
				entityID   uuid.UUID
			}
			if err := pool.QueryRow(context.Background(),
				`SELECT author_id, entity_type, entity_id FROM notes WHERE id = $1`, got.ID,
			).Scan(&stored.authorID, &stored.entityType, &stored.entityID); err != nil {
				t.Fatalf("select stored note: %v", err)
			}
			if stored.authorID == nil || *stored.authorID != hh.UserID {
				t.Errorf("stored author_id: want %s, got %v", hh.UserID, stored.authorID)
			}
			if stored.entityType != tc.wantEntityType || stored.entityID != tc.wantEntityID {
				t.Errorf("stored entity: want %s/%s, got %s/%s", tc.wantEntityType, tc.wantEntityID, stored.entityType, stored.entityID)
			}
		})
	}

	t.Run("list_notes includes author and full body", func(t *testing.T) {
		var notes []authoredNote
		callJSON(t, ctx, list, map[string]any{"entity_type": "household", "entity_id": hh.HouseholdID.String()}, &notes)
		if len(notes) != 1 {
			t.Fatalf("want 1 household note, got %d: %+v", len(notes), notes)
		}
		n := notes[0]
		if n.AuthorID == nil || *n.AuthorID != hh.UserID {
			t.Errorf("author_id: want %s, got %v", hh.UserID, n.AuthorID)
		}
		if n.AuthorName == nil || *n.AuthorName != "Test Owner" {
			t.Errorf("author_name: want %q, got %v", "Test Owner", n.AuthorName)
		}
		if n.Body != longBody {
			t.Errorf("body: want %d chars, got %d", len(longBody), len(n.Body))
		}
		if n.ContentPreview != strings.Repeat("x", 100)+"..." {
			t.Errorf("content_preview: got %q", n.ContentPreview)
		}
	})

	t.Run("caller that is not a user gets a null author", func(t *testing.T) {
		claims := *hh.Claims
		claims.UserID = "local-dev"
		var got authoredNote
		callJSON(t, server.ContextWithClaims(context.Background(), &claims), create,
			map[string]any{"title": "Dev", "content": "from local dev"}, &got)
		if got.AuthorID != nil || got.AuthorName != nil {
			t.Errorf("want null author, got %v / %v", got.AuthorID, got.AuthorName)
		}
	})

	t.Run("entity_type without entity_id is rejected", func(t *testing.T) {
		text := callJSON(t, ctx, create, map[string]any{"title": "Bad", "content": "x", "entity_type": "vehicle"}, nil)
		if text != `{"error":"entity_type and entity_id must be provided together"}` {
			t.Errorf("got %s", text)
		}
	})
}
