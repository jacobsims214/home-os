package note

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
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

// noteGetWire is a note_get result as a client sees it after JSON decoding.
type noteGetWire struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent map[string]any `json:"structuredContent"`
	IsError           bool           `json:"isError"`
}

// noteGetEnvelope is the respond envelope note_get puts in structuredContent.
type noteGetEnvelope struct {
	OK      bool          `json:"ok"`
	Summary string        `json:"summary"`
	Data    *authoredNote `json:"data"`
	Next    []string      `json:"next"`
	Error   *struct {
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Field   string         `json:"field"`
		Example map[string]any `json:"example"`
	} `json:"error"`
}

// noteGetOutputSchema resolves the outputSchema note_get advertises in tools/list.
func noteGetOutputSchema(t *testing.T, tool mcp.Tool) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal tool: %v", err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal tool: %v", err)
	}
	if len(raw.OutputSchema) == 0 {
		t.Fatalf("tool %s declares no outputSchema", tool.Name)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw.OutputSchema, &s); err != nil {
		t.Fatalf("unmarshal outputSchema: %v", err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve outputSchema: %v", err)
	}
	return rs
}

// TestNoteGet runs note_get against a real, fully migrated Postgres. Every
// result (success and error) must mirror structuredContent in its single
// text block and validate against the tool's outputSchema.
func TestNoteGet(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)

	name, tool, get := NewNoteGetTool(pool)
	_, _, create := NewCreateNoteTool(pool)
	if name != "note_get" || tool.Name != "note_get" {
		t.Fatalf("tool name: got %q / %q", name, tool.Name)
	}
	if got := tool.InputSchema.Required; len(got) != 1 || got[0] != "note_id" {
		t.Errorf("required params: want [note_id], got %v", got)
	}
	a := tool.Annotations
	if a.Title != "Get note" || a.ReadOnlyHint == nil || !*a.ReadOnlyHint ||
		a.DestructiveHint == nil || *a.DestructiveHint ||
		a.IdempotentHint == nil || !*a.IdempotentHint ||
		a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations: want class R (read-only, idempotent, not destructive, closed world), got %+v", a)
	}
	rs := noteGetOutputSchema(t, tool)

	call := func(t *testing.T, ctx context.Context, args map[string]any) (noteGetWire, noteGetEnvelope) {
		t.Helper()
		var req mcp.CallToolRequest
		req.Params.Name = "note_get"
		req.Params.Arguments = args
		res, err := get(ctx, req)
		if err != nil {
			t.Fatalf("note_get returned a protocol error: %v", err)
		}
		b, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		var w noteGetWire
		if err := json.Unmarshal(b, &w); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if len(w.Content) != 1 || w.Content[0].Type != "text" {
			t.Fatalf("want exactly one text block, got %+v", w.Content)
		}
		var fromText map[string]any
		if err := json.Unmarshal([]byte(w.Content[0].Text), &fromText); err != nil {
			t.Fatalf("text block is not JSON: %v", err)
		}
		x, _ := json.Marshal(fromText)
		y, _ := json.Marshal(w.StructuredContent)
		if string(x) != string(y) {
			t.Fatalf("text and structuredContent differ:\n%s\n%s", x, y)
		}
		if err := rs.Validate(w.StructuredContent); err != nil {
			t.Fatalf("structuredContent does not match outputSchema: %v\n%s", err, y)
		}
		var env noteGetEnvelope
		if err := json.Unmarshal([]byte(w.Content[0].Text), &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		return w, env
	}

	// A body far beyond list previews, with multi-byte characters and newlines.
	longBody := strings.Repeat("Line with ümlauts and emoji 🏠.\n", 200)
	var mine authoredNote
	callJSON(t, ctx, create, map[string]any{"title": "Wi-Fi setup", "content": longBody}, &mine)

	vehicleID := uuid.New()
	var linked authoredNote
	callJSON(t, ctx, create, map[string]any{
		"title": "Tires", "content": "Rotate every 6k miles", "entity_type": "vehicle", "entity_id": vehicleID.String(),
	}, &linked)

	var theirs authoredNote
	callJSON(t, otherCtx, create, map[string]any{"title": "Secret", "content": "other household"}, &theirs)

	t.Run("returns the complete body, author and timestamps", func(t *testing.T) {
		w, env := call(t, ctx, map[string]any{"note_id": mine.ID.String()})
		if w.IsError || !env.OK || env.Data == nil {
			t.Fatalf("want success, got isError=%v %+v", w.IsError, env)
		}
		n := env.Data
		if n.ID != mine.ID || n.Title == nil || *n.Title != "Wi-Fi setup" {
			t.Errorf("id/title: got %s / %v", n.ID, n.Title)
		}
		if n.Body != longBody {
			t.Errorf("body: want %d bytes, got %d", len(longBody), len(n.Body))
		}
		if n.AuthorID == nil || *n.AuthorID != hh.UserID || n.AuthorName == nil || *n.AuthorName != "Test Owner" {
			t.Errorf("author: got %v / %v", n.AuthorID, n.AuthorName)
		}
		if n.EntityType != "household" || n.EntityID != hh.HouseholdID {
			t.Errorf("entity: got %s/%s", n.EntityType, n.EntityID)
		}
		for _, key := range []string{"created_at", "updated_at"} {
			data := w.StructuredContent["data"].(map[string]any)
			if s, _ := data[key].(string); s == "" {
				t.Errorf("%s missing from data", key)
			}
		}
		if env.Summary != `Found note "Wi-Fi setup" by Test Owner.` {
			t.Errorf("summary: got %q", env.Summary)
		}
		if len(env.Next) == 0 || !strings.Contains(env.Next[0], "note_update") || !strings.Contains(env.Next[0], mine.ID.String()) {
			t.Errorf("next: want a note_update hint with the id, got %v", env.Next)
		}
	})

	t.Run("returns the entity link", func(t *testing.T) {
		_, env := call(t, ctx, map[string]any{"note_id": linked.ID.String()})
		if env.Data == nil || env.Data.EntityType != "vehicle" || env.Data.EntityID != vehicleID {
			t.Fatalf("entity: got %+v", env.Data)
		}
	})

	tests := []struct {
		name      string
		ctx       context.Context
		args      map[string]any
		wantField string
		wantMsg   string
	}{
		{
			name:    "other household's note is not found",
			ctx:     ctx,
			args:    map[string]any{"note_id": theirs.ID.String()},
			wantMsg: `No note with id "` + theirs.ID.String() + `" was found.`,
		},
		{
			name:    "own note is not found from the other household",
			ctx:     otherCtx,
			args:    map[string]any{"note_id": mine.ID.String()},
			wantMsg: `No note with id "` + mine.ID.String() + `" was found.`,
		},
		{
			name:    "unknown id is not found",
			ctx:     ctx,
			args:    map[string]any{"note_id": "00000000-0000-0000-0000-000000000001"},
			wantMsg: `No note with id "00000000-0000-0000-0000-000000000001" was found.`,
		},
		{
			name:      "title instead of id is invalid",
			ctx:       ctx,
			args:      map[string]any{"note_id": "Wi-Fi setup"},
			wantField: "note_id",
			wantMsg:   `note_id "Wi-Fi setup" is not a UUID.`,
		},
		{
			name:      "missing note_id is invalid",
			ctx:       ctx,
			args:      nil,
			wantField: "note_id",
			wantMsg:   "note_id is required.",
		},
		{
			name:    "no claims is refused",
			ctx:     context.Background(),
			args:    map[string]any{"note_id": mine.ID.String()},
			wantMsg: "You are not signed in to a household.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, env := call(t, tc.ctx, tc.args)
			if !w.IsError || env.OK || env.Error == nil {
				t.Fatalf("want isError result, got isError=%v %+v", w.IsError, env)
			}
			if env.Data != nil {
				t.Errorf("error result leaked data: %+v", env.Data)
			}
			if env.Error.Message != tc.wantMsg || env.Summary != tc.wantMsg {
				t.Errorf("message: want %q, got %q (summary %q)", tc.wantMsg, env.Error.Message, env.Summary)
			}
			if env.Error.Field != tc.wantField {
				t.Errorf("field: want %q, got %q", tc.wantField, env.Error.Field)
			}
			if env.Error.Fix == "" {
				t.Error("fix hint is empty")
			}
		})
	}
}

// updatedNote is the data payload of a note_update result.
type updatedNote struct {
	ID         uuid.UUID  `json:"id"`
	Title      *string    `json:"title"`
	Body       string     `json:"body"`
	AuthorID   *uuid.UUID `json:"author_id"`
	EntityType string     `json:"entity_type"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type noteUpdateEnvelope struct {
	OK      bool         `json:"ok"`
	Summary string       `json:"summary"`
	Data    *updatedNote `json:"data"`
	Next    []string     `json:"next"`
	Error   *struct {
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Field   string         `json:"field"`
		Example map[string]any `json:"example"`
	} `json:"error"`
}

// TestNoteUpdate runs note_update against a real, fully migrated Postgres.
// Every result must mirror structuredContent in its text block and validate
// against the tool's outputSchema.
func TestNoteUpdate(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)

	name, tool, update := NewNoteUpdateTool(pool)
	_, _, create := NewCreateNoteTool(pool)
	if name != "note_update" || tool.Name != "note_update" {
		t.Fatalf("tool name: got %q / %q", name, tool.Name)
	}
	if got := tool.InputSchema.Required; len(got) != 1 || got[0] != "note_id" {
		t.Errorf("required params: want [note_id], got %v", got)
	}
	for _, p := range []string{"title", "content", "append_text"} {
		if _, ok := tool.InputSchema.Properties[p]; !ok {
			t.Errorf("missing optional param %q", p)
		}
	}
	a := tool.Annotations
	if a.Title != "Update note" || a.ReadOnlyHint == nil || *a.ReadOnlyHint ||
		a.DestructiveHint == nil || *a.DestructiveHint ||
		a.IdempotentHint == nil || !*a.IdempotentHint ||
		a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations: want class U (not read-only, idempotent, not destructive, closed world), got %+v", a)
	}
	rs := noteGetOutputSchema(t, tool)

	call := func(t *testing.T, ctx context.Context, args map[string]any) (bool, noteUpdateEnvelope) {
		t.Helper()
		var req mcp.CallToolRequest
		req.Params.Name = "note_update"
		req.Params.Arguments = args
		res, err := update(ctx, req)
		if err != nil {
			t.Fatalf("note_update returned a protocol error: %v", err)
		}
		b, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		var w noteGetWire
		if err := json.Unmarshal(b, &w); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if len(w.Content) != 1 || w.Content[0].Type != "text" {
			t.Fatalf("want exactly one text block, got %+v", w.Content)
		}
		var fromText map[string]any
		if err := json.Unmarshal([]byte(w.Content[0].Text), &fromText); err != nil {
			t.Fatalf("text block is not JSON: %v", err)
		}
		x, _ := json.Marshal(fromText)
		y, _ := json.Marshal(w.StructuredContent)
		if string(x) != string(y) {
			t.Fatalf("text and structuredContent differ:\n%s\n%s", x, y)
		}
		if err := rs.Validate(w.StructuredContent); err != nil {
			t.Fatalf("structuredContent does not match outputSchema: %v\n%s", err, y)
		}
		var env noteUpdateEnvelope
		if err := json.Unmarshal([]byte(w.Content[0].Text), &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		return w.IsError, env
	}

	newNote := func(t *testing.T, ctx context.Context, title, body string) uuid.UUID {
		t.Helper()
		var n authoredNote
		callJSON(t, ctx, create, map[string]any{"title": title, "content": body}, &n)
		// Backdate so updated_at visibly moves and the append guard is out of its window.
		if _, err := pool.Exec(context.Background(),
			`UPDATE notes SET created_at = NOW() - interval '1 hour', updated_at = NOW() - interval '1 hour' WHERE id = $1`,
			n.ID); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		return n.ID
	}
	stored := func(t *testing.T, id uuid.UUID) (title *string, body string, updatedAt time.Time) {
		t.Helper()
		if err := pool.QueryRow(context.Background(),
			`SELECT title, body, updated_at FROM notes WHERE id = $1`, id).Scan(&title, &body, &updatedAt); err != nil {
			t.Fatalf("read note: %v", err)
		}
		return title, body, updatedAt
	}

	t.Run("title and content persist and updated_at moves", func(t *testing.T) {
		id := newNote(t, ctx, "Groceries", "eggs")
		_, _, before := stored(t, id)

		isErr, env := call(t, ctx, map[string]any{"note_id": id.String(), "title": "Weekly groceries", "content": "milk\nbread"})
		if isErr || !env.OK || env.Data == nil {
			t.Fatalf("want success, got isError=%v %+v", isErr, env)
		}
		if env.Data.ID != id || env.Data.Title == nil || *env.Data.Title != "Weekly groceries" || env.Data.Body != "milk\nbread" {
			t.Errorf("returned note: %+v", env.Data)
		}
		if !env.Data.UpdatedAt.After(before) || !env.Data.UpdatedAt.After(env.Data.CreatedAt) {
			t.Errorf("updated_at did not move: before %v, created %v, now %v", before, env.Data.CreatedAt, env.Data.UpdatedAt)
		}
		if env.Data.AuthorID == nil || *env.Data.AuthorID != hh.UserID {
			t.Errorf("author changed: %v", env.Data.AuthorID)
		}
		title, body, after := stored(t, id)
		if title == nil || *title != "Weekly groceries" || body != "milk\nbread" || !after.After(before) {
			t.Errorf("stored: title %v body %q updated_at %v (before %v)", title, body, after, before)
		}
		if env.Summary != `Updated note "Weekly groceries": renamed it and replaced its body.` {
			t.Errorf("summary: got %q", env.Summary)
		}
		if len(env.Next) == 0 || !strings.Contains(env.Next[0], id.String()) {
			t.Errorf("next: want hints with the id, got %v", env.Next)
		}
	})

	t.Run("title only keeps the body", func(t *testing.T) {
		id := newNote(t, ctx, "Old", "keep me")
		_, env := call(t, ctx, map[string]any{"note_id": id.String(), "title": "New", "content": "", "append_text": ""})
		if env.Data == nil || *env.Data.Title != "New" || env.Data.Body != "keep me" {
			t.Fatalf("got %+v", env.Data)
		}
	})

	t.Run("append_text adds a line and keeps the rest", func(t *testing.T) {
		id := newNote(t, ctx, "Packing", "- socks")
		_, env := call(t, ctx, map[string]any{"note_id": id.String(), "append_text": "- toothbrush"})
		if env.Data == nil || env.Data.Body != "- socks\n- toothbrush" || *env.Data.Title != "Packing" {
			t.Fatalf("got %+v", env.Data)
		}
		if env.Summary != `Updated note "Packing": added text to the end.` {
			t.Errorf("summary: got %q", env.Summary)
		}

		// The identical append again within 60 s is ignored.
		_, _, at := stored(t, id)
		isErr, env := call(t, ctx, map[string]any{"note_id": id.String(), "append_text": "- toothbrush"})
		if isErr || env.Data == nil || env.Data.Body != "- socks\n- toothbrush" {
			t.Fatalf("duplicate append: isError=%v %+v", isErr, env.Data)
		}
		if !strings.Contains(env.Summary, "nothing was added") {
			t.Errorf("duplicate summary: got %q", env.Summary)
		}
		if _, body, at2 := stored(t, id); body != "- socks\n- toothbrush" || !at2.Equal(at) {
			t.Errorf("duplicate append wrote: body %q updated_at %v -> %v", body, at, at2)
		}

		// A different line is still appended.
		_, env = call(t, ctx, map[string]any{"note_id": id.String(), "append_text": "- charger"})
		if env.Data == nil || env.Data.Body != "- socks\n- toothbrush\n- charger" {
			t.Fatalf("second append: %+v", env.Data)
		}
	})

	t.Run("the same append is accepted again after the window", func(t *testing.T) {
		id := newNote(t, ctx, "Log", "fed the cat")
		if _, err := pool.Exec(context.Background(),
			`UPDATE notes SET body = 'fed the cat', updated_at = NOW() - interval '2 minutes' WHERE id = $1`, id); err != nil {
			t.Fatalf("reset: %v", err)
		}
		_, env := call(t, ctx, map[string]any{"note_id": id.String(), "append_text": "fed the cat"})
		if env.Data == nil || env.Data.Body != "fed the cat\nfed the cat" {
			t.Fatalf("got %+v", env.Data)
		}
	})

	theirs := newNote(t, otherCtx, "Secret", "other household")
	mine := newNote(t, ctx, "Mine", "body")

	t.Run("cross-household update changes nothing", func(t *testing.T) {
		for _, args := range []map[string]any{
			{"note_id": theirs.String(), "content": "hijacked"},
			{"note_id": theirs.String(), "append_text": "other household"},
		} {
			isErr, env := call(t, ctx, args)
			want := `No note with id "` + theirs.String() + `" was found.`
			if !isErr || env.Error == nil || env.Error.Message != want {
				t.Fatalf("want not-found, got isError=%v %+v", isErr, env)
			}
		}
		if title, body, _ := stored(t, theirs); *title != "Secret" || body != "other household" {
			t.Errorf("other household's note changed: %v %q", title, body)
		}
	})

	tests := []struct {
		name      string
		ctx       context.Context
		args      map[string]any
		wantField string
		wantMsg   string
	}{
		{
			name:    "own note is not found from the other household",
			ctx:     otherCtx,
			args:    map[string]any{"note_id": mine.String(), "title": "x"},
			wantMsg: `No note with id "` + mine.String() + `" was found.`,
		},
		{
			name:    "unknown id is not found",
			ctx:     ctx,
			args:    map[string]any{"note_id": "00000000-0000-0000-0000-000000000001", "title": "x"},
			wantMsg: `No note with id "00000000-0000-0000-0000-000000000001" was found.`,
		},
		{
			name:      "title instead of id is invalid",
			ctx:       ctx,
			args:      map[string]any{"note_id": "Groceries", "title": "x"},
			wantField: "note_id",
			wantMsg:   `note_id "Groceries" is not a UUID.`,
		},
		{
			name:      "missing note_id is invalid",
			ctx:       ctx,
			args:      map[string]any{"title": "x"},
			wantField: "note_id",
			wantMsg:   "note_id is required.",
		},
		{
			name:    "nothing to change is invalid",
			ctx:     ctx,
			args:    map[string]any{"note_id": mine.String(), "title": ""},
			wantMsg: "Nothing to change: pass title, content or append_text.",
		},
		{
			name:      "content with append_text is invalid",
			ctx:       ctx,
			args:      map[string]any{"note_id": mine.String(), "content": "a", "append_text": "b"},
			wantField: "append_text",
			wantMsg:   "Pass content or append_text, not both.",
		},
		{
			name:      "title over 500 characters is invalid",
			ctx:       ctx,
			args:      map[string]any{"note_id": mine.String(), "title": strings.Repeat("é", 501)},
			wantField: "title",
			wantMsg:   "title is 501 characters; the limit is 500.",
		},
		{
			name:    "no claims is refused",
			ctx:     context.Background(),
			args:    map[string]any{"note_id": mine.String(), "title": "x"},
			wantMsg: "You are not signed in to a household.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isErr, env := call(t, tc.ctx, tc.args)
			if !isErr || env.OK || env.Error == nil {
				t.Fatalf("want isError result, got isError=%v %+v", isErr, env)
			}
			if env.Data != nil {
				t.Errorf("error result leaked data: %+v", env.Data)
			}
			if env.Error.Message != tc.wantMsg || env.Summary != tc.wantMsg {
				t.Errorf("message: want %q, got %q (summary %q)", tc.wantMsg, env.Error.Message, env.Summary)
			}
			if env.Error.Field != tc.wantField {
				t.Errorf("field: want %q, got %q", tc.wantField, env.Error.Field)
			}
			if env.Error.Fix == "" {
				t.Error("fix hint is empty")
			}
		})
	}
	if title, body, _ := stored(t, mine); *title != "Mine" || body != "body" {
		t.Errorf("rejected calls changed the note: %v %q", title, body)
	}
}

// TestNoteDelete runs note_delete against a real, fully migrated Postgres.
// Every result must mirror structuredContent in its text block and validate
// against the tool's outputSchema.
func TestNoteDelete(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)

	name, tool, del := NewNoteDeleteTool(pool)
	_, _, create := NewCreateNoteTool(pool)
	_, _, list := NewListNotesTool(pool)
	if name != "note_delete" || tool.Name != "note_delete" {
		t.Fatalf("tool name: got %q / %q", name, tool.Name)
	}
	if got := tool.InputSchema.Required; len(got) != 1 || got[0] != "note_id" {
		t.Errorf("required params: want [note_id], got %v", got)
	}
	a := tool.Annotations
	if a.Title != "Delete note" || a.ReadOnlyHint == nil || *a.ReadOnlyHint ||
		a.DestructiveHint == nil || !*a.DestructiveHint ||
		a.IdempotentHint == nil || !*a.IdempotentHint ||
		a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations: want class X (not read-only, destructive, idempotent, closed world), got %+v", a)
	}
	rs := noteGetOutputSchema(t, tool)

	call := func(t *testing.T, ctx context.Context, args map[string]any) (bool, noteGetEnvelope) {
		t.Helper()
		var req mcp.CallToolRequest
		req.Params.Name = "note_delete"
		req.Params.Arguments = args
		res, err := del(ctx, req)
		if err != nil {
			t.Fatalf("note_delete returned a protocol error: %v", err)
		}
		b, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		var w noteGetWire
		if err := json.Unmarshal(b, &w); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		if len(w.Content) != 1 || w.Content[0].Type != "text" {
			t.Fatalf("want exactly one text block, got %+v", w.Content)
		}
		var fromText map[string]any
		if err := json.Unmarshal([]byte(w.Content[0].Text), &fromText); err != nil {
			t.Fatalf("text block is not JSON: %v", err)
		}
		x, _ := json.Marshal(fromText)
		y, _ := json.Marshal(w.StructuredContent)
		if string(x) != string(y) {
			t.Fatalf("text and structuredContent differ:\n%s\n%s", x, y)
		}
		if err := rs.Validate(w.StructuredContent); err != nil {
			t.Fatalf("structuredContent does not match outputSchema: %v\n%s", err, y)
		}
		var env noteGetEnvelope
		if err := json.Unmarshal([]byte(w.Content[0].Text), &env); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		return w.IsError, env
	}
	newNote := func(t *testing.T, ctx context.Context, title, body string) uuid.UUID {
		t.Helper()
		var n authoredNote
		callJSON(t, ctx, create, map[string]any{"title": title, "content": body}, &n)
		return n.ID
	}
	listed := func(t *testing.T, ctx context.Context) map[uuid.UUID]bool {
		t.Helper()
		var notes []authoredNote
		callJSON(t, ctx, list, nil, &notes)
		ids := map[uuid.UUID]bool{}
		for _, n := range notes {
			ids[n.ID] = true
		}
		return ids
	}
	exists := func(t *testing.T, id uuid.UUID) bool {
		t.Helper()
		var ok bool
		if err := pool.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM notes WHERE id = $1)`, id).Scan(&ok); err != nil {
			t.Fatalf("check note: %v", err)
		}
		return ok
	}

	t.Run("deleted note disappears from list_notes", func(t *testing.T) {
		gone := newNote(t, ctx, "Old Wi-Fi password", "hunter2")
		kept := newNote(t, ctx, "Keep me", "still here")
		if ids := listed(t, ctx); !ids[gone] || !ids[kept] {
			t.Fatalf("precondition: both notes should be listed, got %v", ids)
		}

		isErr, env := call(t, ctx, map[string]any{"note_id": gone.String()})
		if isErr || !env.OK || env.Data == nil {
			t.Fatalf("want success, got isError=%v %+v", isErr, env)
		}
		if env.Data.ID != gone || env.Data.Title == nil || *env.Data.Title != "Old Wi-Fi password" || env.Data.Body != "hunter2" {
			t.Errorf("returned note: %+v", env.Data)
		}
		if env.Data.AuthorID == nil || *env.Data.AuthorID != hh.UserID || env.Data.AuthorName == nil || *env.Data.AuthorName != "Test Owner" {
			t.Errorf("author: got %v / %v", env.Data.AuthorID, env.Data.AuthorName)
		}
		if env.Summary != `Deleted note "Old Wi-Fi password".` {
			t.Errorf("summary: got %q", env.Summary)
		}
		if len(env.Next) == 0 || !strings.Contains(env.Next[0], "note_create") {
			t.Errorf("next: want a note_create hint, got %v", env.Next)
		}

		ids := listed(t, ctx)
		if ids[gone] {
			t.Error("deleted note is still in list_notes")
		}
		if !ids[kept] {
			t.Error("other note disappeared from list_notes")
		}
		if exists(t, gone) {
			t.Error("deleted note is still in the table")
		}

		// A repeat finds nothing and changes nothing more.
		isErr, env = call(t, ctx, map[string]any{"note_id": gone.String()})
		want := `No note with id "` + gone.String() + `" was found.`
		if !isErr || env.Error == nil || env.Error.Message != want {
			t.Fatalf("repeat: want not-found, got isError=%v %+v", isErr, env)
		}
	})

	t.Run("untitled note summary", func(t *testing.T) {
		id := newNote(t, ctx, "x", "body")
		if _, err := pool.Exec(context.Background(), `UPDATE notes SET title = NULL WHERE id = $1`, id); err != nil {
			t.Fatalf("clear title: %v", err)
		}
		_, env := call(t, ctx, map[string]any{"note_id": id.String()})
		if env.Summary != `Deleted note "(untitled)".` || env.Data == nil || env.Data.Title != nil {
			t.Errorf("got summary %q data %+v", env.Summary, env.Data)
		}
	})

	theirs := newNote(t, otherCtx, "Secret", "other household")
	mine := newNote(t, ctx, "Mine", "body")

	tests := []struct {
		name      string
		ctx       context.Context
		args      map[string]any
		wantField string
		wantMsg   string
	}{
		{
			name:    "other household's note is not found",
			ctx:     ctx,
			args:    map[string]any{"note_id": theirs.String()},
			wantMsg: `No note with id "` + theirs.String() + `" was found.`,
		},
		{
			name:    "own note is not found from the other household",
			ctx:     otherCtx,
			args:    map[string]any{"note_id": mine.String()},
			wantMsg: `No note with id "` + mine.String() + `" was found.`,
		},
		{
			name:    "unknown id is not found",
			ctx:     ctx,
			args:    map[string]any{"note_id": "00000000-0000-0000-0000-000000000001"},
			wantMsg: `No note with id "00000000-0000-0000-0000-000000000001" was found.`,
		},
		{
			name:      "title instead of id is invalid",
			ctx:       ctx,
			args:      map[string]any{"note_id": "Mine"},
			wantField: "note_id",
			wantMsg:   `note_id "Mine" is not a UUID.`,
		},
		{
			name:      "missing note_id is invalid",
			ctx:       ctx,
			args:      nil,
			wantField: "note_id",
			wantMsg:   "note_id is required.",
		},
		{
			name:    "no claims is refused",
			ctx:     context.Background(),
			args:    map[string]any{"note_id": mine.String()},
			wantMsg: "You are not signed in to a household.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isErr, env := call(t, tc.ctx, tc.args)
			if !isErr || env.OK || env.Error == nil {
				t.Fatalf("want isError result, got isError=%v %+v", isErr, env)
			}
			if env.Data != nil {
				t.Errorf("error result leaked data: %+v", env.Data)
			}
			if env.Error.Message != tc.wantMsg || env.Summary != tc.wantMsg {
				t.Errorf("message: want %q, got %q (summary %q)", tc.wantMsg, env.Error.Message, env.Summary)
			}
			if env.Error.Field != tc.wantField {
				t.Errorf("field: want %q, got %q", tc.wantField, env.Error.Field)
			}
			if env.Error.Fix == "" {
				t.Error("fix hint is empty")
			}
		})
	}

	if !exists(t, theirs) || !exists(t, mine) {
		t.Errorf("rejected calls deleted a note: theirs=%v mine=%v", exists(t, theirs), exists(t, mine))
	}
	if ids := listed(t, otherCtx); !ids[theirs] {
		t.Error("other household's note vanished from its list_notes")
	}
}
