package search

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/testutil"
)

// envelope is the wire shape of a search_all result (respond.Envelope).
type envelope struct {
	OK      bool         `json:"ok"`
	Summary string       `json:"summary"`
	Data    searchResult `json:"data"`
	Next    []string     `json:"next"`
	Error   *struct {
		Message string `json:"message"`
		Field   string `json:"field"`
		Fix     string `json:"fix"`
	} `json:"error"`
}

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

// call runs search_all with args and checks the result the way a strict
// client would: a tool result with one text block equal to
// structuredContent, which validates against the tool's outputSchema.
func call(t *testing.T, ctx context.Context, pool *pgxpool.Pool, args map[string]any) (bool, envelope) {
	t.Helper()
	name, tool, handler := NewSearchTool(pool)
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := handler(ctx, req)
	if err != nil {
		t.Fatalf("search_all returned a protocol error: %v", err)
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
	sc, _ := json.Marshal(w.StructuredContent)
	tx, _ := json.Marshal(fromText)
	if string(sc) != string(tx) {
		t.Fatalf("text block != structuredContent:\n%s\n%s", tx, sc)
	}
	if err := outputSchema(t, tool).Validate(w.StructuredContent); err != nil {
		t.Fatalf("result does not match outputSchema: %v\n%s", err, sc)
	}
	var env envelope
	if err := json.Unmarshal([]byte(w.Content[0].Text), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return w.IsError, env
}

// search runs a successful search_all call.
func search(t *testing.T, ctx context.Context, pool *pgxpool.Pool, args map[string]any) envelope {
	t.Helper()
	isErr, env := call(t, ctx, pool, args)
	if isErr || !env.OK {
		t.Fatalf("search_all %v failed: %+v", args, env)
	}
	return env
}

func group(env envelope, typ string) *searchGroup {
	for i := range env.Data.Groups {
		if env.Data.Groups[i].Type == typ {
			return &env.Data.Groups[i]
		}
	}
	return nil
}

func ids(g *searchGroup) []string {
	out := []string{}
	if g == nil {
		return out
	}
	for _, h := range g.Items {
		out = append(out, h.ID)
	}
	sort.Strings(out)
	return out
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id
}

func seedCalendar(t *testing.T, pool *pgxpool.Pool, householdID uuid.UUID, name string) string {
	t.Helper()
	return mustExec(t, pool,
		`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, $2, 'family', $3) RETURNING id`,
		householdID, name, uuid.NewString())
}

func seedEvent(t *testing.T, pool *pgxpool.Pool, calendarID, icalData string) string {
	t.Helper()
	return mustExec(t, pool,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type) VALUES ($1, $2, $3, 'etag-0', 'event') RETURNING id::text`,
		calendarID, uuid.NewString(), icalData)
}

func seedNote(t *testing.T, pool *pgxpool.Pool, householdID uuid.UUID, title, body string) string {
	t.Helper()
	return mustExec(t, pool,
		`INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'household', $1, $2, $3) RETURNING id::text`,
		householdID, title, body)
}

// seeders insert one row of each SQL area whose title/name is text, and
// return its id. The room seeder also creates its property.
var seeders = map[string]func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string{
	"person": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		return mustExec(t, pool, `INSERT INTO people (household_id, display_name) VALUES ($1, $2) RETURNING id::text`, h, text)
	},
	"bill": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		return mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, $2, 10, 1) RETURNING id::text`, h, text)
	},
	"loan": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		return mustExec(t, pool, `INSERT INTO loans (household_id, name, original_amount, remaining_balance) VALUES ($1, $2, 100, 50) RETURNING id::text`, h, text)
	},
	"maintenance": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		return mustExec(t, pool, `INSERT INTO maintenance_tasks (household_id, name) VALUES ($1, $2) RETURNING id::text`, h, text)
	},
	"contact": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		return mustExec(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, $2) RETURNING id::text`, h, text)
	},
	"note": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		return seedNote(t, pool, h, text, "body")
	},
	"list": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		return mustExec(t, pool, `INSERT INTO lists (household_id, name, entity_type, entity_id) VALUES ($1, $2, 'household', $1) RETURNING id::text`, h, text)
	},
	"recipe": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		return mustExec(t, pool, `INSERT INTO recipes (household_id, title) VALUES ($1, $2) RETURNING id::text`, h, text)
	},
	"document": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		blob := mustExec(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id::text`)
		return mustExec(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id) VALUES ($1, $2, $3, 1, 'household', $1) RETURNING id::text`, h, blob, text)
	},
	"asset": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		return mustExec(t, pool, `INSERT INTO assets (household_id, name) VALUES ($1, $2) RETURNING id::text`, h, text)
	},
	"vehicle": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		return mustExec(t, pool, `INSERT INTO vehicles (household_id, make) VALUES ($1, $2) RETURNING id::text`, h, text)
	},
	"pet": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		return mustExec(t, pool, `INSERT INTO pets (household_id, name) VALUES ($1, $2) RETURNING id::text`, h, text)
	},
	"property": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		return mustExec(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, $2) RETURNING id::text`, h, text)
	},
	"room": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		prop := mustExec(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Home') RETURNING id::text`, h)
		return mustExec(t, pool, `INSERT INTO rooms (property_id, name) VALUES ($1, $2) RETURNING id::text`, prop, text)
	},
	"credential": func(t *testing.T, pool *pgxpool.Pool, h uuid.UUID, text string) string {
		return mustExec(t, pool, `INSERT INTO secrets (household_id, entity_type, entity_id, encrypted_data, iv, name, secret_type) VALUES ($1, 'household', $1, 'enc', 'iv', $2, 'login') RETURNING id::text`, h, text)
	},
}

func TestSearchAllTool(t *testing.T) {
	name, tool, _ := NewSearchTool(nil)
	if name != "search_all" || tool.Name != "search_all" {
		t.Fatalf("name = %q / %q, want search_all", name, tool.Name)
	}
	if len(tool.Description) > 200 {
		t.Errorf("description is %d chars, max 200", len(tool.Description))
	}
	a := tool.Annotations
	if a.Title != "Search everything" || a.ReadOnlyHint == nil || !*a.ReadOnlyHint ||
		a.DestructiveHint == nil || *a.DestructiveHint || a.IdempotentHint == nil || !*a.IdempotentHint ||
		a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations = %+v, want class R with all four hints set", a)
	}
	if len(tool.InputSchema.Required) != 1 || tool.InputSchema.Required[0] != "query" {
		t.Errorf("required = %v, want [query]", tool.InputSchema.Required)
	}
	for _, p := range []string{"query", "types", "limit"} {
		if _, ok := tool.InputSchema.Properties[p]; !ok {
			t.Errorf("missing param %q", p)
		}
	}
	b, _ := json.Marshal(tool.InputSchema.Properties["types"])
	for _, want := range []string{`"room"`, `"event"`, `"person"`, `"document"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("types enum %s lacks %s", b, want)
		}
	}
	outputSchema(t, tool)
	if len(seeders)+1 != len(areas) {
		t.Errorf("tests seed %d SQL areas, the tool has %d areas plus event", len(seeders), len(areas)-1)
	}
}

// TestSearchAllEscapesWildcards checks every area: "%" and "_" in the query
// match literally, never as LIKE wildcards (manager note on #4382).
func TestSearchAllEscapesWildcards(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, h := testutil.SeedHousehold(t, pool)

	want := map[string]string{}
	for typ, seed := range seeders {
		want[typ] = seed(t, pool, h.HouseholdID, typ+" code a_b 50%")
		seed(t, pool, h.HouseholdID, typ+" code axb 50x")
	}
	cal := seedCalendar(t, pool, h.HouseholdID, "Family")
	want["event"] = seedEvent(t, pool, cal, `{"title":"event code a_b 50%","start":"2026-11-18T17:00:00Z","end":"2026-11-18T18:00:00Z"}`)
	seedEvent(t, pool, cal, `{"title":"event code axb 50x","start":"2026-11-18T17:00:00Z","end":"2026-11-18T18:00:00Z"}`)

	for _, q := range []string{"a_b", "50%", "b 50%"} {
		t.Run("literal "+q, func(t *testing.T) {
			env := search(t, ctx, pool, map[string]any{"query": q})
			for _, a := range areas {
				g := group(env, a.Name)
				if got := ids(g); len(got) != 1 || got[0] != want[a.Name] {
					t.Errorf("%s: query %q got %v, want only %s", a.Name, q, got, want[a.Name])
				}
			}
			if env.Data.Total != len(areas) {
				t.Errorf("total = %d, want %d", env.Data.Total, len(areas))
			}
		})
	}
	for _, q := range []string{"%", "_", "%%", "a%b"} {
		t.Run("wildcard "+q+" matches nothing extra", func(t *testing.T) {
			env := search(t, ctx, pool, map[string]any{"query": q})
			for _, g := range env.Data.Groups {
				for _, hit := range g.Items {
					if !strings.Contains(hit.Title, q) {
						t.Errorf("%s: query %q matched %q", g.Type, q, hit.Title)
					}
				}
			}
		})
	}
}

func TestSearchAllAreas(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, h := testutil.SeedHousehold(t, pool)
	otherCtx, other := testutil.SeedHousehold(t, pool)

	mine := map[string]string{}
	for typ, seed := range seeders {
		mine[typ] = seed(t, pool, h.HouseholdID, "Zephyr "+typ)
		seed(t, pool, other.HouseholdID, "Zephyr "+typ)
	}

	env := search(t, ctx, pool, map[string]any{"query": "zephyr"})
	for typ, id := range mine {
		g := group(env, typ)
		if got := ids(g); len(got) != 1 || got[0] != id {
			t.Errorf("%s: got %v, want [%s] (own household only)", typ, got, id)
		}
	}
	if len(env.Data.Searched) != len(areas) {
		t.Errorf("searched = %v, want every area", env.Data.Searched)
	}
	// Groups come in catalog order.
	order := []string{}
	for _, g := range env.Data.Groups {
		order = append(order, g.Type)
	}
	if !sort.SliceIsSorted(order, func(i, j int) bool { return areaIndex(order[i]) < areaIndex(order[j]) }) {
		t.Errorf("groups out of catalog order: %v", order)
	}

	t.Run("open hints name the reading tool and id", func(t *testing.T) {
		wantOpen := map[string]string{
			"note":     "note_get note_id=" + mine["note"],
			"bill":     "bill_find id=" + mine["bill"],
			"contact":  "contact_find id=" + mine["contact"],
			"document": "document_get document_id=" + mine["document"],
			"list":     `Tell the user it is their list "Zephyr list" in Home OS; opening lists here is coming soon.`,
		}
		for typ, want := range wantOpen {
			if got := group(env, typ).Items[0].Open; got != want {
				t.Errorf("%s open = %q, want %q", typ, got, want)
			}
		}
	})

	t.Run("room is opened through its property and titled with it", func(t *testing.T) {
		var propID string
		if err := pool.QueryRow(context.Background(), `SELECT property_id::text FROM rooms WHERE id = $1`, mine["room"]).Scan(&propID); err != nil {
			t.Fatal(err)
		}
		hit := group(env, "room").Items[0]
		if hit.Open != "property_find id="+propID || hit.Title != "Zephyr room (Home)" {
			t.Errorf("room hit = %+v", hit)
		}
	})

	t.Run("credential hits never carry the value", func(t *testing.T) {
		hit := group(env, "credential").Items[0]
		if strings.Contains(hit.Snippet, "enc") || !strings.Contains(hit.Open, "never see the secret value") {
			t.Errorf("credential hit = %+v", hit)
		}
	})

	t.Run("other household sees only its own rows", func(t *testing.T) {
		env := search(t, otherCtx, pool, map[string]any{"query": "zephyr"})
		for typ, id := range mine {
			for _, got := range ids(group(env, typ)) {
				if got == id {
					t.Errorf("%s: other household sees %s", typ, id)
				}
			}
		}
	})
}

func areaIndex(name string) int {
	for i, a := range areas {
		if a.Name == name {
			return i
		}
	}
	return -1
}

func TestSearchAllTypesFilter(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, h := testutil.SeedHousehold(t, pool)
	note := seedNote(t, pool, h.HouseholdID, "Plumber number", "Joe 555-1234")
	contact := seeders["contact"](t, pool, h.HouseholdID, "Joe's Plumbing")
	cal := seedCalendar(t, pool, h.HouseholdID, "Family")
	event := seedEvent(t, pool, cal, `{"title":"Plumber visit","start":"2026-11-18T17:00:00Z","end":"2026-11-18T18:00:00Z"}`)

	tests := []struct {
		name  string
		types any
		want  map[string]string
	}{
		{"all by default", nil, map[string]string{"note": note, "contact": contact, "event": event}},
		{"list", []any{"note"}, map[string]string{"note": note}},
		{"two types", []any{"event", "contact"}, map[string]string{"contact": contact, "event": event}},
		{"comma string with plurals and aliases", "Notes, vendors", map[string]string{"note": note, "contact": contact}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"query": "plumb"}
			if tc.types != nil {
				args["types"] = tc.types
			}
			env := search(t, ctx, pool, args)
			if len(env.Data.Groups) != len(tc.want) {
				t.Fatalf("groups = %+v, want %v", env.Data.Groups, tc.want)
			}
			for typ, id := range tc.want {
				if got := ids(group(env, typ)); len(got) != 1 || got[0] != id {
					t.Errorf("%s: got %v, want [%s]", typ, got, id)
				}
			}
		})
	}

	t.Run("searched lists only the requested types", func(t *testing.T) {
		env := search(t, ctx, pool, map[string]any{"query": "plumb", "types": []any{"contact", "note"}})
		if strings.Join(env.Data.Searched, ",") != "contact,note" {
			t.Errorf("searched = %v, want [contact note]", env.Data.Searched)
		}
	})
}

func TestSearchAllInvalidArguments(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)

	tests := []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"missing query", map[string]any{}, "query"},
		{"blank query", map[string]any{"query": "   "}, "query"},
		{"unknown type", map[string]any{"query": "x", "types": []any{"spaceship"}}, "types"},
		{"type not a string", map[string]any{"query": "x", "types": []any{3}}, "types"},
		{"limit too big", map[string]any{"query": "x", "limit": 500}, "limit"},
		{"limit zero", map[string]any{"query": "x", "limit": 0}, "limit"},
		{"limit fraction", map[string]any{"query": "x", "limit": 2.5}, "limit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isErr, env := call(t, ctx, pool, tc.args)
			if !isErr || env.OK || env.Error == nil {
				t.Fatalf("want isError, got %+v", env)
			}
			if env.Error.Field != tc.field || env.Error.Fix == "" {
				t.Errorf("error = %+v, want field %q with a fix", env.Error, tc.field)
			}
		})
	}

	t.Run("not signed in", func(t *testing.T) {
		isErr, env := call(t, context.Background(), pool, map[string]any{"query": "x"})
		if !isErr || env.Error == nil {
			t.Fatalf("want isError, got %+v", env)
		}
	})
}

func TestSearchAllLimitAndSnippets(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, h := testutil.SeedHousehold(t, pool)

	for _, title := range []string{"Gutter one", "Gutter two", "Gutter three"} {
		seedNote(t, pool, h.HouseholdID, title, "x")
	}
	// A body match ranks after title matches even when it is newest.
	long := "Line one about the yard.\n\n" + strings.Repeat("filler words here ", 10) +
		"call the gutter cleaner before December " + strings.Repeat("more filler text ", 10)
	bodyHit := seedNote(t, pool, h.HouseholdID, "Fall chores", long)

	env := search(t, ctx, pool, map[string]any{"query": "GUTTER", "types": []any{"note"}, "limit": 2})
	g := group(env, "note")
	if g == nil || g.Returned != 2 || g.Total != 4 || !g.Truncated || len(g.Items) != 2 {
		t.Fatalf("group = %+v, want 2 of 4, truncated", g)
	}
	for _, hit := range g.Items {
		if !strings.HasPrefix(hit.Title, "Gutter") {
			t.Errorf("title matches should rank first, got %q", hit.Title)
		}
	}
	if env.Data.Total != 4 || !strings.Contains(env.Summary, "4 notes") {
		t.Errorf("total = %d, summary %q", env.Data.Total, env.Summary)
	}
	if !strings.Contains(strings.Join(env.Next, " "), `types=["note"] and limit=4`) {
		t.Errorf("next = %v, want a more-results hint", env.Next)
	}

	env = search(t, ctx, pool, map[string]any{"query": "gutter cleaner"})
	g = group(env, "note")
	if got := ids(g); len(got) != 1 || got[0] != bodyHit {
		t.Fatalf("got %v, want [%s]", got, bodyHit)
	}
	snip := g.Items[0].Snippet
	if !strings.HasPrefix(snip, "…") || !strings.HasSuffix(snip, "…") ||
		!strings.Contains(snip, "call the gutter cleaner before December") ||
		strings.Contains(snip, "\n") || strings.Contains(snip, "  ") {
		t.Errorf("snippet = %q", snip)
	}
	if n := len([]rune(snip)); n > snippetLen+2 {
		t.Errorf("snippet is %d chars", n)
	}

	t.Run("short text is not marked as cut", func(t *testing.T) {
		env := search(t, ctx, pool, map[string]any{"query": "gutter two"})
		if s := group(env, "note").Items[0].Snippet; s != "Gutter two x" {
			t.Errorf("snippet = %q, want %q", s, "Gutter two x")
		}
	})

	t.Run("no match", func(t *testing.T) {
		env := search(t, ctx, pool, map[string]any{"query": "dentist"})
		if env.Data.Total != 0 || len(env.Data.Groups) != 0 || len(env.Next) == 0 ||
			env.Summary != `Nothing matched "dentist".` {
			t.Errorf("got %+v", env)
		}
	})
}

func eventGroup(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string) []searchHit {
	t.Helper()
	g := group(search(t, ctx, pool, map[string]any{"query": query}), "event")
	if g == nil {
		return nil
	}
	return g.Items
}

func TestSearchEvents(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, h := testutil.SeedHousehold(t, pool)
	_, other := testutil.SeedHousehold(t, pool)

	cal := seedCalendar(t, pool, h.HouseholdID, "Kids")
	soccer := seedEvent(t, pool, cal, `{"title":"Emma Soccer Practice","description":"bring shin guards","start":"2026-11-18T17:00:00Z","end":"2026-11-18T18:30:00Z","location":"Riverside Park"}`)
	electric := seedEvent(t, pool, cal, `{"title":"Pay bill","description":"Electric company autopay check","start":"2026-11-20T09:00:00Z","end":"2026-11-20T09:30:00Z","location":""}`)
	cafe := seedEvent(t, pool, cal, `{"title":"Coffee","description":"","start":"2026-11-21T08:00:00Z","end":"2026-11-21T09:00:00Z","location":"Café Luna"}`)
	seedEvent(t, pool, cal, `not json at all: soccer`)

	otherCal := seedCalendar(t, pool, other.HouseholdID, "Other Family")
	otherSoccer := seedEvent(t, pool, otherCal, `{"title":"Soccer tournament","start":"2026-11-18T10:00:00Z","end":"2026-11-18T16:00:00Z"}`)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"title match, case-insensitive", "soccer", []string{soccer}},
		{"description match", "electric", []string{electric}},
		{"location match", "riverside", []string{soccer}},
		{"non-ASCII query matches escaped JSON", "café", []string{cafe}},
		{"JSON keys are not searched", "description", nil},
		{"other fields are not searched", "2026-11-18", nil},
		{"LIKE wildcards are literal", "%", nil},
		{"no match", "dentist", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := []string{}
			for _, e := range eventGroup(t, ctx, pool, tc.query) {
				if e.ID == otherSoccer {
					t.Fatalf("result includes another household's event %s", otherSoccer)
				}
				got = append(got, e.ID)
			}
			want := append([]string{}, tc.want...)
			sort.Strings(got)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("query %q: got %v, want %v", tc.query, got, want)
			}
		})
	}

	t.Run("event hit carries title, snippet, start, calendar and open hint", func(t *testing.T) {
		hits := eventGroup(t, ctx, pool, "riverside")
		if len(hits) != 1 {
			t.Fatalf("want 1 event hit, got %+v", hits)
		}
		want := searchHit{ID: soccer, Title: "Emma Soccer Practice", Snippet: "Emma Soccer Practice bring shin guards Riverside Park",
			Open: "event_find id=" + soccer, Start: "2026-11-18T17:00:00Z", LocalDate: "2026-11-18", Weekday: "Wednesday", CalendarName: "Kids"}
		if hits[0] != want {
			t.Fatalf("got %+v, want %+v", hits[0], want)
		}
	})
}

func TestSearchEventsHouseholdLocalDay(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, h := testutil.SeedHousehold(t, pool)
	if _, err := pool.Exec(context.Background(),
		`UPDATE households SET timezone = 'America/New_York' WHERE id = $1`, h.HouseholdID,
	); err != nil {
		t.Fatalf("set timezone: %v", err)
	}
	cal := seedCalendar(t, pool, h.HouseholdID, "Family")
	// 23:30 New York on Wednesday 11/18 is 04:30Z on Thursday 11/19.
	late := seedEvent(t, pool, cal, `{"title":"Late call","start":"2026-11-19T04:30:00Z","end":"2026-11-19T05:00:00Z","all_day":false}`)
	// All-day events are dated by the date as written, not shifted to 11/17.
	allDay := seedEvent(t, pool, cal, `{"title":"Teacher workday","start":"2026-11-18T00:00:00Z","end":"2026-11-19T00:00:00Z","all_day":true}`)
	seedNote(t, pool, h.HouseholdID, "Late call notes", "agenda")

	tests := []struct {
		name, query, id, start string
	}{
		{"timed event in household time", "late call", late, "2026-11-18T23:30:00-05:00"},
		{"all-day event dated as written", "teacher", allDay, "2026-11-18T00:00:00-05:00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits := eventGroup(t, ctx, pool, tc.query)
			if len(hits) != 1 {
				t.Fatalf("want 1 event hit, got %+v", hits)
			}
			got := hits[0]
			if got.ID != tc.id || got.Start != tc.start || got.LocalDate != "2026-11-18" || got.Weekday != "Wednesday" || got.CalendarName != "Family" {
				t.Fatalf("got %+v", got)
			}
		})
	}

	t.Run("non-event hits carry no event day fields", func(t *testing.T) {
		_, _, handler := NewSearchTool(pool)
		var req mcp.CallToolRequest
		req.Params.Arguments = map[string]any{"query": "late call"}
		res, err := handler(ctx, req)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		var raw struct {
			Data struct {
				Groups []struct {
					Type  string           `json:"type"`
					Items []map[string]any `json:"items"`
				} `json:"groups"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &raw); err != nil {
			t.Fatalf("decode: %v", err)
		}
		found := false
		for _, g := range raw.Data.Groups {
			if g.Type != "note" {
				continue
			}
			found = true
			for _, r := range g.Items {
				for _, k := range []string{"start", "local_date", "weekday", "calendar_name"} {
					if _, ok := r[k]; ok {
						t.Fatalf("note hit has %q: %v", k, r)
					}
				}
			}
		}
		if !found {
			t.Fatalf("no note group in %+v", raw)
		}
	})
}

func TestTidySnippet(t *testing.T) {
	tests := []struct {
		in               string
		cutStart, cutEnd bool
		want             string
	}{
		{"plain  text\nhere", false, false, "plain text here"},
		{"ial word match end", true, false, "…word match end"},
		{" word match", true, false, "…word match"},
		{"start match parti", false, true, "start match…"},
		{"single", true, true, "…single…"},
	}
	for _, tc := range tests {
		if got := tidySnippet(tc.in, tc.cutStart, tc.cutEnd); got != tc.want {
			t.Errorf("tidySnippet(%q, %v, %v) = %q, want %q", tc.in, tc.cutStart, tc.cutEnd, got, tc.want)
		}
	}
}

// TestSearchAllDocumentMetadata: documents match on their 048 title,
// category and description, and are titled by title, else the file name.
func TestSearchAllDocumentMetadata(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, h := testutil.SeedHousehold(t, pool)
	titled := seeders["document"](t, pool, h.HouseholdID, "scan_0042.pdf")
	if _, err := pool.Exec(context.Background(),
		`UPDATE files SET title = 'Car insurance policy', category = 'insurance', description = 'Geico, renews in March' WHERE id = $1`, titled,
	); err != nil {
		t.Fatalf("set metadata: %v", err)
	}
	untitled := seeders["document"](t, pool, h.HouseholdID, "geico-card.pdf")

	env := search(t, ctx, pool, map[string]any{"query": "geico", "types": []any{"document"}})
	g := group(env, "document")
	if got := ids(g); len(got) != 2 {
		t.Fatalf("got %v, want both documents", got)
	}
	titles := map[string]string{}
	for _, hit := range g.Items {
		titles[hit.ID] = hit.Title
	}
	if titles[titled] != "Car insurance policy" || titles[untitled] != "geico-card.pdf" {
		t.Errorf("titles = %v", titles)
	}
	if got := ids(group(search(t, ctx, pool, map[string]any{"query": "renews in march"}), "document")); len(got) != 1 || got[0] != titled {
		t.Errorf("description search got %v, want [%s]", got, titled)
	}
}
