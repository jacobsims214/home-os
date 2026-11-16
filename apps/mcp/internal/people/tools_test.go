package people

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// envelope is a respond envelope as a client decodes it.
type envelope[T any] struct {
	OK      bool     `json:"ok"`
	Summary string   `json:"summary"`
	Data    *T       `json:"data"`
	Next    []string `json:"next"`
	Error   *struct {
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Field   string         `json:"field"`
		Example map[string]any `json:"example"`
	} `json:"error"`
}

func outputSchema(t *testing.T, tool mcp.Tool) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.OutputSchema) == 0 {
		t.Fatalf("tool %s declares no outputSchema", tool.Name)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw.OutputSchema, &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// toolCaller runs one tool and checks every result as a strict client would:
// one text block equal to structuredContent, valid against the outputSchema.
type toolCaller[T any] struct {
	tool    mcp.Tool
	handler handlerFunc
	rs      *jsonschema.Resolved
}

func newCaller[T any](t *testing.T, name string, tool mcp.Tool, h handlerFunc) toolCaller[T] {
	t.Helper()
	if name != tool.Name {
		t.Fatalf("returned name %q != tool name %q", name, tool.Name)
	}
	return toolCaller[T]{tool: tool, handler: h, rs: outputSchema(t, tool)}
}

func (c toolCaller[T]) call(t testing.TB, ctx context.Context, args map[string]any) envelope[T] {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = c.tool.Name
	req.Params.Arguments = args
	res, err := c.handler(ctx, req)
	if err != nil {
		t.Fatalf("%s returned a protocol error: %v", c.tool.Name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("%s: %d content blocks, want 1", c.tool.Name, len(res.Content))
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("%s: content is %T", c.tool.Name, res.Content[0])
	}
	sc, _ := json.Marshal(res.StructuredContent)
	if string(sc) != text.Text {
		t.Fatalf("%s: text != structuredContent\n%s\n%s", c.tool.Name, text.Text, sc)
	}
	var inst any
	if err := json.Unmarshal(sc, &inst); err != nil {
		t.Fatal(err)
	}
	if err := c.rs.Validate(inst); err != nil {
		t.Fatalf("%s: result does not match outputSchema: %v\n%s", c.tool.Name, err, sc)
	}
	var env envelope[T]
	if err := json.Unmarshal(sc, &env); err != nil {
		t.Fatal(err)
	}
	if env.OK == res.IsError {
		t.Fatalf("%s: ok=%v but isError=%v", c.tool.Name, env.OK, res.IsError)
	}
	if !env.OK && (env.Error == nil || env.Error.Message == "" || env.Error.Fix == "") {
		t.Fatalf("%s: error without message/fix: %s", c.tool.Name, sc)
	}
	return env
}

// mustOK calls and fails the test unless the call succeeded.
func (c toolCaller[T]) mustOK(t testing.TB, ctx context.Context, args map[string]any) T {
	t.Helper()
	env := c.call(t, ctx, args)
	if !env.OK || env.Data == nil {
		t.Fatalf("%s %v: want ok, got %+v", c.tool.Name, args, env)
	}
	return *env.Data
}

// mustFail calls and returns the error envelope; field "" skips the field check.
func (c toolCaller[T]) mustFail(t testing.TB, ctx context.Context, args map[string]any, field string) envelope[T] {
	t.Helper()
	env := c.call(t, ctx, args)
	if env.OK {
		t.Fatalf("%s %v: want isError, got %+v", c.tool.Name, args, env)
	}
	if field != "" && env.Error.Field != field {
		t.Fatalf("%s %v: error field %q, want %q (%s)", c.tool.Name, args, env.Error.Field, field, env.Error.Message)
	}
	return env
}

type tools struct {
	get    toolCaller[Person]
	create toolCaller[Person]
	update toolCaller[Person]
	del    toolCaller[Deleted]
}

func newTools(t *testing.T, pool *pgxpool.Pool) tools {
	t.Helper()
	n, tl, h := NewPersonGetTool(pool)
	get := newCaller[Person](t, n, tl, h)
	n, tl, h = NewPersonCreateTool(pool)
	create := newCaller[Person](t, n, tl, h)
	n, tl, h = NewPersonUpdateTool(pool)
	update := newCaller[Person](t, n, tl, h)
	n, tl, h = NewPersonDeleteTool(pool)
	del := newCaller[Deleted](t, n, tl, h)
	return tools{get: get, create: create, update: update, del: del}
}

// addMember adds a user with role to hh and returns a context with their
// claims and their person id.
func addMember(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role, name string) (context.Context, string) {
	t.Helper()
	ctx := context.Background()
	email := role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`, email, name).Scan(&uid); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hh.HouseholdID, uid, role); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	var pid string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM people WHERE household_id = $1 AND user_id = $2`, hh.HouseholdID, uid).Scan(&pid); err != nil {
		t.Fatalf("person of member: %v", err)
	}
	return server.ContextWithClaims(ctx, &auth.Claims{
		Identity:    auth.Identity{Subject: uid.String(), Email: email},
		UserID:      uid.String(),
		HouseholdID: hh.HouseholdID.String(),
		Role:        role,
	}), pid
}

func insertKid(t *testing.T, pool *pgxpool.Pool, hh uuid.UUID, name string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO people (household_id, display_name, relationship, birthdate, school, grade, notes)
		 VALUES ($1, $2, 'child', '2015-04-02', 'Maple Elementary', '5', 'Loves dinosaurs') RETURNING id::text`, hh, name).Scan(&id); err != nil {
		t.Fatalf("insert kid: %v", err)
	}
	return id
}

func ownerPerson(t *testing.T, pool *pgxpool.Pool, hh testutil.Household) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT id::text FROM people WHERE household_id = $1 AND user_id = $2`,
		hh.HouseholdID, hh.UserID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func prefLabels(p Person) []string {
	var out []string
	for _, pr := range p.Preferences {
		out = append(out, pr.Kind+": "+pr.Item)
	}
	return out
}

// TestAllergyRoundTrip is the ticket's acceptance case: "Nora is allergic to
// peanuts" is stored with one person_update call and read back with one
// person_get call.
func TestAllergyRoundTrip(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	tl := newTools(t, pool)
	nora := insertKid(t, pool, hh.HouseholdID, "Nora")

	up := tl.update.mustOK(t, ctx, map[string]any{
		"person_id":       "Nora",
		"add_preferences": []any{map[string]any{"kind": "allergy", "item": "peanuts"}},
	})
	if up.PersonID != nora || !slices.Equal(up.Allergies, []string{"peanuts"}) || !slices.Equal(up.Added, []string{"allergy: peanuts"}) ||
		!slices.Equal(up.Changed, []string{"allergies"}) {
		t.Fatalf("update = %+v", up)
	}

	got := tl.get.call(t, ctx, map[string]any{"person_id": "nora"})
	if !got.OK || !slices.Equal(got.Data.Allergies, []string{"peanuts"}) || !strings.Contains(got.Summary, "Allergies: peanuts") {
		t.Fatalf("get = %+v", got)
	}
	var stored []string
	if err := pool.QueryRow(context.Background(), `SELECT allergies FROM people WHERE id = $1`, nora).Scan(&stored); err != nil ||
		!slices.Equal(stored, []string{"peanuts"}) {
		t.Fatalf("stored allergies = %v (%v)", stored, err)
	}

	t.Run("repeat changes nothing", func(t *testing.T) {
		env := tl.update.call(t, ctx, map[string]any{
			"person_id":       nora,
			"add_preferences": []any{map[string]any{"kind": "allergies", "item": " PEANUTS "}},
		})
		if !env.OK || !strings.HasPrefix(env.Summary, "Nothing changed for Nora") {
			t.Fatalf("repeat = %+v", env)
		}
		up := *env.Data
		if len(up.Changed) != 0 || len(up.Added) != 0 || len(up.Unchanged) != 1 || !slices.Equal(up.Allergies, []string{"peanuts"}) {
			t.Fatalf("repeat = %+v", up)
		}
	})

	t.Run("preferences of every kind, in one call", func(t *testing.T) {
		up := tl.update.mustOK(t, ctx, map[string]any{
			"person_id": "Nora",
			"add_preferences": []any{
				map[string]any{"kind": "like", "item": "pasta", "category": "Food"},
				map[string]any{"kind": "dislike", "item": "broccoli"},
				map[string]any{"kind": "diet", "item": "vegetarian", "notes": "since 2025"},
				map[string]any{"kind": "note", "item": "afraid of dogs"},
				map[string]any{"kind": "allergy", "item": "bee stings"},
				map[string]any{"kind": "like", "item": "Pasta", "category": "food"}, // same as the first
			},
		})
		if len(up.Added) != 5 || !slices.Equal(up.Allergies, []string{"peanuts", "bee stings"}) {
			t.Fatalf("added = %v allergies %v", up.Added, up.Allergies)
		}
		want := []string{"diet: vegetarian", "dislike: broccoli", "like: pasta", "note: afraid of dogs"} // by stored kind name
		if got := prefLabels(up); !slices.Equal(got, want) {
			t.Fatalf("preferences = %v, want %v", got, want)
		}
		rows, err := pool.Query(context.Background(),
			`SELECT kind::text || ':' || category || ':' || value FROM person_preferences WHERE person_id = $1 ORDER BY 1`, nora)
		if err != nil {
			t.Fatal(err)
		}
		var db []string
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			db = append(db, s)
		}
		rows.Close()
		if !slices.Equal(db, []string{"dietary:general:vegetarian", "dislike:general:broccoli", "like:food:pasta", "other:general:afraid of dogs"}) {
			t.Fatalf("stored = %v", db)
		}
	})

	t.Run("remove", func(t *testing.T) {
		up := tl.update.mustOK(t, ctx, map[string]any{
			"person_id": "Nora",
			"remove_preferences": []any{
				map[string]any{"kind": "allergy", "item": "Peanuts"},
				map[string]any{"kind": "dislike", "item": "BROCCOLI"},
				map[string]any{"kind": "like", "item": "sushi"},
			},
		})
		if !slices.Equal(up.Removed, []string{"allergy: peanuts", "dislike: broccoli"}) || !slices.Equal(up.Unchanged, []string{"like: sushi (was not listed)"}) ||
			!slices.Equal(up.Allergies, []string{"bee stings"}) || slices.Contains(prefLabels(up), "dislike: broccoli") {
			t.Fatalf("remove = %+v", up)
		}
	})

	t.Run("JSON-string and single-object forms", func(t *testing.T) {
		up := tl.update.mustOK(t, ctx, map[string]any{"person_id": "Nora", "add_preferences": `[{"kind":"allergy","item":"shellfish"}]`})
		if !slices.Contains(up.Allergies, "shellfish") {
			t.Fatalf("string form = %+v", up)
		}
		up = tl.update.mustOK(t, ctx, map[string]any{"person_id": "Nora", "remove_preferences": map[string]any{"kind": "allergy", "item": "shellfish"}})
		if slices.Contains(up.Allergies, "shellfish") {
			t.Fatalf("object form = %+v", up)
		}
	})

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"unknown kind", map[string]any{"person_id": "Nora", "add_preferences": []any{map[string]any{"kind": "fear", "item": "dogs"}}}},
		{"missing item", map[string]any{"person_id": "Nora", "add_preferences": []any{map[string]any{"kind": "like"}}}},
		{"plain strings", map[string]any{"person_id": "Nora", "add_preferences": []any{"peanuts"}}},
		{"not a list", map[string]any{"person_id": "Nora", "add_preferences": 5}},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			env := tl.update.mustFail(t, ctx, tc.args, "add_preferences")
			if env.Error.Example == nil {
				t.Errorf("no example: %+v", env.Error)
			}
		})
	}
}

// TestAllergyNotes covers #4742: an allergy's notes are kept in the entry
// ("peanuts (carries an EpiPen)") so they are never dropped and housesitters
// see them; the bare allergen still matches it on add and remove.
func TestAllergyNotes(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	tl := newTools(t, pool)
	nora := insertKid(t, pool, hh.HouseholdID, "Nora")
	const epi = "peanuts (carries an EpiPen)"

	stored := func(t *testing.T) []string {
		t.Helper()
		var got []string
		if err := pool.QueryRow(context.Background(), `SELECT allergies FROM people WHERE id = $1`, nora).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	env := tl.update.call(t, ctx, map[string]any{
		"person_id": "Nora",
		"add_preferences": []any{map[string]any{"kind": "allergy", "item": "peanuts", "notes": " carries  an EpiPen ",
			"category": "food"}}, // category is ignored for allergies
	})
	if !env.OK || !slices.Equal(env.Data.Allergies, []string{epi}) || !slices.Equal(env.Data.Added, []string{"allergy: " + epi}) ||
		!strings.Contains(env.Summary, epi) {
		t.Fatalf("add with notes = %+v", env)
	}
	if got := stored(t); !slices.Equal(got, []string{epi}) {
		t.Fatalf("stored = %v", got)
	}

	t.Run("owner and housesitter see the note", func(t *testing.T) {
		got := tl.get.call(t, ctx, map[string]any{"person_id": "Nora"})
		if !got.OK || !slices.Equal(got.Data.Allergies, []string{epi}) || !strings.Contains(got.Summary, "Allergies: "+epi+".") {
			t.Fatalf("owner get = %+v", got)
		}
		sitterCtx, _ := addMember(t, pool, hh, "housesitter", "Sam")
		sit := tl.get.call(t, sitterCtx, map[string]any{"person_id": nora})
		if !sit.OK || !sit.Data.Limited || !slices.Equal(sit.Data.Allergies, []string{epi}) || !strings.Contains(sit.Summary, "carries an EpiPen") {
			t.Fatalf("housesitter get = %+v", sit)
		}
	})

	t.Run("bare re-add keeps the note", func(t *testing.T) {
		env := tl.update.call(t, ctx, map[string]any{"person_id": "Nora",
			"add_preferences": []any{map[string]any{"kind": "allergy", "item": "PEANUTS"}}})
		up := env.Data
		if !env.OK || len(up.Added) != 0 || len(up.Changed) != 0 || !slices.Equal(up.Unchanged, []string{"allergy: " + epi + " (already listed)"}) ||
			!slices.Equal(stored(t), []string{epi}) {
			t.Fatalf("bare re-add = %+v", env)
		}
		// The same allergen and note again is unchanged too.
		again := tl.update.mustOK(t, ctx, map[string]any{"person_id": "Nora",
			"add_preferences": []any{map[string]any{"kind": "allergy", "item": "Peanuts", "notes": "Carries an EpiPen"}}})
		if len(again.Added) != 0 || len(again.Unchanged) != 1 || !slices.Equal(stored(t), []string{epi}) {
			t.Fatalf("same note again = %+v", again)
		}
	})

	t.Run("new note replaces, no duplicate", func(t *testing.T) {
		const two = "peanuts (carries two EpiPens)"
		up := tl.update.mustOK(t, ctx, map[string]any{"person_id": "Nora",
			"add_preferences": []any{map[string]any{"kind": "allergy", "item": "peanuts", "notes": "carries two EpiPens"}}})
		if !slices.Equal(up.Allergies, []string{two}) || !slices.Equal(up.Changed, []string{"allergies"}) ||
			!slices.Equal(up.Added, []string{"allergy: " + two + ", replacing " + epi}) || len(up.Unchanged) != 0 {
			t.Fatalf("replace = %+v", up)
		}
		// A bare allergen gets a note the same way.
		tl.update.mustOK(t, ctx, map[string]any{"person_id": "Nora", "add_preferences": []any{map[string]any{"kind": "allergy", "item": "milk"}}})
		up = tl.update.mustOK(t, ctx, map[string]any{"person_id": "Nora",
			"add_preferences": []any{map[string]any{"kind": "allergy", "item": "milk", "notes": "hives only"}}})
		if got := stored(t); !slices.Equal(got, []string{two, "milk (hives only)"}) || len(up.Added) != 1 {
			t.Fatalf("note on bare allergen: stored %v, update %+v", got, up)
		}
	})

	t.Run("remove by bare allergen", func(t *testing.T) {
		up := tl.update.mustOK(t, ctx, map[string]any{"person_id": "Nora",
			"remove_preferences": []any{map[string]any{"kind": "allergy", "item": "Peanuts"}, map[string]any{"kind": "allergy", "item": "kiwi"}}})
		if !slices.Equal(up.Removed, []string{"allergy: peanuts (carries two EpiPens)"}) || !slices.Equal(up.Unchanged, []string{"allergy: kiwi (was not listed)"}) ||
			!slices.Equal(stored(t), []string{"milk (hives only)"}) {
			t.Fatalf("remove = %+v", up)
		}
		// The full stored text matches too.
		up = tl.update.mustOK(t, ctx, map[string]any{"person_id": "Nora",
			"remove_preferences": []any{map[string]any{"kind": "allergy", "item": "Milk (hives only)"}}})
		if len(up.Removed) != 1 || len(stored(t)) != 0 {
			t.Fatalf("remove full text = %+v", up)
		}
	})

	t.Run("within one call the entry with notes wins", func(t *testing.T) {
		up := tl.update.mustOK(t, ctx, map[string]any{"person_id": "Nora", "add_preferences": []any{
			map[string]any{"kind": "allergy", "item": "eggs"},
			map[string]any{"kind": "allergy", "item": "Eggs", "notes": "baked is fine"},
		}})
		if !slices.Equal(stored(t), []string{"Eggs (baked is fine)"}) || len(up.Added) != 1 {
			t.Fatalf("in-call duplicate = %+v", up)
		}
	})

	t.Run("too long with notes", func(t *testing.T) {
		tl.update.mustFail(t, ctx, map[string]any{"person_id": "Nora", "add_preferences": []any{
			map[string]any{"kind": "allergy", "item": "peanuts", "notes": strings.Repeat("x", maxItem)}}}, "add_preferences")
	})
}

func TestPersonGet(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, other := testutil.SeedHousehold(t, pool)
	tl := newTools(t, pool)
	nora := insertKid(t, pool, hh.HouseholdID, "Nora Smith")
	otherKid := insertKid(t, pool, other.HouseholdID, "Nora")
	bg := context.Background()
	if _, err := pool.Exec(bg, `UPDATE people SET allergies = '{peanuts}' WHERE id = $1`, nora); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO person_preferences (household_id, person_id, kind, value) VALUES ($1, $2, 'like', 'pasta')`, hh.HouseholdID, nora); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'person', $2, 'Shoe size', '13')`, hh.HouseholdID, nora); err != nil {
		t.Fatal(err)
	}

	t.Run("full view by first name", func(t *testing.T) {
		env := tl.get.call(t, ctx, map[string]any{"person_id": "nora"})
		if !env.OK {
			t.Fatalf("got %+v", env)
		}
		p := *env.Data
		if p.PersonID != nora || p.Name != "Nora Smith" || p.Relationship == nil || *p.Relationship != "child" || p.HasAccount ||
			p.Birthdate == nil || *p.Birthdate != "2015-04-02" || p.Age == nil || p.NextBirthday == nil ||
			p.School == nil || *p.School != "Maple Elementary" || p.Grade == nil || *p.Grade != "5" || p.Notes == nil ||
			!slices.Equal(prefLabels(p), []string{"like: pasta"}) || p.Linked == nil || p.Linked.Notes != 1 || p.Limited {
			t.Fatalf("person = %+v", p)
		}
		if p.NextBirthday.Turns != *p.Age+1 && p.NextBirthday.Turns != *p.Age {
			t.Errorf("next birthday %+v with age %d", p.NextBirthday, *p.Age)
		}
		if len(env.Next) < 2 || !strings.Contains(env.Next[1], "note_find about_type=person about_id="+nora) {
			t.Errorf("next = %v", env.Next)
		}
	})

	t.Run("a member with a login", func(t *testing.T) {
		p := tl.get.mustOK(t, ctx, map[string]any{"person_id": hh.UserID.String()}) // user id works too
		if p.PersonID != ownerPerson(t, pool, hh) || !p.HasAccount || p.Role == nil || *p.Role != "owner" || p.Email == nil {
			t.Fatalf("owner = %+v", p)
		}
	})

	t.Run("another family's person is never found", func(t *testing.T) {
		tl.get.mustFail(t, ctx, map[string]any{"person_id": otherKid}, "person_id")
		p := tl.get.mustOK(t, otherCtx, map[string]any{"person_id": "Nora"})
		if p.PersonID != otherKid {
			t.Fatalf("other family got %+v", p)
		}
	})

	t.Run("unknown and ambiguous names", func(t *testing.T) {
		env := tl.get.mustFail(t, ctx, map[string]any{"person_id": "Zed"}, "person_id")
		if !strings.Contains(env.Error.Message, "Nora Smith") {
			t.Errorf("not-found error does not list people: %s", env.Error.Message)
		}
		insertKid(t, pool, hh.HouseholdID, "Nora Jones")
		env = tl.get.mustFail(t, ctx, map[string]any{"person_id": "Nora"}, "person_id")
		if !strings.Contains(env.Error.Message, nora) {
			t.Errorf("ambiguous error does not list ids: %s", env.Error.Message)
		}
		tl.get.mustFail(t, ctx, map[string]any{}, "person_id")
	})

	t.Run("roles", func(t *testing.T) {
		viewerCtx, _ := addMember(t, pool, hh, "viewer", "Vic")
		if p := tl.get.mustOK(t, viewerCtx, map[string]any{"person_id": nora}); p.Limited || p.Birthdate == nil || len(p.Preferences) != 1 {
			t.Errorf("viewer = %+v", p)
		}
		sitterCtx, _ := addMember(t, pool, hh, "housesitter", "Sam")
		env := tl.get.call(t, sitterCtx, map[string]any{"person_id": nora})
		p := env.Data
		if !env.OK || !p.Limited || p.Name != "Nora Smith" || p.Relationship == nil || !slices.Equal(p.Allergies, []string{"peanuts"}) ||
			p.Birthdate != nil || p.Age != nil || p.NextBirthday != nil || p.School != nil || p.Grade != nil || p.Notes != nil ||
			p.Preferences != nil || p.Linked != nil || p.Email != nil || len(env.Next) != 0 {
			t.Errorf("housesitter = %+v", env)
		}
		vendorCtx, _ := addMember(t, pool, hh, "vendor", "Vin")
		tl.get.mustFail(t, vendorCtx, map[string]any{"person_id": nora}, "")

		// The role is re-read from memberships: claims that still say owner
		// do not help a member demoted to vendor, and a removed member gets
		// nothing.
		demotedCtx, _ := addMember(t, pool, hh, "owner", "Dee")
		demoted := server.ClaimsFromContext(demotedCtx)
		if _, err := pool.Exec(bg, `UPDATE memberships SET role = 'vendor' WHERE user_id = $1`, demoted.UserID); err != nil {
			t.Fatal(err)
		}
		tl.get.mustFail(t, demotedCtx, map[string]any{"person_id": nora}, "")
		if _, err := pool.Exec(bg, `DELETE FROM memberships WHERE user_id = $1`, demoted.UserID); err != nil {
			t.Fatal(err)
		}
		if env := tl.get.mustFail(t, demotedCtx, map[string]any{"person_id": nora}, ""); !strings.Contains(env.Error.Message, "not signed in") {
			t.Errorf("removed member: %+v", env.Error)
		}
		tl.get.mustFail(t, context.Background(), map[string]any{"person_id": nora}, "")
	})
}

func TestPersonCreate(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	tl := newTools(t, pool)

	p := tl.create.mustOK(t, ctx, map[string]any{"display_name": " Leo ", "relationship": "Son", "birthdate": "2019-06-30", "grade": 1, "school": ""})
	if p.Name != "Leo" || p.Relationship == nil || *p.Relationship != "child" || p.Birthdate == nil || *p.Birthdate != "2019-06-30" ||
		p.Grade == nil || *p.Grade != "1" || p.School != nil || p.HasAccount || p.Duplicate || !slices.Equal(p.Allergies, []string{}) {
		t.Fatalf("created = %+v", p)
	}
	var hid, createdBy string
	if err := pool.QueryRow(context.Background(), `SELECT household_id::text, COALESCE(created_by::text, '') FROM people WHERE id = $1 AND user_id IS NULL`,
		p.PersonID).Scan(&hid, &createdBy); err != nil || hid != hh.HouseholdID.String() {
		t.Fatalf("stored household %q (%v)", hid, err)
	}
	if createdBy != hh.UserID.String() { // the pool attributes MCP writes (#4491)
		t.Errorf("created_by = %q, want the owner %s", createdBy, hh.UserID)
	}

	t.Run("loop guard returns the same person", func(t *testing.T) {
		again := tl.create.mustOK(t, ctx, map[string]any{"display_name": "leo", "relationship": "child"})
		if !again.Duplicate || again.PersonID != p.PersonID {
			t.Fatalf("repeat = %+v", again)
		}
		var n int
		_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM people WHERE household_id = $1 AND lower(display_name) = 'leo'`, hh.HouseholdID).Scan(&n)
		if n != 1 {
			t.Fatalf("%d Leos", n)
		}
	})

	t.Run("parallel identical creates make one person", func(t *testing.T) {
		var wg sync.WaitGroup
		ids := make([]string, 6)
		for i := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ids[i] = tl.create.mustOK(t, ctx, map[string]any{"display_name": "Grandma Jo", "relationship": "grandparent"}).PersonID
			}()
		}
		wg.Wait()
		for _, id := range ids {
			if id != ids[0] {
				t.Fatalf("ids = %v", ids)
			}
		}
	})

	t.Run("same name, other relationship warns about names", func(t *testing.T) {
		env := tl.create.call(t, ctx, map[string]any{"display_name": "Leo", "relationship": "caregiver"})
		if !env.OK || env.Data.Duplicate || env.Data.PersonID == p.PersonID || !strings.Contains(strings.Join(env.Next, " "), p.PersonID) {
			t.Fatalf("got %+v", env)
		}
	})

	for _, tc := range []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"no name", map[string]any{"relationship": "child"}, "display_name"},
		{"blank name", map[string]any{"display_name": "  ", "relationship": "child"}, "display_name"},
		{"no relationship", map[string]any{"display_name": "Max"}, "relationship"},
		{"bad relationship", map[string]any{"display_name": "Max", "relationship": "neighbor"}, "relationship"},
		{"bad birthdate", map[string]any{"display_name": "Max", "relationship": "child", "birthdate": "04/02/2015"}, "birthdate"},
		{"future birthdate", map[string]any{"display_name": "Max", "relationship": "child", "birthdate": time.Now().AddDate(0, 0, 3).Format(time.DateOnly)}, "birthdate"},
		{"long grade", map[string]any{"display_name": "Max", "relationship": "child", "grade": strings.Repeat("x", 51)}, "grade"},
		{"name not text", map[string]any{"display_name": []any{"Max"}, "relationship": "child"}, "display_name"},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			tl.create.mustFail(t, ctx, tc.args, tc.field)
		})
	}

	t.Run("only owners and family managers", func(t *testing.T) {
		mgrCtx, _ := addMember(t, pool, hh, "family_manager", "Mia")
		tl.create.mustOK(t, mgrCtx, map[string]any{"display_name": "Nanny Ana", "relationship": "nanny"})
		for _, role := range []string{"family_member", "viewer", "housesitter", "vendor"} {
			rctx, _ := addMember(t, pool, hh, role, "R "+role)
			env := tl.create.mustFail(t, rctx, map[string]any{"display_name": "Sneaky", "relationship": "other"}, "")
			if !strings.Contains(env.Error.Message, role) {
				t.Errorf("%s: %s", role, env.Error.Message)
			}
		}
		var n int
		_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM people WHERE display_name = 'Sneaky'`).Scan(&n)
		if n != 0 {
			t.Fatalf("refused roles created %d people", n)
		}
	})
}

func TestPersonUpdateFields(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, other := testutil.SeedHousehold(t, pool)
	tl := newTools(t, pool)
	nora := insertKid(t, pool, hh.HouseholdID, "Nora")
	otherKid := insertKid(t, pool, other.HouseholdID, "Otto")

	t.Run("only given fields change; null clears; empty is not given", func(t *testing.T) {
		p := tl.update.mustOK(t, ctx, map[string]any{"person_id": nora, "grade": "6", "school": nil, "notes": "", "birthdate": "2015-04-03"})
		if p.Grade == nil || *p.Grade != "6" || p.School != nil || p.Notes == nil || *p.Notes != "Loves dinosaurs" ||
			*p.Birthdate != "2015-04-03" || !slices.Equal(p.Changed, []string{"school", "grade", "birthdate"}) {
			t.Fatalf("update = %+v", p)
		}
		env := tl.update.call(t, ctx, map[string]any{"person_id": nora, "grade": "6"})
		if !env.OK || len(env.Data.Changed) != 0 || !strings.HasPrefix(env.Summary, "Nothing changed for Nora") {
			t.Fatalf("repeat = %+v", env)
		}
		p = tl.update.mustOK(t, ctx, map[string]any{"person_id": nora, "display_name": "Nora Lee", "relationship": "daughter", "birthdate": nil})
		if p.Name != "Nora Lee" || *p.Relationship != "child" || p.Birthdate != nil || p.Age != nil {
			t.Fatalf("rename = %+v", p)
		}
	})

	for _, tc := range []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"nothing to change", map[string]any{"person_id": nora}, "person_id"},
		{"clearing the name", map[string]any{"person_id": nora, "display_name": nil}, "display_name"},
		{"bad relationship", map[string]any{"person_id": nora, "relationship": "cousin"}, "relationship"},
		{"another family's person", map[string]any{"person_id": otherKid, "grade": "1"}, "person_id"},
		{"unknown name", map[string]any{"person_id": "Otto", "grade": "1"}, "person_id"},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			tl.update.mustFail(t, ctx, tc.args, tc.field)
		})
	}
	var grade string
	_ = pool.QueryRow(context.Background(), `SELECT grade FROM people WHERE id = $1`, otherKid).Scan(&grade)
	if grade != "5" {
		t.Fatalf("other family's kid changed: grade %q", grade)
	}

	t.Run("family_member edits only their own record", func(t *testing.T) {
		memberCtx, me := addMember(t, pool, hh, "family_member", "Max")
		p := tl.update.mustOK(t, memberCtx, map[string]any{"person_id": "Max", "add_preferences": []any{map[string]any{"kind": "like", "item": "coffee"}}})
		if p.PersonID != me || !slices.Equal(prefLabels(p), []string{"like: coffee"}) {
			t.Fatalf("own = %+v", p)
		}
		env := tl.update.mustFail(t, memberCtx, map[string]any{"person_id": nora, "add_preferences": []any{map[string]any{"kind": "allergy", "item": "milk"}}}, "")
		if !strings.Contains(env.Error.Message, "own") {
			t.Errorf("message %q", env.Error.Message)
		}
		tl.update.mustFail(t, memberCtx, map[string]any{"person_id": ownerPerson(t, pool, hh), "grade": "1"}, "")
	})

	t.Run("read-only roles change nothing", func(t *testing.T) {
		for _, role := range []string{"viewer", "housesitter", "vendor"} {
			rctx, _ := addMember(t, pool, hh, role, "RO "+role)
			tl.update.mustFail(t, rctx, map[string]any{"person_id": "RO " + role, "notes": "mine"}, "")
		}
		var n int
		_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM people WHERE notes = 'mine'`).Scan(&n)
		if n != 0 {
			t.Fatalf("read-only roles changed %d people", n)
		}
	})
}

func TestPersonDelete(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, other := testutil.SeedHousehold(t, pool)
	tl := newTools(t, pool)
	bg := context.Background()

	t.Run("a person with nothing attached goes at once", func(t *testing.T) {
		id := insertKid(t, pool, hh.HouseholdID, "Temp")
		if _, err := pool.Exec(bg, `UPDATE people SET allergies = '{milk}' WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(bg, `INSERT INTO person_preferences (household_id, person_id, kind, value) VALUES ($1, $2, 'like', 'pasta')`, hh.HouseholdID, id); err != nil {
			t.Fatal(err)
		}
		d := tl.del.mustOK(t, ctx, map[string]any{"person_id": "Temp"})
		if !d.Deleted || d.PersonID != id || d.Removed.Preferences != 1 || d.Removed.Allergies != 1 {
			t.Fatalf("delete = %+v", d)
		}
		tl.del.mustFail(t, ctx, map[string]any{"person_id": id}, "person_id") // gone
	})

	t.Run("needs confirm when records point at them", func(t *testing.T) {
		id := insertKid(t, pool, hh.HouseholdID, "Nora")
		if _, err := pool.Exec(bg, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'person', $2, 'Shoe size', '13')`, hh.HouseholdID, id); err != nil {
			t.Fatal(err)
		}
		var listID string
		if err := pool.QueryRow(bg, `INSERT INTO lists (household_id, name, kind, entity_type, entity_id) VALUES ($1, 'Chores', 'chores', 'household', $1) RETURNING id::text`, hh.HouseholdID).Scan(&listID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(bg, `INSERT INTO list_items (list_id, household_id, text, assignee_person_id, position) VALUES ($1, $2, 'Feed the cat', $3, 1)`, listID, hh.HouseholdID, id); err != nil {
			t.Fatal(err)
		}
		env := tl.del.mustFail(t, ctx, map[string]any{"person_id": "Nora"}, "confirm")
		if !strings.Contains(env.Error.Message, "1 note about them") || !strings.Contains(env.Error.Message, "1 list item") ||
			env.Error.Example["confirm"] != true {
			t.Fatalf("error = %+v", env.Error)
		}
		d := tl.del.mustOK(t, ctx, map[string]any{"person_id": "Nora", "confirm": "true"})
		if d.Removed.Notes != 1 || d.Unlinked.ListItems != 1 {
			t.Fatalf("delete = %+v", d)
		}
		var text string
		var assignee *string
		if err := pool.QueryRow(bg, `SELECT text, assignee_person_id::text FROM list_items WHERE list_id = $1`, listID).Scan(&text, &assignee); err != nil ||
			text != "Feed the cat" || assignee != nil {
			t.Fatalf("list item after delete: %q %v (%v)", text, assignee, err)
		}
	})

	t.Run("a member's record is refused", func(t *testing.T) {
		_, member := addMember(t, pool, hh, "family_member", "Max")
		env := tl.del.mustFail(t, ctx, map[string]any{"person_id": member, "confirm": true}, "person_id")
		if !strings.Contains(env.Error.Fix, "remove the member") {
			t.Errorf("fix %q", env.Error.Fix)
		}
		tl.del.mustFail(t, ctx, map[string]any{"person_id": ownerPerson(t, pool, hh), "confirm": true}, "person_id")
	})

	t.Run("roles and families", func(t *testing.T) {
		id := insertKid(t, pool, hh.HouseholdID, "Eli")
		otherKid := insertKid(t, pool, other.HouseholdID, "Otto")
		for _, role := range []string{"family_member", "viewer", "housesitter", "vendor"} {
			rctx, _ := addMember(t, pool, hh, role, "D "+role)
			tl.del.mustFail(t, rctx, map[string]any{"person_id": id, "confirm": true}, "")
		}
		tl.del.mustFail(t, ctx, map[string]any{"person_id": otherKid, "confirm": true}, "person_id")
		var n int
		_ = pool.QueryRow(bg, `SELECT count(*) FROM people WHERE id IN ($1, $2)`, id, otherKid).Scan(&n)
		if n != 2 {
			t.Fatalf("%d of 2 people left", n)
		}
		mgrCtx, _ := addMember(t, pool, hh, "family_manager", "Mia")
		tl.del.mustOK(t, mgrCtx, map[string]any{"person_id": "Eli"})
	})
}

// TestToolDefinitions checks names, annotations (catalog classes) and the
// catalog's description limits.
func TestToolDefinitions(t *testing.T) {
	type def struct {
		name  string
		tool  mcp.Tool
		class string
	}
	var defs []def
	for _, mk := range []struct {
		f     func(*pgxpool.Pool) (string, mcp.Tool, handlerFunc)
		class string
	}{{NewPersonGetTool, "R"}, {NewPersonCreateTool, "C"}, {NewPersonUpdateTool, "U"}, {NewPersonDeleteTool, "X"}} {
		n, tl, _ := mk.f(nil)
		defs = append(defs, def{n, tl, mk.class})
	}
	want := map[string][4]bool{ // readOnly, destructive, idempotent, openWorld
		"R": {true, false, true, false}, "C": {false, false, false, false},
		"U": {false, false, true, false}, "X": {false, true, true, false},
	}
	names := []string{}
	for _, d := range defs {
		names = append(names, d.name)
		a := d.tool.Annotations
		if a.Title == "" || a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
			t.Fatalf("%s: incomplete annotations %+v", d.name, a)
		}
		if got := [4]bool{*a.ReadOnlyHint, *a.DestructiveHint, *a.IdempotentHint, *a.OpenWorldHint}; got != want[d.class] {
			t.Errorf("%s: hints %v, want class %s %v", d.name, got, d.class, want[d.class])
		}
		if len(d.tool.Description) > 200 {
			t.Errorf("%s: description is %d characters", d.name, len(d.tool.Description))
		}
		for p, raw := range d.tool.InputSchema.Properties {
			if m, ok := raw.(map[string]any); ok {
				if desc, _ := m["description"].(string); len(desc) > 80 {
					t.Errorf("%s.%s: param description is %d characters", d.name, p, len(desc))
				}
			}
		}
		if !slices.Contains(d.tool.InputSchema.Required, "person_id") && d.name != "person_create" {
			t.Errorf("%s: person_id is not required", d.name)
		}
	}
	if !slices.Equal(names, []string{"person_get", "person_create", "person_update", "person_delete"}) {
		t.Fatalf("names = %v", names)
	}
	_, create, _ := NewPersonCreateTool(nil)
	if fmt.Sprint(create.InputSchema.Required) != "[display_name relationship]" {
		t.Errorf("person_create required = %v", create.InputSchema.Required)
	}
	_, update, _ := NewPersonUpdateTool(nil)
	for _, p := range []string{"display_name", "relationship", "birthdate", "school", "grade", "notes", "add_preferences", "remove_preferences"} {
		if _, ok := update.InputSchema.Properties[p]; !ok {
			t.Errorf("person_update has no %s", p)
		}
	}
}

func TestAgeOn(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	for _, tc := range []struct {
		birth, today string
		age          int
		next         string
		turns        int
	}{
		{"2015-04-02", "2026-04-01", 10, "2026-04-02", 11},
		{"2015-04-02", "2026-04-02", 11, "2026-04-02", 11},
		{"2015-04-02", "2026-11-15", 11, "2027-04-02", 12},
		{"2016-02-29", "2027-02-27", 10, "2027-02-28", 11},
		{"2016-02-29", "2028-02-28", 11, "2028-02-29", 12},
	} {
		age, b := ageOn(d(tc.birth), d(tc.today))
		if age != tc.age || b.LocalDate != tc.next || b.Turns != tc.turns || b.Weekday != d(tc.next).Format("Mon") {
			t.Errorf("ageOn(%s, %s) = %d %+v, want %d %s turns %d", tc.birth, tc.today, age, b, tc.age, tc.next, tc.turns)
		}
	}
}
