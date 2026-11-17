package vehicle

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

// writeEnv is the JSON a vehicle write tool returns.
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

func (e writeEnv) vehicle(t *testing.T) vehicleData { t.Helper(); return decodeData[vehicleData](t, e) }

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

// writeTool is one tool under test: its definition and handler.
type writeTool struct {
	tool mcp.Tool
	h    toolHandler
}

type writeTools struct {
	create, update, del writeTool
}

func newWriteTools(pool *pgxpool.Pool) writeTools {
	wt := func(_ string, tool mcp.Tool, h toolHandler) writeTool { return writeTool{tool, h} }
	return writeTools{
		create: wt(NewVehicleCreateTool(pool)),
		update: wt(NewVehicleUpdateTool(pool)),
		del:    wt(NewVehicleDeleteTool(pool)),
	}
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
// !isError, and at most 3 next hints.
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
	if len(env.Next) > 3 {
		t.Errorf("%s gives %d next hints, want at most 3: %v", w.tool.Name, len(env.Next), env.Next)
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

// legacyParams are legacy names a model might send; they must not be
// advertised or written. The vehicles finance columns they named are dropped
// by #4707, so the tests prove "not written" through loans and bills (a car
// loan is a loan, insurance and registration are bills), never the columns.
var legacyParams = []string{"lender", "loan_amount", "loan_term_months", "monthly_payment", "registration_renewal_month",
	"registration_cost", "insurance_provider", "insurance_cost", "odometer", "nickname"}

// TestWriteToolDefinitions pins the write tools to the catalog: names,
// required params, descriptions, all four hints and an outputSchema.
func TestWriteToolDefinitions(t *testing.T) {
	w := newWriteTools(nil)
	fields := []string{"year", "vin", "license_plate", "color", "purchase_date", "purchase_price", "current_value", "notes"}
	for _, tc := range []struct {
		w                      writeTool
		name, title            string
		required, params       []string
		readOnly, destr, idemp bool
	}{
		{w.create, "vehicle_create", "Add vehicle", []string{"make", "model"}, append([]string{"make", "model"}, fields...), false, false, false},
		{w.update, "vehicle_update", "Update vehicle", []string{"vehicle_id"}, append([]string{"vehicle_id", "make", "model"}, fields...), false, false, true},
		{w.del, "vehicle_delete", "Delete vehicle", []string{"vehicle_id"}, []string{"vehicle_id", "confirm"}, false, true, true},
	} {
		tool := tc.w.tool
		if tool.Name != tc.name {
			t.Errorf("name = %q, want %q", tool.Name, tc.name)
		}
		if n := len(tool.Description); n == 0 || n > 200 {
			t.Errorf("%s description is %d chars: %q", tool.Name, n, tool.Description)
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
		for _, legacy := range legacyParams {
			if _, ok := tool.InputSchema.Properties[legacy]; ok {
				t.Errorf("%s advertises legacy param %q", tool.Name, legacy)
			}
		}
		a := tool.Annotations
		if a.Title != tc.title || a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
			t.Fatalf("%s annotations not all explicit: %+v", tool.Name, a)
		}
		if *a.ReadOnlyHint != tc.readOnly || *a.DestructiveHint != tc.destr || *a.IdempotentHint != tc.idemp || *a.OpenWorldHint {
			t.Errorf("%s hints = %+v", tool.Name, a)
		}
		b, _ := json.Marshal(tool)
		if strings.Contains(string(b), `"array","items":{"type":"integer"`) {
			t.Errorf("%s outputSchema advertises a byte-array id", tool.Name)
		}
		resolvedSchema(t, tool)
	}
}

// TestVehicleCreate is the ticket's acceptance path: "Add our 2019 Honda
// Odyssey" is one call; the loop guard; validation; legacy finance params
// and columns are never written.
func TestVehicleCreate(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	w := newWriteTools(pool)
	_, _, find := NewVehicleFindTool(pool)

	env := w.create.run(t, ctx, map[string]any{"make": "Honda", "model": "Odyssey", "year": 2019})
	v := env.vehicle(t)
	if v.Name != "2019 Honda Odyssey" || v.Year == nil || *v.Year != 2019 || v.Duplicate || v.LoanCount == nil || *v.LoanCount != 0 {
		t.Fatalf("created = %+v", v)
	}
	if env.Summary != "Added 2019 Honda Odyssey." {
		t.Errorf("summary = %q", env.Summary)
	}
	if len(env.Next) != 3 || !strings.Contains(env.Next[0], "bill_create") || !strings.Contains(env.Next[0], "vehicle_id="+v.ID) ||
		!strings.Contains(env.Next[1], "maintenance_create") || !strings.Contains(env.Next[2], "loan_create") {
		t.Errorf("next = %v", env.Next)
	}
	if n := count(t, pool, `SELECT count(*) FROM vehicles WHERE id = $1 AND household_id = $2`, v.ID, hh.HouseholdID); n != 1 {
		t.Fatalf("vehicle not in the caller's family")
	}
	// vehicle_find sees it.
	if got := call(t, ctx, find, map[string]any{"query": "odyssey"}).vehicles(t); len(got) != 1 || got[0].ID != v.ID {
		t.Errorf("vehicle_find after create = %+v", got)
	}

	// Every field, aliases, money as text, VIN normalised; legacy params and
	// unstored params are not written, and odometer gets a notes hint.
	env = w.create.run(t, ctx, map[string]any{
		"brand": "Ford", "model": "F-150", "model_year": "2008", "vin": " 1ftfw1e 50 ", "plate": "OLD111", "colour": "Red",
		"purchase_date": "2010-04-01", "price": "$21,500", "worth": 4000.5, "notes": "Tow hitch",
		"insurance_cost": 900, "loan_amount": 15000, "lender": "Bank", "registration_cost": 80, "odometer": 145210,
	})
	f := env.vehicle(t)
	if f.Name != "2008 Ford F-150" || *f.VIN != "1FTFW1E50" || *f.LicensePlate != "OLD111" || *f.Color != "Red" ||
		*f.PurchaseDate != "2010-04-01" || *f.PurchasePrice != 21500 || *f.CurrentValue != 4000.5 || *f.Notes != "Tow hitch" {
		t.Fatalf("full create = %+v", f)
	}
	if !strings.Contains(env.Summary, "plate OLD111, worth $4,000.50") {
		t.Errorf("summary = %q", env.Summary)
	}
	if last := env.Next[len(env.Next)-1]; !strings.Contains(last, "no odometer field") || !strings.Contains(last, "vehicle_update vehicle_id="+f.ID) {
		t.Errorf("unstored hint missing: %v", env.Next)
	}
	if n := count(t, pool, `SELECT count(*) FROM loans WHERE household_id = $1`, hh.HouseholdID) +
		count(t, pool, `SELECT count(*) FROM bills WHERE household_id = $1`, hh.HouseholdID); n != 0 {
		t.Errorf("vehicle_create made %d loans/bills from legacy params", n)
	}

	// Loop guard: the same make/model within 10 minutes returns the vehicle;
	// a repeat without the year too; a different year is a new vehicle.
	for _, args := range []map[string]any{
		{"make": "HONDA", "model": "odyssey", "year": 2019},
		{"make": "Honda", "model": "Odyssey"},
	} {
		env = w.create.run(t, ctx, args)
		dup := env.vehicle(t)
		if !dup.Duplicate || dup.ID != v.ID || !strings.Contains(env.Next[0], "vehicle_update vehicle_id="+v.ID) {
			t.Errorf("duplicate %v = %+v next %v", args, dup, env.Next)
		}
	}
	env = w.create.run(t, ctx, map[string]any{"make": "Honda", "model": "Odyssey", "year": 2022})
	second := env.vehicle(t)
	if second.Duplicate || second.ID == v.ID {
		t.Fatalf("different year treated as duplicate: %+v", second)
	}
	if !strings.Contains(env.Next[2], "another Honda Odyssey") {
		t.Errorf("same-name hint missing: %v", env.Next)
	}
	if n := count(t, pool, `SELECT count(*) FROM vehicles WHERE household_id = $1`, hh.HouseholdID); n != 3 {
		t.Errorf("vehicles = %d, want 3", n)
	}

	// Validation: nothing is written.
	w.create.run(t, ctx, map[string]any{"model": "Civic"}).wantError(t, "make")
	w.create.run(t, ctx, map[string]any{"make": "Honda", "name": "Civic"}).wantError(t, "model")
	w.create.run(t, ctx, map[string]any{"make": "Honda", "model": "Civic", "year": 19}).wantError(t, "year")
	w.create.run(t, ctx, map[string]any{"make": "Honda", "model": "Civic", "year": time.Now().Year() + 5}).wantError(t, "year")
	w.create.run(t, ctx, map[string]any{"make": "Honda", "model": "Civic", "year": "nineteen"}).wantError(t, "year")
	w.create.run(t, ctx, map[string]any{"make": "Honda", "model": "Civic", "current_value": -1}).wantError(t, "current_value")
	w.create.run(t, ctx, map[string]any{"make": "Honda", "model": "Civic", "purchase_price": "lots"}).wantError(t, "purchase_price")
	w.create.run(t, ctx, map[string]any{"make": "Honda", "model": "Civic", "purchase_date": "June 2019"}).wantError(t, "purchase_date")
	w.create.run(t, ctx, map[string]any{"make": "Honda", "model": "Civic", "vin": 123}).wantError(t, "vin")
	w.create.run(t, ctx, map[string]any{"make": strings.Repeat("x", 101), "model": "Civic"}).wantError(t, "make")
	if n := count(t, pool, `SELECT count(*) FROM vehicles WHERE household_id = $1`, hh.HouseholdID); n != 3 {
		t.Errorf("invalid calls created vehicles: %d", n)
	}
}

// TestVehicleCreateParallel: parallel identical calls make one vehicle.
func TestVehicleCreateParallel(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	w := newWriteTools(pool)
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var req mcp.CallToolRequest
			req.Params.Arguments = map[string]any{"make": "Toyota", "model": "RAV4", "year": 2022}
			if res, err := w.create.h(ctx, req); err != nil || res.IsError {
				t.Errorf("parallel create failed: %v %+v", err, res)
			}
		}()
	}
	wg.Wait()
	if n := count(t, pool, `SELECT count(*) FROM vehicles WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("parallel creates made %d vehicles", n)
	}
}

// TestVehicleUpdate: partial update with null clears, a no-op writes
// nothing, name-or-id resolution with an ambiguity error, another family's
// vehicle is not found, and legacy finance params write no loan or bill.
func TestVehicleUpdate(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	w := newWriteTools(pool)
	bg := context.Background()

	// The vehicle's loan lives in loans (typed vehicle_id): legacy finance
	// params on vehicle_update must not touch it or add loans/bills.
	odyssey := insertID(t, pool, `INSERT INTO vehicles (household_id, year, make, model, license_plate, color, current_value)
		VALUES ($1, 2019, 'Honda', 'Odyssey', 'ABC123', 'Blue', 20000) RETURNING id`, hh.HouseholdID)
	insertID(t, pool, `INSERT INTO loans (household_id, name, lender, original_amount, remaining_balance, monthly_payment, vehicle_id)
		VALUES ($1, 'Odyssey loan', 'Old Bank', 30000, 21000, 550, $2) RETURNING id`, hh.HouseholdID, odyssey)
	financeRows := func() string {
		var s string
		if err := pool.QueryRow(bg, `SELECT concat_ws('|', (SELECT count(*) FROM bills WHERE household_id = $1),
			(SELECT string_agg(concat_ws(',', name, lender, original_amount, monthly_payment), ';') FROM loans WHERE household_id = $1))`,
			hh.HouseholdID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	financeBefore := financeRows()

	// By model name, any case: a new value and a cleared color.
	env := w.update.run(t, ctx, map[string]any{"vehicle_id": "odyssey", "current_value": "18,500", "color": nil,
		"insurance_cost": 1, "loan_amount": 2, "monthly_payment": 3})
	v := env.vehicle(t)
	if v.ID != odyssey || *v.CurrentValue != 18500 || v.Color != nil || *v.LicensePlate != "ABC123" ||
		strings.Join(v.Changed, ",") != "color,current_value" {
		t.Fatalf("updated = %+v", v)
	}
	if !strings.HasPrefix(env.Summary, "Updated 2019 Honda Odyssey") || !strings.Contains(env.Summary, "(color, current_value)") {
		t.Errorf("summary = %q", env.Summary)
	}
	if got := financeRows(); got != financeBefore {
		t.Errorf("legacy params changed loans/bills: %q -> %q", financeBefore, got)
	}

	// No-op: nothing written, updated_at unchanged.
	var before, after time.Time
	if err := pool.QueryRow(bg, `SELECT updated_at FROM vehicles WHERE id = $1`, odyssey).Scan(&before); err != nil {
		t.Fatal(err)
	}
	env = w.update.run(t, ctx, map[string]any{"vehicle_id": "2019 Honda Odyssey", "current_value": 18500, "license_plate": "ABC123", "color": ""})
	if d := env.vehicle(t); len(d.Changed) != 0 || !strings.HasPrefix(env.Summary, "Nothing changed") {
		t.Errorf("no-op = %+v %q", d, env.Summary)
	}
	if err := pool.QueryRow(bg, `SELECT updated_at FROM vehicles WHERE id = $1`, odyssey).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Errorf("no-op update wrote the row")
	}

	// Year null clears; make and model cannot be cleared; odometer is not
	// stored but hinted.
	env = w.update.run(t, ctx, map[string]any{"vehicle_id": odyssey, "year": nil, "odometer": 45210})
	if d := env.vehicle(t); d.Year != nil || d.Name != "Honda Odyssey" || len(env.Next) != 2 || !strings.Contains(env.Next[1], "no odometer field") {
		t.Errorf("year clear = %+v next %v", d, env.Next)
	}
	w.update.run(t, ctx, map[string]any{"vehicle_id": odyssey, "make": nil}).wantError(t, "make")
	w.update.run(t, ctx, map[string]any{"vehicle_id": odyssey, "model": nil}).wantError(t, "model")
	w.update.run(t, ctx, map[string]any{"vehicle_id": odyssey}).wantError(t, "")
	w.update.run(t, ctx, map[string]any{"vehicle_id": odyssey, "year": 1700}).wantError(t, "year")
	w.update.run(t, ctx, map[string]any{"current_value": 1}).wantError(t, "vehicle_id")

	// Ambiguity: two Honda Odysseys.
	insertID(t, pool, `INSERT INTO vehicles (household_id, year, make, model) VALUES ($1, 2022, 'Honda', 'Odyssey') RETURNING id`, hh.HouseholdID)
	env = w.update.run(t, ctx, map[string]any{"vehicle_id": "Honda Odyssey", "color": "Grey"})
	env.wantError(t, "vehicle_id")
	if !strings.Contains(env.Error.Message, "2 vehicles match") || !strings.Contains(env.Error.Message, odyssey) {
		t.Errorf("ambiguity = %q", env.Error.Message)
	}
	// The full name tells them apart.
	if d := w.update.run(t, ctx, map[string]any{"vehicle_id": "2022 honda odyssey", "color": "Grey"}).vehicle(t); d.ID == odyssey {
		t.Errorf("full name resolved to the wrong vehicle")
	}
	// No match lists the family's vehicles.
	env = w.update.run(t, ctx, map[string]any{"vehicle_id": "Tesla", "color": "Red"})
	env.wantError(t, "vehicle_id")
	if !strings.Contains(env.Error.Message, "Honda Odyssey") {
		t.Errorf("no-match = %q", env.Error.Message)
	}

	// Another family's vehicle: not found by id or by name, never changed.
	_, other := testutil.SeedHousehold(t, pool)
	foreign := insertID(t, pool, `INSERT INTO vehicles (household_id, make, model, color) VALUES ($1, 'Tesla', 'Model 3', 'White') RETURNING id`, other.HouseholdID)
	if env := w.update.run(t, ctx, map[string]any{"vehicle_id": foreign, "color": "Red"}); env.OK || !strings.Contains(env.Error.Fix, "vehicle_find") {
		t.Errorf("foreign update = %+v", env)
	}
	if env := w.update.run(t, ctx, map[string]any{"vehicle_id": "Model 3", "color": "Red"}); env.OK {
		t.Errorf("foreign update by name = %+v", env)
	}
	if n := count(t, pool, `SELECT count(*) FROM vehicles WHERE id = $1 AND color = 'White'`, foreign); n != 1 {
		t.Errorf("foreign vehicle changed")
	}
}

// TestVehicleDelete: a vehicle with upkeep needs confirm:true; its loans and
// bills stay family-level; upkeep and its calendar entry go (with a CalDAV
// delete change); documents move to the family; one with only money links
// deletes without confirm; another family's vehicle is not found.
func TestVehicleDelete(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	w := newWriteTools(pool)
	hid := hh.HouseholdID
	bg := context.Background()

	v := w.create.run(t, ctx, map[string]any{"make": "Ford", "model": "F-150", "year": 2008}).vehicle(t)
	loan := insertID(t, pool, `INSERT INTO loans (household_id, name, original_amount, remaining_balance, vehicle_id) VALUES ($1, 'Truck loan', 12000, 4000, $2) RETURNING id`, hid, v.ID)
	bill := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day, vehicle_id) VALUES ($1, 'Truck insurance', 90, 5, $2) RETURNING id`, hid, v.ID)
	task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, vehicle_id) VALUES ($1, 'Oil change', current_date + 7, $2) RETURNING id`, hid, v.ID)
	sched := insertID(t, pool, `INSERT INTO maintenance_schedules (household_id, name, rrule, vehicle_id) VALUES ($1, 'Tire rotation', 'FREQ=MONTHLY;INTERVAL=6', $2) RETURNING id`, hid, v.ID)
	if _, err := pool.Exec(bg, `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'vehicle', $2, 'Tires', '265/70R17')`, hid, v.ID); err != nil {
		t.Fatal(err)
	}
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id)
		VALUES ($1, $2, 'title.pdf', 1, 'vehicle', $3) RETURNING id`, hid, blob, v.ID)
	cal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Maintenance', 'maintenance', $2) RETURNING id`, hid, uuid.NewString())
	insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
		VALUES ($1, 'evt-oil', '{}', 'e0', 'maintenance', 'maintenance_task', $2) RETURNING id`, cal, task)

	// No confirm: refused with what would be lost, nothing deleted.
	env := w.del.run(t, ctx, map[string]any{"vehicle_id": "F-150"})
	env.wantError(t, "confirm")
	for _, want := range []string{"1 upkeep task", "1 upkeep schedule", "1 note"} {
		if !strings.Contains(env.Error.Message, want) {
			t.Errorf("refusal %q lacks %q", env.Error.Message, want)
		}
	}
	if env.Error.Example["confirm"] != true || env.Error.Example["vehicle_id"] != v.ID {
		t.Errorf("refusal example = %v", env.Error.Example)
	}
	if n := count(t, pool, `SELECT count(*) FROM vehicles WHERE id = $1`, v.ID); n != 1 {
		t.Fatalf("refused delete removed the vehicle")
	}

	env = w.del.run(t, ctx, map[string]any{"vehicle_id": v.ID, "confirm": "true"})
	d := decodeData[vehicleDeleted](t, env)
	if !d.Deleted || d.Name != "2008 Ford F-150" || d.Removed.UpkeepTasks != 1 || d.Removed.UpkeepSchedules != 1 || d.Removed.Events != 1 ||
		d.Removed.NotesDeleted != 1 || d.Kept.BillsUnlinked != 1 || d.Kept.LoansUnlinked != 1 || d.Kept.DocumentsMoved != 1 {
		t.Fatalf("deleted = %+v", d)
	}
	if env.Summary != "Deleted 2008 Ford F-150 with 1 upkeep task, 1 upkeep schedule and 1 note. Its 1 bill, 1 loan and 1 document stay, family-level." {
		t.Errorf("summary = %q", env.Summary)
	}
	for _, c := range []struct {
		what, sql string
		args      []any
		want      int
	}{
		{"vehicle", `SELECT count(*) FROM vehicles WHERE id = $1`, []any{v.ID}, 0},
		{"task", `SELECT count(*) FROM maintenance_tasks WHERE id = $1`, []any{task}, 0},
		{"schedule", `SELECT count(*) FROM maintenance_schedules WHERE id = $1`, []any{sched}, 0},
		{"upkeep event", `SELECT count(*) FROM calendar_objects WHERE calendar_id = $1`, []any{cal}, 0},
		{"upkeep delete change", `SELECT count(*) FROM calendar_changes WHERE calendar_id = $1 AND event_uid = 'evt-oil' AND action = 'delete'`, []any{cal}, 1},
		{"bill kept unlinked", `SELECT count(*) FROM bills WHERE id = $1 AND vehicle_id IS NULL`, []any{bill}, 1},
		{"loan kept unlinked", `SELECT count(*) FROM loans WHERE id = $1 AND vehicle_id IS NULL`, []any{loan}, 1},
		{"document moved", `SELECT count(*) FROM files WHERE id = $1 AND entity_type = 'household' AND entity_id = $2`, []any{file, hid}, 1},
		{"note", `SELECT count(*) FROM notes WHERE entity_type = 'vehicle' AND entity_id = $1`, []any{v.ID}, 0},
	} {
		if n := count(t, pool, c.sql, c.args...); n != c.want {
			t.Errorf("%s: got %d, want %d", c.what, n, c.want)
		}
	}

	// Repeat: not found.
	if env := w.del.run(t, ctx, map[string]any{"vehicle_id": v.ID, "confirm": true}); env.OK || !strings.Contains(env.Error.Fix, "vehicle_find") {
		t.Errorf("repeat delete = %+v", env)
	}
	// Only money attached: no confirm needed; the bill stays.
	car := insertID(t, pool, `INSERT INTO vehicles (household_id, year, make, model) VALUES ($1, 2015, 'Subaru', 'Outback') RETURNING id`, hid)
	bill2 := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day, vehicle_id) VALUES ($1, 'Outback insurance', 70, 9, $2) RETURNING id`, hid, car)
	env = w.del.run(t, ctx, map[string]any{"vehicle_id": "outback"})
	if d := decodeData[vehicleDeleted](t, env); !d.Deleted || d.VehicleID != car || d.Kept.BillsUnlinked != 1 || len(env.Next) != 2 {
		t.Errorf("money-only delete = %+v next %v", d, env.Next)
	}
	if n := count(t, pool, `SELECT count(*) FROM bills WHERE id = $1 AND vehicle_id IS NULL`, bill2); n != 1 {
		t.Errorf("bill not kept")
	}
	// Another family's vehicle.
	_, other := testutil.SeedHousehold(t, pool)
	foreign := insertID(t, pool, `INSERT INTO vehicles (household_id, make, model) VALUES ($1, 'Tesla', 'Model 3') RETURNING id`, other.HouseholdID)
	if env := w.del.run(t, ctx, map[string]any{"vehicle_id": foreign, "confirm": true}); env.OK {
		t.Errorf("foreign delete = %+v", env)
	}
	if n := count(t, pool, `SELECT count(*) FROM vehicles WHERE id = $1`, foreign); n != 1 {
		t.Errorf("foreign vehicle deleted")
	}
	w.del.run(t, ctx, map[string]any{}).wantError(t, "vehicle_id")
}

// TestVehicleWriteRoles: family roles write; viewer, housesitter and vendor
// are refused; a demoted member is refused at once; a removed one is not
// signed in.
func TestVehicleWriteRoles(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ownerCtx, hh := testutil.SeedHousehold(t, pool)
	w := newWriteTools(pool)
	bg := context.Background()

	v := w.create.run(t, ownerCtx, map[string]any{"make": "Honda", "model": "Odyssey"}).vehicle(t)

	for _, role := range []string{"family_manager", "family_member"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		d := w.create.run(t, ctx, map[string]any{"make": "Car of", "model": role}).vehicle(t)
		w.update.run(t, ctx, map[string]any{"vehicle_id": d.ID, "current_value": 1}).vehicle(t)
		decodeData[vehicleDeleted](t, w.del.run(t, ctx, map[string]any{"vehicle_id": d.ID}))
	}
	for _, role := range []string{"viewer", "housesitter", "vendor"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		for _, c := range []struct {
			w    writeTool
			args map[string]any
		}{
			{w.create, map[string]any{"make": "Nope", "model": "Nope"}},
			{w.update, map[string]any{"vehicle_id": v.ID, "current_value": 1}},
			{w.del, map[string]any{"vehicle_id": v.ID, "confirm": true}},
		} {
			env := c.w.run(t, ctx, c.args)
			if env.OK || env.Error == nil || !strings.Contains(env.Error.Message, "The "+role+" role can't change vehicles") {
				t.Errorf("%s %s = %+v", role, c.w.tool.Name, env)
			}
		}
	}

	// Demotion: the claims still say family_member, memberships say viewer.
	ctx, uid := memberCtx(t, pool, hh, "family_member")
	if _, err := pool.Exec(bg, `UPDATE memberships SET role = 'viewer' WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if env := w.update.run(t, ctx, map[string]any{"vehicle_id": v.ID, "current_value": 1}); env.OK {
		t.Errorf("demoted member updated a vehicle")
	}
	// Removed: not signed in.
	if _, err := pool.Exec(bg, `DELETE FROM memberships WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if env := w.create.run(t, ctx, map[string]any{"make": "Nope", "model": "Nope"}); env.OK || !strings.Contains(env.Summary, "not signed in") {
		t.Errorf("removed member = %+v", env)
	}
	if n := count(t, pool, `SELECT count(*) FROM vehicles WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("refused calls left %d vehicles, want 1", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM vehicles WHERE id = $1 AND current_value IS NULL`, v.ID); n != 1 {
		t.Errorf("refused calls changed the vehicle")
	}
}
