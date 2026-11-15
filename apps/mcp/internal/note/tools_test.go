package note

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

type handlerFunc = func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)

// wireNote is a full note (noteResult) as a client decodes it.
type wireNote struct {
	ID               uuid.UUID  `json:"id"`
	AboutType        string     `json:"about_type"`
	AboutID          *uuid.UUID `json:"about_id"`
	Title            *string    `json:"title"`
	Body             string     `json:"body"`
	AuthorID         *uuid.UUID `json:"author_id"`
	AuthorName       *string    `json:"author_name"`
	CreatedAt        time.Time  `json:"created_at"`
	CreatedLocalDate string     `json:"created_local_date"`
	CreatedWeekday   string     `json:"created_weekday"`
	UpdatedAt        time.Time  `json:"updated_at"`
	UpdatedLocalDate string     `json:"updated_local_date"`
	UpdatedWeekday   string     `json:"updated_weekday"`
	Duplicate        bool       `json:"duplicate"`
}

// wireSummary is one note_find item as a client decodes it.
type wireSummary struct {
	ID         uuid.UUID  `json:"id"`
	AboutType  string     `json:"about_type"`
	AboutID    *uuid.UUID `json:"about_id"`
	Title      *string    `json:"title"`
	Preview    string     `json:"preview"`
	AuthorName *string    `json:"author_name"`
}

// envelope is a respond envelope (single or list) as a client decodes it.
type envelope[T any] struct {
	OK        bool     `json:"ok"`
	Summary   string   `json:"summary"`
	Data      *T       `json:"data"`
	Items     []T      `json:"items"`
	Returned  *int     `json:"returned"`
	Total     *int     `json:"total"`
	Truncated bool     `json:"truncated"`
	Narrow    string   `json:"narrow"`
	Next      []string `json:"next"`
	Error     *struct {
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Field   string         `json:"field"`
		Example map[string]any `json:"example"`
	} `json:"error"`
}

// outputSchema resolves the outputSchema a tool advertises in tools/list.
func outputSchema(t *testing.T, tool mcp.Tool) *jsonschema.Resolved {
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

// caller runs one tool and checks every result the way a strict client
// would: a tool result (never a protocol error) with exactly one text block
// that equals structuredContent, which validates against the outputSchema.
type caller[T any] struct {
	t       *testing.T
	tool    mcp.Tool
	handler handlerFunc
	rs      *jsonschema.Resolved
}

func newCaller[T any](t *testing.T, name string, tool mcp.Tool, h handlerFunc) caller[T] {
	t.Helper()
	if name != tool.Name {
		t.Fatalf("returned name %q != tool name %q", name, tool.Name)
	}
	return caller[T]{t: t, tool: tool, handler: h, rs: outputSchema(t, tool)}
}

func (c caller[T]) call(t *testing.T, ctx context.Context, args map[string]any) (bool, envelope[T]) {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = c.tool.Name
	req.Params.Arguments = args
	res, err := c.handler(ctx, req)
	if err != nil {
		t.Fatalf("%s returned a protocol error: %v", c.tool.Name, err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var w struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent map[string]any `json:"structuredContent"`
		IsError           bool           `json:"isError"`
	}
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
	if err := c.rs.Validate(w.StructuredContent); err != nil {
		t.Fatalf("structuredContent does not match outputSchema: %v\n%s", err, y)
	}
	var env envelope[T]
	if err := json.Unmarshal([]byte(w.Content[0].Text), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if w.IsError == env.OK {
		t.Fatalf("isError=%v but ok=%v", w.IsError, env.OK)
	}
	return w.IsError, env
}

// mustOK calls and fails the test unless the result is a success.
func (c caller[T]) mustOK(t *testing.T, ctx context.Context, args map[string]any) envelope[T] {
	t.Helper()
	isErr, env := c.call(t, ctx, args)
	if isErr || !env.OK {
		t.Fatalf("%s %v: want success, got %+v (error %+v)", c.tool.Name, args, env, env.Error)
	}
	return env
}

// errCase is one table row of a tool's error results.
type errCase struct {
	name      string
	ctx       context.Context
	args      map[string]any
	wantField string
	wantMsg   string
}

func runErrCases[T any](t *testing.T, c caller[T], cases []errCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isErr, env := c.call(t, tc.ctx, tc.args)
			if !isErr || env.OK || env.Error == nil {
				t.Fatalf("want isError result, got isError=%v %+v", isErr, env)
			}
			if env.Data != nil || env.Items != nil {
				t.Errorf("error result leaked data: %+v %+v", env.Data, env.Items)
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

// TestToolDefinitions checks names, required params, the four annotation
// hints of each catalog class, outputSchemas and description lengths.
func TestToolDefinitions(t *testing.T) {
	type class struct{ readOnly, destructive, idempotent bool }
	var (
		classR = class{true, false, true}
		classC = class{false, false, false}
		classU = class{false, false, true}
		classX = class{false, true, true}
	)
	tests := []struct {
		build    func() (string, mcp.Tool, handlerFunc)
		name     string
		title    string
		required []string
		optional []string
		class    class
	}{
		{func() (string, mcp.Tool, handlerFunc) { return NewNoteFindTool(nil) }, "note_find", "Find notes",
			nil, []string{"query", "about_type", "about_id", "limit", "cursor"}, classR},
		{func() (string, mcp.Tool, handlerFunc) { return NewNoteGetTool(nil) }, "note_get", "Get note",
			[]string{"note_id"}, nil, classR},
		{func() (string, mcp.Tool, handlerFunc) { return NewNoteCreateTool(nil) }, "note_create", "Create note",
			[]string{"title", "content"}, []string{"about_type", "about_id"}, classC},
		{func() (string, mcp.Tool, handlerFunc) { return NewNoteUpdateTool(nil) }, "note_update", "Update note",
			[]string{"note_id"}, []string{"title", "content", "append_text"}, classU},
		{func() (string, mcp.Tool, handlerFunc) { return NewNoteDeleteTool(nil) }, "note_delete", "Delete note",
			[]string{"note_id"}, nil, classX},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, tool, _ := tc.build()
			if name != tc.name || tool.Name != tc.name {
				t.Fatalf("name: got %q / %q", name, tool.Name)
			}
			if got := tool.InputSchema.Required; strings.Join(got, ",") != strings.Join(tc.required, ",") {
				t.Errorf("required: want %v, got %v", tc.required, got)
			}
			want := map[string]bool{}
			for _, p := range append(append([]string{}, tc.required...), tc.optional...) {
				want[p] = true
			}
			if len(tool.InputSchema.Properties) != len(want) {
				t.Errorf("params: want %d, got %d (%v)", len(want), len(tool.InputSchema.Properties), tool.InputSchema.Properties)
			}
			for p, raw := range tool.InputSchema.Properties {
				if !want[p] {
					t.Errorf("unexpected param %q", p)
				}
				desc, _ := raw.(map[string]any)["description"].(string)
				if desc == "" || len([]rune(desc)) > 80 {
					t.Errorf("param %q description must be 1-80 chars, got %d: %q", p, len([]rune(desc)), desc)
				}
			}
			if n := len([]rune(tool.Description)); n == 0 || n > 200 {
				t.Errorf("description must be 1-200 chars, got %d", n)
			}
			a := tool.Annotations
			if a.Title != tc.title || a.ReadOnlyHint == nil || a.DestructiveHint == nil ||
				a.IdempotentHint == nil || a.OpenWorldHint == nil {
				t.Fatalf("annotations: want title %q and all four hints set, got %+v", tc.title, a)
			}
			got := class{*a.ReadOnlyHint, *a.DestructiveHint, *a.IdempotentHint}
			if got != tc.class || *a.OpenWorldHint {
				t.Errorf("hints: want %+v openWorld=false, got %+v openWorld=%v", tc.class, got, *a.OpenWorldHint)
			}
			outputSchema(t, tool)
		})
	}

	t.Run("about_type enum matches the catalog", func(t *testing.T) {
		for _, build := range []func() (string, mcp.Tool, handlerFunc){
			func() (string, mcp.Tool, handlerFunc) { return NewNoteFindTool(nil) },
			func() (string, mcp.Tool, handlerFunc) { return NewNoteCreateTool(nil) },
		} {
			_, tool, _ := build()
			enum, _ := tool.InputSchema.Properties["about_type"].(map[string]any)["enum"].([]string)
			want := "family,person,property,room,asset,vehicle,pet,contact,bill,loan,maintenance,event,list,recipe,guide"
			if strings.Join(enum, ",") != want {
				t.Errorf("%s about_type enum: got %v", tool.Name, enum)
			}
		}
	})
}

// seedRow inserts a minimal row of table for householdID and returns its id.
// Notes must be about a row of their own family (038).
func seedRow(t *testing.T, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, table string, householdID uuid.UUID) uuid.UUID {
	t.Helper()
	var sql string
	switch table {
	case "properties":
		sql = `INSERT INTO properties (household_id, name) VALUES ($1, 'Test property') RETURNING id`
	case "vehicles":
		sql = `INSERT INTO vehicles (household_id, make) VALUES ($1, 'Test') RETURNING id`
	case "vendors":
		sql = `INSERT INTO vendors (household_id, name) VALUES ($1, 'Test plumber') RETURNING id`
	case "people":
		sql = `INSERT INTO people (household_id, display_name) VALUES ($1, 'Nora') RETURNING id`
	default:
		t.Fatalf("seedRow: unsupported table %q", table)
	}
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), sql, householdID).Scan(&id); err != nil {
		t.Fatalf("seed %s: %v", table, err)
	}
	return id
}

var localDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// TestNoteCreate runs note_create against a real, fully migrated Postgres.
func TestNoteCreate(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, other := testutil.SeedHousehold(t, pool)
	_ = otherCtx

	name, tool, h := NewNoteCreateTool(pool)
	create := newCaller[wireNote](t, name, tool, h)

	vehicleID := seedRow(t, pool, "vehicles", hh.HouseholdID)
	vendorID := seedRow(t, pool, "vendors", hh.HouseholdID)
	personID := seedRow(t, pool, "people", hh.HouseholdID)
	theirVehicle := seedRow(t, pool, "vehicles", other.HouseholdID)

	stored := func(t *testing.T, id uuid.UUID) (author *uuid.UUID, kind string, entity uuid.UUID) {
		t.Helper()
		if err := pool.QueryRow(context.Background(),
			`SELECT author_id, entity_type, entity_id FROM notes WHERE id = $1`, id,
		).Scan(&author, &kind, &entity); err != nil {
			t.Fatalf("select stored note: %v", err)
		}
		return author, kind, entity
	}

	tests := []struct {
		name        string
		args        map[string]any
		wantAbout   string
		wantAboutID *uuid.UUID
		wantKind    string
		wantEntity  uuid.UUID
		wantSummary string
	}{
		{
			name: "default is a family note", args: map[string]any{"title": "Wi-Fi", "content": "HomeNet / hunter2"},
			wantAbout: "family", wantKind: "household", wantEntity: hh.HouseholdID,
			wantSummary: `Saved note "Wi-Fi" for the family.`,
		},
		{
			name: "explicit family ignores about_id", args: map[string]any{"title": "Trash day", "content": "Tuesday", "about_type": "family", "about_id": uuid.NewString()},
			wantAbout: "family", wantKind: "household", wantEntity: hh.HouseholdID,
		},
		{
			name: "about a vehicle", args: map[string]any{"title": "Tires", "content": "Rotate tires", "about_type": "vehicle", "about_id": vehicleID.String()},
			wantAbout: "vehicle", wantAboutID: &vehicleID, wantKind: "vehicle", wantEntity: vehicleID,
			wantSummary: `Saved note "Tires" about vehicle ` + vehicleID.String() + `.`,
		},
		{
			name: "contact maps to vendor", args: map[string]any{"title": "Plumber", "content": "Ask for Sam", "about_type": "contact", "about_id": vendorID.String()},
			wantAbout: "contact", wantAboutID: &vendorID, wantKind: "vendor", wantEntity: vendorID,
		},
		{
			name: "about a person", args: map[string]any{"title": "Nora sizes", "content": "Shoes 11", "about_type": "Person", "about_id": personID.String()},
			wantAbout: "person", wantAboutID: &personID, wantKind: "person", wantEntity: personID,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := create.mustOK(t, ctx, tc.args)
			n := env.Data
			if n.AboutType != tc.wantAbout || (n.AboutID == nil) != (tc.wantAboutID == nil) ||
				(n.AboutID != nil && *n.AboutID != *tc.wantAboutID) {
				t.Errorf("about: want %s/%v, got %s/%v", tc.wantAbout, tc.wantAboutID, n.AboutType, n.AboutID)
			}
			if n.Body != tc.args["content"] || n.Title == nil || *n.Title != tc.args["title"] {
				t.Errorf("title/body: got %v / %q", n.Title, n.Body)
			}
			if n.AuthorID == nil || *n.AuthorID != hh.UserID || n.AuthorName == nil || *n.AuthorName != "Test Owner" {
				t.Errorf("author: got %v / %v", n.AuthorID, n.AuthorName)
			}
			if !localDateRe.MatchString(n.CreatedLocalDate) || n.CreatedWeekday == "" || n.Duplicate {
				t.Errorf("local date/weekday/duplicate: %q %q %v", n.CreatedLocalDate, n.CreatedWeekday, n.Duplicate)
			}
			if tc.wantSummary != "" && env.Summary != tc.wantSummary {
				t.Errorf("summary: want %q, got %q", tc.wantSummary, env.Summary)
			}
			if len(env.Next) == 0 || !strings.Contains(env.Next[0], "note_update note_id="+n.ID.String()) {
				t.Errorf("next: want a note_update append hint, got %v", env.Next)
			}
			author, kind, entity := stored(t, n.ID)
			if author == nil || *author != hh.UserID || kind != tc.wantKind || entity != tc.wantEntity {
				t.Errorf("stored: author %v entity %s/%s, want %s/%s", author, kind, entity, tc.wantKind, tc.wantEntity)
			}
		})
	}

	t.Run("an identical create within 10 minutes returns the first note", func(t *testing.T) {
		args := map[string]any{"title": "Alarm code", "content": "1234"}
		first := create.mustOK(t, ctx, args)
		again := create.mustOK(t, ctx, args)
		if again.Data.ID != first.Data.ID || !again.Data.Duplicate {
			t.Fatalf("want the same note with duplicate:true, got %+v", again.Data)
		}
		if !strings.Contains(again.Summary, "already saved") || !strings.Contains(again.Next[0], "note_update") {
			t.Errorf("summary/next: %q %v", again.Summary, again.Next)
		}
		var count int
		if err := pool.QueryRow(context.Background(),
			`SELECT count(*) FROM notes WHERE household_id = $1 AND title = 'Alarm code'`, hh.HouseholdID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("want 1 row, got %d", count)
		}
		// Outside the window a second row is made.
		if _, err := pool.Exec(context.Background(),
			`UPDATE notes SET created_at = NOW() - interval '11 minutes' WHERE id = $1`, first.Data.ID); err != nil {
			t.Fatal(err)
		}
		if later := create.mustOK(t, ctx, args); later.Data.ID == first.Data.ID || later.Data.Duplicate {
			t.Errorf("after the window: want a new note, got %+v", later.Data)
		}
	})

	t.Run("checklist-like content gets a list_create hint", func(t *testing.T) {
		env := create.mustOK(t, ctx, map[string]any{"title": "Groceries", "content": "milk\neggs\nbread"})
		if len(env.Next) < 2 || !strings.Contains(env.Next[1], "list_create") {
			t.Errorf("next: %v", env.Next)
		}
		env = create.mustOK(t, ctx, map[string]any{"title": "Story", "content": "One line only."})
		if len(env.Next) != 1 {
			t.Errorf("prose should get one hint, got %v", env.Next)
		}
	})

	t.Run("caller that is not a user gets a null author", func(t *testing.T) {
		claims := *hh.Claims
		claims.UserID = "local-dev"
		env := create.mustOK(t, server.ContextWithClaims(context.Background(), &claims),
			map[string]any{"title": "Dev", "content": "from local dev"})
		if env.Data.AuthorID != nil || env.Data.AuthorName != nil {
			t.Errorf("want null author, got %v / %v", env.Data.AuthorID, env.Data.AuthorName)
		}
	})

	t.Run("local date follows the family timezone", func(t *testing.T) {
		if _, err := pool.Exec(context.Background(),
			`UPDATE households SET timezone = 'Pacific/Kiritimati' WHERE id = $1`, other.HouseholdID); err != nil {
			t.Fatal(err)
		}
		env := create.mustOK(t, otherCtx, map[string]any{"title": "TZ", "content": "x"})
		loc, _ := time.LoadLocation("Pacific/Kiritimati")
		local := env.Data.CreatedAt.In(loc)
		if env.Data.CreatedLocalDate != local.Format("2006-01-02") || env.Data.CreatedWeekday != local.Format("Mon") {
			t.Errorf("want %s %s, got %s %s", local.Format("2006-01-02"), local.Format("Mon"),
				env.Data.CreatedLocalDate, env.Data.CreatedWeekday)
		}
	})

	runErrCases(t, create, []errCase{
		{name: "another family's vehicle is refused", ctx: ctx, wantField: "about_id",
			args:    map[string]any{"title": "x", "content": "y", "about_type": "vehicle", "about_id": theirVehicle.String()},
			wantMsg: "No vehicle with id " + theirVehicle.String() + " was found in this family."},
		{name: "unknown about_id is refused", ctx: ctx, wantField: "about_id",
			args:    map[string]any{"title": "x", "content": "y", "about_type": "pet", "about_id": "00000000-0000-0000-0000-000000000001"},
			wantMsg: "No pet with id 00000000-0000-0000-0000-000000000001 was found in this family."},
		{name: "a kind the DB does not know yet is refused", ctx: ctx, wantField: "about_type",
			args:    map[string]any{"title": "x", "content": "y", "about_type": "recipe", "about_id": uuid.NewString()},
			wantMsg: "Notes about a recipe can't be saved yet."},
		{name: "about_type without about_id", ctx: ctx, wantField: "about_id",
			args: map[string]any{"title": "x", "content": "y", "about_type": "vehicle"}, wantMsg: "about_type vehicle needs about_id."},
		{name: "about_id without about_type", ctx: ctx, wantField: "about_type",
			args: map[string]any{"title": "x", "content": "y", "about_id": vehicleID.String()}, wantMsg: "about_id was given without about_type."},
		{name: "unknown about_type", ctx: ctx, wantField: "about_type",
			args: map[string]any{"title": "x", "content": "y", "about_type": "car", "about_id": vehicleID.String()}, wantMsg: `about_type "car" is not one of the known kinds.`},
		{name: "about_id that is a name", ctx: ctx, wantField: "about_id",
			args: map[string]any{"title": "x", "content": "y", "about_type": "vehicle", "about_id": "Minivan"}, wantMsg: `about_id "Minivan" is not a UUID.`},
		{name: "missing title", ctx: ctx, wantField: "title", args: map[string]any{"content": "y"}, wantMsg: "title is required."},
		{name: "blank content", ctx: ctx, wantField: "content", args: map[string]any{"title": "x", "content": "  "}, wantMsg: "content is required."},
		{name: "title over 500 characters", ctx: ctx, wantField: "title",
			args: map[string]any{"title": strings.Repeat("é", 501), "content": "y"}, wantMsg: "title is 501 characters; the limit is 500."},
		{name: "no claims is refused", ctx: context.Background(),
			args: map[string]any{"title": "x", "content": "y"}, wantMsg: "You are not signed in to a household."},
	})

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM notes WHERE title = 'x'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("rejected creates wrote %d rows", count)
	}
}

// TestNoteFind runs note_find against a real, fully migrated Postgres:
// household scoping, about filters, text search, previews and paging.
func TestNoteFind(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, other := testutil.SeedHousehold(t, pool)

	name, tool, h := NewNoteFindTool(pool)
	find := newCaller[wireSummary](t, name, tool, h)

	propertyID := seedRow(t, pool, "properties", hh.HouseholdID)
	vehicleID := seedRow(t, pool, "vehicles", hh.HouseholdID)
	vendorID := seedRow(t, pool, "vendors", hh.HouseholdID)
	age := 0
	insert := func(householdID uuid.UUID, kind string, entityID uuid.UUID, title, body string) uuid.UUID {
		t.Helper()
		age++ // later inserts are newer
		var id uuid.UUID
		if err := pool.QueryRow(context.Background(),
			`INSERT INTO notes (household_id, entity_type, entity_id, title, body, author_id, updated_at)
			 VALUES ($1, $2, $3, $4, $5, (SELECT user_id FROM memberships WHERE household_id = $1 LIMIT 1),
			         NOW() - make_interval(mins => 100 - $6::int)) RETURNING id`,
			householdID, kind, entityID, title, body, age,
		).Scan(&id); err != nil {
			t.Fatalf("insert note: %v", err)
		}
		return id
	}
	longBody := strings.Repeat("ä", 150)
	roof := insert(hh.HouseholdID, "property", propertyID, "Roof", "Replace shingles in spring")
	oil := insert(hh.HouseholdID, "vehicle", vehicleID, "Oil", "Change oil at 5k miles")
	plumber := insert(hh.HouseholdID, "vendor", vendorID, "Plumber", "Ask for Sam")
	wifi := insert(hh.HouseholdID, "household", hh.HouseholdID, "Wi-Fi", longBody+" WIFI password")
	insert(other.HouseholdID, "household", other.HouseholdID, "Not mine", "Wi-Fi of another household")

	ids := func(items []wireSummary) []uuid.UUID {
		out := make([]uuid.UUID, len(items))
		for i, it := range items {
			out[i] = it.ID
		}
		return out
	}
	same := func(a, b []uuid.UUID) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	tests := []struct {
		name string
		args map[string]any
		want []uuid.UUID
	}{
		{"everything in the household, newest first", nil, []uuid.UUID{wifi, plumber, oil, roof}},
		{"about one property", map[string]any{"about_type": "property", "about_id": propertyID.String()}, []uuid.UUID{roof}},
		{"every vehicle note", map[string]any{"about_type": "vehicle"}, []uuid.UUID{oil}},
		{"contact means vendor rows", map[string]any{"about_type": "contact"}, []uuid.UUID{plumber}},
		{"family notes", map[string]any{"about_type": "family"}, []uuid.UUID{wifi}},
		{"query matches title or body, any case", map[string]any{"query": "wifi"}, []uuid.UUID{wifi}},
		{"query with a filter", map[string]any{"query": "sam", "about_type": "contact"}, []uuid.UUID{plumber}},
		{"kind the DB does not know matches nothing", map[string]any{"about_type": "recipe"}, nil},
		{"empty strings are not filters", map[string]any{"query": "", "about_type": "", "about_id": "", "cursor": ""}, []uuid.UUID{wifi, plumber, oil, roof}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := find.mustOK(t, ctx, tc.args)
			if got := ids(env.Items); !same(got, tc.want) {
				t.Fatalf("want %v, got %v (%+v)", tc.want, got, env.Items)
			}
			if env.Items == nil || env.Total == nil || *env.Total != len(tc.want) || env.Truncated {
				t.Errorf("items/total/truncated: %v %v %v", env.Items, env.Total, env.Truncated)
			}
		})
	}

	t.Run("items are previews in catalog vocabulary", func(t *testing.T) {
		env := find.mustOK(t, ctx, nil)
		byID := map[uuid.UUID]wireSummary{}
		for _, it := range env.Items {
			byID[it.ID] = it
		}
		if w := byID[wifi]; w.AboutType != "family" || w.AboutID != nil || w.Preview != strings.Repeat("ä", 100)+"..." {
			t.Errorf("family note: %+v", w)
		}
		if p := byID[plumber]; p.AboutType != "contact" || p.AboutID == nil || *p.AboutID != vendorID {
			t.Errorf("vendor note: %+v", p)
		}
		if a := byID[oil].AuthorName; a == nil || *a != "Test Owner" {
			t.Errorf("author_name: %v", a)
		}
		if env.Summary != "Found 4 notes." || len(env.Next) == 0 || !strings.Contains(env.Next[0], "note_get note_id="+wifi.String()) {
			t.Errorf("summary/next: %q %v", env.Summary, env.Next)
		}
	})

	t.Run("cursor pages through every note once", func(t *testing.T) {
		first := find.mustOK(t, ctx, map[string]any{"limit": 3})
		if !same(ids(first.Items), []uuid.UUID{wifi, plumber, oil}) || !first.Truncated || *first.Total != 4 {
			t.Fatalf("page 1: %v truncated=%v total=%v", ids(first.Items), first.Truncated, *first.Total)
		}
		m := regexp.MustCompile(`cursor="([^"]+)"`).FindStringSubmatch(first.Narrow)
		if m == nil || first.Summary != "Found 4 notes; showing 1–3." || !strings.Contains(first.Next[len(first.Next)-1], m[1]) {
			t.Fatalf("narrow/summary/next: %q %q %v", first.Narrow, first.Summary, first.Next)
		}
		second := find.mustOK(t, ctx, map[string]any{"limit": "3", "cursor": m[1]})
		if !same(ids(second.Items), []uuid.UUID{roof}) || second.Truncated || second.Summary != "Found 4 notes; showing 4–4." {
			t.Fatalf("page 2: %v truncated=%v %q", ids(second.Items), second.Truncated, second.Summary)
		}
		past := find.mustOK(t, ctx, map[string]any{"cursor": encodeCursor(10)})
		if len(past.Items) != 0 || past.Summary != "No more notes past this cursor." {
			t.Errorf("past the end: %+v", past)
		}
	})

	t.Run("another household sees only its own", func(t *testing.T) {
		env := find.mustOK(t, otherCtx, map[string]any{"query": "wi-fi"})
		if len(env.Items) != 1 || *env.Items[0].Title != "Not mine" {
			t.Errorf("got %+v", env.Items)
		}
		env = find.mustOK(t, otherCtx, map[string]any{"about_type": "property", "about_id": propertyID.String()})
		if len(env.Items) != 0 {
			t.Errorf("cross-household about_id leaked: %+v", env.Items)
		}
	})

	t.Run("no match says how to add one", func(t *testing.T) {
		env := find.mustOK(t, ctx, map[string]any{"query": "zzz-nothing"})
		if env.Items == nil || len(env.Items) != 0 || env.Summary != "No notes found." || !strings.Contains(env.Next[0], "note_create") {
			t.Errorf("got %+v", env)
		}
	})

	runErrCases(t, find, []errCase{
		{name: "about_id without about_type", ctx: ctx, wantField: "about_type",
			args: map[string]any{"about_id": propertyID.String()}, wantMsg: "about_id was given without about_type."},
		{name: "unknown about_type", ctx: ctx, wantField: "about_type",
			args: map[string]any{"about_type": "house"}, wantMsg: `about_type "house" is not one of the known kinds.`},
		{name: "about_id that is a name", ctx: ctx, wantField: "about_id",
			args: map[string]any{"about_type": "property", "about_id": "Lake House"}, wantMsg: `about_id "Lake House" is not a UUID.`},
		{name: "limit too big", ctx: ctx, wantField: "limit",
			args: map[string]any{"limit": 500}, wantMsg: "limit must be a whole number from 1 to 100."},
		{name: "limit not a number", ctx: ctx, wantField: "limit",
			args: map[string]any{"limit": "lots"}, wantMsg: "limit must be a whole number from 1 to 100."},
		{name: "made-up cursor", ctx: ctx, wantField: "cursor",
			args: map[string]any{"cursor": "page2"}, wantMsg: "cursor is not one note_find returned."},
		{name: "no claims is refused", ctx: context.Background(), args: nil, wantMsg: "You are not signed in to a household."},
	})
}

// TestNoteGet runs note_get against a real, fully migrated Postgres.
func TestNoteGet(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)

	name, tool, h := NewNoteGetTool(pool)
	get := newCaller[wireNote](t, name, tool, h)
	cn, ct, ch := NewNoteCreateTool(pool)
	create := newCaller[wireNote](t, cn, ct, ch)

	// A body far beyond list previews, with multi-byte characters and newlines.
	longBody := strings.Repeat("Line with ümlauts and emoji 🏠.\n", 200)
	mine := create.mustOK(t, ctx, map[string]any{"title": "Wi-Fi setup", "content": longBody}).Data
	vehicleID := seedRow(t, pool, "vehicles", hh.HouseholdID)
	linked := create.mustOK(t, ctx, map[string]any{
		"title": "Tires", "content": "Rotate every 6k miles", "about_type": "vehicle", "about_id": vehicleID.String(),
	}).Data
	theirs := create.mustOK(t, otherCtx, map[string]any{"title": "Secret", "content": "other household"}).Data

	t.Run("returns the complete body, author and timestamps", func(t *testing.T) {
		env := get.mustOK(t, ctx, map[string]any{"note_id": mine.ID.String()})
		n := env.Data
		if n.ID != mine.ID || n.Title == nil || *n.Title != "Wi-Fi setup" || n.Body != longBody {
			t.Errorf("id/title/body: got %s / %v / %d bytes", n.ID, n.Title, len(n.Body))
		}
		if n.AuthorID == nil || *n.AuthorID != hh.UserID || n.AuthorName == nil || *n.AuthorName != "Test Owner" {
			t.Errorf("author: got %v / %v", n.AuthorID, n.AuthorName)
		}
		if n.AboutType != "family" || n.AboutID != nil {
			t.Errorf("about: got %s/%v", n.AboutType, n.AboutID)
		}
		if n.CreatedAt.IsZero() || n.UpdatedAt.IsZero() || !localDateRe.MatchString(n.UpdatedLocalDate) || n.UpdatedWeekday == "" {
			t.Errorf("timestamps: %+v", n)
		}
		if env.Summary != `Found note "Wi-Fi setup" by Test Owner.` {
			t.Errorf("summary: got %q", env.Summary)
		}
		if len(env.Next) == 0 || !strings.Contains(env.Next[0], "note_update") || !strings.Contains(env.Next[0], mine.ID.String()) {
			t.Errorf("next: want a note_update hint with the id, got %v", env.Next)
		}
	})

	t.Run("returns what it is about", func(t *testing.T) {
		env := get.mustOK(t, ctx, map[string]any{"note_id": linked.ID.String()})
		if env.Data.AboutType != "vehicle" || env.Data.AboutID == nil || *env.Data.AboutID != vehicleID {
			t.Fatalf("about: got %+v", env.Data)
		}
	})

	runErrCases(t, get, []errCase{
		{name: "other household's note is not found", ctx: ctx,
			args: map[string]any{"note_id": theirs.ID.String()}, wantMsg: `No note with id "` + theirs.ID.String() + `" was found.`},
		{name: "own note is not found from the other household", ctx: otherCtx,
			args: map[string]any{"note_id": mine.ID.String()}, wantMsg: `No note with id "` + mine.ID.String() + `" was found.`},
		{name: "unknown id is not found", ctx: ctx,
			args:    map[string]any{"note_id": "00000000-0000-0000-0000-000000000001"},
			wantMsg: `No note with id "00000000-0000-0000-0000-000000000001" was found.`},
		{name: "title instead of id is invalid", ctx: ctx, wantField: "note_id",
			args: map[string]any{"note_id": "Wi-Fi setup"}, wantMsg: `note_id "Wi-Fi setup" is not a UUID.`},
		{name: "missing note_id is invalid", ctx: ctx, wantField: "note_id", args: nil, wantMsg: "note_id is required."},
		{name: "no claims is refused", ctx: context.Background(),
			args: map[string]any{"note_id": mine.ID.String()}, wantMsg: "You are not signed in to a household."},
	})
}

// TestNoteUpdate runs note_update against a real, fully migrated Postgres.
func TestNoteUpdate(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)

	name, tool, h := NewNoteUpdateTool(pool)
	update := newCaller[wireNote](t, name, tool, h)
	cn, ct, ch := NewNoteCreateTool(pool)
	create := newCaller[wireNote](t, cn, ct, ch)

	newNote := func(t *testing.T, ctx context.Context, title, body string) uuid.UUID {
		t.Helper()
		n := create.mustOK(t, ctx, map[string]any{"title": title, "content": body}).Data
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

		env := update.mustOK(t, ctx, map[string]any{"note_id": id.String(), "title": "Weekly groceries", "content": "milk\nbread"})
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
		env := update.mustOK(t, ctx, map[string]any{"note_id": id.String(), "title": "New", "content": "", "append_text": ""})
		if *env.Data.Title != "New" || env.Data.Body != "keep me" {
			t.Fatalf("got %+v", env.Data)
		}
	})

	t.Run("append_text adds a line and keeps the rest", func(t *testing.T) {
		id := newNote(t, ctx, "Packing", "- socks")
		env := update.mustOK(t, ctx, map[string]any{"note_id": id.String(), "append_text": "- toothbrush"})
		if env.Data.Body != "- socks\n- toothbrush" || *env.Data.Title != "Packing" {
			t.Fatalf("got %+v", env.Data)
		}
		if env.Summary != `Updated note "Packing": added text to the end.` {
			t.Errorf("summary: got %q", env.Summary)
		}

		// The identical append again within 60 s is ignored.
		_, _, at := stored(t, id)
		env = update.mustOK(t, ctx, map[string]any{"note_id": id.String(), "append_text": "- toothbrush"})
		if env.Data.Body != "- socks\n- toothbrush" || !strings.Contains(env.Summary, "nothing was added") {
			t.Fatalf("duplicate append: %q %+v", env.Summary, env.Data)
		}
		if _, body, at2 := stored(t, id); body != "- socks\n- toothbrush" || !at2.Equal(at) {
			t.Errorf("duplicate append wrote: body %q updated_at %v -> %v", body, at, at2)
		}

		// A different line is still appended.
		env = update.mustOK(t, ctx, map[string]any{"note_id": id.String(), "append_text": "- charger"})
		if env.Data.Body != "- socks\n- toothbrush\n- charger" {
			t.Fatalf("second append: %+v", env.Data)
		}
	})

	// #4612: a text that only matches the end of the last line is a new line.
	t.Run("a partial suffix of the last line is appended", func(t *testing.T) {
		id := newNote(t, ctx, "Shopping", "x")
		if _, err := pool.Exec(context.Background(),
			`UPDATE notes SET body = '- buy 2 milk', updated_at = NOW() WHERE id = $1`, id); err != nil {
			t.Fatalf("reset: %v", err)
		}
		env := update.mustOK(t, ctx, map[string]any{"note_id": id.String(), "append_text": "milk"})
		if env.Data.Body != "- buy 2 milk\nmilk" || strings.Contains(env.Summary, "nothing was added") {
			t.Fatalf("got %q %+v", env.Summary, env.Data)
		}
		// A single-line body equal to the text is still a duplicate.
		if _, err := pool.Exec(context.Background(),
			`UPDATE notes SET body = 'call the plumber', updated_at = NOW() WHERE id = $1`, id); err != nil {
			t.Fatalf("reset: %v", err)
		}
		env = update.mustOK(t, ctx, map[string]any{"note_id": id.String(), "append_text": "plumber"})
		if env.Data.Body != "call the plumber\nplumber" {
			t.Fatalf("partial suffix of a one-line body: %+v", env.Data)
		}
		env = update.mustOK(t, ctx, map[string]any{"note_id": id.String(), "append_text": "plumber"})
		if env.Data.Body != "call the plumber\nplumber" || !strings.Contains(env.Summary, "nothing was added") {
			t.Fatalf("identical line: %q %+v", env.Summary, env.Data)
		}
	})

	t.Run("the same append is accepted again after the window", func(t *testing.T) {
		id := newNote(t, ctx, "Log", "fed the cat")
		if _, err := pool.Exec(context.Background(),
			`UPDATE notes SET body = 'fed the cat', updated_at = NOW() - interval '2 minutes' WHERE id = $1`, id); err != nil {
			t.Fatalf("reset: %v", err)
		}
		env := update.mustOK(t, ctx, map[string]any{"note_id": id.String(), "append_text": "fed the cat"})
		if env.Data.Body != "fed the cat\nfed the cat" {
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
			isErr, env := update.call(t, ctx, args)
			want := `No note with id "` + theirs.String() + `" was found.`
			if !isErr || env.Error == nil || env.Error.Message != want {
				t.Fatalf("want not-found, got isError=%v %+v", isErr, env)
			}
		}
		if title, body, _ := stored(t, theirs); *title != "Secret" || body != "other household" {
			t.Errorf("other household's note changed: %v %q", title, body)
		}
	})

	runErrCases(t, update, []errCase{
		{name: "own note is not found from the other household", ctx: otherCtx,
			args: map[string]any{"note_id": mine.String(), "title": "x"}, wantMsg: `No note with id "` + mine.String() + `" was found.`},
		{name: "unknown id is not found", ctx: ctx,
			args:    map[string]any{"note_id": "00000000-0000-0000-0000-000000000001", "title": "x"},
			wantMsg: `No note with id "00000000-0000-0000-0000-000000000001" was found.`},
		{name: "title instead of id is invalid", ctx: ctx, wantField: "note_id",
			args: map[string]any{"note_id": "Groceries", "title": "x"}, wantMsg: `note_id "Groceries" is not a UUID.`},
		{name: "missing note_id is invalid", ctx: ctx, wantField: "note_id",
			args: map[string]any{"title": "x"}, wantMsg: "note_id is required."},
		{name: "nothing to change is invalid", ctx: ctx,
			args: map[string]any{"note_id": mine.String(), "title": ""}, wantMsg: "Nothing to change: pass title, content or append_text."},
		{name: "content with append_text is invalid", ctx: ctx, wantField: "append_text",
			args: map[string]any{"note_id": mine.String(), "content": "a", "append_text": "b"}, wantMsg: "Pass content or append_text, not both."},
		{name: "title over 500 characters is invalid", ctx: ctx, wantField: "title",
			args: map[string]any{"note_id": mine.String(), "title": strings.Repeat("é", 501)}, wantMsg: "title is 501 characters; the limit is 500."},
		{name: "no claims is refused", ctx: context.Background(),
			args: map[string]any{"note_id": mine.String(), "title": "x"}, wantMsg: "You are not signed in to a household."},
	})
	if title, body, _ := stored(t, mine); *title != "Mine" || body != "body" {
		t.Errorf("rejected calls changed the note: %v %q", title, body)
	}
}

// TestNoteDelete runs note_delete against a real, fully migrated Postgres.
func TestNoteDelete(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)

	name, tool, h := NewNoteDeleteTool(pool)
	del := newCaller[wireNote](t, name, tool, h)
	cn, ct, ch := NewNoteCreateTool(pool)
	create := newCaller[wireNote](t, cn, ct, ch)
	fn, ft, fh := NewNoteFindTool(pool)
	find := newCaller[wireSummary](t, fn, ft, fh)

	newNote := func(t *testing.T, ctx context.Context, title, body string) uuid.UUID {
		t.Helper()
		return create.mustOK(t, ctx, map[string]any{"title": title, "content": body}).Data.ID
	}
	listed := func(t *testing.T, ctx context.Context) map[uuid.UUID]bool {
		t.Helper()
		ids := map[uuid.UUID]bool{}
		for _, n := range find.mustOK(t, ctx, map[string]any{"limit": 100}).Items {
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

	t.Run("deleted note disappears from note_find", func(t *testing.T) {
		gone := newNote(t, ctx, "Old Wi-Fi password", "hunter2")
		kept := newNote(t, ctx, "Keep me", "still here")
		if ids := listed(t, ctx); !ids[gone] || !ids[kept] {
			t.Fatalf("precondition: both notes should be listed, got %v", ids)
		}

		env := del.mustOK(t, ctx, map[string]any{"note_id": gone.String()})
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
		if ids[gone] || !ids[kept] {
			t.Errorf("after delete: gone listed=%v, kept listed=%v", ids[gone], ids[kept])
		}
		if exists(t, gone) {
			t.Error("deleted note is still in the table")
		}

		// A repeat finds nothing and changes nothing more.
		isErr, env := del.call(t, ctx, map[string]any{"note_id": gone.String()})
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
		env := del.mustOK(t, ctx, map[string]any{"note_id": id.String()})
		if env.Summary != `Deleted note "(untitled)".` || env.Data.Title != nil {
			t.Errorf("got summary %q data %+v", env.Summary, env.Data)
		}
	})

	theirs := newNote(t, otherCtx, "Secret", "other household")
	mine := newNote(t, ctx, "Mine", "body")

	runErrCases(t, del, []errCase{
		{name: "other household's note is not found", ctx: ctx,
			args: map[string]any{"note_id": theirs.String()}, wantMsg: `No note with id "` + theirs.String() + `" was found.`},
		{name: "own note is not found from the other household", ctx: otherCtx,
			args: map[string]any{"note_id": mine.String()}, wantMsg: `No note with id "` + mine.String() + `" was found.`},
		{name: "unknown id is not found", ctx: ctx,
			args:    map[string]any{"note_id": "00000000-0000-0000-0000-000000000001"},
			wantMsg: `No note with id "00000000-0000-0000-0000-000000000001" was found.`},
		{name: "title instead of id is invalid", ctx: ctx, wantField: "note_id",
			args: map[string]any{"note_id": "Mine"}, wantMsg: `note_id "Mine" is not a UUID.`},
		{name: "missing note_id is invalid", ctx: ctx, wantField: "note_id", args: nil, wantMsg: "note_id is required."},
		{name: "no claims is refused", ctx: context.Background(),
			args: map[string]any{"note_id": mine.String()}, wantMsg: "You are not signed in to a household."},
	})

	if !exists(t, theirs) || !exists(t, mine) {
		t.Errorf("rejected calls deleted a note: theirs=%v mine=%v", exists(t, theirs), exists(t, mine))
	}
	if ids := listed(t, otherCtx); !ids[theirs] {
		t.Error("other household's note vanished from its note_find")
	}
}
