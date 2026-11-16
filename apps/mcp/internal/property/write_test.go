package property

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/asset"
	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// writeEnv is the JSON a property write tool returns.
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

func (e writeEnv) property(t *testing.T) propertyData { t.Helper(); return decodeData[propertyData](t, e) }

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
	create, update, del, roomDel, find writeTool
}

func newWriteTools(pool *pgxpool.Pool) writeTools {
	wt := func(_ string, tool mcp.Tool, h toolHandler) writeTool { return writeTool{tool, h} }
	return writeTools{
		create:  wt(NewPropertyCreateTool(pool)),
		update:  wt(NewPropertyUpdateTool(pool)),
		del:     wt(NewPropertyDeleteTool(pool)),
		roomDel: wt(NewRoomDeleteTool(pool)),
		find:    wt(NewPropertyFindTool(pool)),
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
// !isError, and no concrete fake data in next hints.
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

func roomNamed(t *testing.T, d propertyData, name string) roomData {
	t.Helper()
	for _, r := range d.Rooms {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("%s has no room %q: %+v", d.Name, name, d.Rooms)
	return roomData{}
}

// TestWriteToolDefinitions pins the write tools to the catalog: names,
// required params, descriptions, all four hints and an outputSchema.
func TestWriteToolDefinitions(t *testing.T) {
	w := newWriteTools(nil)
	fields := []string{"address", "property_type", "purchase_date", "purchase_price", "current_value"}
	for _, tc := range []struct {
		w                      writeTool
		name, desc, title      string
		required, params       []string
		readOnly, destr, idemp bool
	}{
		{w.create, "property_create", "Add a home or other property. Rooms can be added in the same call.", "Add property",
			[]string{"name"}, append([]string{"name", "rooms"}, fields...), false, false, false},
		{w.update, "property_update", "Change a property, add rooms (add_rooms) or rename them (rename_rooms). Use room_delete to remove a room.", "Update property",
			[]string{"property_id"}, append([]string{"property_id", "name", "add_rooms", "rename_rooms"}, fields...), false, false, true},
		{w.del, "property_delete", "Delete a property with its rooms, assets and upkeep. Documents and credentials move to the family. Needs confirm:true.", "Delete property",
			[]string{"property_id"}, []string{"property_id", "confirm"}, false, true, true},
		{w.roomDel, "room_delete", "Delete a room. Its assets stay in the property. Needs confirm:true if the room holds things.", "Delete room",
			[]string{"room_id"}, []string{"room_id", "confirm"}, false, true, true},
	} {
		tool := tc.w.tool
		if tool.Name != tc.name {
			t.Errorf("name = %q, want %q", tool.Name, tc.name)
		}
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
		for _, legacy := range []string{"mortgage_amount", "mortgage_lender", "property_tax_annual", "insurance_annual", "hoa_fee_monthly", "notes"} {
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
		resolvedSchema(t, tool)
	}
}

// TestPropertyRoomAsset is the ticket's acceptance path: create a property
// with rooms, add a room, and link an existing (family-level) asset to the
// new room with asset_update; the asset's property follows the room.
func TestPropertyRoomAsset(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	w := newWriteTools(pool)
	_, aut, auh := asset.NewAssetUpdateTool(pool, nil)
	assetUpdate := writeTool{aut, auh}

	env := w.create.run(t, ctx, map[string]any{
		"name": "Lake House", "address": "1 Shore Rd", "property_type": "cabin",
		"purchase_date": "2019-06-14", "purchase_price": "$320,000", "current_value": 450000,
		"rooms": []any{"Kitchen", " Living room ", "kitchen", ""},
	})
	p := env.property(t)
	if p.Name != "Lake House" || p.Address == nil || *p.Address != "1 Shore Rd" || p.PurchaseDate == nil || *p.PurchaseDate != "2019-06-14" ||
		p.PurchasePrice == nil || *p.PurchasePrice != 320000 || p.CurrentValue == nil || *p.CurrentValue != 450000 {
		t.Fatalf("created = %+v", p)
	}
	if len(p.Rooms) != 2 || p.Counts.Rooms != 2 || strings.Join(p.RoomsAdded, ",") != "Kitchen,Living room" {
		t.Fatalf("rooms = %+v added %v", p.Rooms, p.RoomsAdded)
	}
	if !strings.HasPrefix(env.Summary, "Added Lake House with rooms Kitchen and Living room:") {
		t.Errorf("summary = %q", env.Summary)
	}
	kitchen := roomNamed(t, p, "Kitchen")
	if !strings.Contains(env.Next[0], "room_id="+kitchen.ID) || !strings.Contains(env.Next[0], "asset_update asset_id=…") {
		t.Errorf("next = %v", env.Next)
	}
	// The legacy finance columns are never written (#4704).
	if n := count(t, pool, `SELECT count(*) FROM properties WHERE id = $1 AND mortgage_amount IS NULL AND property_tax_annual IS NULL
		AND insurance_annual IS NULL AND hoa_fee_monthly IS NULL AND down_payment IS NULL`, p.ID); n != 1 {
		t.Errorf("legacy finance columns written")
	}
	if n := count(t, pool, `SELECT count(*) FROM rooms WHERE property_id = $1 AND household_id = $2`, p.ID, hh.HouseholdID); n != 2 {
		t.Errorf("rooms household_id not set: %d", n)
	}

	// Add a room.
	env = w.update.run(t, ctx, map[string]any{"property_id": "lake house", "add_rooms": []any{"Boathouse"}})
	p = env.property(t)
	boathouse := roomNamed(t, p, "Boathouse")
	if strings.Join(p.Changed, ",") != "add_rooms" || p.Counts.Rooms != 3 {
		t.Fatalf("after add_rooms = %+v", p)
	}

	// Link an existing asset (no property) to the new room.
	thing := insertID(t, pool, `INSERT INTO assets (household_id, name, category) VALUES ($1, 'Kayak', 'outdoor') RETURNING id`, hh.HouseholdID)
	aenv := assetUpdate.run(t, ctx, map[string]any{"asset_id": thing, "room_id": boathouse.ID})
	if !aenv.OK {
		t.Fatalf("asset_update = %+v", aenv)
	}
	if n := count(t, pool, `SELECT count(*) FROM assets WHERE id = $1 AND room_id = $2 AND property_id = $3`, thing, boathouse.ID, p.ID); n != 1 {
		t.Fatalf("asset not linked to the room and property")
	}
	fenv := call(t, ctx, w.find.h, map[string]any{"id": p.ID})
	got := fenv.properties(t)[0]
	if got.Counts.Assets != 1 || roomNamed(t, got, "Boathouse").AssetCount != 1 {
		t.Errorf("property_find after link = %+v", got)
	}

	// Loop guard: the same name within 10 minutes returns the property.
	env = w.create.run(t, ctx, map[string]any{"name": "LAKE HOUSE", "rooms": []any{"Attic"}})
	dup := env.property(t)
	if !dup.Duplicate || dup.ID != p.ID || !strings.Contains(env.Next[0], "property_update property_id="+p.ID) {
		t.Errorf("duplicate = %+v next %v", dup, env.Next)
	}
	if n := count(t, pool, `SELECT count(*) FROM properties WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("loop guard made %d properties", n)
	}

	// Validation.
	w.create.run(t, ctx, map[string]any{"address": "nowhere"}).wantError(t, "name")
	w.create.run(t, ctx, map[string]any{"name": "X", "purchase_price": "lots"}).wantError(t, "purchase_price")
	w.create.run(t, ctx, map[string]any{"name": "X", "current_value": -5}).wantError(t, "current_value")
	w.create.run(t, ctx, map[string]any{"name": "X", "purchase_date": "June 2019"}).wantError(t, "purchase_date")
	w.create.run(t, ctx, map[string]any{"name": "X", "rooms": 7}).wantError(t, "rooms")
	if n := count(t, pool, `SELECT count(*) FROM properties WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("invalid calls created properties")
	}
}

// TestPropertyCreateParallel: parallel identical calls make one property.
func TestPropertyCreateParallel(t *testing.T) {
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
			req.Params.Arguments = map[string]any{"name": "Beach condo", "rooms": []any{"Kitchen"}}
			if res, err := w.create.h(ctx, req); err != nil || res.IsError {
				t.Errorf("parallel create failed: %v %+v", err, res)
			}
		}()
	}
	wg.Wait()
	if n := count(t, pool, `SELECT count(*) FROM properties WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("parallel creates made %d properties", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM rooms WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("parallel creates made %d rooms", n)
	}
}

// TestPropertyUpdate: partial update with null clears, a no-op writes
// nothing, add_rooms skips names the property has, rename_rooms by name or
// id with conflict checks, name-or-id resolution with an ambiguity error,
// and another family's property is not found.
func TestPropertyUpdate(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	w := newWriteTools(pool)
	bg := context.Background()

	p := w.create.run(t, ctx, map[string]any{"name": "Home", "address": "12 Oak St", "property_type": "house",
		"current_value": 400000, "rooms": []any{"Kitchen", "Den"}}).property(t)
	den := roomNamed(t, p, "Den")

	env := w.update.run(t, ctx, map[string]any{"property_id": p.ID, "current_value": "425000", "address": nil, "property_type": ""})
	u := env.property(t)
	if strings.Join(u.Changed, ",") != "address,current_value" || u.Address != nil || u.PropertyType == nil || *u.PropertyType != "house" ||
		u.CurrentValue == nil || *u.CurrentValue != 425000 {
		t.Fatalf("partial update = %+v", u)
	}
	if !strings.HasPrefix(env.Summary, "Updated Home (address, current_value):") {
		t.Errorf("summary = %q", env.Summary)
	}

	// The same call again changes nothing and writes nothing.
	var before, after string
	_ = pool.QueryRow(bg, `SELECT updated_at::text FROM properties WHERE id = $1`, p.ID).Scan(&before)
	env = w.update.run(t, ctx, map[string]any{"property_id": "HOME", "current_value": 425000, "add_rooms": []any{"kitchen"}})
	_ = pool.QueryRow(bg, `SELECT updated_at::text FROM properties WHERE id = $1`, p.ID).Scan(&after)
	u = env.property(t)
	if len(u.Changed) != 0 || before != after || !strings.HasPrefix(env.Summary, "Nothing changed") ||
		strings.Join(u.RoomsSkipped, ",") != "kitchen" || u.Counts.Rooms != 2 {
		t.Errorf("no-op update = %+v (%s); updated_at %s -> %s", u, env.Summary, before, after)
	}

	// Rename by name and by id; add in the same call.
	env = w.update.run(t, ctx, map[string]any{"property_id": p.ID,
		"rename_rooms": []any{map[string]any{"room_id": "kitchen", "name": "Big kitchen"}, map[string]any{"room_id": den.ID, "name": "Office"}},
		"add_rooms":    []any{"Den", "Office"}})
	u = env.property(t)
	if strings.Join(u.Changed, ",") != "rename_rooms,add_rooms" || strings.Join(u.RoomsAdded, ",") != "Den" ||
		strings.Join(u.RoomsSkipped, ",") != "Office" || u.Counts.Rooms != 3 {
		t.Fatalf("rename+add = %+v", u)
	}
	if roomNamed(t, u, "Office").ID != den.ID {
		t.Errorf("rename by id renamed the wrong room")
	}
	roomNamed(t, u, "Big kitchen")
	if !strings.Contains(env.Summary, "renamed Kitchen → Big kitchen and Den → Office") {
		t.Errorf("summary = %q", env.Summary)
	}

	// Rename conflicts and unknown rooms.
	w.update.run(t, ctx, map[string]any{"property_id": p.ID,
		"rename_rooms": []any{map[string]any{"room_id": "Office", "name": "big KITCHEN"}}}).wantError(t, "rename_rooms")
	w.update.run(t, ctx, map[string]any{"property_id": p.ID,
		"rename_rooms": []any{map[string]any{"room_id": "Pantry", "name": "Larder"}}}).wantError(t, "rename_rooms")
	w.update.run(t, ctx, map[string]any{"property_id": p.ID,
		"rename_rooms": []any{map[string]any{"room_id": uuid.NewString(), "name": "Larder"}}}).wantError(t, "rename_rooms")
	w.update.run(t, ctx, map[string]any{"property_id": p.ID, "rename_rooms": "Office"}).wantError(t, "rename_rooms")

	// name cannot be cleared; no fields is an error.
	w.update.run(t, ctx, map[string]any{"property_id": p.ID, "name": nil}).wantError(t, "name")
	w.update.run(t, ctx, map[string]any{"property_id": p.ID}).wantError(t, "")
	w.update.run(t, ctx, map[string]any{"current_value": 1}).wantError(t, "property_id")

	// Name resolution: unknown lists the family's properties; ambiguity lists ids.
	env = w.update.run(t, ctx, map[string]any{"property_id": "Castle", "current_value": 1})
	env.wantError(t, "property_id")
	if !strings.Contains(env.Error.Message, "Home") {
		t.Errorf("unknown name message = %q", env.Error.Message)
	}
	second := insertID(t, pool, `INSERT INTO properties (household_id, name, created_at) VALUES ($1, 'home', now() - interval '1 day') RETURNING id`, hh.HouseholdID)
	env = w.update.run(t, ctx, map[string]any{"property_id": "Home", "current_value": 1})
	env.wantError(t, "property_id")
	if !strings.Contains(env.Error.Message, p.ID) || !strings.Contains(env.Error.Message, second) {
		t.Errorf("ambiguity message = %q", env.Error.Message)
	}

	// Another family's property: not found, unchanged.
	_, other := testutil.SeedHousehold(t, pool)
	foreign := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Theirs') RETURNING id`, other.HouseholdID)
	if env := w.update.run(t, ctx, map[string]any{"property_id": foreign, "add_rooms": []any{"Spy room"}}); env.OK {
		t.Errorf("foreign update = %+v", env)
	}
	if n := count(t, pool, `SELECT count(*) FROM rooms WHERE property_id = $1`, foreign); n != 0 {
		t.Errorf("rooms added to another family's property")
	}
	// Another family's room id can't be renamed through our property.
	foreignRoom := insertID(t, pool, `INSERT INTO rooms (property_id, household_id, name) VALUES ($1, $2, 'Den') RETURNING id`, foreign, other.HouseholdID)
	w.update.run(t, ctx, map[string]any{"property_id": p.ID,
		"rename_rooms": []any{map[string]any{"room_id": foreignRoom, "name": "Mine"}}}).wantError(t, "rename_rooms")
}

// TestPropertyDelete: with things attached, the call is refused with the
// counts until confirm:true; then rooms, assets, upkeep and the property's
// calendars go, notes go, documents move to the family, bills stay without
// the property, and upkeep events on family calendars are recorded as
// deletes. An empty property deletes without confirm; a repeat is not found.
func TestPropertyDelete(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	w := newWriteTools(pool)
	hid := hh.HouseholdID

	p := w.create.run(t, ctx, map[string]any{"name": "Lake House", "rooms": []any{"Kitchen", "Den"}}).property(t)
	kitchen := roomNamed(t, p, "Kitchen")
	stove := insertID(t, pool, `INSERT INTO assets (household_id, name, category, room_id) VALUES ($1, 'Stove', 'appliance', $2) RETURNING id`, hid, kitchen.ID)
	insertID(t, pool, `INSERT INTO assets (household_id, name, category, property_id) VALUES ($1, 'Dock', 'outdoor', $2) RETURNING id`, hid, p.ID)
	task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, asset_id) VALUES ($1, 'Clean stove', current_date + 7, $2) RETURNING id`, hid, stove)
	bill := insertID(t, pool, `INSERT INTO bills (household_id, name, amount, due_day, property_id) VALUES ($1, 'Lake electric', 80, 5, $2) RETURNING id`, hid, p.ID)
	loan := insertID(t, pool, `INSERT INTO loans (household_id, name, original_amount, remaining_balance, property_id) VALUES ($1, 'Lake mortgage', 200000, 150000, $2) RETURNING id`, hid, p.ID)
	if _, err := pool.Exec(context.Background(), `INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'room', $2, 'Paint', 'Eggshell')`, hid, kitchen.ID); err != nil {
		t.Fatal(err)
	}
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id)
		VALUES ($1, $2, 'deed.pdf', 1, 'property', $3) RETURNING id`, hid, blob, p.ID)
	manual := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id)
		VALUES ($1, $2, 'stove.pdf', 1, 'asset', $3) RETURNING id`, hid, blob, stove)
	// A family maintenance calendar with the task's event, and the property's own calendar.
	famCal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Maintenance', 'maintenance', $2) RETURNING id`, hid, uuid.NewString())
	insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
		VALUES ($1, 'evt-clean-stove', '{}', 'e0', 'maintenance', 'maintenance_task', $2) RETURNING id`, famCal, task)
	propCal := insertID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid, property_id) VALUES ($1, 'Lake House', 'custom', $2, $3) RETURNING id`, hid, uuid.NewString(), p.ID)
	insertID(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type) VALUES ($1, 'evt-lake', '{}', 'e0', 'event') RETURNING id`, propCal)

	// No confirm: refused with the counts, nothing deleted.
	env := w.del.run(t, ctx, map[string]any{"property_id": "Lake House"})
	env.wantError(t, "confirm")
	for _, want := range []string{"2 rooms", "2 assets", "1 upkeep task", "1 calendar", "1 note", "1 bill", "1 loan", "2 documents"} {
		if !strings.Contains(env.Error.Message, want) {
			t.Errorf("refusal %q lacks %q", env.Error.Message, want)
		}
	}
	if env.Error.Example["confirm"] != true || env.Error.Example["property_id"] != p.ID {
		t.Errorf("refusal example = %v", env.Error.Example)
	}
	if n := count(t, pool, `SELECT count(*) FROM properties WHERE id = $1`, p.ID); n != 1 {
		t.Fatalf("refused delete removed the property")
	}

	env = w.del.run(t, ctx, map[string]any{"property_id": p.ID, "confirm": "true"})
	d := decodeData[propertyDeleted](t, env)
	if !d.Deleted || d.Removed.Rooms != 2 || d.Removed.Assets != 2 || d.Removed.UpkeepTasks != 1 || d.Removed.Calendars != 1 ||
		d.Removed.Events != 1 || d.Removed.NotesDeleted != 1 || d.Kept.BillsUnlinked != 1 || d.Kept.LoansUnlinked != 1 || d.Kept.DocumentsMoved != 2 {
		t.Fatalf("deleted = %+v", d)
	}
	for _, c := range []struct {
		what, sql string
		args      []any
		want      int
	}{
		{"property", `SELECT count(*) FROM properties WHERE id = $1`, []any{p.ID}, 0},
		{"rooms", `SELECT count(*) FROM rooms WHERE property_id = $1`, []any{p.ID}, 0},
		{"assets", `SELECT count(*) FROM assets WHERE household_id = $1`, []any{hid}, 0},
		{"task", `SELECT count(*) FROM maintenance_tasks WHERE id = $1`, []any{task}, 0},
		{"property calendar", `SELECT count(*) FROM calendars WHERE id = $1`, []any{propCal}, 0},
		{"family calendar", `SELECT count(*) FROM calendars WHERE id = $1`, []any{famCal}, 1},
		{"upkeep event", `SELECT count(*) FROM calendar_objects WHERE calendar_id = $1`, []any{famCal}, 0},
		{"upkeep delete change", `SELECT count(*) FROM calendar_changes WHERE calendar_id = $1 AND event_uid = 'evt-clean-stove' AND action = 'delete'`, []any{famCal}, 1},
		{"bill kept unlinked", `SELECT count(*) FROM bills WHERE id = $1 AND property_id IS NULL`, []any{bill}, 1},
		{"loan kept unlinked", `SELECT count(*) FROM loans WHERE id = $1 AND property_id IS NULL`, []any{loan}, 1},
		{"documents moved", `SELECT count(*) FROM files WHERE id IN ($1, $2) AND entity_type = 'household' AND entity_id = $3`, []any{file, manual, hid}, 2},
		{"room note", `SELECT count(*) FROM notes WHERE entity_type = 'room' AND entity_id = $1`, []any{kitchen.ID}, 0},
	} {
		if n := count(t, pool, c.sql, c.args...); n != c.want {
			t.Errorf("%s: got %d, want %d", c.what, n, c.want)
		}
	}
	if !strings.HasPrefix(env.Summary, "Deleted Lake House (had 2 rooms, 2 assets") {
		t.Errorf("summary = %q", env.Summary)
	}

	// Repeat: not found.
	if env := w.del.run(t, ctx, map[string]any{"property_id": p.ID, "confirm": true}); env.OK || !strings.Contains(env.Error.Fix, "property_find") {
		t.Errorf("repeat delete = %+v", env)
	}
	// An empty property deletes without confirm.
	empty := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Lot 7') RETURNING id`, hid)
	if d := decodeData[propertyDeleted](t, w.del.run(t, ctx, map[string]any{"property_id": "lot 7"})); !d.Deleted || d.PropertyID != empty {
		t.Errorf("empty delete = %+v", d)
	}
	// Another family's property.
	_, other := testutil.SeedHousehold(t, pool)
	foreign := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Theirs') RETURNING id`, other.HouseholdID)
	if env := w.del.run(t, ctx, map[string]any{"property_id": foreign, "confirm": true}); env.OK {
		t.Errorf("foreign delete = %+v", env)
	}
	if n := count(t, pool, `SELECT count(*) FROM properties WHERE id = $1`, foreign); n != 1 {
		t.Errorf("foreign property deleted")
	}
	w.del.run(t, ctx, map[string]any{}).wantError(t, "property_id")
}

// TestRoomDelete: a room holding things needs confirm:true; its assets stay
// in the property without a room; documents move to the family; an empty
// room deletes without confirm; a name shared by rooms in two properties is
// an ambiguity error listing both.
func TestRoomDelete(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	w := newWriteTools(pool)
	hid := hh.HouseholdID

	home := w.create.run(t, ctx, map[string]any{"name": "Home", "rooms": []any{"Kitchen", "Den", "Attic"}}).property(t)
	cabin := w.create.run(t, ctx, map[string]any{"name": "Cabin", "rooms": []any{"Kitchen"}}).property(t)
	den := roomNamed(t, home, "Den")
	tv := insertID(t, pool, `INSERT INTO assets (household_id, name, category, room_id) VALUES ($1, 'TV', 'electronics', $2) RETURNING id`, hid, den.ID)
	blob := insertID(t, pool, `INSERT INTO file_blobs (data, size) VALUES ('x', 1) RETURNING id`)
	file := insertID(t, pool, `INSERT INTO files (household_id, blob_id, name, size, entity_type, entity_id)
		VALUES ($1, $2, 'den-plan.pdf', 1, 'room', $3) RETURNING id`, hid, blob, den.ID)

	// Ambiguous name.
	env := w.roomDel.run(t, ctx, map[string]any{"room_id": "kitchen"})
	env.wantError(t, "room_id")
	if !strings.Contains(env.Error.Message, "Kitchen in Home") || !strings.Contains(env.Error.Message, "Kitchen in Cabin") ||
		!strings.Contains(env.Error.Message, roomNamed(t, cabin, "Kitchen").ID) {
		t.Errorf("ambiguity = %q", env.Error.Message)
	}

	// Needs confirm while it holds a thing.
	env = w.roomDel.run(t, ctx, map[string]any{"room_id": "Den"})
	env.wantError(t, "confirm")
	if !strings.Contains(env.Error.Message, "1 asset and 1 document") {
		t.Errorf("refusal = %q", env.Error.Message)
	}
	env = w.roomDel.run(t, ctx, map[string]any{"room_id": "Den", "confirm": true})
	d := decodeData[roomDeleted](t, env)
	if !d.Deleted || d.RoomID != den.ID || d.PropertyID != home.ID || d.AssetsKept != 1 || d.DocumentsMoved != 1 {
		t.Fatalf("deleted = %+v", d)
	}
	if env.Summary != "Deleted the Den in Home. Its things (1 asset) stay in Home without a room." {
		t.Errorf("summary = %q", env.Summary)
	}
	if n := count(t, pool, `SELECT count(*) FROM assets WHERE id = $1 AND room_id IS NULL AND property_id = $2`, tv, home.ID); n != 1 {
		t.Errorf("asset did not stay in the property")
	}
	if n := count(t, pool, `SELECT count(*) FROM files WHERE id = $1 AND entity_type = 'household'`, file); n != 1 {
		t.Errorf("room document not moved to the family")
	}

	// Empty room: no confirm needed; repeat is not found.
	attic := roomNamed(t, home, "Attic")
	decodeData[roomDeleted](t, w.roomDel.run(t, ctx, map[string]any{"room_id": attic.ID}))
	if env := w.roomDel.run(t, ctx, map[string]any{"room_id": attic.ID}); env.OK || !strings.Contains(env.Error.Fix, "property_find") {
		t.Errorf("repeat = %+v", env)
	}
	w.roomDel.run(t, ctx, map[string]any{"room_id": "Sauna"}).wantError(t, "room_id")
	w.roomDel.run(t, ctx, map[string]any{}).wantError(t, "room_id")

	// Another family's room.
	_, other := testutil.SeedHousehold(t, pool)
	op := insertID(t, pool, `INSERT INTO properties (household_id, name) VALUES ($1, 'Theirs') RETURNING id`, other.HouseholdID)
	or := insertID(t, pool, `INSERT INTO rooms (property_id, household_id, name) VALUES ($1, $2, 'Den') RETURNING id`, op, other.HouseholdID)
	if env := w.roomDel.run(t, ctx, map[string]any{"room_id": or, "confirm": true}); env.OK {
		t.Errorf("foreign room delete = %+v", env)
	}
	if n := count(t, pool, `SELECT count(*) FROM rooms WHERE id = $1`, or); n != 1 {
		t.Errorf("foreign room deleted")
	}
}

// TestWriteRoles: owner, family_manager and family_member write; viewer,
// housesitter and vendor are refused; the role is re-read per call, so a
// demotion applies at once and a removed member is not signed in.
func TestWriteRoles(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	ownerCtx, hh := testutil.SeedHousehold(t, pool)
	w := newWriteTools(pool)
	bg := context.Background()

	p := w.create.run(t, ownerCtx, map[string]any{"name": "Home", "rooms": []any{"Kitchen"}}).property(t)
	kitchen := roomNamed(t, p, "Kitchen")

	for _, role := range []string{"family_manager", "family_member"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		d := w.create.run(t, ctx, map[string]any{"name": "Place of " + role, "rooms": []any{"Hall"}}).property(t)
		w.update.run(t, ctx, map[string]any{"property_id": d.ID, "current_value": 1}).property(t)
		decodeData[roomDeleted](t, w.roomDel.run(t, ctx, map[string]any{"room_id": roomNamed(t, d, "Hall").ID}))
		decodeData[propertyDeleted](t, w.del.run(t, ctx, map[string]any{"property_id": d.ID}))
	}
	for _, role := range []string{"viewer", "housesitter", "vendor"} {
		ctx, _ := memberCtx(t, pool, hh, role)
		for _, c := range []struct {
			w    writeTool
			args map[string]any
		}{
			{w.create, map[string]any{"name": "Nope"}},
			{w.update, map[string]any{"property_id": p.ID, "current_value": 1}},
			{w.del, map[string]any{"property_id": p.ID, "confirm": true}},
			{w.roomDel, map[string]any{"room_id": kitchen.ID, "confirm": true}},
		} {
			env := c.w.run(t, ctx, c.args)
			if env.OK || env.Error == nil || !strings.Contains(env.Error.Message, "The "+role+" role can't change properties") {
				t.Errorf("%s %s = %+v", role, c.w.tool.Name, env)
			}
		}
	}

	// Demotion: the claims still say family_member, memberships say viewer.
	ctx, uid := memberCtx(t, pool, hh, "family_member")
	if _, err := pool.Exec(bg, `UPDATE memberships SET role = 'viewer' WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if env := w.update.run(t, ctx, map[string]any{"property_id": p.ID, "current_value": 1}); env.OK {
		t.Errorf("demoted member updated a property")
	}
	// Removed: not signed in.
	if _, err := pool.Exec(bg, `DELETE FROM memberships WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if env := w.create.run(t, ctx, map[string]any{"name": "Nope"}); env.OK || !strings.Contains(env.Summary, "not signed in") {
		t.Errorf("removed member = %+v", env)
	}
	if n := count(t, pool, `SELECT count(*) FROM properties WHERE household_id = $1`, hh.HouseholdID); n != 1 {
		t.Errorf("refused calls left %d properties, want 1", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM properties WHERE id = $1 AND current_value IS NULL`, p.ID); n != 1 {
		t.Errorf("refused calls changed the property")
	}
}
