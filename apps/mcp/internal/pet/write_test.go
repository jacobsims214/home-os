package pet

import (
	"context"
	"encoding/json"
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

// writeEnv is the JSON a pet write tool returns.
type writeEnv struct {
	OK      bool            `json:"ok"`
	Summary string          `json:"summary"`
	Data    json.RawMessage `json:"data"`
	Next    []string        `json:"next"`
	Error   *struct {
		Message string         `json:"message"`
		Fix     string         `json:"fix"`
		Field   string         `json:"field"`
		Example map[string]any `json:"example"`
	} `json:"error"`
}

func (e writeEnv) pet(t *testing.T) petData {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d petData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return d
}

func (e writeEnv) wantError(t *testing.T, field string) {
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

// writeTool is one write tool under test: its definition and handler.
type writeTool struct {
	tool mcp.Tool
	h    toolHandler
}

func newWriteTools(pool *pgxpool.Pool) (create, update, del writeTool) {
	_, ct, ch := NewPetCreateTool(pool)
	_, ut, uh := NewPetUpdateTool(pool)
	_, dt, dh := NewPetDeleteTool(pool)
	return writeTool{ct, ch}, writeTool{ut, uh}, writeTool{dt, dh}
}

func resolvedSchema(t *testing.T, tool mcp.Tool) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || len(raw.OutputSchema) == 0 {
		t.Fatalf("%s declares no outputSchema (%v)", tool.Name, err)
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

// run calls the tool and checks the respond invariants: one text block equal
// to structuredContent, valid against the tool's outputSchema, ok ==
// !isError.
func (w writeTool) run(t *testing.T, ctx context.Context, args map[string]any) writeEnv {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = w.tool.Name
	req.Params.Arguments = args
	res, err := w.h(ctx, req)
	if err != nil {
		t.Fatalf("%s returned a Go error: %v", w.tool.Name, err)
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
	if err := resolvedSchema(t, w.tool).Validate(instance); err != nil {
		t.Fatalf("%s result does not match its outputSchema: %v\n%s", w.tool.Name, err, text)
	}
	var env writeEnv
	if err := json.Unmarshal(sc, &env); err != nil {
		t.Fatal(err)
	}
	if env.OK == res.IsError {
		t.Fatalf("ok=%v but isError=%v", env.OK, res.IsError)
	}
	return env
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v\n%s", err, sql)
	}
	return n
}

// memberCtx adds a member with role to hh and returns their claims context.
func memberCtx(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) (context.Context, uuid.UUID) {
	t.Helper()
	bg := context.Background()
	email := role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(bg, `INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`, email, role).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(bg, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hh.HouseholdID, uid, role); err != nil {
		t.Fatal(err)
	}
	return server.ContextWithClaims(bg, &auth.Claims{
		Identity: auth.Identity{Subject: uid.String(), Email: email}, UserID: uid.String(),
		HouseholdID: hh.HouseholdID.String(), Role: role,
	}), uid
}

// TestWriteToolDefinitions pins the write tools to the catalog: names,
// required params, descriptions, all four hints and an outputSchema.
func TestWriteToolDefinitions(t *testing.T) {
	create, update, del := newWriteTools(nil)
	for _, tc := range []struct {
		w                      writeTool
		desc, title            string
		required               []string
		params                 []string
		readOnly, destr, idemp bool
	}{
		{create, "Add a pet. Link the vet with vet_contact_id (a contact with trade vet).", "Add pet",
			[]string{"name", "species"},
			[]string{"name", "species", "breed", "date_of_birth", "vet_contact_id", "vet_name", "vet_phone", "notes"},
			false, false, false},
		{update, "Change a pet; only the given fields change.", "Update pet",
			[]string{"pet_id"},
			[]string{"pet_id", "name", "species", "breed", "date_of_birth", "vet_contact_id", "vet_name", "vet_phone", "notes"},
			false, false, true},
		{del, "Delete a pet. Its documents move to the family; its notes are deleted (needs confirm:true if it has any).", "Delete pet",
			[]string{"pet_id"}, []string{"pet_id", "confirm"}, false, true, true},
	} {
		tool := tc.w.tool
		if tool.Description != tc.desc || len(tool.Description) > 200 {
			t.Errorf("%s description = %q", tool.Name, tool.Description)
		}
		if strings.Join(tool.InputSchema.Required, ",") != strings.Join(tc.required, ",") {
			t.Errorf("%s required = %v, want %v", tool.Name, tool.InputSchema.Required, tc.required)
		}
		if len(tool.InputSchema.Properties) != len(tc.params) {
			t.Errorf("%s has %d params, want %v", tool.Name, len(tool.InputSchema.Properties), tc.params)
		}
		for _, p := range tc.params {
			schema, ok := tool.InputSchema.Properties[p]
			if !ok {
				t.Errorf("%s lacks param %q", tool.Name, p)
				continue
			}
			if d, _ := schema.(map[string]any)["description"].(string); d == "" || len(d) > 80 {
				t.Errorf("%s.%s description is %d chars, want 1-80", tool.Name, p, len(d))
			}
		}
		a := tool.Annotations
		if a.Title != tc.title || a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
			t.Fatalf("%s annotations not all explicit: %+v", tool.Name, a)
		}
		if *a.ReadOnlyHint != tc.readOnly || *a.DestructiveHint != tc.destr || *a.IdempotentHint != tc.idemp || *a.OpenWorldHint {
			t.Errorf("%s hints = %+v", tool.Name, a)
		}
		resolvedSchema(t, tool)
	}
}

// TestPetCreate: "Add our dog Biscuit" is one call; the vet is linked by
// vet_name/vet_phone (found or saved as a vet contact) or vet_contact_id;
// the legacy vet columns are never written; the loop guard returns the same
// pet; validation errors point at the field.
func TestPetCreate(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	create, _, _ := newWriteTools(pool)
	bg := context.Background()

	// One call, the two required fields.
	env := create.run(t, ctx, map[string]any{"name": "Biscuit", "species": "dog"})
	b := env.pet(t)
	if b.Name != "Biscuit" || str(b.Species) != "dog" || b.Vet != nil || b.Duplicate || b.VetCreated {
		t.Fatalf("biscuit = %+v", b)
	}
	if env.Summary != "Added Biscuit: dog, no vet saved." {
		t.Errorf("summary = %q", env.Summary)
	}
	nexts := strings.Join(env.Next, " ")
	if len(env.Next) != 3 || !strings.Contains(nexts, "event_create") ||
		!strings.Contains(nexts, "document_upload about_type=pet about_id="+b.ID) || !strings.Contains(nexts, "pet_update pet_id="+b.ID) {
		t.Errorf("next = %v", env.Next)
	}
	var createdBy, hid string
	if err := pool.QueryRow(bg, `SELECT household_id::text, COALESCE(created_by::text, '') FROM pets WHERE id = $1`, b.ID).Scan(&hid, &createdBy); err != nil {
		t.Fatal(err)
	}
	if hid != hh.HouseholdID.String() || createdBy != hh.UserID.String() {
		t.Errorf("household %s created_by %q, want %s / %s", hid, createdBy, hh.HouseholdID, hh.UserID)
	}

	// Loop guard: the same name and species again returns that pet.
	env = create.run(t, ctx, map[string]any{"name": "biscuit", "species": "Dog", "breed": "Beagle"})
	if d := env.pet(t); !d.Duplicate || d.ID != b.ID || d.Breed != nil || !strings.Contains(env.Next[0], "pet_update pet_id="+b.ID) {
		t.Errorf("duplicate = %+v / %v", d, env.Next)
	}
	if n := count(t, pool, `SELECT count(*) FROM pets WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("pets after duplicate = %d", n)
	}
	// The same name with another species is a different pet.
	env = create.run(t, ctx, map[string]any{"name": "Biscuit", "species": "cat"})
	if d := env.pet(t); d.Duplicate || d.ID == b.ID || !strings.Contains(strings.Join(env.Next, " "), "Another pet is also named Biscuit") {
		t.Errorf("second biscuit = %+v / %v", d, env.Next)
	}

	// Every field, with a new vet by name and phone (aliases: birthday, type).
	env = create.run(t, ctx, map[string]any{
		"name": "Luna", "type": "cat", "breed": "Siamese", "birthday": "2020-03-14",
		"vet_name": "Dr. Paws Clinic", "vet_phone": "(555) 010-0100", "notes": "Thyroid pill with breakfast",
	})
	l := env.pet(t)
	if str(l.Species) != "cat" || str(l.Breed) != "Siamese" || str(l.DateOfBirth) != "2020-03-14" || l.Age == nil ||
		str(l.Notes) != "Thyroid pill with breakfast" || !l.VetCreated || l.Vet == nil || l.Vet.Source != vetContact ||
		l.Vet.Name != "Dr. Paws Clinic" || str(l.Vet.Phone) != "(555) 010-0100" {
		t.Fatalf("luna = %+v vet %+v", l, l.Vet)
	}
	if !strings.Contains(env.Summary, "Saved Dr. Paws Clinic as a new vet contact.") {
		t.Errorf("luna summary = %q", env.Summary)
	}
	vetID := *l.Vet.ContactID
	var trades []string
	var specialty string
	if err := pool.QueryRow(bg, `SELECT trades, specialty FROM vendors WHERE id = $1 AND household_id = $2`, vetID, hh.HouseholdID).Scan(&trades, &specialty); err != nil {
		t.Fatal(err)
	}
	if strings.Join(trades, ",") != "vet" || specialty != "Veterinarian" {
		t.Errorf("vet vendor trades %v specialty %q", trades, specialty)
	}

	// The same vet again (other case, same digits) is found, not saved twice.
	env = create.run(t, ctx, map[string]any{"name": "Max", "species": "dog", "vet_name": "dr. paws clinic", "vet_phone": "555-010-0100"})
	if d := env.pet(t); d.VetCreated || d.Vet == nil || str(d.Vet.ContactID) != vetID {
		t.Errorf("max vet = %+v", d.Vet)
	}
	// Only a phone: matched by digits.
	if d := create.run(t, ctx, map[string]any{"name": "Pip", "species": "bird", "vet_phone": "5550100100"}).pet(t); d.VetCreated || str(d.Vet.ContactID) != vetID {
		t.Errorf("pip vet = %+v", d.Vet)
	}
	// A contradicting phone saves a new contact instead of overwriting.
	if d := create.run(t, ctx, map[string]any{"name": "Kiwi", "species": "bird", "vet_name": "Dr. Paws Clinic", "vet_phone": "555-0199"}).pet(t); !d.VetCreated || str(d.Vet.ContactID) == vetID {
		t.Errorf("kiwi vet = %+v", d.Vet)
	}
	// vet_contact_id: a UUID of this family's contact, or a contact's name.
	if d := create.run(t, ctx, map[string]any{"name": "Rex", "species": "dog", "vet_contact_id": vetID}).pet(t); d.VetCreated || str(d.Vet.ContactID) != vetID {
		t.Errorf("rex vet = %+v", d.Vet)
	}
	if d := create.run(t, ctx, map[string]any{"name": "Rio", "species": "dog", "vet_vendor_id": "DR. PAWS CLINIC"}).pet(t); str(d.Vet.ContactID) != vetID {
		t.Errorf("rio vet (name in vet_contact_id) = %+v", d.Vet)
	}
	// vet_contact_id wins over vet_name.
	if d := create.run(t, ctx, map[string]any{"name": "Ace", "species": "dog", "vet_contact_id": vetID, "vet_name": "Someone Else"}).pet(t); str(d.Vet.ContactID) != vetID || d.VetCreated {
		t.Errorf("ace vet = %+v", d.Vet)
	}

	// Another family's contact is not found and nothing is added.
	_, other := testutil.SeedHousehold(t, pool)
	foreignVet := insertID(t, pool, `INSERT INTO vendors (household_id, name, trades) VALUES ($1, 'Other Vet', ARRAY['vet']) RETURNING id`, other.HouseholdID)
	before := count(t, pool, `SELECT count(*) FROM pets WHERE household_id = $1`, hh.HouseholdID)
	create.run(t, ctx, map[string]any{"name": "Zed", "species": "dog", "vet_contact_id": foreignVet}).wantError(t, "vet_contact_id")
	create.run(t, ctx, map[string]any{"name": "Zed", "species": "dog", "vet_contact_id": "1234abcd-12ab"}).wantError(t, "vet_contact_id")
	if n := count(t, pool, `SELECT count(*) FROM pets WHERE household_id = $1`, hh.HouseholdID); n != before {
		t.Errorf("failed creates added pets: %d -> %d", before, n)
	}

	// Validation.
	create.run(t, ctx, map[string]any{"name": "Nope"}).wantError(t, "species")
	create.run(t, ctx, map[string]any{"species": "dog", "name": "  "}).wantError(t, "name")
	create.run(t, ctx, map[string]any{"name": "Nope", "species": "dog", "date_of_birth": "June 2021"}).wantError(t, "date_of_birth")
	future := time.Now().AddDate(0, 0, 3).Format(time.DateOnly)
	create.run(t, ctx, map[string]any{"name": "Nope", "species": "dog", "date_of_birth": future}).wantError(t, "date_of_birth")
	create.run(t, ctx, map[string]any{"name": strings.Repeat("x", 256), "species": "dog"}).wantError(t, "name")
	create.run(t, ctx, map[string]any{"name": "Nope", "species": 5}).wantError(t, "species")

	if env := create.run(t, bg, map[string]any{"name": "Nope", "species": "dog"}); env.OK {
		t.Errorf("no claims: %+v", env)
	}
}

// TestPetCreateParallel: identical creates at the same time make one pet.
func TestPetCreateParallel(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	create, _, _ := newWriteTools(pool)
	var wg sync.WaitGroup
	ids := make([]string, 6)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var req mcp.CallToolRequest
			req.Params.Arguments = map[string]any{"name": "Biscuit", "species": "dog", "vet_name": "New Vet"}
			res, err := create.h(ctx, req)
			if err != nil || res.IsError {
				t.Errorf("parallel create %d: %v %+v", i, err, res)
				return
			}
			var env writeEnv
			_ = json.Unmarshal([]byte(res.Content[0].(mcp.TextContent).Text), &env)
			var d petData
			_ = json.Unmarshal(env.Data, &d)
			ids[i] = d.ID
		}(i)
	}
	wg.Wait()
	if n := count(t, pool, `SELECT count(*) FROM pets WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("parallel creates made %d pets", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM vendors WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("parallel creates made %d vet contacts", n)
	}
	for _, id := range ids[1:] {
		if id != ids[0] {
			t.Errorf("parallel ids differ: %v", ids)
		}
	}
}

// vendorRow reads a contact's trades, phone and updated_at.
func vendorRow(t *testing.T, pool *pgxpool.Pool, id string) (trades []string, phone *string, updated time.Time) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `SELECT trades, phone, updated_at FROM vendors WHERE id = $1`, id).Scan(&trades, &phone, &updated); err != nil {
		t.Fatal(err)
	}
	return trades, phone, updated
}

// TestPetVetMatchTagsContact (#4768): a contact found by vet_name / vet_phone
// gets trade vet and a blank phone filled, as migrations 043/060 do; an
// explicit vet_contact_id is never changed.
func TestPetVetMatchTagsContact(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	create, update, _ := newWriteTools(pool)
	old := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	vendor := func(name string, trades []string, phone *string) string {
		return insertID(t, pool, `INSERT INTO vendors (household_id, name, trades, phone, updated_at) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			hh.HouseholdID, name, trades, phone, old)
	}
	p := func(s string) *string { return &s }

	// No trades, no phone: tagged vet, phone filled.
	smith := vendor("Dr. Smith", []string{}, nil)
	d := create.run(t, ctx, map[string]any{"name": "Biscuit", "species": "dog", "vet_name": "dr. smith", "vet_phone": " 555-0111 "}).pet(t)
	if d.VetCreated || d.Vet == nil || str(d.Vet.ContactID) != smith {
		t.Fatalf("smith vet = %+v", d.Vet)
	}
	if tr, ph, up := vendorRow(t, pool, smith); strings.Join(tr, ",") != "vet" || str(ph) != "555-0111" || !up.After(old) {
		t.Errorf("smith after match: trades %v phone %s updated %v", tr, str(ph), up)
	}
	// Found through contact_find's filter: trade vet.
	if n := count(t, pool, `SELECT count(*) FROM vendors WHERE household_id = $1 AND 'vet' = ANY (trades)`, hh.HouseholdID); n != 1 {
		t.Errorf("vet contacts = %d", n)
	}

	// Other trades and a blank (spaces) phone, no phone given: vet appended;
	// the blank phone becomes NULL (COALESCE(NULLIF(btrim(phone), ''), NULL), as in 060).
	jones := vendor("Dr. Jones", []string{"groomer"}, p("  "))
	create.run(t, ctx, map[string]any{"name": "Max", "species": "dog", "vet": "Dr. Jones"}).pet(t)
	if tr, ph, _ := vendorRow(t, pool, jones); strings.Join(tr, ",") != "groomer,vet" || ph != nil {
		t.Errorf("jones after match: trades %v phone %q", tr, str(ph))
	}

	// Already a vet with a phone: not written at all.
	ready := vendor("Dr. Ready", []string{"vet"}, p("555-0133"))
	create.run(t, ctx, map[string]any{"name": "Luna", "species": "cat", "vet_name": "Dr. Ready", "vet_phone": "5550133"}).pet(t)
	if tr, ph, up := vendorRow(t, pool, ready); strings.Join(tr, ",") != "vet" || str(ph) != "555-0133" || !up.Equal(old) {
		t.Errorf("ready vet was written: trades %v phone %s updated %v", tr, str(ph), up)
	}

	// Phone only, matched by digits: tagged, its phone kept.
	clinic := vendor("Corner Clinic", []string{}, p("(555) 0144"))
	if d := create.run(t, ctx, map[string]any{"name": "Pip", "species": "bird", "vet_phone": "555-0144"}).pet(t); str(d.Vet.ContactID) != clinic {
		t.Fatalf("pip vet = %+v", d.Vet)
	}
	if tr, ph, _ := vendorRow(t, pool, clinic); strings.Join(tr, ",") != "vet" || str(ph) != "(555) 0144" {
		t.Errorf("clinic after phone match: trades %v phone %s", tr, str(ph))
	}

	// pet_update resolves the same way.
	walker := vendor("Wally", []string{"dog_walker"}, nil)
	update.run(t, ctx, map[string]any{"pet_id": "Pip", "vet_name": "Wally", "vet_phone": "555-0155"}).pet(t)
	if tr, ph, _ := vendorRow(t, pool, walker); strings.Join(tr, ",") != "dog_walker,vet" || str(ph) != "555-0155" {
		t.Errorf("wally after update match: trades %v phone %s", tr, str(ph))
	}

	// An explicit vet_contact_id is the user's choice: the contact is untouched.
	plain := vendor("Plain Contact", []string{}, nil)
	if d := create.run(t, ctx, map[string]any{"name": "Rex", "species": "dog", "vet_contact_id": plain, "vet_phone": "555-0166"}).pet(t); str(d.Vet.ContactID) != plain {
		t.Fatalf("rex vet = %+v", d.Vet)
	}
	update.run(t, ctx, map[string]any{"pet_id": "Biscuit", "vet_vendor_id": plain}).pet(t)
	if tr, ph, up := vendorRow(t, pool, plain); len(tr) != 0 || ph != nil || !up.Equal(old) {
		t.Errorf("explicit contact was changed: trades %v phone %s updated %v", tr, str(ph), up)
	}
}

// TestPetVetSharesContactCreateLock (#4768): a pet's vet lookup waits on
// contact_create's per-name lock, so a contact being saved under that name
// is found instead of a second contact being made.
func TestPetVetSharesContactCreateLock(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	create, _, _ := newWriteTools(pool)
	bg := context.Background()

	tx, err := pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	// What contact_create does for "Dr. Lock": its lock, then the insert.
	if _, err := tx.Exec(bg, `SELECT pg_advisory_xact_lock(hashtextextended('contact_create:' || $1::text || ':' || lower($2), 0))`, hh.HouseholdID, "Dr. Lock"); err != nil {
		t.Fatal(err)
	}
	var vid string
	if err := tx.QueryRow(bg, `INSERT INTO vendors (household_id, name, trades) VALUES ($1, 'Dr. Lock', '{}') RETURNING id::text`, hh.HouseholdID).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	done := make(chan writeEnv, 1)
	go func() {
		done <- create.run(t, ctx, map[string]any{"name": "Locky", "species": "dog", "vet_name": "Dr. Lock"})
	}()
	select {
	case env := <-done:
		t.Fatalf("pet_create did not wait for contact_create's lock: %s", env.Summary)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	select {
	case env := <-done:
		if d := env.pet(t); d.VetCreated || str(d.Vet.ContactID) != vid {
			t.Errorf("locky vet = %+v (want the contact being created, %s)", d.Vet, vid)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pet_create still waiting after the lock was released")
	}
	if n := count(t, pool, `SELECT count(*) FROM vendors WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("vendors = %d, want 1", n)
	}
}

// TestPetUpdate: only given fields change, null clears, a no-op writes
// nothing, the vet moves through vet_vendor_id, pet_id takes a name, and
// another family's pet is never touched.
func TestPetUpdate(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	create, update, _ := newWriteTools(pool)
	bg := context.Background()

	b := create.run(t, ctx, map[string]any{"name": "Biscuit", "species": "dog", "breed": "Beagle", "date_of_birth": "2021-06-01"}).pet(t)

	// By name, one field.
	env := update.run(t, ctx, map[string]any{"pet_id": "biscuit", "notes": "Allergic to chicken"})
	d := env.pet(t)
	if d.ID != b.ID || str(d.Notes) != "Allergic to chicken" || str(d.Breed) != "Beagle" || str(d.DateOfBirth) != "2021-06-01" ||
		strings.Join(d.Changed, ",") != "notes" {
		t.Fatalf("update notes = %+v", d)
	}
	if !strings.HasPrefix(env.Summary, "Updated Biscuit (notes): ") || !strings.Contains(env.Next[0], "pet_find id="+b.ID) {
		t.Errorf("summary %q next %v", env.Summary, env.Next)
	}

	// The same call again changes nothing and writes nothing.
	var stamp time.Time
	if err := pool.QueryRow(bg, `SELECT updated_at FROM pets WHERE id = $1`, b.ID).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	env = update.run(t, ctx, map[string]any{"pet_id": b.ID, "notes": "Allergic to chicken", "name": ""})
	if d := env.pet(t); len(d.Changed) != 0 || !strings.HasPrefix(env.Summary, "Nothing changed") {
		t.Errorf("no-op = %+v %q", d, env.Summary)
	}
	var stamp2 time.Time
	if err := pool.QueryRow(bg, `SELECT updated_at FROM pets WHERE id = $1`, b.ID).Scan(&stamp2); err != nil {
		t.Fatal(err)
	}
	if !stamp2.Equal(stamp) {
		t.Errorf("no-op update touched updated_at")
	}

	// null clears breed and date_of_birth; name and species change.
	d = update.run(t, ctx, map[string]any{"pet_id": b.ID, "breed": nil, "dob": nil, "name": "Biscuit Jr", "species": "puppy"}).pet(t)
	if d.Breed != nil || d.DateOfBirth != nil || d.Age != nil || d.Name != "Biscuit Jr" || str(d.Species) != "puppy" ||
		strings.Join(d.Changed, ",") != "name,species,breed,date_of_birth" {
		t.Errorf("clear = %+v", d)
	}
	update.run(t, ctx, map[string]any{"pet_id": b.ID, "species": nil}).wantError(t, "species")
	update.run(t, ctx, map[string]any{"pet_id": b.ID, "name": nil}).wantError(t, "name")
	update.run(t, ctx, map[string]any{"pet_id": b.ID}).wantError(t, "")
	update.run(t, ctx, map[string]any{"pet_id": b.ID, "date_of_birth": "2021-13-01"}).wantError(t, "date_of_birth")

	// The vet: by name (saved as a contact), then cleared with null.
	env = update.run(t, ctx, map[string]any{"pet_id": b.ID, "vet_name": "Dr. Paws Clinic"})
	d = env.pet(t)
	if !d.VetCreated || d.Vet == nil || d.Vet.Name != "Dr. Paws Clinic" || strings.Join(d.Changed, ",") != "vet" ||
		!strings.Contains(strings.Join(env.Next, " "), "contact_find id="+*d.Vet.ContactID) {
		t.Fatalf("set vet = %+v / %v", d, env.Next)
	}
	vetID := *d.Vet.ContactID
	if d := update.run(t, ctx, map[string]any{"pet_id": b.ID, "vet_contact_id": vetID}).pet(t); len(d.Changed) != 0 {
		t.Errorf("same vet again changed %v", d.Changed)
	}
	if d := update.run(t, ctx, map[string]any{"pet_id": b.ID, "vet_contact_id": nil}).pet(t); d.Vet != nil || strings.Join(d.Changed, ",") != "vet" {
		t.Errorf("clear vet = %+v", d)
	}
	if n := count(t, pool, `SELECT count(*) FROM vendors WHERE id = $1`, vetID); n != 1 {
		t.Errorf("clearing the vet deleted the contact")
	}
	if n := count(t, pool, `SELECT count(*) FROM pets WHERE id = $1 AND vet_vendor_id IS NULL`, b.ID); n != 1 {
		t.Errorf("pet vet columns not clean after clear")
	}

	// pet_id resolution: several with one name, unknown name.
	create.run(t, ctx, map[string]any{"name": "Twin", "species": "cat"})
	create.run(t, ctx, map[string]any{"name": "Twin", "species": "dog"})
	env = update.run(t, ctx, map[string]any{"pet_id": "twin", "notes": "x"})
	env.wantError(t, "pet_id")
	if !strings.Contains(env.Error.Message, "2 pets are named twin") {
		t.Errorf("ambiguous = %q", env.Error.Message)
	}
	env = update.run(t, ctx, map[string]any{"pet_id": "Ghost", "notes": "x"})
	env.wantError(t, "pet_id")
	if !strings.Contains(env.Error.Message, "Biscuit Jr, Twin, Twin") {
		t.Errorf("unknown name = %q", env.Error.Message)
	}
	update.run(t, ctx, map[string]any{"notes": "x"}).wantError(t, "pet_id")

	// Another family's pet: not found, unchanged, and no vet contact saved.
	_, other := testutil.SeedHousehold(t, pool)
	foreign := insertID(t, pool, `INSERT INTO pets (household_id, name, species) VALUES ($1, 'Rexy', 'dog') RETURNING id`, other.HouseholdID)
	vendors := count(t, pool, `SELECT count(*) FROM vendors`)
	env = update.run(t, ctx, map[string]any{"pet_id": foreign, "name": "Stolen", "vet_name": "Brand New Vet"})
	if env.OK || env.Error == nil || !strings.Contains(env.Error.Fix, "pet_find") {
		t.Errorf("foreign update = %+v", env)
	}
	if n := count(t, pool, `SELECT count(*) FROM pets WHERE id = $1 AND name = 'Rexy'`, foreign); n != 1 {
		t.Errorf("foreign pet changed")
	}
	if n := count(t, pool, `SELECT count(*) FROM vendors`); n != vendors {
		t.Errorf("a missing pet saved a vet contact")
	}
	update.run(t, ctx, map[string]any{"pet_id": "Rexy", "notes": "x"}).wantError(t, "pet_id")
}

// TestPetDelete: documents move to the family, notes go, the vet contact
// stays, a repeat is not found, another family's pet is untouched.
func TestPetDelete(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	create, _, del := newWriteTools(pool)
	bg := context.Background()

	b := create.run(t, ctx, map[string]any{"name": "Biscuit", "species": "dog", "vet_name": "Dr. Paws Clinic"}).pet(t)
	if _, err := pool.Exec(bg, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'pet', $2, 'Food', 'Kibble')`, hh.HouseholdID, b.ID); err != nil {
		t.Fatal(err)
	}
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id)
		VALUES ($1, $2, 'vaccines.pdf', 1, 'pet', $3) RETURNING id`, hh.HouseholdID, blob, b.ID)

	// Notes would be deleted: refused without confirm, nothing deleted.
	env := del.run(t, ctx, map[string]any{"pet_id": "Biscuit"})
	env.wantError(t, "confirm")
	if env.Error.Message != "Biscuit has 1 note; pass confirm:true to delete it." {
		t.Errorf("refusal message = %q", env.Error.Message)
	}
	if ex := env.Error.Example; ex["pet_id"] != b.ID || ex["confirm"] != true {
		t.Errorf("refusal example = %#v", env.Error.Example)
	}
	for _, a := range []map[string]any{{"pet_id": "Biscuit", "confirm": false}, {"pet_id": "Biscuit", "confirm": "no"}} {
		del.run(t, ctx, a).wantError(t, "confirm")
	}
	if n := count(t, pool, `SELECT count(*) FROM pets WHERE id = $1`, b.ID); n != 1 {
		t.Fatalf("refused delete removed the pet")
	}
	if n := count(t, pool, `SELECT count(*) FROM notes WHERE entity_type = 'pet' AND entity_id = $1`, b.ID); n != 1 {
		t.Fatalf("refused delete removed the note")
	}
	if n := count(t, pool, `SELECT count(*) FROM files WHERE id = $1 AND entity_type = 'pet' AND entity_id = $2`, file, b.ID); n != 1 {
		t.Fatalf("refused delete moved the document")
	}

	// confirm:true ("true" as text counts) deletes it.
	env = del.run(t, ctx, map[string]any{"pet_id": "Biscuit", "confirm": "true"})
	d := env.pet(t)
	if d.ID != b.ID || d.Name != "Biscuit" || d.DocumentsMoved == nil || *d.DocumentsMoved != 1 || d.NotesDeleted == nil || *d.NotesDeleted != 1 {
		t.Fatalf("deleted = %+v", d)
	}
	if env.Summary != "Deleted Biscuit. 1 document moved to the family; 1 note deleted." {
		t.Errorf("summary = %q", env.Summary)
	}
	if n := count(t, pool, `SELECT count(*) FROM pets WHERE id = $1`, b.ID); n != 0 {
		t.Errorf("pet still there")
	}
	if n := count(t, pool, `SELECT count(*) FROM files WHERE id = $1 AND entity_type = 'household' AND entity_id = $2`, file, hh.HouseholdID); n != 1 {
		t.Errorf("document not moved to the family")
	}
	if n := count(t, pool, `SELECT count(*) FROM notes WHERE entity_type = 'pet' AND entity_id = $1`, b.ID); n != 0 {
		t.Errorf("notes about the pet remain")
	}
	if n := count(t, pool, `SELECT count(*) FROM vendors WHERE id = $1`, *b.Vet.ContactID); n != 1 {
		t.Errorf("vet contact deleted with the pet")
	}

	// Repeat: not found.
	env = del.run(t, ctx, map[string]any{"pet_id": b.ID})
	if env.OK || !strings.Contains(env.Error.Fix, "pet_find") {
		t.Errorf("repeat delete = %+v", env)
	}

	// Another family's pet.
	_, other := testutil.SeedHousehold(t, pool)
	foreign := insertID(t, pool, `INSERT INTO pets (household_id, name, species) VALUES ($1, 'Rexy', 'dog') RETURNING id`, other.HouseholdID)
	if env := del.run(t, ctx, map[string]any{"pet_id": foreign}); env.OK {
		t.Errorf("foreign delete = %+v", env)
	}
	if n := count(t, pool, `SELECT count(*) FROM pets WHERE id = $1`, foreign); n != 1 {
		t.Errorf("foreign pet deleted")
	}
	del.run(t, ctx, map[string]any{}).wantError(t, "pet_id")

	// No notes: deleted without confirm, even with a document.
	m := create.run(t, ctx, map[string]any{"name": "Mittens", "species": "cat"}).pet(t)
	insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id)
		VALUES ($1, $2, 'adoption.pdf', 1, 'pet', $3) RETURNING id`, hh.HouseholdID, blob, m.ID)
	env = del.run(t, ctx, map[string]any{"pet_id": m.ID})
	if d := env.pet(t); d.NotesDeleted == nil || *d.NotesDeleted != 0 || d.DocumentsMoved == nil || *d.DocumentsMoved != 1 {
		t.Errorf("no-notes delete = %+v", d)
	}
	if n := count(t, pool, `SELECT count(*) FROM pets WHERE id = $1`, m.ID); n != 0 {
		t.Errorf("pet without notes not deleted")
	}
}

// TestPetWriteRoles: owner, family_manager and family_member write; viewer,
// housesitter and vendor are refused; the role is re-read per call, so a
// demotion applies at once and a removed member is not signed in.
func TestPetWriteRoles(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ownerCtx, hh := testutil.SeedHousehold(t, pool)
	create, update, del := newWriteTools(pool)
	bg := context.Background()

	pet := create.run(t, ownerCtx, map[string]any{"name": "Biscuit", "species": "dog"}).pet(t)

	for _, role := range []string{"family_manager", "family_member"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		d := create.run(t, ctx, map[string]any{"name": "Pet of " + role, "species": "cat"}).pet(t)
		update.run(t, ctx, map[string]any{"pet_id": d.ID, "notes": role}).pet(t)
		del.run(t, ctx, map[string]any{"pet_id": d.ID}).pet(t)
	}
	for _, role := range []string{"viewer", "housesitter", "vendor"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		for _, c := range []struct {
			w    writeTool
			args map[string]any
		}{
			{create, map[string]any{"name": "Nope", "species": "dog"}},
			{update, map[string]any{"pet_id": pet.ID, "notes": "nope"}},
			{del, map[string]any{"pet_id": pet.ID}},
		} {
			env := c.w.run(t, ctx, c.args)
			if env.OK || env.Error == nil || !strings.Contains(env.Error.Message, "The "+role+" role can't change pets") {
				t.Errorf("%s %s = %+v", role, c.w.tool.Name, env)
			}
		}
	}

	// Demotion: the claims still say family_member, memberships say viewer.
	ctx, uid := memberCtx(t, pool, hh, "family_member")
	if _, err := pool.Exec(bg, `UPDATE memberships SET role = 'viewer' WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if env := update.run(t, ctx, map[string]any{"pet_id": pet.ID, "notes": "nope"}); env.OK {
		t.Errorf("demoted member updated a pet")
	}
	// Removed: not signed in.
	if _, err := pool.Exec(bg, `DELETE FROM memberships WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if env := create.run(t, ctx, map[string]any{"name": "Nope", "species": "dog"}); env.OK || !strings.Contains(env.Summary, "not signed in") {
		t.Errorf("removed member = %+v", env)
	}
	if n := count(t, pool, `SELECT count(*) FROM pets WHERE id = $1 AND notes IS NULL`, pet.ID); n != 1 {
		t.Errorf("refused calls changed the pet")
	}
}
