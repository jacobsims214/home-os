package pet

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/testutil"
)

// --- shape helpers (pattern from internal/property tests) ---

func findToolDef() mcp.Tool {
	_, tool, _ := NewPetFindTool(nil)
	return tool
}

func outputSchema(t *testing.T) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(findToolDef())
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || len(raw.OutputSchema) == 0 {
		t.Fatalf("pet_find declares no outputSchema (%v)", err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw.OutputSchema, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve outputSchema: %v", err)
	}
	return rs
}

// wireEnvelope is the JSON pet_find returns.
type wireEnvelope struct {
	OK         bool            `json:"ok"`
	Summary    string          `json:"summary"`
	Items      json.RawMessage `json:"items"`
	Returned   int             `json:"returned"`
	Total      *int            `json:"total"`
	Truncated  bool            `json:"truncated"`
	Narrow     string          `json:"narrow"`
	NextCursor string          `json:"next_cursor"`
	Next       []string        `json:"next"`
	Error      *struct {
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Field   string         `json:"field"`
		Example map[string]any `json:"example"`
	} `json:"error"`
}

// call runs the handler and checks the respond invariants: one text block
// equal to structuredContent, valid against the advertised outputSchema
// (errors included), ok == !isError.
func call(t *testing.T, ctx context.Context, h toolHandler, args map[string]any) wireEnvelope {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = "pet_find"
	req.Params.Arguments = args
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("pet_find returned a Go error (protocol fault): %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("want 1 content item, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	sc, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structuredContent: %v", err)
	}
	if string(sc) != text.Text {
		t.Fatalf("text differs from structuredContent:\n%s\n%s", text.Text, sc)
	}
	var instance map[string]any
	if err := json.Unmarshal(sc, &instance); err != nil {
		t.Fatalf("structuredContent is not an object: %v", err)
	}
	if err := outputSchema(t).Validate(instance); err != nil {
		t.Fatalf("result does not match its outputSchema: %v\n%s", err, text.Text)
	}
	var env wireEnvelope
	if err := json.Unmarshal(sc, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.OK == res.IsError {
		t.Fatalf("ok=%v but isError=%v", env.OK, res.IsError)
	}
	return env
}

func (e wireEnvelope) pets(t *testing.T) []petData {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d []petData
	if err := json.Unmarshal(e.Items, &d); err != nil {
		t.Fatalf("decode items: %v", err)
	}
	return d
}

func wantError(t *testing.T, e wireEnvelope, field string) {
	t.Helper()
	if e.OK || e.Error == nil {
		t.Fatalf("want isError on %q, got success: %s", field, e.Summary)
	}
	if e.Error.Field != field {
		t.Fatalf("error field = %q, want %q (%s)", e.Error.Field, field, e.Error.Message)
	}
	if e.Error.Fix == "" {
		t.Errorf("error lacks a fix: %+v", e.Error)
	}
}

func insertID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("insert: %v\n%s", err, sql)
	}
	return id.String()
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// --- tests ---

// TestToolDefinition pins pet_find to the catalog: name, params, all four
// hints (class R), description lengths, no legacy params advertised, and
// string ids in the outputSchema.
func TestToolDefinition(t *testing.T) {
	tool := findToolDef()
	if tool.Name != "pet_find" {
		t.Fatalf("name = %q", tool.Name)
	}
	if len(tool.InputSchema.Required) != 0 {
		t.Errorf("required = %v, want none", tool.InputSchema.Required)
	}
	want := []string{"id", "query", "limit", "cursor"}
	if len(tool.InputSchema.Properties) != len(want) {
		t.Errorf("got %d params, want %v", len(tool.InputSchema.Properties), want)
	}
	for _, p := range want {
		if _, ok := tool.InputSchema.Properties[p]; !ok {
			t.Errorf("missing param %q", p)
		}
	}
	if tool.Description != "List or search pets with species, breed, birthday and vet. Pass id for full details." {
		t.Errorf("description differs from the catalog: %q", tool.Description)
	}
	for param, schema := range tool.InputSchema.Properties {
		d, _ := schema.(map[string]any)["description"].(string)
		if d == "" || len(d) > 80 {
			t.Errorf("%s description is %d chars, want 1-80", param, len(d))
		}
	}
	a := tool.Annotations
	if a.Title != "Find pets" {
		t.Errorf("title = %q", a.Title)
	}
	if a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
		t.Fatalf("annotations not all explicit: %+v", a)
	}
	if !*a.ReadOnlyHint || *a.DestructiveHint || !*a.IdempotentHint || *a.OpenWorldHint {
		t.Errorf("hints = %+v, want class R (true,false,true,false)", a)
	}
	b, _ := json.Marshal(tool)
	var raw struct {
		OutputSchema map[string]any `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || raw.OutputSchema == nil {
		t.Fatalf("no outputSchema: %v", err)
	}
	item := raw.OutputSchema["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)
	props := item["properties"].(map[string]any)
	if s := fmt.Sprint(props["id"].(map[string]any)["type"]); !strings.Contains(s, "string") {
		t.Errorf("id schema type = %s, want string", s)
	}
	vet := props["vet"].(map[string]any)["properties"].(map[string]any)
	if s := fmt.Sprint(vet["contact_id"].(map[string]any)["type"]); !strings.Contains(s, "string") {
		t.Errorf("vet contact_id schema type = %s, want string", s)
	}
	outputSchema(t)
}

// TestPetFind covers listing, the vet from the linked contact vs the old
// vet_name/vet_phone fallback, id lookup (search_all's open hint), query,
// household scoping, pagination, aliases and validation.
func TestPetFind(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, _, find := NewPetFindTool(pool)

	// Empty household.
	env := call(t, ctx, find, map[string]any{})
	if got := env.pets(t); len(got) != 0 || env.Summary != "No pets yet." || env.Total == nil || *env.Total != 0 {
		t.Fatalf("empty: %+v", env)
	}
	if len(env.Next) != 1 || !strings.Contains(env.Next[0], "Home OS app") {
		t.Errorf("empty next = %v", env.Next)
	}

	// A family vet contact, linked by 043's vet_vendor_id.
	vetID := insertID(t, pool, `INSERT INTO vendors (household_id, name, phone, email, trades)
		VALUES ($1, 'Dr. Paws Clinic', '555-0100', 'desk@paws.example', ARRAY['vet']) RETURNING id`, hh.HouseholdID)
	// Rex: linked vet, plus stale old columns that must be ignored.
	rex := insertID(t, pool, `INSERT INTO pets (household_id, name, species, breed, date_of_birth, vet_name, vet_phone,
			vet_vendor_id, microchip_id, notes)
		VALUES ($1, 'Rex', 'dog', 'Beagle', (CURRENT_DATE - INTERVAL '4 years 2 months')::date, 'Old Vet', '555-9999',
			$2, '985112003', 'allergic to chicken') RETURNING id`, hh.HouseholdID, vetID)
	// Mittens: only the old columns (written by pre-043 code, not yet backfilled).
	mittens := insertID(t, pool, `INSERT INTO pets (household_id, name, species, vet_name, vet_phone)
		VALUES ($1, 'Mittens', 'cat', 'Whisker Hospital', '555-0200') RETURNING id`, hh.HouseholdID)
	// Bubbles: no vet at all, blank species.
	bubbles := insertID(t, pool, `INSERT INTO pets (household_id, name, species) VALUES ($1, 'bubbles', '  ') RETURNING id`, hh.HouseholdID)

	// Another family's pet never shows.
	_, other := testutil.SeedHousehold(t, pool)
	foreign := insertID(t, pool, `INSERT INTO pets (household_id, name, species) VALUES ($1, 'Rexy', 'dog') RETURNING id`, other.HouseholdID)

	// List: sorted by name case-insensitively, every pet in full.
	env = call(t, ctx, find, map[string]any{})
	got := env.pets(t)
	if len(got) != 3 || got[0].ID != bubbles || got[1].ID != mittens || got[2].ID != rex || *env.Total != 3 || env.Truncated {
		t.Fatalf("list: %+v", env)
	}
	if env.Summary != "Found 3 pets: bubbles, Mittens (cat), Rex (dog)." {
		t.Errorf("summary = %q", env.Summary)
	}

	b, m, r := got[0], got[1], got[2]
	if b.Species != nil || b.Vet != nil || b.Age != nil || b.DateOfBirth != nil {
		t.Errorf("bubbles = %+v", b)
	}
	if m.Vet == nil || m.Vet.Source != vetLegacy || m.Vet.ContactID != nil || m.Vet.Name != "Whisker Hospital" || str(m.Vet.Phone) != "555-0200" {
		t.Errorf("mittens vet (legacy fallback) = %+v", m.Vet)
	}
	if r.Vet == nil || r.Vet.Source != vetContact || str(r.Vet.ContactID) != vetID || r.Vet.Name != "Dr. Paws Clinic" ||
		str(r.Vet.Phone) != "555-0100" || str(r.Vet.Email) != "desk@paws.example" {
		t.Errorf("rex vet (linked contact wins) = %+v", r.Vet)
	}
	if r.Age == nil || *r.Age != 4 || r.DateOfBirth == nil || len(*r.DateOfBirth) != 10 || str(r.Breed) != "Beagle" ||
		str(r.MicrochipID) != "985112003" || str(r.Notes) != "allergic to chicken" {
		t.Errorf("rex fields = %+v", r)
	}

	// id: exactly one pet (search_all opens hits with pet_find id=...).
	env = call(t, ctx, find, map[string]any{"id": rex})
	if got := env.pets(t); len(got) != 1 || got[0].ID != rex {
		t.Fatalf("id: %+v", env)
	}
	if !strings.HasPrefix(env.Summary, "Found Rex: dog (Beagle), 4 years old, born ") || !strings.HasSuffix(env.Summary, ", vet Dr. Paws Clinic.") {
		t.Errorf("id summary = %q", env.Summary)
	}
	nexts := strings.Join(env.Next, " ")
	if !strings.Contains(nexts, "contact_find id="+vetID) || !strings.Contains(nexts, "event_create") ||
		!strings.Contains(nexts, "note_find about_type=pet about_id="+rex) || len(env.Next) > 3 {
		t.Errorf("rex next = %v", env.Next)
	}
	// A pet with no vet points to contact_find trade=vet.
	env = call(t, ctx, find, map[string]any{"pet_id": bubbles}) // legacy get_pet-style alias
	if got := env.pets(t); len(got) != 1 || got[0].ID != bubbles || !strings.Contains(env.Next[0], "contact_find trade=vet") {
		t.Errorf("bubbles by pet_id alias: %+v", env)
	}
	if !strings.HasSuffix(env.Summary, "no vet saved.") {
		t.Errorf("bubbles summary = %q", env.Summary)
	}

	// Deleting the vet contact clears only the link; the old columns show again.
	if _, err := pool.Exec(context.Background(), `DELETE FROM vendors WHERE id = $1`, vetID); err != nil {
		t.Fatal(err)
	}
	if got := call(t, ctx, find, map[string]any{"id": rex}).pets(t); got[0].Vet == nil || got[0].Vet.Source != vetLegacy || got[0].Vet.Name != "Old Vet" {
		t.Errorf("rex after vendor delete: %+v", got[0].Vet)
	}
	vetID = insertID(t, pool, `INSERT INTO vendors (household_id, name, trades) VALUES ($1, 'Dr. Paws Clinic', ARRAY['vet']) RETURNING id`, hh.HouseholdID)
	if _, err := pool.Exec(context.Background(), `UPDATE pets SET vet_vendor_id = $1 WHERE id = $2`, vetID, rex); err != nil {
		t.Fatal(err)
	}
	// A linked contact without a phone borrows the pet's old vet_phone.
	if got := call(t, ctx, find, map[string]any{"id": rex}).pets(t); str(got[0].Vet.Phone) != "555-9999" || got[0].Vet.Source != vetContact {
		t.Errorf("rex phone fallback: %+v", got[0].Vet)
	}

	// query matches name, species, breed, notes, microchip and vet name;
	// wildcards are literal.
	for q, want := range map[string]string{
		"rex": rex, "CAT": mittens, "beag": rex, "chicken": rex, "985112": rex,
		"paws": rex, "whisker": mittens, "old vet": "", "100%": "", "_": "",
	} {
		got := call(t, ctx, find, map[string]any{"query": q}).pets(t)
		if want == "" {
			if len(got) != 0 {
				t.Errorf("query %q: want none, got %d", q, len(got))
			}
			continue
		}
		if len(got) != 1 || got[0].ID != want {
			t.Errorf("query %q: got %+v", q, got)
		}
	}
	if env := call(t, ctx, find, map[string]any{"name": "nobody"}); env.Summary != "No pets match." {
		t.Errorf("no match summary (name alias) = %q", env.Summary)
	}

	// Another family's pet: not found, never leaked.
	env = call(t, ctx, find, map[string]any{"id": foreign})
	if env.OK || env.Error == nil || !strings.Contains(env.Error.Fix, "pet_find") {
		t.Errorf("foreign id: %+v", env)
	}
	if got := call(t, ctx, find, map[string]any{"query": "rexy"}).pets(t); len(got) != 0 {
		t.Errorf("foreign query leaked: %+v", got)
	}

	// Pagination: limit + next_cursor passed back as cursor.
	env = call(t, ctx, find, map[string]any{"limit": 2})
	if got := env.pets(t); len(got) != 2 || !env.Truncated || *env.Total != 3 || env.NextCursor != respond.EncodeCursor(2) ||
		env.Summary != "Found 3 pets; showing 1-2." {
		t.Fatalf("page 1: %+v", env)
	}
	env = call(t, ctx, find, map[string]any{"limit": "2", "cursor": env.NextCursor})
	if got := env.pets(t); len(got) != 1 || got[0].ID != rex || env.Truncated || env.NextCursor != "" {
		t.Fatalf("page 2: %+v", env)
	}
	env = call(t, ctx, find, map[string]any{"cursor": "9"})
	if got := env.pets(t); len(got) != 0 || !strings.Contains(env.Summary, "past the end") {
		t.Fatalf("past end: %+v", env)
	}

	// Validation errors are isError results on the right field.
	wantError(t, call(t, ctx, find, map[string]any{"id": "Rex"}), "id")
	wantError(t, call(t, ctx, find, map[string]any{"limit": 0}), "limit")
	wantError(t, call(t, ctx, find, map[string]any{"limit": 101}), "limit")
	wantError(t, call(t, ctx, find, map[string]any{"limit": 2.5}), "limit")
	wantError(t, call(t, ctx, find, map[string]any{"cursor": "abc"}), "cursor")
	wantError(t, call(t, ctx, find, map[string]any{"query": 5}), "query")

	// No claims.
	if env := call(t, context.Background(), find, map[string]any{}); env.OK {
		t.Errorf("no claims: %+v", env)
	}
}

// TestPetAgeUsesFamilyTimezone: a birthday that is "today" in the family's
// timezone counts, even when UTC is still on the previous day (or ahead).
func TestPetAgeUsesFamilyTimezone(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, _, find := NewPetFindTool(pool)
	for _, tz := range []string{"Pacific/Kiritimati", "Pacific/Pago_Pago"} {
		if _, err := pool.Exec(context.Background(), `UPDATE households SET timezone = $1 WHERE id = $2`, tz, hh.HouseholdID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM pets WHERE household_id = $1`, hh.HouseholdID); err != nil {
			t.Fatal(err)
		}
		// Born exactly 3 years before today in the family timezone.
		insertID(t, pool, `INSERT INTO pets (household_id, name, date_of_birth)
			VALUES ($1, 'Tz', ((NOW() AT TIME ZONE $2)::date - INTERVAL '3 years')::date) RETURNING id`, hh.HouseholdID, tz)
		got := call(t, ctx, find, map[string]any{}).pets(t)
		if len(got) != 1 || got[0].Age == nil || *got[0].Age != 3 {
			t.Errorf("%s: age = %+v", tz, got)
		}
	}
}
