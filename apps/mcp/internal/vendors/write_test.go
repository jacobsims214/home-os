package vendors

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// writeEnv is the JSON a contact write tool returns.
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

func decodeData[T any](t *testing.T, e writeEnv) T {
	t.Helper()
	if !e.OK {
		t.Fatalf("want success, got error: %s / %+v", e.Summary, e.Error)
	}
	var d T
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	return d
}

func (e writeEnv) contact(t *testing.T) contactData { t.Helper(); return decodeData[contactData](t, e) }

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

type writeTool struct {
	tool mcp.Tool
	h    toolHandler
}

type contactTools struct {
	find, create, update, del writeTool
}

func newTools(pool *pgxpool.Pool) contactTools {
	wt := func(_ string, tool mcp.Tool, h toolHandler) writeTool { return writeTool{tool, h} }
	return contactTools{
		find:   wt(NewContactFindTool(pool)),
		create: wt(NewContactCreateTool(pool)),
		update: wt(NewContactUpdateTool(pool)),
		del:    wt(NewContactDeleteTool(pool)),
	}
}

func schemaOf(t *testing.T, tool mcp.Tool) map[string]any {
	t.Helper()
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		OutputSchema map[string]any `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil || raw.OutputSchema == nil {
		t.Fatalf("%s declares no outputSchema (%v)", tool.Name, err)
	}
	return raw.OutputSchema
}

// run calls the tool and checks the respond invariants (text equals
// structuredContent, valid against the outputSchema, ok == !isError, at most
// 3 next hints).
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
	var env writeEnv
	if err := json.Unmarshal(sc, &env); err != nil {
		t.Fatal(err)
	}
	if env.OK == res.IsError {
		t.Fatalf("ok=%v but isError=%v", env.OK, res.IsError)
	}
	if len(env.Next) > 3 {
		t.Errorf("%s gives %d next hints, want at most 3: %v", w.tool.Name, len(env.Next), env.Next)
	}
	return env
}

// findAll runs contact_find (validated by call in tools_test.go).
func (ct contactTools) findAll(t *testing.T, ctx context.Context, args map[string]any) []contactData {
	t.Helper()
	return call(t, ctx, ct.find.h, args).contacts(t)
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
	ct := newTools(nil)
	fields := []string{"trades", "phone", "email", "website", "address", "notes"}
	for _, tc := range []struct {
		w                      writeTool
		name, title            string
		required, params       []string
		readOnly, destr, idemp bool
	}{
		{ct.create, "contact_create", "Add contact", []string{"name"}, append([]string{"name"}, fields...), false, false, false},
		{ct.update, "contact_update", "Update contact", []string{"contact_id"}, append([]string{"contact_id", "name"}, fields...), false, false, true},
		{ct.del, "contact_delete", "Delete contact", []string{"contact_id"}, []string{"contact_id", "confirm"}, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := tc.w.tool
			if tool.Name != tc.name {
				t.Fatalf("name = %q", tool.Name)
			}
			if fmt.Sprint(tool.InputSchema.Required) != fmt.Sprint(tc.required) {
				t.Errorf("required = %v, want %v", tool.InputSchema.Required, tc.required)
			}
			if len(tool.InputSchema.Properties) != len(tc.params) {
				t.Errorf("got %d params, want %v", len(tool.InputSchema.Properties), tc.params)
			}
			for _, p := range tc.params {
				if _, ok := tool.InputSchema.Properties[p]; !ok {
					t.Errorf("missing param %q", p)
				}
			}
			if n := len(tool.Description); n == 0 || n > 200 {
				t.Errorf("description is %d chars, want 1-200", n)
			}
			for param, schema := range tool.InputSchema.Properties {
				d, _ := schema.(map[string]any)["description"].(string)
				if d == "" || len(d) > 80 {
					t.Errorf("%s description is %d chars, want 1-80", param, len(d))
				}
			}
			if tr, ok := tool.InputSchema.Properties["trades"].(map[string]any); ok {
				if tr["type"] != "array" || fmt.Sprint(tr["items"]) != "map[type:string]" {
					t.Errorf("trades schema = %v, want array of strings", tr)
				}
			}
			a := tool.Annotations
			if a.Title != tc.title {
				t.Errorf("title = %q", a.Title)
			}
			if a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
				t.Fatalf("annotations not all explicit: %+v", a)
			}
			if *a.ReadOnlyHint != tc.readOnly || *a.DestructiveHint != tc.destr || *a.IdempotentHint != tc.idemp || *a.OpenWorldHint {
				t.Errorf("hints = %v/%v/%v/%v", *a.ReadOnlyHint, *a.DestructiveHint, *a.IdempotentHint, *a.OpenWorldHint)
			}
			schemaOf(t, tool)
		})
	}
}

// TestFindPlumberByTrade is the ticket's acceptance check: a plumber saved
// with contact_create comes back from one contact_find trade=plumber call,
// with the phone, whatever word was used for the trade.
func TestFindPlumberByTrade(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	ct := newTools(pool)

	joe := ct.create.run(t, ctx, map[string]any{"name": "Joe's Plumbing", "trades": []any{"Plumbing", "handyman"}, "phone": "555-0101"}).contact(t)
	if fmt.Sprint(joe.Trades) != "[plumber handyman]" || joe.Phone == nil || *joe.Phone != "555-0101" {
		t.Fatalf("created = %+v", joe)
	}
	ct.create.run(t, ctx, map[string]any{"name": "Paws Clinic", "trades": "Veterinarian", "phone": "555-0202"}).contact(t)

	for _, trade := range []string{"plumber", "Plumber", "plumbing", "PLUMBING", "plumb"} {
		env := call(t, ctx, ct.find.h, map[string]any{"trade": trade})
		got := env.contacts(t)
		if len(got) != 1 || got[0].ID != joe.ID || got[0].Phone == nil || *got[0].Phone != "555-0101" {
			t.Errorf("trade=%s: %+v", trade, got)
		}
		if !strings.Contains(env.Summary, "phone 555-0101") {
			t.Errorf("trade=%s summary = %q", trade, env.Summary)
		}
	}
	if got := ct.findAll(t, ctx, map[string]any{"trade": "vet"}); len(got) != 1 || got[0].Name != "Paws Clinic" {
		t.Errorf("trade=vet: %+v", got)
	}
}

// TestContactCreate covers normalization, the loop guard, address, the
// second-same-name hint, validation and household scoping.
func TestContactCreate(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ct := newTools(pool)

	env := ct.create.run(t, ctx, map[string]any{
		"name": "  Sparky Co ", "trades": "Electrical, handyman, electrician", "email": "hi@sparky.example",
		"website": "sparky.example", "address": "12 Main St", "notes": "Ask for Sam", "phone": "",
	})
	d := env.contact(t)
	if d.Name != "Sparky Co" || fmt.Sprint(d.Trades) != "[electrician handyman]" || d.Phone != nil ||
		d.Address == nil || *d.Address != "12 Main St" || d.Notes == nil || *d.Notes != "Ask for Sam" || d.Duplicate {
		t.Fatalf("created = %+v", d)
	}
	if env.Summary != "Added Sparky Co (electrician, handyman), email hi@sparky.example, website sparky.example." {
		t.Errorf("summary = %q", env.Summary)
	}
	if !strings.Contains(strings.Join(env.Next, " "), "maintenance_create contact_id="+d.ID) {
		t.Errorf("next = %v", env.Next)
	}
	var stored string
	if err := pool.QueryRow(context.Background(), `SELECT notes FROM vendors WHERE id = $1`, d.ID).Scan(&stored); err != nil || stored != "Address: 12 Main St\nAsk for Sam" {
		t.Errorf("stored notes = %q (%v)", stored, err)
	}
	if got := ct.findAll(t, ctx, map[string]any{"query": "main st"}); len(got) != 1 || got[0].ID != d.ID || *got[0].Address != "12 Main St" {
		t.Errorf("find by address: %+v", got)
	}

	// Loop guard: same name (any case) within 10 minutes returns that row.
	env = ct.create.run(t, ctx, map[string]any{"name": "sparky co", "trades": []any{"roofer"}})
	if dup := env.contact(t); !dup.Duplicate || dup.ID != d.ID || !strings.Contains(env.Next[0], "contact_update") {
		t.Errorf("duplicate: %+v / %v", dup, env.Next)
	}
	if n := count(t, pool, `SELECT count(*) FROM vendors WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("%d vendors after duplicate create, want 1", n)
	}

	// An older contact with the same name: a second row and a warning.
	if _, err := pool.Exec(context.Background(), `UPDATE vendors SET created_at = NOW() - interval '1 hour' WHERE id = $1`, d.ID); err != nil {
		t.Fatal(err)
	}
	env = ct.create.run(t, ctx, map[string]any{"name": "Sparky Co", "trades": []any{}})
	if d2 := env.contact(t); d2.ID == d.ID || len(d2.Trades) != 0 || !strings.Contains(strings.Join(env.Next, " "), "also named Sparky Co") {
		t.Errorf("second same-name: %+v / %v", d2, env.Next)
	}

	// A vet points at pet_update.
	env = ct.create.run(t, ctx, map[string]any{"name": "Oak Animal Hospital", "trade": "animal hospital"})
	if v := env.contact(t); fmt.Sprint(v.Trades) != "[vet]" || !strings.Contains(strings.Join(env.Next, " "), "vet_contact_id="+v.ID) {
		t.Errorf("vet: %+v / %v", v, env.Next)
	}

	// Validation.
	many := make([]any, 21)
	for i := range many {
		many[i] = fmt.Sprintf("trade%d", i)
	}
	for _, tc := range []struct {
		args  map[string]any
		field string
	}{
		{map[string]any{"trades": []any{"plumber"}}, "name"},
		{map[string]any{"name": "  "}, "name"},
		{map[string]any{"name": "X", "trades": many}, "trades"},
		{map[string]any{"name": "X", "trades": []any{"plumber", " "}}, "trades"},
		{map[string]any{"name": "X", "trades": []any{strings.Repeat("a", 65)}}, "trades"},
		{map[string]any{"name": "X", "trades": 5}, "trades"},
		{map[string]any{"name": "X", "trades": []any{5}}, "trades"},
		{map[string]any{"name": "X", "email": "not an email"}, "email"},
		{map[string]any{"name": "X", "phone": strings.Repeat("5", 51)}, "phone"},
		{map[string]any{"name": 7}, "name"},
	} {
		ct.create.run(t, ctx, tc.args).wantError(t, tc.field)
	}

	// Another family never sees it.
	otherCtx, _ := testutil.SeedHousehold(t, pool)
	if got := ct.findAll(t, otherCtx, map[string]any{"trade": "electrician"}); len(got) != 0 {
		t.Errorf("other family sees %+v", got)
	}
}

// TestContactCreateParallel: parallel identical creates make one row.
func TestContactCreateParallel(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ct := newTools(pool)
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var req mcp.CallToolRequest
			req.Params.Arguments = map[string]any{"name": "Joe's Plumbing", "trades": []any{"plumber"}}
			if _, err := ct.create.h(ctx, req); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := count(t, pool, `SELECT count(*) FROM vendors WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("parallel creates made %d rows, want 1", n)
	}
}

// TestContactUpdate covers partial updates, trades replace/clear/keep,
// clearing, address and notes sharing a column, name-or-id resolution and
// household scoping.
func TestContactUpdate(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	ct := newTools(pool)

	joe := ct.create.run(t, ctx, map[string]any{"name": "Joe's Plumbing", "trades": []any{"plumber"},
		"phone": "555-0101", "email": "joe@example.com", "notes": "Did the water heater"}).contact(t)

	// Only phone changes; trades, email and notes are kept. By name, any case.
	env := ct.update.run(t, ctx, map[string]any{"contact_id": "joe's plumbing", "phone": "555-0199"})
	d := env.contact(t)
	if *d.Phone != "555-0199" || fmt.Sprint(d.Trades) != "[plumber]" || *d.Email != "joe@example.com" ||
		*d.Notes != "Did the water heater" || fmt.Sprint(d.Changed) != "[phone]" {
		t.Fatalf("partial update = %+v", d)
	}
	if !strings.HasPrefix(env.Summary, "Updated Joe's Plumbing (phone): ") {
		t.Errorf("summary = %q", env.Summary)
	}
	// Repeat: nothing changes.
	env = ct.update.run(t, ctx, map[string]any{"contact_id": joe.ID, "phone": "555-0199"})
	if d := env.contact(t); len(d.Changed) != 0 || !strings.HasPrefix(env.Summary, "Nothing changed") {
		t.Errorf("repeat = %+v %q", d, env.Summary)
	}

	// trades replaces the list (normalized); [] and null clear it.
	if d := ct.update.run(t, ctx, map[string]any{"contact_id": joe.ID, "trades": []any{"Plumbing", "HVAC", "heating"}}).contact(t); fmt.Sprint(d.Trades) != "[plumber hvac]" {
		t.Errorf("replace trades = %v", d.Trades)
	}
	if got := ct.findAll(t, ctx, map[string]any{"trade": "heating and cooling"}); len(got) != 1 || got[0].ID != joe.ID {
		t.Errorf("find by new trade: %+v", got)
	}
	if d := ct.update.run(t, ctx, map[string]any{"contact_id": joe.ID, "trades": []any{}}).contact(t); len(d.Trades) != 0 || fmt.Sprint(d.Changed) != "[trades]" {
		t.Errorf("clear trades = %+v", d)
	}
	ct.update.run(t, ctx, map[string]any{"contact_id": joe.ID, "trades": []any{"plumber"}}).contact(t)
	if d := ct.update.run(t, ctx, map[string]any{"contact_id": joe.ID, "trades": nil}).contact(t); len(d.Trades) != 0 {
		t.Errorf("null trades = %v", d.Trades)
	}
	ct.update.run(t, ctx, map[string]any{"contact_id": joe.ID, "trades": "plumber"}).contact(t)

	// null clears email; "" leaves phone alone.
	d = ct.update.run(t, ctx, map[string]any{"contact_id": joe.ID, "email": nil, "phone": ""}).contact(t)
	if d.Email != nil || *d.Phone != "555-0199" || fmt.Sprint(d.Changed) != "[email]" {
		t.Errorf("clear email = %+v", d)
	}

	// address and notes are independent although they share a column.
	d = ct.update.run(t, ctx, map[string]any{"contact_id": joe.ID, "address": "9 Elm St"}).contact(t)
	if *d.Address != "9 Elm St" || *d.Notes != "Did the water heater" || fmt.Sprint(d.Changed) != "[address]" {
		t.Errorf("set address = %+v", d)
	}
	d = ct.update.run(t, ctx, map[string]any{"contact_id": joe.ID, "notes": "Ask for Joe"}).contact(t)
	if *d.Address != "9 Elm St" || *d.Notes != "Ask for Joe" || fmt.Sprint(d.Changed) != "[notes]" {
		t.Errorf("set notes = %+v", d)
	}
	d = ct.update.run(t, ctx, map[string]any{"contact_id": joe.ID, "address": nil}).contact(t)
	if d.Address != nil || *d.Notes != "Ask for Joe" {
		t.Errorf("clear address = %+v", d)
	}

	// Rename; name cannot be cleared.
	if d := ct.update.run(t, ctx, map[string]any{"id": joe.ID, "name": "Joe & Sons Plumbing"}).contact(t); d.Name != "Joe & Sons Plumbing" {
		t.Errorf("rename = %+v", d)
	}
	ct.update.run(t, ctx, map[string]any{"contact_id": joe.ID, "name": nil}).wantError(t, "name")

	// Errors: no fields, missing id, bad trades, unknown and ambiguous names.
	ct.update.run(t, ctx, map[string]any{"contact_id": joe.ID}).wantError(t, "")
	ct.update.run(t, ctx, map[string]any{"phone": "1"}).wantError(t, "contact_id")
	ct.update.run(t, ctx, map[string]any{"contact_id": joe.ID, "trades": []any{""}}).wantError(t, "trades")
	env = ct.update.run(t, ctx, map[string]any{"contact_id": "Nobody", "phone": "1"})
	env.wantError(t, "contact_id")
	if !strings.Contains(env.Error.Fix, "contact_find query=Nobody") {
		t.Errorf("unknown name fix = %q", env.Error.Fix)
	}
	insertID(t, pool, `INSERT INTO vendors (household_id, name) SELECT household_id, 'Twin' FROM vendors WHERE id = $1 RETURNING id`, joe.ID)
	insertID(t, pool, `INSERT INTO vendors (household_id, name) SELECT household_id, 'twin' FROM vendors WHERE id = $1 RETURNING id`, joe.ID)
	env = ct.update.run(t, ctx, map[string]any{"contact_id": "Twin", "phone": "1"})
	env.wantError(t, "contact_id")
	if !strings.Contains(env.Error.Message, "2 contacts are named Twin") {
		t.Errorf("ambiguous = %q", env.Error.Message)
	}

	// Another family's contact is not found and not changed.
	otherCtx, other := testutil.SeedHousehold(t, pool)
	foreign := insertID(t, pool, `INSERT INTO vendors (household_id, name, phone) VALUES ($1, 'Away', '1') RETURNING id`, other.HouseholdID)
	if env := ct.update.run(t, ctx, map[string]any{"contact_id": foreign, "phone": "2"}); env.OK {
		t.Errorf("updated a foreign contact: %+v", env)
	}
	if env := ct.update.run(t, otherCtx, map[string]any{"contact_id": joe.ID, "phone": "2"}); env.OK {
		t.Errorf("other family updated ours: %+v", env)
	}
	if n := count(t, pool, `SELECT count(*) FROM vendors WHERE id = $1 AND phone = '1'`, foreign); n != 1 {
		t.Errorf("foreign contact changed")
	}
}

// TestContactDelete: no confirm needed when nothing uses the contact;
// confirm:true needed when bills, upkeep, schedules, pets or notes do, and
// then those keep working with no contact (058/043 SET NULL).
func TestContactDelete(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ct := newTools(pool)
	hid := hh.HouseholdID

	lone := ct.create.run(t, ctx, map[string]any{"name": "Lone Roofer", "trades": []any{"roofer"}}).contact(t)
	env := ct.del.run(t, ctx, map[string]any{"contact_id": "Lone Roofer"})
	if d := decodeData[contactDeleted](t, env); !d.Deleted || d.ContactID != lone.ID || d.Name != "Lone Roofer" {
		t.Errorf("delete lone = %+v", d)
	}
	if env.Summary != "Deleted Lone Roofer (roofer)." || !strings.Contains(strings.Join(env.Next, " "), "contact_create name=… trades=[roofer]") {
		t.Errorf("lone summary/next = %q %v", env.Summary, env.Next)
	}
	// Repeat: not found.
	if env := ct.del.run(t, ctx, map[string]any{"contact_id": lone.ID}); env.OK || !strings.Contains(env.Summary, "was found") {
		t.Errorf("repeat delete = %+v", env)
	}

	vet := ct.create.run(t, ctx, map[string]any{"name": "Paws Clinic", "trades": []any{"vet", "groomer"}}).contact(t)
	bill := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, vendor_id) VALUES ($1, 'Grooming plan', 20, $2) RETURNING id`, hid, vet.ID)
	task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, vendor_id) VALUES ($1, 'Nail trim', '2026-12-01', $2) RETURNING id`, hid, vet.ID)
	sched := insertID(t, pool, `INSERT INTO maintenance_schedules (household_id, name, rrule, vendor_id) VALUES ($1, 'Checkup', 'FREQ=YEARLY', $2) RETURNING id`, hid, vet.ID)
	pet := insertID(t, pool, `INSERT INTO pets (household_id, name, species, vet_vendor_id) VALUES ($1, 'Rex', 'dog', $2) RETURNING id`, hid, vet.ID)
	insertID(t, pool, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'vendor', $2, 'Hours', 'M-F') RETURNING id`, hid, vet.ID)

	// Without confirm: refused with the counts; nothing deleted.
	env = ct.del.run(t, ctx, map[string]any{"contact_id": vet.ID})
	env.wantError(t, "confirm")
	if env.Error.Message != "Paws Clinic has 1 bill, 1 upkeep task, 1 upkeep schedule, 1 pet (as vet) and 1 note; pass confirm:true to delete it." ||
		env.Error.Example["confirm"] != true {
		t.Errorf("refusal = %+v", env.Error)
	}
	if n := count(t, pool, `SELECT count(*) FROM vendors WHERE id = $1`, vet.ID); n != 1 {
		t.Fatalf("contact deleted without confirm")
	}
	if env := ct.del.run(t, ctx, map[string]any{"contact_id": vet.ID, "confirm": false}); env.OK {
		t.Errorf("confirm:false deleted")
	}

	// Another family can't delete it, confirm or not.
	otherCtx, _ := testutil.SeedHousehold(t, pool)
	if env := ct.del.run(t, otherCtx, map[string]any{"contact_id": vet.ID, "confirm": true}); env.OK {
		t.Errorf("other family deleted our contact")
	}

	// With confirm: deleted; references set to NULL, rows kept.
	env = ct.del.run(t, ctx, map[string]any{"contact_id": vet.ID, "confirm": "true"})
	d := decodeData[contactDeleted](t, env)
	if !d.Deleted || d.Unlinked != (contactUnlinked{Bills: 1, UpkeepTasks: 1, UpkeepSchedules: 1, Pets: 1}) || d.NotesDeleted != 1 {
		t.Errorf("deleted = %+v", d)
	}
	if !strings.Contains(env.Summary, "Kept without a contact: 1 bill, 1 upkeep task, 1 upkeep schedule and 1 pet (as vet).") ||
		!strings.Contains(env.Summary, "1 note about them deleted.") {
		t.Errorf("summary = %q", env.Summary)
	}
	for _, q := range []struct{ sql, id string }{
		{`SELECT count(*) FROM bills WHERE id = $1 AND vendor_id IS NULL`, bill},
		{`SELECT count(*) FROM maintenance_tasks WHERE id = $1 AND vendor_id IS NULL`, task},
		{`SELECT count(*) FROM maintenance_schedules WHERE id = $1 AND vendor_id IS NULL`, sched},
		{`SELECT count(*) FROM pets WHERE id = $1 AND vet_vendor_id IS NULL`, pet},
	} {
		if n := count(t, pool, q.sql, q.id); n != 1 {
			t.Errorf("not kept with NULL contact: %s", q.sql)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM notes WHERE entity_type = 'vendor' AND entity_id = $1`, vet.ID); n != 0 {
		t.Errorf("notes about the contact left: %d", n)
	}
	ct.del.run(t, ctx, map[string]any{}).wantError(t, "contact_id")
}

// TestContactRoles: family roles write; viewer and housesitter only read;
// vendor gets nothing; a demoted member loses write at once; a removed one
// is not signed in.
func TestContactRoles(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ownerCtx, hh := testutil.SeedHousehold(t, pool)
	ct := newTools(pool)
	bg := context.Background()

	joe := ct.create.run(t, ownerCtx, map[string]any{"name": "Joe's Plumbing", "trades": []any{"plumber"}, "phone": "555-0101"}).contact(t)

	for _, role := range []string{"family_manager", "family_member"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		d := ct.create.run(t, ctx, map[string]any{"name": "Contact of " + role}).contact(t)
		ct.update.run(t, ctx, map[string]any{"contact_id": d.ID, "phone": "1"}).contact(t)
		decodeData[contactDeleted](t, ct.del.run(t, ctx, map[string]any{"contact_id": d.ID}))
	}
	for _, role := range []string{"viewer", "housesitter", "vendor"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		for _, c := range []struct {
			w    writeTool
			args map[string]any
		}{
			{ct.create, map[string]any{"name": "Nope"}},
			{ct.update, map[string]any{"contact_id": joe.ID, "phone": "1"}},
			{ct.del, map[string]any{"contact_id": joe.ID, "confirm": true}},
		} {
			env := c.w.run(t, ctx, c.args)
			if env.OK || env.Error == nil || !strings.Contains(env.Error.Message, "The "+role+" role can't change contacts") {
				t.Errorf("%s %s = %+v", role, c.w.tool.Name, env)
			}
		}
		env := call(t, ctx, ct.find.h, map[string]any{"trade": "plumber"})
		switch role {
		case "vendor":
			if env.OK || env.Error == nil || !strings.Contains(env.Error.Message, "can't see the family's contacts") {
				t.Errorf("vendor find = %+v", env)
			}
		default:
			got := env.contacts(t)
			if len(got) != 1 || *got[0].Phone != "555-0101" {
				t.Errorf("%s find = %+v", role, got)
			}
			if strings.Contains(strings.Join(env.Next, " "), "_create") || strings.Contains(strings.Join(env.Next, " "), "_update") {
				t.Errorf("%s is pointed at write tools: %v", role, env.Next)
			}
		}
	}

	// Demotion: claims say family_member, memberships say viewer.
	ctx, uid := memberCtx(t, pool, hh, "family_member")
	if _, err := pool.Exec(bg, `UPDATE memberships SET role = 'viewer' WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if env := ct.update.run(t, ctx, map[string]any{"contact_id": joe.ID, "phone": "1"}); env.OK {
		t.Errorf("demoted member updated a contact")
	}
	if _, err := pool.Exec(bg, `DELETE FROM memberships WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if env := ct.create.run(t, ctx, map[string]any{"name": "Nope"}); env.OK || !strings.Contains(env.Summary, "not signed in") {
		t.Errorf("removed member = %+v", env)
	}
	if env := call(t, ctx, ct.find.h, map[string]any{}); env.OK {
		t.Errorf("removed member can find")
	}
	if n := count(t, pool, `SELECT count(*) FROM vendors WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("refused calls left %d contacts, want 1", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM vendors WHERE id = $1 AND phone = '555-0101'`, joe.ID); n != 1 {
		t.Errorf("refused calls changed the contact")
	}
}
