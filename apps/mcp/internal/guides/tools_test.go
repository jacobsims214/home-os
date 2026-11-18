package guides

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/search"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// env is the JSON a guide tool returns.
type env struct {
	OK         bool            `json:"ok"`
	Summary    string          `json:"summary"`
	Data       json.RawMessage `json:"data"`
	Items      []guideItem     `json:"items"`
	Total      int             `json:"total"`
	NextCursor string          `json:"next_cursor"`
	Next       []string        `json:"next"`
	Error      *struct {
		Message string `json:"message"`
		Fix     string `json:"fix"`
		Field   string `json:"field"`
	} `json:"error"`
	raw string // the whole result text, for leak checks
}

func (e env) guide(t *testing.T) guideData {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d guideData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func (e env) titles() []string {
	out := make([]string, len(e.Items))
	for i, g := range e.Items {
		out[i] = g.Title
	}
	return out
}

func (e env) wantError(t *testing.T, field, contains string) {
	t.Helper()
	if e.OK || e.Error == nil {
		t.Fatalf("want isError on %q, got success: %s", field, e.Summary)
	}
	if e.Error.Field != field {
		t.Errorf("error field = %q, want %q (%s)", e.Error.Field, field, e.Error.Message)
	}
	if e.Error.Fix == "" {
		t.Errorf("error lacks a fix: %+v", e.Error)
	}
	if !strings.Contains(e.Error.Message, contains) {
		t.Errorf("error %q does not contain %q", e.Error.Message, contains)
	}
}

type tool struct {
	def mcp.Tool
	h   toolHandler
}

type tools struct{ find, get, create, update, del tool }

func newTools(pool *pgxpool.Pool, idx searchIndex) tools {
	mk := func(_ string, d mcp.Tool, h toolHandler) tool { return tool{d, h} }
	return tools{
		find:   mk(NewGuideFindTool(pool, idx)),
		get:    mk(NewGuideGetTool(pool)),
		create: mk(NewGuideCreateTool(pool, idx)),
		update: mk(NewGuideUpdateTool(pool, idx)),
		del:    mk(NewGuideDeleteTool(pool, idx)),
	}
}

func resolvedSchema(t *testing.T, d mcp.Tool) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || len(raw.OutputSchema) == 0 {
		t.Fatalf("%s declares no outputSchema (%v)", d.Name, err)
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

// run calls the tool and checks the respond invariants: one text block equal
// to structuredContent, valid against the outputSchema, ok == !isError, and
// at most 3 next hints.
func (w tool) run(t *testing.T, ctx context.Context, args map[string]any) env {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = w.def.Name
	req.Params.Arguments = args
	res, err := w.h(ctx, req)
	if err != nil {
		t.Fatalf("%s returned a Go error: %v", w.def.Name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("want 1 content item, got %d", len(res.Content))
	}
	text := res.Content[0].(mcp.TextContent).Text
	sc, err := json.Marshal(res.StructuredContent)
	if err != nil || string(sc) != text {
		t.Fatalf("text differs from structuredContent (%v):\n%s\n%s", err, text, sc)
	}
	var instance map[string]any
	if err := json.Unmarshal(sc, &instance); err != nil {
		t.Fatal(err)
	}
	if err := resolvedSchema(t, w.def).Validate(instance); err != nil {
		t.Fatalf("%s result does not match its outputSchema: %v\n%s", w.def.Name, err, text)
	}
	var e env
	if err := json.Unmarshal(sc, &e); err != nil {
		t.Fatal(err)
	}
	if e.OK == res.IsError {
		t.Fatalf("ok=%v but isError=%v", e.OK, res.IsError)
	}
	if len(e.Next) > 3 {
		t.Errorf("%s gives %d next hints: %v", w.def.Name, len(e.Next), e.Next)
	}
	e.raw = text
	return e
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v\n%s", err, sql)
	}
	return n
}

func insertID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("insert: %v\n%s", err, sql)
	}
	return id
}

// memberCtx adds a member with role to hh and returns their claims context.
func memberCtx(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) (context.Context, uuid.UUID) {
	t.Helper()
	bg := context.Background()
	email := role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	uid := insertID(t, pool, `INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`, email, "Member "+role)
	if _, err := pool.Exec(bg, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hh.HouseholdID, uid, role); err != nil {
		t.Fatal(err)
	}
	return server.ContextWithClaims(bg, &auth.Claims{
		Identity: auth.Identity{Subject: uid.String(), Email: email}, UserID: uid.String(),
		HouseholdID: hh.HouseholdID.String(), Role: role,
	}), uid
}

// family is a seeded household: a home with a furnace, a lake house and a
// van.
type family struct {
	ctx                      context.Context
	hh                       testutil.Household
	home, lake, furnace, van uuid.UUID
}

func seedFamily(t *testing.T, pool *pgxpool.Pool) family {
	t.Helper()
	ctx, hh := testutil.SeedHousehold(t, pool)
	f := family{ctx: ctx, hh: hh}
	f.home = insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Home') RETURNING id`, hh.HouseholdID)
	f.lake = insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Lake House') RETURNING id`, hh.HouseholdID)
	f.furnace = insertID(t, pool, `INSERT INTO assets (household_id, property_id, name, category) VALUES ($1, $2, 'Furnace', 'hvac') RETURNING id`, hh.HouseholdID, f.home)
	f.van = insertID(t, pool, `INSERT INTO vehicles (household_id, year, make, model) VALUES ($1, 2019, 'Honda', 'Odyssey') RETURNING id`, hh.HouseholdID)
	return f
}

// hvacGuide shares no word with "How do I replace the air filter?" except
// "filter" in the body; the router guide shares none with "the internet is
// down".
var (
	hvacGuide = map[string]any{
		"title":    "HVAC maintenance",
		"summary":  "Swap the furnace's pleated MERV 11 element every 90 days",
		"body":     "1. Turn the thermostat off.\n2. Open the blower door and slide the old filter out.\n3. Note the airflow arrow and slide the new one in.",
		"tags":     []string{"HVAC", " hvac ", "Furnace"},
		"about_id": "furnace",
	}
	routerGuide = map[string]any{
		"title": "Reset the router",
		"body":  "When the wifi drops: unplug the modem and the router, wait 30 seconds, plug the modem in first, then the router.",
		"tags":  "network, wifi",
	}
	oilGuide = map[string]any{
		"title": "Change the oil in the van", "about_type": "vehicle", "about_id": "Honda Odyssey",
		"body": "Every 5000 miles. Use 0W-20. The oil filter is a Fram PH7317.",
	}
	dogGuide = map[string]any{
		"title": "Dog care for the sitter", "summary": "Feeding, walks and the vet",
		"body": "Rex eats twice a day. Walk him in the morning and at night. No grapes.",
	}
)

// TestToolDefinitions pins the tools to the catalog: names, required params,
// all four hints, description lengths and an outputSchema.
func TestToolDefinitions(t *testing.T) {
	w := newTools(nil, nil)
	for _, tc := range []struct {
		w                      tool
		name                   string
		required, params       []string
		readOnly, destr, idemp bool
	}{
		{w.find, "guide_find", nil, []string{"query", "about_type", "about_id", "tag", "limit", "cursor"}, true, false, true},
		{w.get, "guide_get", []string{"guide_id"}, []string{"guide_id"}, true, false, true},
		{w.create, "guide_create", []string{"title", "body"}, []string{"title", "body", "summary", "tags", "about_type", "about_id"}, false, false, false},
		{w.update, "guide_update", []string{"guide_id"}, []string{"guide_id", "title", "body", "summary", "tags", "about_type", "about_id", "change_note"}, false, false, true},
		{w.del, "guide_delete", []string{"guide_id"}, []string{"guide_id", "confirm"}, false, true, true},
	} {
		d := tc.w.def
		if d.Name != tc.name {
			t.Errorf("name = %q, want %q", d.Name, tc.name)
		}
		if n := utf8.RuneCountInString(d.Description); n > 200 || n == 0 {
			t.Errorf("%s description has %d characters", d.Name, n)
		}
		if strings.Join(d.InputSchema.Required, ",") != strings.Join(tc.required, ",") {
			t.Errorf("%s required = %v, want %v", d.Name, d.InputSchema.Required, tc.required)
		}
		if len(d.InputSchema.Properties) != len(tc.params) {
			t.Errorf("%s has %d params, want %v", d.Name, len(d.InputSchema.Properties), tc.params)
		}
		for _, p := range tc.params {
			prop, ok := d.InputSchema.Properties[p].(map[string]any)
			if !ok {
				t.Errorf("%s lacks param %s", d.Name, p)
				continue
			}
			if desc, _ := prop["description"].(string); desc == "" || utf8.RuneCountInString(desc) > 80 {
				t.Errorf("%s.%s description %q (want 1-80 characters)", d.Name, p, desc)
			}
		}
		if p, ok := d.InputSchema.Properties["about_type"].(map[string]any); ok {
			b, _ := json.Marshal(p["enum"])
			if string(b) != `["family","property","asset","vehicle"]` {
				t.Errorf("%s about_type enum = %s", d.Name, b)
			}
		}
		a := d.Annotations
		if a.ReadOnlyHint == nil || *a.ReadOnlyHint != tc.readOnly || a.DestructiveHint == nil || *a.DestructiveHint != tc.destr ||
			a.IdempotentHint == nil || *a.IdempotentHint != tc.idemp || a.OpenWorldHint == nil || *a.OpenWorldHint || a.Title == "" {
			t.Errorf("%s annotations = %+v", d.Name, a)
		}
		resolvedSchema(t, d)
	}
}

// TestGuideRevisions: create writes the first revision; every update that
// changes something writes one more in the same transaction, the newest
// equals the guide's body and the previous body is kept; a no-op writes
// nothing.
func TestGuideRevisions(t *testing.T) {
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool)
	w := newTools(pool, nil)

	d := w.create.run(t, f.ctx, hvacGuide).guide(t)
	if d.About.Type != "asset" || d.About.Name != "Furnace" || d.About.Property != "Home" || *d.About.ID != f.furnace.String() {
		t.Errorf("about = %+v", d.About)
	}
	if strings.Join(d.Tags, ",") != "hvac,furnace" {
		t.Errorf("tags = %v", d.Tags)
	}
	if d.RevisionCount != 1 || len(d.RecentRevisions) != 1 || d.RecentRevisions[0].EditedBy != "Test Owner" {
		t.Errorf("revisions after create = %d %+v", d.RevisionCount, d.RecentRevisions)
	}
	// property_id is derived from the asset (derive_subject_property).
	if n := count(t, pool, `SELECT count(*) FROM guides WHERE id = $1 AND property_id = $2 AND asset_id = $3`, d.ID, f.home, f.furnace); n != 1 {
		t.Error("asset guide has no derived property_id")
	}
	if n := count(t, pool, `SELECT count(*) FROM guide_revisions WHERE guide_id = $1 AND edited_by = $2 AND body = $3`,
		d.ID, f.hh.UserID, hvacGuide["body"]); n != 1 {
		t.Error("first revision is not the body as created by the owner")
	}

	oldBody := hvacGuide["body"].(string)
	newBody := "1. Turn the thermostat off.\n2. Replace the 16x25x1 filter.\n3. Write the date on it."
	e := w.update.run(t, f.ctx, map[string]any{"guide_id": "hvac maintenance", "body": newBody, "change_note": "new filter size"})
	u := e.guide(t)
	if u.Body != newBody || u.RevisionCount != 2 || strings.Join(u.Changed, ",") != "body" {
		t.Errorf("update = body %q, %d revisions, changed %v", u.Body, u.RevisionCount, u.Changed)
	}
	if !strings.Contains(e.Summary, "new filter size") || !strings.Contains(e.Summary, "previous version is kept") {
		t.Errorf("summary = %q", e.Summary)
	}
	var newest string
	var note *string
	if err := pool.QueryRow(context.Background(), `SELECT body, change_note FROM guide_revisions WHERE guide_id = $1
		ORDER BY created_at DESC, id DESC LIMIT 1`, d.ID).Scan(&newest, &note); err != nil || newest != newBody {
		t.Errorf("newest revision = %q (%v), want the saved body", newest, err)
	}
	// change_note is stored on the new revision and returned by guide_get.
	if note == nil || *note != "new filter size" {
		t.Errorf("newest revision change_note = %v, want new filter size", note)
	}
	if len(u.RecentRevisions) != 2 || u.RecentRevisions[0].ChangeNote != "new filter size" || u.RecentRevisions[1].ChangeNote != "" {
		t.Errorf("update recent_revisions = %+v", u.RecentRevisions)
	}
	if g := w.get.run(t, f.ctx, map[string]any{"guide_id": d.ID}).guide(t); len(g.RecentRevisions) != 2 ||
		g.RecentRevisions[0].ChangeNote != "new filter size" || g.RecentRevisions[1].ChangeNote != "" {
		t.Errorf("guide_get recent_revisions = %+v", g.RecentRevisions)
	}
	if raw := w.get.run(t, f.ctx, map[string]any{"guide_id": d.ID}).raw; strings.Count(raw, `"change_note"`) != 1 {
		t.Errorf("guide_get should omit an empty change_note: %s", raw)
	}
	if n := count(t, pool, `SELECT count(*) FROM guide_revisions WHERE guide_id = $1 AND body = $2`, d.ID, oldBody); n != 1 {
		t.Error("the previous body is not kept as a revision")
	}

	// A no-op writes nothing.
	var before time.Time
	_ = pool.QueryRow(context.Background(), `SELECT updated_at FROM guides WHERE id = $1`, d.ID).Scan(&before)
	e = w.update.run(t, f.ctx, map[string]any{"guide_id": d.ID, "body": newBody, "tags": []string{"HVAC", "furnace"}, "about_type": "asset", "about_id": "Furnace", "title": ""})
	if !strings.HasPrefix(e.Summary, "Nothing changed") || e.guide(t).RevisionCount != 2 {
		t.Errorf("no-op update: %s", e.Summary)
	}
	var after time.Time
	_ = pool.QueryRow(context.Background(), `SELECT updated_at FROM guides WHERE id = $1`, d.ID).Scan(&after)
	if !after.Equal(before) {
		t.Error("no-op update touched the guide")
	}

	// Title, summary (null clears) and tags: one more revision, body as is.
	u = w.update.run(t, f.ctx, map[string]any{"guide_id": d.ID, "title": "HVAC filter replacement", "summary": nil, "tags": nil}).guide(t)
	if u.Title != "HVAC filter replacement" || u.Summary != nil || len(u.Tags) != 0 || u.RevisionCount != 3 ||
		strings.Join(u.Changed, ",") != "title,summary,tags" {
		t.Errorf("update = %+v", u)
	}

	// About: a vehicle clears the property and asset; family clears all.
	u = w.update.run(t, f.ctx, map[string]any{"guide_id": d.ID, "about_type": "vehicle", "about_id": f.van.String()}).guide(t)
	if u.About.Type != "vehicle" || u.About.Name != "2019 Honda Odyssey" {
		t.Errorf("about vehicle = %+v", u.About)
	}
	if n := count(t, pool, `SELECT count(*) FROM guides WHERE id = $1 AND property_id IS NULL AND asset_id IS NULL AND vehicle_id = $2`, d.ID, f.van); n != 1 {
		t.Error("vehicle guide kept a property or asset")
	}
	u = w.update.run(t, f.ctx, map[string]any{"guide_id": d.ID, "about_type": "family"}).guide(t)
	if u.About.Type != "family" || u.About.ID != nil {
		t.Errorf("about family = %+v", u.About)
	}
	u = w.update.run(t, f.ctx, map[string]any{"guide_id": d.ID, "about_type": "property", "about_id": "lake house"}).guide(t)
	if u.About.Type != "property" || u.About.Name != "Lake House" {
		t.Errorf("about property = %+v", u.About)
	}

	// guide_get by id and by title.
	e = w.get.run(t, f.ctx, map[string]any{"guide_id": "HVAC FILTER REPLACEMENT"})
	g := e.guide(t)
	if g.Body != newBody || g.RevisionCount != 6 || len(g.RecentRevisions) != recentRevisions || g.RecentRevisions[0].BodyLength != utf8.RuneCountInString(newBody) {
		t.Errorf("get = %d revisions %+v", g.RevisionCount, g.RecentRevisions)
	}
	if !strings.Contains(e.Next[0], "maintenance_create") || !strings.Contains(e.Next[0], "property_id="+f.lake.String()) {
		t.Errorf("get next = %v", e.Next)
	}

	// Errors.
	w.update.run(t, f.ctx, map[string]any{"guide_id": d.ID}).wantError(t, "", "No fields")
	w.update.run(t, f.ctx, map[string]any{"guide_id": d.ID, "body": nil}).wantError(t, "body", "cannot be cleared")
	w.update.run(t, f.ctx, map[string]any{"guide_id": d.ID, "about_type": "pet", "about_id": "Rex"}).wantError(t, "about_type", "can't be about a pet")
	w.update.run(t, f.ctx, map[string]any{"guide_id": d.ID, "about_type": "asset"}).wantError(t, "about_id", "required")
	w.update.run(t, f.ctx, map[string]any{"guide_id": d.ID, "about_id": "Dishwasher"}).wantError(t, "about_id", "Furnace")
	w.update.run(t, f.ctx, map[string]any{"guide_id": "Nope", "body": "x"}).wantError(t, "guide_id", "No guide titled")
	w.get.run(t, f.ctx, map[string]any{"guide_id": uuid.NewString()}).wantError(t, "", "No guide")
	w.create.run(t, f.ctx, map[string]any{"title": "No steps"}).wantError(t, "body", "body is required")
	w.create.run(t, f.ctx, map[string]any{"title": "Too many", "body": "x", "tags": manyTags(31)}).wantError(t, "tags", "")
	w.create.run(t, f.ctx, map[string]any{"title": "Big", "body": strings.Repeat("x", maxBody+1)}).wantError(t, "body", "256 KiB")
}

// TestGuideConcurrentUpdates: racing saves each write a revision, and the
// newest revision equals the saved body (row lock + clock_timestamp()).
func TestGuideConcurrentUpdates(t *testing.T) {
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool)
	w := newTools(pool, nil)
	d := w.create.run(t, f.ctx, routerGuide).guide(t)

	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var req mcp.CallToolRequest
			req.Params.Arguments = map[string]any{"guide_id": d.ID, "body": strings.Repeat("step ", i+1)}
			if res, err := w.update.h(f.ctx, req); err != nil || res.IsError {
				t.Errorf("update %d failed: %v %+v", i, err, res)
			}
		}()
	}
	wg.Wait()
	if n := count(t, pool, `SELECT count(*) FROM guide_revisions WHERE guide_id = $1`, d.ID); n != 13 {
		t.Errorf("%d revisions, want 13", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM guides g WHERE g.id = $1 AND g.body = (SELECT r.body FROM guide_revisions r
		WHERE r.guide_id = g.id ORDER BY r.created_at DESC, r.id DESC LIMIT 1)`, d.ID); n != 1 {
		t.Error("newest revision is not the saved body")
	}
}

// TestGuideCreateLoopGuard: the same title within 10 minutes returns the
// first guide (duplicate:true), also under parallel calls.
func TestGuideCreateLoopGuard(t *testing.T) {
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool)
	w := newTools(pool, nil)

	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var req mcp.CallToolRequest
			req.Params.Arguments = dogGuide
			if res, err := w.create.h(f.ctx, req); err != nil || res.IsError {
				t.Errorf("create failed: %v %+v", err, res)
			}
		}()
	}
	wg.Wait()
	if n := count(t, pool, `SELECT count(*) FROM guides WHERE household_id = $1`, f.hh.HouseholdID); n != 1 {
		t.Fatalf("%d guides after parallel creates, want 1", n)
	}
	e := w.create.run(t, f.ctx, map[string]any{"title": "dog care for the SITTER", "body": "other words"})
	d := e.guide(t)
	if !d.Duplicate || !strings.Contains(e.Next[0], "guide_update") {
		t.Errorf("repeat create = %+v / %v", d, e.Next)
	}
	if n := count(t, pool, `SELECT count(*) FROM guide_revisions r JOIN guides g ON g.id = r.guide_id WHERE g.household_id = $1`, f.hh.HouseholdID); n != 1 {
		t.Errorf("%d revisions, want 1", n)
	}
	// Outside the window it is a new guide, and the hint says to use ids.
	if _, err := pool.Exec(context.Background(), `UPDATE guides SET created_at = now() - interval '11 minutes'`); err != nil {
		t.Fatal(err)
	}
	e = w.create.run(t, f.ctx, dogGuide)
	if e.guide(t).Duplicate || !strings.Contains(strings.Join(e.Next, " "), "pass guide ids") {
		t.Errorf("create after window = %s %v", e.Summary, e.Next)
	}
}

// fakeIndex records documents and answers searches with fixed ids.
type fakeIndex struct {
	mu      sync.Mutex
	docs    map[string]search.Document
	removed []string
	hits    []uuid.UUID
	err     error
	last    struct {
		query      string
		household  uuid.UUID
		propertyID *uuid.UUID
	}
}

func (f *fakeIndex) index(_ context.Context, d search.Document) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.docs == nil {
		f.docs = map[string]search.Document{}
	}
	f.docs[d.ID] = d
	return nil
}

func (f *fakeIndex) remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.docs, id)
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeIndex) search(_ context.Context, hid uuid.UUID, q string, p *uuid.UUID, _ int) ([]uuid.UUID, error) {
	f.last.query, f.last.household, f.last.propertyID = q, hid, p
	return f.hits, f.err
}

// TestGuideIndexing: saves upsert the API's document shape (guide-<id>,
// summary first in the body, tags, derived property_id), delete removes it,
// and find keeps the index's order, drops ids that are gone or another
// family's, then adds word matches; a failing index falls back to words.
func TestGuideIndexing(t *testing.T) {
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool)
	idx := &fakeIndex{}
	w := newTools(pool, idx)

	hvac := w.create.run(t, f.ctx, hvacGuide).guide(t)
	router := w.create.run(t, f.ctx, routerGuide).guide(t)
	oil := w.create.run(t, f.ctx, oilGuide).guide(t)
	doc, ok := idx.docs["guide-"+hvac.ID]
	if !ok {
		t.Fatalf("hvac guide not indexed: %v", idx.docs)
	}
	wantBody := hvacGuide["summary"].(string) + "\n\n" + hvacGuide["body"].(string)
	if doc.EntityType != "guide" || doc.EntityID != hvac.ID || doc.HouseholdID != f.hh.HouseholdID.String() || doc.Title != "HVAC maintenance" ||
		doc.Body != wantBody || strings.Join(doc.Tags, ",") != "hvac,furnace" || doc.PropertyID == nil || *doc.PropertyID != f.home.String() || doc.UpdatedAt == 0 {
		t.Errorf("document = %+v", doc)
	}
	if d := idx.docs["guide-"+oil.ID]; d.PropertyID != nil || d.Body != oilGuide["body"] {
		t.Errorf("vehicle guide document = %+v", d)
	}
	w.update.run(t, f.ctx, map[string]any{"guide_id": router.ID, "title": "Reset the wifi router"})
	if idx.docs["guide-"+router.ID].Title != "Reset the wifi router" {
		t.Error("update did not re-index")
	}

	// The index's order wins; unknown and other-family ids are dropped; word
	// matches follow.
	other, _ := testutil.SeedHousehold(t, pool)
	otherGuide := newTools(pool, nil).create.run(t, other, map[string]any{"title": "Their filter guide", "body": "filter"}).guide(t)
	idx.hits = []uuid.UUID{uuid.MustParse(oil.ID), uuid.New(), uuid.MustParse(otherGuide.ID), uuid.MustParse(hvac.ID)}
	e := w.find.run(t, f.ctx, map[string]any{"query": "How do I replace the air filter?"})
	if strings.Join(e.titles(), "|") != "Change the oil in the van|HVAC maintenance" || !strings.Contains(e.Summary, "by meaning") {
		t.Errorf("find = %v / %s", e.titles(), e.Summary)
	}
	if idx.last.household != f.hh.HouseholdID || idx.last.propertyID != nil {
		t.Errorf("search scoped to %v / %v", idx.last.household, idx.last.propertyID)
	}
	idx.hits = []uuid.UUID{uuid.MustParse(router.ID)}
	e = w.find.run(t, f.ctx, map[string]any{"query": "filter"})
	if strings.Join(e.titles(), "|") != "Reset the wifi router|Change the oil in the van|HVAC maintenance" {
		t.Errorf("semantic then words = %v", e.titles())
	}
	// about property: the index is asked for the property, SQL keeps it.
	e = w.find.run(t, f.ctx, map[string]any{"query": "filter", "about_type": "property", "about_id": "Home"})
	if idx.last.propertyID == nil || *idx.last.propertyID != f.home || strings.Join(e.titles(), "|") != "HVAC maintenance" {
		t.Errorf("about Home = %v (property %v)", e.titles(), idx.last.propertyID)
	}

	idx.err = errors.New("typesense down")
	e = w.find.run(t, f.ctx, map[string]any{"query": "How do I replace the air filter?"})
	if !e.OK || len(e.Items) == 0 || e.Items[0].Title != "HVAC maintenance" || strings.Contains(e.Summary, "by meaning") {
		t.Errorf("fallback find = %v / %s", e.titles(), e.Summary)
	}

	w.del.run(t, f.ctx, map[string]any{"guide_id": hvac.ID})
	if _, ok := idx.docs["guide-"+hvac.ID]; ok || len(idx.removed) != 1 {
		t.Errorf("delete did not unindex: %v", idx.removed)
	}
}

// TestGuideFindWords: without Typesense, the SQL word match finds the HVAC
// guide for the question; filters and paging work.
func TestGuideFindWords(t *testing.T) {
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool)
	w := newTools(pool, nil)

	w.find.run(t, f.ctx, map[string]any{}).wantNoItems(t, "No guides yet.")
	for _, g := range []map[string]any{hvacGuide, routerGuide, oilGuide, dogGuide} {
		w.create.run(t, f.ctx, g)
	}
	e := w.find.run(t, f.ctx, map[string]any{"query": "How do I replace the air filter?"})
	if len(e.Items) == 0 || e.Items[0].Title != "HVAC maintenance" {
		t.Fatalf("find = %v", e.titles())
	}
	if !strings.Contains(e.Next[0], "guide_get guide_id="+e.Items[0].ID) || !strings.Contains(e.Next[1], "asset_id="+f.furnace.String()) {
		t.Errorf("next = %v", e.Next)
	}
	if e.Items[0].Summary != hvacGuide["summary"] {
		t.Errorf("summary = %q", e.Items[0].Summary)
	}
	e = w.find.run(t, f.ctx, map[string]any{})
	if strings.Join(e.titles(), "|") != "Change the oil in the van|Dog care for the sitter|HVAC maintenance|Reset the router" {
		t.Errorf("list = %v", e.titles())
	}
	if e.Items[3].Summary == "" || !strings.HasPrefix(e.Items[3].Summary, "When the wifi drops") {
		t.Errorf("excerpt = %q", e.Items[3].Summary)
	}
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"about_type": "property", "about_id": "home"}, "HVAC maintenance"},
		{map[string]any{"about_id": "Furnace"}, "HVAC maintenance"},
		{map[string]any{"about_type": "car", "about_id": "odyssey"}, "Change the oil in the van"},
		{map[string]any{"about_type": "family"}, "Dog care for the sitter|Reset the router"},
		{map[string]any{"tag": "WIFI"}, "Reset the router"},
		{map[string]any{"query": "wifi", "about_type": "family"}, "Reset the router"},
		{map[string]any{"query": "bake bread"}, ""},
	} {
		if got := strings.Join(w.find.run(t, f.ctx, tc.args).titles(), "|"); got != tc.want {
			t.Errorf("find %v = %q, want %q", tc.args, got, tc.want)
		}
	}
	e = w.find.run(t, f.ctx, map[string]any{"limit": 3})
	if len(e.Items) != 3 || e.NextCursor == "" || e.Total != 4 {
		t.Fatalf("page 1 = %v %q %d", e.titles(), e.NextCursor, e.Total)
	}
	e = w.find.run(t, f.ctx, map[string]any{"limit": 3, "cursor": e.NextCursor})
	if strings.Join(e.titles(), "|") != "Reset the router" || e.NextCursor != "" {
		t.Errorf("page 2 = %v %q", e.titles(), e.NextCursor)
	}
	w.find.run(t, f.ctx, map[string]any{"cursor": "bogus!"}).wantError(t, "cursor", "")
	w.find.run(t, f.ctx, map[string]any{"about_type": "room", "about_id": "Kitchen"}).wantError(t, "about_type", "room")
}

func (e env) wantNoItems(t *testing.T, summary string) {
	t.Helper()
	if !e.OK || len(e.Items) != 0 || e.Summary != summary {
		t.Errorf("want no items and %q, got %v / %s", summary, e.titles(), e.Summary)
	}
}

// TestGuideRoles follows roles.go ResGuides: family roles write; viewer and
// housesitter read (a housesitter without the car's name); vendor reads
// nothing; a demoted member is refused at once; a removed one is not signed
// in.
func TestGuideRoles(t *testing.T) {
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool)
	w := newTools(pool, nil)
	oil := w.create.run(t, f.ctx, oilGuide).guide(t)

	// Next hints name only calls the role can make (#4771): family roles get
	// maintenance_create with the subject param and guide_update; read-only
	// roles get neither, nor guide_create on an empty result.
	writeHint := func(next []string) string {
		for _, n := range next {
			for _, w := range []string{"maintenance_create", "guide_create", "guide_update", "guide_delete"} {
				if strings.Contains(n, w) {
					return n
				}
			}
		}
		return ""
	}
	vanParam := "vehicle_id=" + f.van.String()
	for _, role := range []string{"family_manager", "family_member"} {
		ctx, _ := memberCtx(t, pool, f.hh, role)
		d := w.create.run(t, ctx, map[string]any{"title": "Guide by " + role, "body": "steps"}).guide(t)
		w.update.run(t, ctx, map[string]any{"guide_id": d.ID, "body": "more steps"}).guide(t)
		if e := w.get.run(t, ctx, map[string]any{"guide_id": oil.ID}); !slices.ContainsFunc(e.Next, func(n string) bool {
			return strings.Contains(n, "maintenance_create") && strings.Contains(n, vanParam)
		}) || !slices.ContainsFunc(e.Next, func(n string) bool { return strings.Contains(n, "guide_update") }) {
			t.Errorf("%s guide_get next = %q, want maintenance_create %s and guide_update", role, e.Next, vanParam)
		}
		if e := w.find.run(t, ctx, map[string]any{"about_type": "vehicle", "about_id": f.van.String()}); !slices.ContainsFunc(e.Next, func(n string) bool {
			return strings.Contains(n, "maintenance_create") && strings.Contains(n, vanParam)
		}) {
			t.Errorf("%s guide_find next = %q, want maintenance_create %s", role, e.Next, vanParam)
		}
		if e := w.find.run(t, ctx, map[string]any{"query": "zzqx nothing matches"}); !slices.ContainsFunc(e.Next, func(n string) bool { return strings.Contains(n, "guide_create") }) {
			t.Errorf("%s empty guide_find next = %q, want guide_create", role, e.Next)
		}
	}
	for _, role := range []string{"viewer", "housesitter"} {
		ctx, _ := memberCtx(t, pool, f.hh, role)
		if e := w.find.run(t, ctx, map[string]any{}); !e.OK || len(e.Items) != 3 {
			t.Errorf("%s find = %v / %s", role, e.titles(), e.Summary)
		}
		for _, args := range []map[string]any{{"about_type": "vehicle"}, {"query": "oil"}, {"query": "zzqx nothing matches"}} {
			if e := w.find.run(t, ctx, args); writeHint(e.Next) != "" {
				t.Errorf("%s guide_find %v suggests a write tool: %q", role, args, writeHint(e.Next))
			}
		}
		if e := w.get.run(t, ctx, map[string]any{"guide_id": oil.ID}); writeHint(e.Next) != "" {
			t.Errorf("%s guide_get suggests a write tool: %q", role, writeHint(e.Next))
		}
		g := w.get.run(t, ctx, map[string]any{"guide_id": oil.ID}).guide(t)
		if wantName := map[string]string{"viewer": "2019 Honda Odyssey", "housesitter": ""}[role]; g.About.Name != wantName || g.About.Type != "vehicle" {
			t.Errorf("%s sees about = %+v", role, g.About)
		}
		for _, c := range []struct {
			w    tool
			args map[string]any
		}{
			{w.create, map[string]any{"title": "x", "body": "y"}},
			{w.update, map[string]any{"guide_id": oil.ID, "body": "y"}},
			{w.del, map[string]any{"guide_id": oil.ID}},
		} {
			e := c.w.run(t, ctx, c.args)
			if e.OK || !strings.Contains(e.Error.Message, "The "+role+" role can't change guides") {
				t.Errorf("%s %s = %+v", role, c.w.def.Name, e)
			}
		}
	}
	// A housesitter can't read vehicles, so guide_find's about_id never
	// matches, lists or confirms a vehicle by name or VIN (#4769).
	if _, err := pool.Exec(context.Background(), `UPDATE vehicles SET vin = '5FNRL6H78KB000001' WHERE id = $1`, f.van); err != nil {
		t.Fatal(err)
	}
	insertID(t, pool, `INSERT INTO vehicles (household_id, year, make, model) VALUES ($1, 2021, 'Toyota', 'Sienna') RETURNING id`, f.hh.HouseholdID)
	sctx, _ := memberCtx(t, pool, f.hh, "housesitter")
	// leaks reports a vehicle name or VIN in e. The caller's own about_id
	// may be echoed back; that tells them nothing.
	leaks := func(t *testing.T, what string, e env, echo ...string) {
		t.Helper()
		raw := e.raw
		for _, s := range echo {
			raw = strings.ReplaceAll(raw, s, "")
		}
		for _, secret := range []string{"Honda", "Odyssey", "Toyota", "Sienna", "5FNRL6H78KB000001", "2019", "2021"} {
			if strings.Contains(raw, secret) {
				t.Errorf("housesitter %s leaks %q: %s", what, secret, e.raw)
			}
		}
	}
	for _, args := range []map[string]any{
		{"about_id": "nope"},
		{"about_id": "Honda Odyssey"},
		{"about_id": "Odyssey"},
		{"about_id": "5FNRL6H78KB000001"},
		{"about_type": "vehicle"},
	} {
		e := w.find.run(t, sctx, args)
		if e.OK {
			t.Errorf("housesitter find %v matched: %s", args, e.raw)
		}
		echo, _ := args["about_id"].(string)
		leaks(t, fmt.Sprint(args), e, echo)
	}
	// By name: refused the same way whether the vehicle exists or not.
	hit := w.find.run(t, sctx, map[string]any{"about_type": "vehicle", "about_id": "Honda Odyssey"})
	miss := w.find.run(t, sctx, map[string]any{"about_type": "car", "about_id": "Ford Pinto"})
	hit.wantError(t, "about_id", "You can't look vehicles up by name")
	if hit.raw != miss.raw {
		t.Errorf("vehicle name hit and miss differ:\n%s\n%s", hit.raw, miss.raw)
	}
	leaks(t, "vehicle by name", hit, "Honda Odyssey")
	vin := w.find.run(t, sctx, map[string]any{"about_type": "vehicle", "about_id": "5fnrl6h78kb000001"})
	if vin.raw != miss.raw {
		t.Errorf("VIN probe differs from a miss: %s", vin.raw)
	}
	// By id: the family's vehicle works, with no name.
	for _, args := range []map[string]any{
		{"about_type": "vehicle", "about_id": f.van.String()},
		{"about_id": f.van.String()},
	} {
		e := w.find.run(t, sctx, args)
		if !e.OK || len(e.Items) != 1 || e.Items[0].ID != oil.ID {
			t.Errorf("housesitter find %v = %v / %s", args, e.titles(), e.raw)
		}
		leaks(t, "find by vehicle id", e)
	}
	// Another family's vehicle id and a made-up id get the same answer.
	_, ohh := testutil.SeedHousehold(t, pool)
	foreign := insertID(t, pool, `INSERT INTO vehicles (household_id, year, make, model) VALUES ($1, 2020, 'Ford', 'Transit') RETURNING id`, ohh.HouseholdID)
	made := uuid.New()
	fe := w.find.run(t, sctx, map[string]any{"about_type": "vehicle", "about_id": foreign.String()})
	me := w.find.run(t, sctx, map[string]any{"about_type": "vehicle", "about_id": made.String()})
	fe.wantError(t, "about_id", "No vehicle")
	if strings.ReplaceAll(fe.raw, foreign.String(), "ID") != strings.ReplaceAll(me.raw, made.String(), "ID") {
		t.Errorf("foreign and missing vehicle ids differ:\n%s\n%s", fe.raw, me.raw)
	}
	if strings.Contains(fe.raw, "Transit") {
		t.Errorf("foreign vehicle leaks: %s", fe.raw)
	}
	// Properties and assets still resolve by name for a housesitter.
	if e := w.find.run(t, sctx, map[string]any{"about_id": "furnace"}); !e.OK {
		t.Errorf("housesitter find about furnace = %s", e.raw)
	}

	// Owner and viewer are unchanged: names, VINs and candidate lists.
	vwctx, _ := memberCtx(t, pool, f.hh, "viewer")
	for _, ctx := range []context.Context{f.ctx, vwctx} {
		if e := w.find.run(t, ctx, map[string]any{"about_type": "vehicle", "about_id": "5FNRL6H78KB000001"}); !e.OK || len(e.Items) != 1 {
			t.Errorf("find by VIN = %s", e.raw)
		}
		w.find.run(t, ctx, map[string]any{"about_id": "nope"}).wantError(t, "about_id", "vehicle 2019 Honda Odyssey")
	}

	vctx, _ := memberCtx(t, pool, f.hh, "vendor")
	for _, c := range []struct {
		w    tool
		args map[string]any
	}{{w.find, map[string]any{}}, {w.get, map[string]any{"guide_id": oil.ID}}} {
		if e := c.w.run(t, vctx, c.args); e.OK || !strings.Contains(e.Error.Message, "vendor role can't read guides") {
			t.Errorf("vendor %s = %+v", c.w.def.Name, e)
		}
	}

	mctx, uid := memberCtx(t, pool, f.hh, "family_member")
	if _, err := pool.Exec(context.Background(), `UPDATE memberships SET role = 'viewer' WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if e := w.update.run(t, mctx, map[string]any{"guide_id": oil.ID, "body": "demoted"}); e.OK {
		t.Error("demoted member updated a guide")
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM memberships WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if e := w.find.run(t, mctx, map[string]any{}); e.OK || !strings.Contains(e.Error.Message, "not signed in") {
		t.Errorf("removed member find = %+v", e)
	}

	// Another family's guide, asset or vehicle is not found.
	octx, _ := testutil.SeedHousehold(t, pool)
	w.get.run(t, octx, map[string]any{"guide_id": oil.ID}).wantError(t, "", "No guide")
	w.update.run(t, octx, map[string]any{"guide_id": oil.ID, "body": "x"}).wantError(t, "", "No guide")
	w.create.run(t, octx, map[string]any{"title": "x", "body": "y", "about_id": f.furnace.String()}).wantError(t, "about_id", "No property, asset or vehicle")
	w.del.run(t, octx, map[string]any{"guide_id": oil.ID}).wantError(t, "", "No guide")
}

// TestGuideDelete: revisions go with the guide; notes about it need
// confirm:true; documents and lists about it move to the family.
func TestGuideDelete(t *testing.T) {
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool)
	w := newTools(pool, nil)
	bg := context.Background()

	plain := w.create.run(t, f.ctx, routerGuide).guide(t)
	w.update.run(t, f.ctx, map[string]any{"guide_id": plain.ID, "body": "v2"})
	e := w.del.run(t, f.ctx, map[string]any{"guide_id": "reset the router"})
	var d deleteData
	_ = json.Unmarshal(e.Data, &d)
	if !e.OK || !d.Deleted || d.RevisionsDeleted != 2 || d.NotesDeleted != 0 {
		t.Errorf("delete = %+v / %s", d, e.Summary)
	}
	if n := count(t, pool, `SELECT count(*) FROM guide_revisions WHERE guide_id = $1`, plain.ID); n != 0 {
		t.Errorf("%d revisions left", n)
	}
	w.del.run(t, f.ctx, map[string]any{"guide_id": plain.ID}).wantError(t, "", "No guide")

	g := w.create.run(t, f.ctx, hvacGuide).guide(t)
	if _, err := pool.Exec(bg, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'guide', $2, 'Filter size', '16x25x1')`,
		f.hh.HouseholdID, g.ID); err != nil {
		t.Fatal(err)
	}
	listID := insertID(t, pool, `INSERT INTO lists (household_id, name, kind, entity_type, entity_id) VALUES ($1, 'Filter parts', 'shopping', 'guide', $2) RETURNING id`,
		f.hh.HouseholdID, g.ID)
	w.del.run(t, f.ctx, map[string]any{"guide_id": g.ID}).wantError(t, "confirm", "1 note")
	e = w.del.run(t, f.ctx, map[string]any{"guide_id": g.ID, "confirm": true})
	_ = json.Unmarshal(e.Data, &d)
	if !e.OK || d.NotesDeleted != 1 || d.ListsMoved != 1 {
		t.Errorf("confirmed delete = %+v / %s", d, e.Summary)
	}
	if n := count(t, pool, `SELECT count(*) FROM lists WHERE id = $1 AND entity_type = 'household'`, listID); n != 1 {
		t.Error("list about the guide was not moved to the family")
	}
	if n := count(t, pool, `SELECT count(*) FROM notes WHERE entity_type = 'guide'`); n != 0 {
		t.Error("note about the guide survived")
	}
}

// TestTypesenseRequest pins the search request: semantic only (query_by is
// the embedding field), household and entity_type scoped, the distance
// threshold, and the property filter.
func TestTypesenseRequest(t *testing.T) {
	hid, pid, gid := uuid.New(), uuid.New(), uuid.New()
	var got http.Header
	var gotQuery map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotQuery = r.Header, r.URL.Query()
		if r.URL.Path != "/collections/household_search/documents/search" {
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"hits":[{"document":{"entity_id":"` + gid.String() + `"}},{"document":{"entity_id":"junk"}}]}`))
	}))
	defer srv.Close()
	ts := &typesense{baseURL: srv.URL, apiKey: "secret-key", client: srv.Client()}
	ids, err := ts.search(context.Background(), hid, "air filter", &pid, 100)
	if err != nil || len(ids) != 1 || ids[0] != gid {
		t.Fatalf("search = %v, %v", ids, err)
	}
	q := func(k string) string { return strings.Join(gotQuery[k], ",") }
	if got.Get("X-TYPESENSE-API-KEY") != "secret-key" || q("q") != "air filter" || q("query_by") != "embedding" ||
		q("vector_query") != "embedding:([], distance_threshold: 0.75)" || q("per_page") != "100" ||
		q("filter_by") != "household_id:=`"+hid.String()+"` && entity_type:=`guide` && property_id:=`"+pid.String()+"`" {
		t.Errorf("request = %v", gotQuery)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Could not find a field named embedding"}`, http.StatusBadRequest)
	}))
	defer failing.Close()
	ts = &typesense{baseURL: failing.URL, apiKey: "k", client: failing.Client()}
	if _, err := ts.search(context.Background(), hid, "x", nil, 10); err == nil {
		t.Error("want an error from a collection without embeddings")
	}
	if newTypesense("ts", "8108", "") != nil {
		t.Error("no API key must mean no index")
	}
}

// TestGuideFindByMeaning runs against a real Typesense (30.2, local
// all-MiniLM-L12-v2 model), as the API's search tests do:
//
//	docker run -d --name ts-test --tmpfs /data -p 127.0.0.1::8108 typesense/typesense:30.2 --data-dir /data --api-key=k
//	TYPESENSE_TEST_ADDR=127.0.0.1:<port> TYPESENSE_TEST_KEY=k go test ./internal/guides -run ByMeaning
//
// "How do I replace the air filter?" finds the HVAC guide first, and "the
// internet is down" finds "Reset the router", which shares no word with it
// (the SQL word match alone finds nothing).
func TestGuideFindByMeaning(t *testing.T) {
	addr, key := os.Getenv("TYPESENSE_TEST_ADDR"), os.Getenv("TYPESENSE_TEST_KEY")
	if addr == "" {
		t.Skip("TYPESENSE_TEST_ADDR not set")
	}
	host, port, _ := strings.Cut(addr, ":")
	ts := newTypesense(host, port, key)
	ensureCollection(t, ts)

	pool := testutil.NewPool(t)
	f := seedFamily(t, pool)
	w := newTools(pool, ts)
	for _, g := range []map[string]any{hvacGuide, routerGuide, oilGuide, dogGuide} {
		w.create.run(t, f.ctx, g)
	}

	e := w.find.run(t, f.ctx, map[string]any{"query": "How do I replace the air filter?"})
	if len(e.Items) == 0 || e.Items[0].Title != "HVAC maintenance" || !strings.Contains(e.Summary, "by meaning") {
		t.Fatalf("air filter = %v / %s", e.titles(), e.Summary)
	}
	e = w.find.run(t, f.ctx, map[string]any{"query": "the internet is down"})
	if len(e.Items) == 0 || e.Items[0].Title != "Reset the router" {
		t.Errorf("internet is down = %v / %s", e.titles(), e.Summary)
	}
	if words := newTools(pool, nil).find.run(t, f.ctx, map[string]any{"query": "the internet is down"}); len(words.Items) != 0 {
		t.Errorf("word match alone found %v; the test no longer proves meaning", words.titles())
	}
	e = w.find.run(t, f.ctx, map[string]any{"query": "how do I look after the dog"})
	if len(e.Items) == 0 || e.Items[0].Title != "Dog care for the sitter" {
		t.Errorf("dog = %v", e.titles())
	}
	// Scoped to the property (and its assets) in Typesense and SQL.
	e = w.find.run(t, f.ctx, map[string]any{"query": "How do I replace the air filter?", "about_type": "property", "about_id": "Home"})
	if strings.Join(e.titles(), "|") != "HVAC maintenance" {
		t.Errorf("about Home = %v", e.titles())
	}
	// Another family sees none of it.
	octx, _ := testutil.SeedHousehold(t, pool)
	if e := w.find.run(t, octx, map[string]any{"query": "How do I replace the air filter?"}); len(e.Items) != 0 {
		t.Errorf("other family found %v", e.titles())
	}

	// Update re-indexes: the router guide, renamed and rewritten, is found
	// by its new meaning; delete removes it from the index.
	router := w.find.run(t, f.ctx, map[string]any{"query": "router"}).Items[0]
	w.update.run(t, f.ctx, map[string]any{"guide_id": router.ID, "title": "Water heater pilot light",
		"body": "Turn the gas knob to pilot, hold it down and press the igniter until the flame stays lit."})
	e = w.find.run(t, f.ctx, map[string]any{"query": "no hot water in the shower"})
	if len(e.Items) == 0 || e.Items[0].ID != router.ID {
		t.Errorf("after update = %v", e.titles())
	}
	w.del.run(t, f.ctx, map[string]any{"guide_id": router.ID})
	ids, err := ts.search(context.Background(), f.hh.HouseholdID, "no hot water in the shower", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id.String() == router.ID {
			t.Error("deleted guide is still in the index")
		}
	}
}

// ensureCollection creates household_search with the API's schema
// (apps/api/internal/search/typesense.go householdSchema) when it is
// missing. The first create downloads the embedding model.
func ensureCollection(t *testing.T, ts *typesense) {
	t.Helper()
	schema := map[string]any{
		"name": collectionName,
		"fields": []map[string]any{
			{"name": "id", "type": "string"},
			{"name": "household_id", "type": "string", "facet": true},
			{"name": "entity_type", "type": "string", "facet": true},
			{"name": "entity_id", "type": "string"},
			{"name": "title", "type": "string"},
			{"name": "body", "type": "string"},
			{"name": "tags", "type": "string[]", "facet": true},
			{"name": "property_id", "type": "string", "optional": true, "facet": true},
			{"name": "created_at", "type": "int64"},
			{"name": "updated_at", "type": "int64"},
			{"name": "embedding", "type": "float[]", "embed": map[string]any{
				"from": []string{"title", "body"}, "model_config": map[string]any{"model_name": "ts/all-MiniLM-L12-v2"}}},
		},
		"default_sorting_field": "updated_at",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	slow := &typesense{baseURL: ts.baseURL, apiKey: ts.apiKey, client: &http.Client{Timeout: 5 * time.Minute}}
	err := slow.do(ctx, http.MethodPost, "/collections", schema, nil)
	var se *statusError
	if err != nil && !(errors.As(err, &se) && se.code == http.StatusConflict) {
		t.Fatalf("create collection: %v", err)
	}
}

func manyTags(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "tag" + strconv.Itoa(i)
	}
	return out
}

// TestQueryWords pins the SQL word match's tokenizer.
func TestQueryWords(t *testing.T) {
	for q, want := range map[string]string{
		"How do I replace the air filter?": "replace,air,filter",
		"Filters, FILTERS & filter's":      "filter",
		"the of a":                         "",
		"glass windows":                    "glass,window",
	} {
		if got := strings.Join(queryWords(q), ","); got != want {
			t.Errorf("queryWords(%q) = %q, want %q", q, got, want)
		}
	}
}
