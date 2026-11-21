package bill

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/testutil"
)

type toolHandler = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// callTool invokes handler and returns the result's single text item.
func callTool(t *testing.T, ctx context.Context, handler toolHandler, name string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := handler(ctx, req)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("%s: want 1 content item, got %d", name, len(res.Content))
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("%s: want TextContent, got %T", name, res.Content[0])
	}
	checkShape(t, name, res, text.Text)
	return res, text.Text
}

func countBills(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM bills`).Scan(&n); err != nil {
		t.Fatalf("count bills: %v", err)
	}
	return n
}

// TestBillCreate_VisibleToReadTools creates a bill and checks it shows up in
// bill_find and bill_find due_within_days for its household only.
func TestBillCreate_VisibleToReadTools(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)

	// Due today (by the database clock) so bill_find due_within_days must include it.
	var today time.Time
	if err := pool.QueryRow(context.Background(), `SELECT CURRENT_DATE`).Scan(&today); err != nil {
		t.Fatalf("current date: %v", err)
	}

	var vendorID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO vendors (household_id, name) VALUES ($1, 'Power Co') RETURNING id`,
		hh.HouseholdID,
	).Scan(&vendorID); err != nil {
		t.Fatalf("insert vendor: %v", err)
	}
	var propertyID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO properties (household_id, name) VALUES ($1, 'Lake house') RETURNING id`,
		hh.HouseholdID,
	).Scan(&propertyID); err != nil {
		t.Fatalf("insert property: %v", err)
	}

	_, _, create := NewBillCreateTool(pool)
	res, text := callTool(t, ctx, create, "bill_create", map[string]any{
		"name":        "  Electric  ",
		"amount":      125.5,
		"due_day":     float64(today.Day()),
		"category":    "Utilities",
		"autopay":     true,
		"contact_id":  vendorID.String(),
		"property_id": propertyID.String(),
		"payment_url": "https://power.example.com/pay",
		"notes":       "Paperless billing",
	})
	if res.IsError {
		t.Fatalf("bill_create returned tool error: %s", text)
	}

	var got billData
	if err := unmarshalData(text, &got); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	if got.ID == "" || got.Name != "Electric" || got.Amount == nil || *got.Amount != 125.5 || got.DueDay == nil || *got.DueDay != today.Day() {
		t.Errorf("unexpected bill: %+v", got)
	}
	if !got.Autopay || got.Category == nil || *got.Category != "Utilities" {
		t.Errorf("autopay/category not stored: %+v", got)
	}
	if got.ContactID == nil || *got.ContactID != vendorID.String() || got.PropertyID == nil || *got.PropertyID != propertyID.String() ||
		strOrNil(got.SubjectType) != "property" || strOrNil(got.SubjectName) != "Lake house" || got.VehicleID != nil || got.AssetID != nil {
		t.Errorf("vendor/subject not stored: %+v", got)
	}
	if want := today.Format("2006-01-02"); strOrNil(got.NextDueDate) != want || got.DaysUntilDue == nil || *got.DaysUntilDue != 0 {
		t.Errorf("next due = %s (+%v days), want %s (+0)", strOrNil(got.NextDueDate), got.DaysUntilDue, want)
	}

	var rrule *string
	var events int
	if err := pool.QueryRow(context.Background(),
		`SELECT b.rrule, (SELECT COUNT(*) FROM outbox_events e
		                  WHERE e.type = 'bill.created' AND e.payload->>'id' = b.id::text
		                    AND e.household_id = b.household_id)
		 FROM bills b WHERE b.id = $1 AND b.household_id = $2`,
		got.ID, hh.HouseholdID,
	).Scan(&rrule, &events); err != nil {
		t.Fatalf("read back bill: %v", err)
	}
	if rrule != nil {
		t.Errorf("rrule = %q, want NULL", *rrule)
	}
	if events != 1 {
		t.Errorf("bill.created outbox events = %d, want 1", events)
	}

	type billRow struct {
		ID uuid.UUID `json:"id"`
	}
	ids := func(t *testing.T, ctx context.Context, handler toolHandler, name string, args map[string]any) []uuid.UUID {
		t.Helper()
		_, text := callTool(t, ctx, handler, name, args)
		var rows []billRow
		if err := unmarshalData(text, &rows); err != nil {
			t.Fatalf("%s: decode %q: %v", name, text, err)
		}
		out := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			out[i] = r.ID
		}
		return out
	}

	_, _, list := NewBillFindTool(pool)
	_, _, upcoming := NewBillFindTool(pool)

	tests := []struct {
		name    string
		ctx     context.Context
		handler toolHandler
		tool    string
		args    map[string]any
		want    int
	}{
		{"bill_find in own household", ctx, list, "bill_find", nil, 1},
		{"bill_find by category", ctx, list, "bill_find", map[string]any{"category": "Utilities"}, 1},
		{"bill_find due_within_days in own household", ctx, upcoming, "bill_find", map[string]any{"due_within_days": float64(7)}, 1},
		{"bill_find in other household", otherCtx, list, "bill_find", nil, 0},
		{"bill_find due_within_days in other household", otherCtx, upcoming, "bill_find", map[string]any{"due_within_days": float64(7)}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got2 := ids(t, tc.ctx, tc.handler, tc.tool, tc.args)
			if len(got2) != tc.want {
				t.Fatalf("got %d bills (%v), want %d", len(got2), got2, tc.want)
			}
			if tc.want == 1 && got2[0].String() != got.ID {
				t.Errorf("got bill %s, want %s", got2[0], got.ID)
			}
		})
	}
}

// TestBillCreate_MinimalFields stores NULL for every optional field.
func TestBillCreate_MinimalFields(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)

	_, _, create := NewBillCreateTool(pool)
	res, text := callTool(t, ctx, create, "bill_create", map[string]any{
		"name": "Netflix", "amount": float64(0), "due_day": float64(15),
	})
	if res.IsError {
		t.Fatalf("bill_create returned tool error: %s", text)
	}
	var got billData
	if err := unmarshalData(text, &got); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	if got.Autopay || got.Category != nil || got.ContactID != nil || got.PropertyID != nil || got.VehicleID != nil ||
		got.AssetID != nil || got.SubjectType != nil || got.SubjectName != nil || got.PaymentURL != nil || got.Notes != nil {
		t.Errorf("optional fields should be empty: %s", text)
	}
	if got.NextDueDate == nil || got.NextDueWeekday == nil {
		t.Error("next_due_date missing")
	}
}

// TestBillCreate_Validation checks that bad input is a tool error and
// inserts nothing.
func TestBillCreate_Validation(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	_, other := testutil.SeedHousehold(t, pool)

	var otherVendor uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO vendors (household_id, name) VALUES ($1, 'Not mine') RETURNING id`,
		other.HouseholdID,
	).Scan(&otherVendor); err != nil {
		t.Fatalf("insert vendor: %v", err)
	}

	valid := func(overrides map[string]any) map[string]any {
		args := map[string]any{"name": "Water", "amount": 40.0, "due_day": 10.0}
		for k, v := range overrides {
			if v == nil {
				delete(args, k)
			} else {
				args[k] = v
			}
		}
		return args
	}

	_, _, create := NewBillCreateTool(pool)

	tests := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"missing name", valid(map[string]any{"name": nil}), "name is required"},
		{"blank name", valid(map[string]any{"name": "   "}), "name is required"},
		{"name not a string", valid(map[string]any{"name": 5.0}), "name must be a string"},
		{"name too long", valid(map[string]any{"name": strings.Repeat("x", 256)}), "name must be at most"},
		{"missing amount", valid(map[string]any{"amount": nil}), "amount is required"},
		{"negative amount", valid(map[string]any{"amount": -1.0}), "amount must be between"},
		{"amount too large", valid(map[string]any{"amount": 1e11}), "amount must be between"},
		{"amount not a number", valid(map[string]any{"amount": "12"}), "amount must be a number"},
		{"missing due_day", valid(map[string]any{"due_day": nil}), "due_day is required"},
		{"due_day zero", valid(map[string]any{"due_day": 0.0}), "due_day must be a whole number"},
		{"due_day 32", valid(map[string]any{"due_day": 32.0}), "due_day must be a whole number"},
		{"due_day fractional", valid(map[string]any{"due_day": 1.5}), "due_day must be a whole number"},
		{"autopay not a bool", valid(map[string]any{"autopay": "yes"}), "autopay must be a boolean"},
		{"contact_id not a uuid", valid(map[string]any{"contact_id": "abc"}), "contact_id must be a UUID"},
		{"vendor from another household", valid(map[string]any{"contact_id": otherVendor.String()}), "No contact with id"},
		{"entity_type without entity_id", valid(map[string]any{"entity_type": "vehicle"}), "entity_type needs entity_id"},
		{"entity_id without entity_type", valid(map[string]any{"entity_id": uuid.NewString()}), "entity_id was given without entity_type"},
		{"legacy pet subject", valid(map[string]any{"entity_type": "pet", "entity_id": uuid.NewString()}), "can't be tied to a"},
		{"legacy id not a uuid", valid(map[string]any{"entity_type": "property", "entity_id": "x"}), "property_id must be a UUID"},
		{"property_id not a uuid", valid(map[string]any{"property_id": "Home"}), "property_id must be a UUID"},
		{"vehicle with property", valid(map[string]any{"vehicle_id": uuid.NewString(), "property_id": uuid.NewString()}), "can't also be for a property"},
		{"unknown property", valid(map[string]any{"property_id": uuid.NewString()}), "No property with id"},
		{"unknown vehicle", valid(map[string]any{"vehicle_id": uuid.NewString()}), "No vehicle with id"},
		{"unknown asset", valid(map[string]any{"asset_id": uuid.NewString()}), "No asset with id"},
		{"payment_url not http", valid(map[string]any{"payment_url": "javascript:alert(1)"}), "payment_url must be"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, text := callTool(t, ctx, create, "bill_create", tc.args)
			if !res.IsError {
				t.Fatalf("want tool error, got %s", text)
			}
			if !strings.Contains(text, tc.wantErr) {
				t.Errorf("error %q does not contain %q", text, tc.wantErr)
			}
		})
	}

	if n := countBills(t, pool); n != 0 {
		t.Errorf("invalid calls inserted %d bills, want 0", n)
	}

	t.Run("no claims", func(t *testing.T) {
		res, _ := callTool(t, context.Background(), create, "bill_create", valid(nil))
		if !res.IsError {
			t.Fatal("want tool error without claims")
		}
		if n := countBills(t, pool); n != 0 {
			t.Errorf("unauthorized call inserted %d bills", n)
		}
	})
}

func TestNextDueDate(t *testing.T) {
	d := func(s string) time.Time {
		tm, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	tests := []struct {
		today  string
		dueDay int
		want   string
	}{
		{"2026-11-15", 15, "2026-11-15"},
		{"2026-11-15", 20, "2026-11-20"},
		{"2026-11-15", 1, "2026-12-01"},
		{"2026-11-15", 31, "2026-11-30"}, // 30-day month: last day
		{"2026-11-30", 31, "2026-11-30"},
		{"2026-12-15", 10, "2027-01-10"}, // year rollover
		{"2027-01-31", 30, "2027-02-28"}, // into February
		{"2028-01-31", 30, "2028-02-29"}, // leap year
		{"2027-02-28", 31, "2027-02-28"},
	}
	for _, tc := range tests {
		if got := nextDueDate(d(tc.today), tc.dueDay).Format("2006-01-02"); got != tc.want {
			t.Errorf("nextDueDate(%s, %d) = %s, want %s", tc.today, tc.dueDay, got, tc.want)
		}
	}
}

// createTestBill creates a fully populated bill through bill_create, named
// Electric unless a name is given. It passes the hidden aliases entity_type,
// entity_id (mapped onto property_id, for a new property in the vendor's
// family) and payment_url too.
func createTestBill(t *testing.T, ctx context.Context, pool *pgxpool.Pool, vendorID uuid.UUID, name ...string) billData {
	t.Helper()
	billName := "Electric"
	if len(name) > 0 {
		billName = name[0]
	}
	var propertyID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO properties (household_id, name) SELECT household_id, 'Home' FROM vendors WHERE id = $1 RETURNING id`,
		vendorID,
	).Scan(&propertyID); err != nil {
		t.Fatalf("insert property: %v", err)
	}
	_, _, create := NewBillCreateTool(pool)
	res, text := callTool(t, ctx, create, "bill_create", map[string]any{
		"name":        billName,
		"amount":      125.5,
		"due_day":     10.0,
		"category":    "Utilities",
		"autopay":     true,
		"contact_id":  vendorID.String(),
		"entity_type": "property",
		"entity_id":   propertyID.String(),
		"payment_url": "https://power.example.com/pay",
		"notes":       "Paperless billing",
	})
	if res.IsError {
		t.Fatalf("bill_create returned tool error: %s", text)
	}
	var b billData
	if err := unmarshalData(text, &b); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return b
}

func insertVendor(t *testing.T, pool *pgxpool.Pool, householdID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO vendors (household_id, name) VALUES ($1, $2) RETURNING id`,
		householdID, name,
	).Scan(&id); err != nil {
		t.Fatalf("insert vendor: %v", err)
	}
	return id
}

func decodeUpdated(t *testing.T, text string) billData {
	t.Helper()
	var b billData
	if err := unmarshalData(text, &b); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return b
}

func updatedEvents(t *testing.T, pool *pgxpool.Pool, billID any, householdID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM outbox_events
		 WHERE type = 'bill.updated' AND payload->>'id' = $1 AND household_id = $2`,
		fmt.Sprint(billID), householdID,
	).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

// TestBillUpdate_PartialUpdate changes amount and due_day only and checks
// every other field keeps its value, in the response and in the table.
func TestBillUpdate_PartialUpdate(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	vendorID := insertVendor(t, pool, hh.HouseholdID, "Power Co")
	orig := createTestBill(t, ctx, pool, vendorID)

	var today time.Time
	if err := pool.QueryRow(context.Background(), `SELECT CURRENT_DATE`).Scan(&today); err != nil {
		t.Fatalf("current date: %v", err)
	}

	_, _, update := NewBillUpdateTool(pool)
	res, text := callTool(t, ctx, update, "bill_update", map[string]any{
		"bill_id": orig.ID,
		"amount":  99.999, // rounded to cents like bill_create
		"due_day": float64(today.Day()),
		"notes":   nil, // null means unchanged
	})
	if res.IsError {
		t.Fatalf("bill_update returned tool error: %s", text)
	}
	got := decodeUpdated(t, text)

	if got.ID != orig.ID || got.Amount == nil || *got.Amount != 100 || got.DueDay == nil || *got.DueDay != today.Day() {
		t.Errorf("amount/due_day not updated: %s", text)
	}
	if got.Name != orig.Name || got.Autopay != orig.Autopay ||
		*got.Category != *orig.Category || *got.ContactID != *orig.ContactID ||
		strOrNil(got.PropertyID) != strOrNil(orig.PropertyID) || orig.PropertyID == nil ||
		*got.PaymentURL != *orig.PaymentURL || *got.Notes != *orig.Notes {
		t.Errorf("untouched fields changed:\n got %s\n was %+v", text, orig)
	}
	if want := today.Format("2006-01-02"); got.NextDueDate == nil || *got.NextDueDate != want ||
		got.DaysUntilDue == nil || *got.DaysUntilDue != 0 {
		t.Errorf("next due = %v (+%v), want %s (+0)", got.NextDueDate, got.DaysUntilDue, want)
	}
	if !got.UpdatedAt.After(got.CreatedAt) && !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Errorf("updated_at %v before created_at %v", got.UpdatedAt, got.CreatedAt)
	}

	// The table agrees with the response.
	var name, notes string
	var amount float64
	var dueDay int
	if err := pool.QueryRow(context.Background(),
		`SELECT name, amount, due_day, notes FROM bills WHERE id = $1`, orig.ID,
	).Scan(&name, &amount, &dueDay, &notes); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if name != "Electric" || amount != 100 || dueDay != today.Day() || notes != "Paperless billing" {
		t.Errorf("stored row: name=%q amount=%v due_day=%d notes=%q", name, amount, dueDay, notes)
	}
	if n := updatedEvents(t, pool, orig.ID, hh.HouseholdID); n != 1 {
		t.Errorf("bill.updated outbox events = %d, want 1", n)
	}
}

// TestBillUpdate_SetAndClearOptionalFields sets the remaining fields, then
// clears every optional field with empty strings.
func TestBillUpdate_SetAndClearOptionalFields(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	orig := createTestBill(t, ctx, pool, insertVendor(t, pool, hh.HouseholdID, "Power Co"))
	newVendor := insertVendor(t, pool, hh.HouseholdID, "Gas Co")
	var vehicleID uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO vehicles (household_id, year, make, model) VALUES ($1, 2022, 'Honda', 'Odyssey') RETURNING id`,
		hh.HouseholdID,
	).Scan(&vehicleID); err != nil {
		t.Fatalf("insert vehicle: %v", err)
	}

	_, _, update := NewBillUpdateTool(pool)
	res, text := callTool(t, ctx, update, "bill_update", map[string]any{
		"bill_id":     orig.ID,
		"name":        "  Gas  ",
		"category":    "Fuel",
		"autopay":     false,
		"contact_id":  newVendor.String(),
		"vehicle_id":  vehicleID.String(),
		"payment_url": "http://gas.example.com",
		"notes":       "new notes",
	})
	if res.IsError {
		t.Fatalf("bill_update returned tool error: %s", text)
	}
	got := decodeUpdated(t, text)
	if got.Name != "Gas" || got.Autopay || *got.Category != "Fuel" || *got.ContactID != newVendor.String() ||
		strOrNil(got.VehicleID) != vehicleID.String() || got.PropertyID != nil || strOrNil(got.SubjectType) != "vehicle" ||
		strOrNil(got.SubjectName) != "2022 Honda Odyssey" ||
		*got.PaymentURL != "http://gas.example.com" || *got.Notes != "new notes" {
		t.Errorf("fields not set: %s", text)
	}
	if *got.Amount != 125.5 || *got.DueDay != 10 {
		t.Errorf("amount/due_day changed: %s", text)
	}

	res, text = callTool(t, ctx, update, "bill_update", map[string]any{
		"bill_id":     orig.ID,
		"category":    "",
		"contact_id":  "",
		"entity_type": " ",
		"entity_id":   "",
		"payment_url": "",
		"notes":       "",
	})
	if res.IsError {
		t.Fatalf("clearing update returned tool error: %s", text)
	}
	got = decodeUpdated(t, text)
	if got.Category != nil || got.ContactID != nil || got.PropertyID != nil || got.VehicleID != nil || got.SubjectType != nil ||
		got.PaymentURL != nil || got.Notes != nil {
		t.Errorf("optional fields not cleared: %s", text)
	}
	if got.Name != "Gas" {
		t.Errorf("name changed by clearing update: %s", text)
	}
	if n := updatedEvents(t, pool, orig.ID, hh.HouseholdID); n != 2 {
		t.Errorf("bill.updated outbox events = %d, want 2", n)
	}
}

// TestBillUpdate_OtherHouseholdNotFound checks a bill in another household
// is reported as not found and left unchanged.
func TestBillUpdate_OtherHouseholdNotFound(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, other := testutil.SeedHousehold(t, pool)
	orig := createTestBill(t, ctx, pool, insertVendor(t, pool, hh.HouseholdID, "Power Co"))

	_, _, update := NewBillUpdateTool(pool)

	tests := []struct {
		name string
		id   string
	}{
		{"bill in another household", orig.ID},
		{"bill that does not exist", uuid.NewString()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, text := callTool(t, otherCtx, update, "bill_update", map[string]any{
				"bill_id": tc.id, "amount": 1.0,
			})
			if !res.IsError || !strings.Contains(text, "No bill with id") {
				t.Fatalf("want 'No bill with id' tool error, got %s", text)
			}
		})
	}

	var amount float64
	if err := pool.QueryRow(context.Background(),
		`SELECT amount FROM bills WHERE id = $1`, orig.ID).Scan(&amount); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if amount != 125.5 {
		t.Errorf("other household changed amount to %v", amount)
	}
	if n := updatedEvents(t, pool, orig.ID, other.HouseholdID) + updatedEvents(t, pool, orig.ID, hh.HouseholdID); n != 0 {
		t.Errorf("bill.updated outbox events = %d, want 0", n)
	}
}

// TestBillUpdate_LegacyNullFields updates a bill created without amount or
// due_day (possible through the API) without failing.
func TestBillUpdate_LegacyNullFields(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)

	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		// is_autopay is false, not NULL: migration 075 made it NOT NULL DEFAULT
		// false (#5497). What this test is about is the legacy NULL amount and
		// due_day an API-created bill could have.
		`INSERT INTO bills (household_id, name, is_autopay) VALUES ($1, 'Old', false) RETURNING id`,
		hh.HouseholdID,
	).Scan(&id); err != nil {
		t.Fatalf("insert bill: %v", err)
	}

	_, _, update := NewBillUpdateTool(pool)
	res, text := callTool(t, ctx, update, "bill_update", map[string]any{
		"bill_id": id.String(), "notes": "still no due day",
	})
	if res.IsError {
		t.Fatalf("bill_update returned tool error: %s", text)
	}
	got := decodeUpdated(t, text)
	if got.Amount != nil || got.DueDay != nil || got.NextDueDate != nil || got.DaysUntilDue != nil || got.Autopay {
		t.Errorf("unexpected legacy fields: %s", text)
	}
	if got.Notes == nil || *got.Notes != "still no due day" {
		t.Errorf("notes not set: %s", text)
	}
}

// TestBillUpdate_ToolDefinition pins the catalog v2 name, required param and
// class U annotations.
func TestBillUpdate_ToolDefinition(t *testing.T) {
	name, tool, _ := NewBillUpdateTool(nil)
	if name != "bill_update" || tool.Name != "bill_update" {
		t.Errorf("name = %q / %q, want bill_update", name, tool.Name)
	}
	if len(tool.InputSchema.Required) != 1 || tool.InputSchema.Required[0] != "bill_id" {
		t.Errorf("required = %v, want [bill_id]", tool.InputSchema.Required)
	}
	if n := len(tool.Description); n > 200 {
		t.Errorf("description is %d chars, max 200", n)
	}
	for param, schema := range tool.InputSchema.Properties {
		if d, _ := schema.(map[string]any)["description"].(string); len(d) > 80 {
			t.Errorf("%s description is %d chars, max 80", param, len(d))
		}
	}
	a := tool.Annotations
	if a.ReadOnlyHint == nil || *a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint ||
		a.IdempotentHint == nil || !*a.IdempotentHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations = %+v, want class U (false, false, true, false)", a)
	}
}

// TestBillUpdate_Validation checks that bad input is a tool error and
// changes nothing.
func TestBillUpdate_Validation(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, other := testutil.SeedHousehold(t, pool)
	orig := createTestBill(t, ctx, pool, insertVendor(t, pool, hh.HouseholdID, "Power Co"))
	otherVendor := insertVendor(t, pool, other.HouseholdID, "Not mine")

	withID := func(args map[string]any) map[string]any {
		args["bill_id"] = orig.ID
		return args
	}

	_, _, update := NewBillUpdateTool(pool)

	tests := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"missing bill_id", map[string]any{"amount": 1.0}, "bill_id is required"},
		{"bill_id not a uuid", map[string]any{"bill_id": "abc", "amount": 1.0}, "bill_id must be a UUID"},
		{"no fields", withID(map[string]any{}), "No fields to update"},
		{"only null fields", withID(map[string]any{"name": nil, "notes": nil}), "No fields to update"},
		{"blank name", withID(map[string]any{"name": "  "}), "name cannot be blank"},
		{"name too long", withID(map[string]any{"name": strings.Repeat("x", 256)}), "name must be at most"},
		{"negative amount", withID(map[string]any{"amount": -1.0}), "amount must be between"},
		{"amount not a number", withID(map[string]any{"amount": "12"}), "amount must be a number"},
		{"due_day 32", withID(map[string]any{"due_day": 32.0}), "due_day must be a whole number"},
		{"due_day fractional", withID(map[string]any{"due_day": 1.5}), "due_day must be a whole number"},
		{"category too long", withID(map[string]any{"category": strings.Repeat("c", 101)}), "category must be at most"},
		{"autopay not a bool", withID(map[string]any{"autopay": "yes"}), "autopay must be a boolean"},
		{"contact_id not a uuid", withID(map[string]any{"contact_id": "abc"}), "contact_id must be a UUID"},
		{"vendor from another household", withID(map[string]any{"contact_id": otherVendor.String()}), "No contact with id"},
		{"entity_type alone", withID(map[string]any{"entity_type": "vehicle"}), "entity_type needs entity_id"},
		{"entity_id alone", withID(map[string]any{"entity_id": uuid.NewString()}), "entity_id was given without entity_type"},
		{"legacy pet subject", withID(map[string]any{"entity_type": "pet", "entity_id": uuid.NewString()}), "can't be tied to a"},
		{"vehicle_id not a uuid", withID(map[string]any{"vehicle_id": "Honda"}), "vehicle_id must be a UUID"},
		{"vehicle with asset", withID(map[string]any{"vehicle_id": uuid.NewString(), "asset_id": uuid.NewString()}), "can't also be for a property"},
		{"unknown property", withID(map[string]any{"property_id": uuid.NewString()}), "No property with id"},
		{"payment_url not http", withID(map[string]any{"payment_url": "ftp://x.example.com"}), "payment_url must be"},
		{"valid field with invalid field", withID(map[string]any{"amount": 5.0, "due_day": 0.0}), "due_day must be a whole number"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, text := callTool(t, ctx, update, "bill_update", tc.args)
			if !res.IsError {
				t.Fatalf("want tool error, got %s", text)
			}
			if !strings.Contains(text, tc.wantErr) {
				t.Errorf("error %q does not contain %q", text, tc.wantErr)
			}
		})
	}

	t.Run("no claims", func(t *testing.T) {
		res, _ := callTool(t, context.Background(), update, "bill_update", withID(map[string]any{"amount": 1.0}))
		if !res.IsError {
			t.Fatal("want tool error without claims")
		}
	})

	var amount float64
	var vendor uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT amount, vendor_id FROM bills WHERE id = $1`, orig.ID).Scan(&amount, &vendor); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if amount != 125.5 || vendor.String() != *orig.ContactID {
		t.Errorf("invalid calls changed the bill: amount=%v vendor=%s", amount, vendor)
	}
	if n := updatedEvents(t, pool, orig.ID, hh.HouseholdID); n != 0 {
		t.Errorf("invalid calls wrote %d bill.updated events", n)
	}
}

func deletedEvents(t *testing.T, pool *pgxpool.Pool, billID any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM outbox_events WHERE type = 'bill.deleted' AND payload->>'id' = $1`,
		fmt.Sprint(billID),
	).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

func billExists(t *testing.T, pool *pgxpool.Pool, id any) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM bills WHERE id = $1::uuid)`, fmt.Sprint(id)).Scan(&ok); err != nil {
		t.Fatalf("bill exists: %v", err)
	}
	return ok
}

func listBillIDs(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	_, _, list := NewBillFindTool(pool)
	_, text := callTool(t, ctx, list, "bill_find", nil)
	var rows []struct {
		ID string `json:"id"`
	}
	if err := unmarshalData(text, &rows); err != nil {
		t.Fatalf("bill_find: decode %q: %v", text, err)
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// TestBillDelete_RemovesFromListBills deletes one of two bills and checks it
// is gone from bill_find, the response is the deleted row, and a bill.deleted
// event is written.
func TestBillDelete_RemovesFromListBills(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	vendorID := insertVendor(t, pool, hh.HouseholdID, "Power Co")
	doomed := createTestBill(t, ctx, pool, vendorID)
	kept := createTestBill(t, ctx, pool, vendorID, "Water")

	if got := listBillIDs(t, ctx, pool); len(got) != 2 {
		t.Fatalf("before delete: bill_find = %v, want 2 bills", got)
	}

	_, _, del := NewBillDeleteTool(pool)
	res, text := callTool(t, ctx, del, "bill_delete", map[string]any{"bill_id": doomed.ID})
	if res.IsError {
		t.Fatalf("bill_delete returned tool error: %s", text)
	}
	var got billData
	if err := unmarshalData(text, &got); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	if got.ID != doomed.ID || got.Name != "Electric" ||
		got.Amount == nil || *got.Amount != 125.5 || got.DueDay == nil || *got.DueDay != 10 ||
		!got.Autopay || got.ContactID == nil || *got.ContactID != vendorID.String() ||
		got.Notes == nil || *got.Notes != "Paperless billing" {
		t.Errorf("unexpected deleted bill: %s", text)
	}

	ids := listBillIDs(t, ctx, pool)
	if len(ids) != 1 || ids[0] != kept.ID {
		t.Errorf("after delete: bill_find = %v, want only %s", ids, kept.ID)
	}
	if billExists(t, pool, doomed.ID) {
		t.Error("deleted bill still in table")
	}

	var events int
	var name string
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*), MAX(payload->>'name') FROM outbox_events
		 WHERE type = 'bill.deleted' AND payload->>'id' = $1 AND household_id = $2`,
		doomed.ID, hh.HouseholdID,
	).Scan(&events, &name); err != nil {
		t.Fatalf("read events: %v", err)
	}
	if events != 1 || name != "Electric" {
		t.Errorf("bill.deleted events = %d (name %q), want 1 (Electric)", events, name)
	}

	// A repeat call deletes nothing more and says so.
	res, text = callTool(t, ctx, del, "bill_delete", map[string]any{"bill_id": doomed.ID})
	if !res.IsError || !strings.Contains(text, "No bill with id") {
		t.Errorf("repeat delete: want 'No bill with id' tool error, got %s", text)
	}
	if n := deletedEvents(t, pool, doomed.ID); n != 1 {
		t.Errorf("repeat delete wrote events: total %d, want 1", n)
	}
}

// TestBillDelete_OtherHouseholdNotFound checks a bill in another household
// (or a missing bill) is reported as not found and is not deleted.
func TestBillDelete_OtherHouseholdNotFound(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)
	orig := createTestBill(t, ctx, pool, insertVendor(t, pool, hh.HouseholdID, "Power Co"))

	_, _, del := NewBillDeleteTool(pool)
	missing := uuid.New()

	tests := []struct {
		name string
		id   string
	}{
		{"bill in another household", orig.ID},
		{"bill that does not exist", missing.String()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, text := callTool(t, otherCtx, del, "bill_delete", map[string]any{"bill_id": tc.id})
			if !res.IsError || !strings.Contains(text, "No bill with id") {
				t.Fatalf("want 'No bill with id' tool error, got %s", text)
			}
			if n := deletedEvents(t, pool, tc.id); n != 0 {
				t.Errorf("bill.deleted events = %d, want 0", n)
			}
		})
	}

	if !billExists(t, pool, orig.ID) {
		t.Fatal("other household deleted the bill")
	}
	if ids := listBillIDs(t, ctx, pool); len(ids) != 1 || ids[0] != orig.ID {
		t.Errorf("owner's bill_find = %v, want [%s]", ids, orig.ID)
	}
}

// TestBillDelete_LegacyNullFields deletes a bill created without amount,
// due_day or is_autopay (possible through the API).
func TestBillDelete_LegacyNullFields(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)

	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		// is_autopay is false, not NULL: migration 075 made it NOT NULL DEFAULT
		// false (#5497). What this test is about is the legacy NULL amount and
		// due_day an API-created bill could have.
		`INSERT INTO bills (household_id, name, is_autopay) VALUES ($1, 'Old', false) RETURNING id`,
		hh.HouseholdID,
	).Scan(&id); err != nil {
		t.Fatalf("insert bill: %v", err)
	}

	_, _, del := NewBillDeleteTool(pool)
	res, text := callTool(t, ctx, del, "bill_delete", map[string]any{"bill_id": id.String()})
	if res.IsError {
		t.Fatalf("bill_delete returned tool error: %s", text)
	}
	var got billData
	if err := unmarshalData(text, &got); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	if got.Name != "Old" || got.Amount != nil || got.DueDay != nil || got.Autopay {
		t.Errorf("unexpected legacy deleted bill: %s", text)
	}
	if billExists(t, pool, id) {
		t.Error("legacy bill still in table")
	}
}

// TestBillDelete_ToolDefinition pins the catalog v2 name, required param and
// class X annotations.
func TestBillDelete_ToolDefinition(t *testing.T) {
	name, tool, _ := NewBillDeleteTool(nil)
	if name != "bill_delete" || tool.Name != "bill_delete" {
		t.Errorf("name = %q / %q, want bill_delete", name, tool.Name)
	}
	if len(tool.InputSchema.Required) != 1 || tool.InputSchema.Required[0] != "bill_id" {
		t.Errorf("required = %v, want [bill_id]", tool.InputSchema.Required)
	}
	if len(tool.InputSchema.Properties) != 1 {
		t.Errorf("params = %v, want only bill_id", tool.InputSchema.Properties)
	}
	if n := len(tool.Description); n > 200 {
		t.Errorf("description is %d chars, max 200", n)
	}
	for param, schema := range tool.InputSchema.Properties {
		if d, _ := schema.(map[string]any)["description"].(string); len(d) > 80 {
			t.Errorf("%s description is %d chars, max 80", param, len(d))
		}
	}
	a := tool.Annotations
	if a.ReadOnlyHint == nil || *a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint ||
		a.IdempotentHint == nil || !*a.IdempotentHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations = %+v, want class X (false, true, true, false)", a)
	}
	if a.Title != "Delete bill" {
		t.Errorf("title = %q, want Delete bill", a.Title)
	}
}

// TestBillDelete_Validation checks bad input is a tool error and deletes
// nothing.
func TestBillDelete_Validation(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	orig := createTestBill(t, ctx, pool, insertVendor(t, pool, hh.HouseholdID, "Power Co"))

	_, _, del := NewBillDeleteTool(pool)

	tests := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"missing bill_id", map[string]any{}, "bill_id is required"},
		{"null bill_id", map[string]any{"bill_id": nil}, "bill_id is required"},
		{"blank bill_id", map[string]any{"bill_id": "  "}, "bill_id is required"},
		{"bill_id not a uuid", map[string]any{"bill_id": "abc"}, "bill_id must be a UUID"},
		{"bill_id not a string", map[string]any{"bill_id": 5.0}, "bill_id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, text := callTool(t, ctx, del, "bill_delete", tc.args)
			if !res.IsError {
				t.Fatalf("want tool error, got %s", text)
			}
			if !strings.Contains(text, tc.wantErr) {
				t.Errorf("error %q does not contain %q", text, tc.wantErr)
			}
		})
	}

	t.Run("no claims", func(t *testing.T) {
		res, _ := callTool(t, context.Background(), del, "bill_delete", map[string]any{"bill_id": orig.ID})
		if !res.IsError {
			t.Fatal("want tool error without claims")
		}
	})

	if !billExists(t, pool, orig.ID) {
		t.Error("invalid calls deleted the bill")
	}
	if n := deletedEvents(t, pool, orig.ID); n != 0 {
		t.Errorf("invalid calls wrote %d bill.deleted events", n)
	}
}

// ── bill_pay ──────────────────────────────────────────────────────────────

// insertPayBill inserts a bill directly so tests control amount and due_day
// (nil leaves the column NULL, as legacy API rows can).
func insertPayBill(t *testing.T, pool *pgxpool.Pool, householdID uuid.UUID, amount *float64, dueDay *int) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Electric', $2, $3) RETURNING id`,
		householdID, amount, dueDay,
	).Scan(&id); err != nil {
		t.Fatalf("insert bill: %v", err)
	}
	return id
}

func storedPaidDate(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var d *time.Time
	if err := pool.QueryRow(context.Background(), `SELECT paid_date FROM bills WHERE id = $1`, id).Scan(&d); err != nil {
		t.Fatalf("select paid_date: %v", err)
	}
	if d == nil {
		return ""
	}
	return d.Format(time.DateOnly)
}

func storedPaidFor(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var d *time.Time
	if err := pool.QueryRow(context.Background(), `SELECT paid_for_due_date FROM bills WHERE id = $1`, id).Scan(&d); err != nil {
		t.Fatalf("select paid_for_due_date: %v", err)
	}
	if d == nil {
		return ""
	}
	return d.Format(time.DateOnly)
}

func setHouseholdTZ(t *testing.T, pool *pgxpool.Pool, householdID uuid.UUID, tz string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `UPDATE households SET timezone = $2 WHERE id = $1`, householdID, tz); err != nil {
		t.Fatalf("set timezone: %v", err)
	}
}

func decodePaid(t *testing.T, text string) billData {
	t.Helper()
	var b billData
	if err := unmarshalData(text, &b); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return b
}

func payBill(t *testing.T, ctx context.Context, pay toolHandler, args map[string]any) billData {
	t.Helper()
	res, text := callTool(t, ctx, pay, "bill_pay", args)
	if res.IsError {
		t.Fatalf("bill_pay %v returned tool error: %s", args, text)
	}
	return decodePaid(t, text)
}

func strOrNil(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func ptr[T any](v T) *T { return &v }

// TestPaidForDueDate pins which due date a payment date pays: the previous
// due date when at most lateGraceDays late, otherwise the next one, with
// due days clamped to short months.
func TestPaidForDueDate(t *testing.T) {
	tests := []struct {
		paid   string
		dueDay int
		want   string
	}{
		{"2026-01-18", 20, "2026-01-20"}, // early
		{"2026-01-20", 20, "2026-01-20"}, // on the day
		{"2026-01-27", 20, "2026-01-20"}, // 7 days late: still January's
		{"2026-01-28", 20, "2026-02-20"}, // 8 days late: next one
		{"2026-01-02", 5, "2026-01-05"},  // previous (Dec 5) is too far back
		{"2026-01-03", 28, "2025-12-28"}, // late across the year boundary
		{"2026-02-10", 31, "2026-02-28"}, // Jan 31 too far back; Feb clamps
		{"2026-03-03", 31, "2026-02-28"}, // 3 days after the clamped Feb 28
		{"2028-02-29", 30, "2028-02-29"}, // leap year clamp, on the day
		{"2025-12-31", 31, "2025-12-31"},
	}
	for _, tc := range tests {
		paid, _ := time.Parse(time.DateOnly, tc.paid)
		c, _ := cadence(MonthlySchedule(tc.dueDay))
		if got := paidForDueDate(paid, c).Format(time.DateOnly); got != tc.want {
			t.Errorf("paidForDueDate(%s, %d) = %s, want %s", tc.paid, tc.dueDay, got, tc.want)
		}
	}
}

// TestBillPay_SetsPaidDateAndNextDue pays a bill, checks the stored
// paid_date, the covered and next due dates and the outbox event, then
// checks repeats for the same due date change nothing and a payment for the
// next due date moves it on.
func TestBillPay_SetsPaidDateAndNextDue(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	id := insertPayBill(t, pool, hh.HouseholdID, ptr(142.5), ptr(20))

	_, _, pay := NewBillPayTool(pool)

	got := payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": "2026-01-18"})
	if got.ID != id.String() || strOrNil(got.PaidDate) != "2026-01-18" || *got.AlreadyPaid {
		t.Errorf("first payment: id=%s paid_date=%s already_paid=%v", got.ID, strOrNil(got.PaidDate), got.AlreadyPaid)
	}
	if strOrNil(got.PaidForDueDate) != "2026-01-20" {
		t.Errorf("paid_for_due_date=%s, want 2026-01-20", strOrNil(got.PaidForDueDate))
	}
	// next_due_date is the next unpaid due date as of today, as bill_find
	// reports it (#4653), not the due date after the one paid.
	assertPayMatchesFind(t, ctx, pool, got)
	if got.AmountPaid == nil || *got.AmountPaid != 142.5 {
		t.Errorf("amount_paid = %v, want the bill's 142.5", got.AmountPaid)
	}
	if d := storedPaidDate(t, pool, id); d != "2026-01-18" {
		t.Errorf("stored paid_date = %q, want 2026-01-18", d)
	}
	if n := updatedEvents(t, pool, id, hh.HouseholdID); n != 1 {
		t.Errorf("bill.updated events = %d, want 1", n)
	}
	var eventPaid string
	if err := pool.QueryRow(context.Background(),
		`SELECT payload->>'paid_date' FROM outbox_events WHERE type = 'bill.updated' AND payload->>'id' = $1`,
		id.String(),
	).Scan(&eventPaid); err != nil || eventPaid != "2026-01-18" {
		t.Errorf("event paid_date = %q (%v), want 2026-01-18", eventPaid, err)
	}
	firstUpdatedAt := got.UpdatedAt

	// Same call again, and a later payment for the same due date: no change.
	for _, paidDate := range []string{"2026-01-18", "2026-01-22", "2026-01-10"} {
		again := payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": paidDate})
		if !*again.AlreadyPaid || strOrNil(again.PaidDate) != "2026-01-18" || strOrNil(again.PaidForDueDate) != "2026-01-20" {
			t.Errorf("repeat with %s: already_paid=%v paid_date=%s paid_for=%s, want true/2026-01-18/2026-01-20",
				paidDate, again.AlreadyPaid, strOrNil(again.PaidDate), strOrNil(again.PaidForDueDate))
		}
		assertPayMatchesFind(t, ctx, pool, again)
		if !again.UpdatedAt.Equal(firstUpdatedAt) {
			t.Errorf("repeat with %s changed updated_at", paidDate)
		}
	}
	if d := storedPaidDate(t, pool, id); d != "2026-01-18" {
		t.Errorf("stored paid_date after repeats = %q, want 2026-01-18", d)
	}
	if n := updatedEvents(t, pool, id, hh.HouseholdID); n != 1 {
		t.Errorf("bill.updated events after repeats = %d, want 1", n)
	}

	// Next month's payment, with an explicit amount.
	next := payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": "2026-02-15", "amount": 150.555})
	if *next.AlreadyPaid || strOrNil(next.PaidDate) != "2026-02-15" ||
		strOrNil(next.PaidForDueDate) != "2026-02-20" {
		t.Errorf("second payment: %+v", next)
	}
	assertPayMatchesFind(t, ctx, pool, next)
	if next.AmountPaid == nil || *next.AmountPaid != 150.56 {
		t.Errorf("amount_paid = %v, want 150.56", next.AmountPaid)
	}
	if d := storedPaidDate(t, pool, id); d != "2026-02-15" {
		t.Errorf("stored paid_date = %q, want 2026-02-15", d)
	}

	// An older payment date never moves the bill backwards.
	old := payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": "2026-01-19"})
	if !*old.AlreadyPaid || strOrNil(old.PaidDate) != "2026-02-15" {
		t.Errorf("older payment: already_paid=%v paid_date=%s", old.AlreadyPaid, strOrNil(old.PaidDate))
	}
	if n := updatedEvents(t, pool, id, hh.HouseholdID); n != 2 {
		t.Errorf("bill.updated events = %d, want 2", n)
	}
}

// TestBillPay_DefaultsToHouseholdToday checks a call without paid_date uses
// today in the household's timezone, including zones a day ahead of and
// behind UTC, and that an unknown timezone falls back to UTC.
func TestBillPay_DefaultsToHouseholdToday(t *testing.T) {
	pool := testutil.NewPool(t)
	_, _, pay := NewBillPayTool(pool)

	for _, tz := range []string{"Pacific/Kiritimati", "Pacific/Pago_Pago", "America/New_York", "UTC", "Not/A_Zone"} {
		t.Run(tz, func(t *testing.T) {
			ctx, hh := testutil.SeedHousehold(t, pool)
			setHouseholdTZ(t, pool, hh.HouseholdID, tz)
			id := insertPayBill(t, pool, hh.HouseholdID, ptr(80.0), ptr(1))

			loc, err := time.LoadLocation(tz)
			if err != nil {
				loc = time.UTC
			}
			before := time.Now().In(loc).Format(time.DateOnly)
			got := payBill(t, ctx, pay, map[string]any{"bill_id": id.String()})
			after := time.Now().In(loc).Format(time.DateOnly)

			if strOrNil(got.PaidDate) != before && strOrNil(got.PaidDate) != after {
				t.Errorf("paid_date = %s, want household-local today %s", strOrNil(got.PaidDate), before)
			}
			if d := storedPaidDate(t, pool, id); d != strOrNil(got.PaidDate) {
				t.Errorf("stored paid_date = %q, want %q", d, strOrNil(got.PaidDate))
			}
			if got.NextDueDate == nil || got.DaysUntilDue == nil || *got.DaysUntilDue < 0 {
				t.Errorf("next_due_date=%s days_until_due=%v, want a date not before today",
					strOrNil(got.NextDueDate), got.DaysUntilDue)
			}
			if *got.NextDueDate <= strOrNil(got.PaidDate) {
				t.Errorf("next_due_date %s is not after paid_date %s", *got.NextDueDate, strOrNil(got.PaidDate))
			}
		})
	}
}

// TestBillPay_FutureDateRejected checks a paid_date after the household's
// today is a tool error and changes nothing.
func TestBillPay_FutureDateRejected(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	id := insertPayBill(t, pool, hh.HouseholdID, ptr(80.0), ptr(15))

	_, _, pay := NewBillPayTool(pool)
	future := time.Now().UTC().AddDate(0, 0, 2).Format(time.DateOnly)
	res, text := callTool(t, ctx, pay, "bill_pay", map[string]any{"bill_id": id.String(), "paid_date": future})
	if !res.IsError || !strings.Contains(text, "cannot be in the future") {
		t.Fatalf("want future-date tool error, got %s", text)
	}
	if d := storedPaidDate(t, pool, id); d != "" {
		t.Errorf("stored paid_date = %q, want NULL", d)
	}
}

// TestBillPay_OtherHouseholdNotFound checks a bill from another household,
// and a missing bill, are not found and left unpaid.
func TestBillPay_OtherHouseholdNotFound(t *testing.T) {
	pool := testutil.NewPool(t)
	_, owner := testutil.SeedHousehold(t, pool)
	otherCtx, other := testutil.SeedHousehold(t, pool)
	id := insertPayBill(t, pool, owner.HouseholdID, ptr(80.0), ptr(15))

	_, _, pay := NewBillPayTool(pool)
	for _, billID := range []string{id.String(), uuid.NewString()} {
		res, text := callTool(t, otherCtx, pay, "bill_pay", map[string]any{"bill_id": billID, "paid_date": "2026-01-10"})
		if !res.IsError || !strings.Contains(text, "No bill with id") {
			t.Errorf("bill %s: want bill not found, got %s", billID, text)
		}
	}
	if d := storedPaidDate(t, pool, id); d != "" {
		t.Errorf("stored paid_date = %q, want NULL", d)
	}
	if n := updatedEvents(t, pool, id, other.HouseholdID) + updatedEvents(t, pool, id, owner.HouseholdID); n != 0 {
		t.Errorf("bill.updated events = %d, want 0", n)
	}
}

// TestBillPay_LegacyNullFields pays a bill without amount or due_day: the
// paid_date is set, the due-date fields are null, and a repeat is a no-op.
func TestBillPay_LegacyNullFields(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		// is_autopay is false, not NULL: migration 075 made it NOT NULL DEFAULT
		// false (#5497). What this test is about is the legacy NULL amount and
		// due_day an API-created bill could have.
		`INSERT INTO bills (household_id, name, is_autopay) VALUES ($1, 'Old', false) RETURNING id`,
		hh.HouseholdID,
	).Scan(&id); err != nil {
		t.Fatalf("insert bill: %v", err)
	}

	_, _, pay := NewBillPayTool(pool)
	got := payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": "2026-01-10"})
	if *got.AlreadyPaid || strOrNil(got.PaidDate) != "2026-01-10" || got.Amount != nil || got.DueDay != nil ||
		got.PaidForDueDate != nil || got.NextDueDate != nil || got.DaysUntilDue != nil || got.AmountPaid != nil {
		t.Errorf("unexpected legacy response: %+v", got)
	}
	if d := storedPaidDate(t, pool, id); d != "2026-01-10" {
		t.Errorf("stored paid_date = %q, want 2026-01-10", d)
	}
	again := payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": "2026-01-10"})
	if !*again.AlreadyPaid {
		t.Error("repeat payment of a legacy bill was not already_paid")
	}
	if n := updatedEvents(t, pool, id, hh.HouseholdID); n != 1 {
		t.Errorf("bill.updated events = %d, want 1", n)
	}
}

// TestBillPay_ToolDefinition pins the catalog v2 name, params and class U
// annotations.
func TestBillPay_ToolDefinition(t *testing.T) {
	name, tool, _ := NewBillPayTool(nil)
	if name != "bill_pay" || tool.Name != "bill_pay" {
		t.Errorf("name = %q / %q, want bill_pay", name, tool.Name)
	}
	if len(tool.InputSchema.Required) != 1 || tool.InputSchema.Required[0] != "bill_id" {
		t.Errorf("required = %v, want [bill_id]", tool.InputSchema.Required)
	}
	for _, p := range []string{"bill_id", "paid_date", "for_due_date", "amount"} {
		if _, ok := tool.InputSchema.Properties[p]; !ok {
			t.Errorf("missing param %s", p)
		}
	}
	if len(tool.InputSchema.Properties) != 4 {
		t.Errorf("params = %v, want bill_id, paid_date, for_due_date, amount", tool.InputSchema.Properties)
	}
	if n := len(tool.Description); n > 200 {
		t.Errorf("description is %d chars, max 200", n)
	}
	for param, schema := range tool.InputSchema.Properties {
		if d, _ := schema.(map[string]any)["description"].(string); len(d) > 80 {
			t.Errorf("%s description is %d chars, max 80", param, len(d))
		}
	}
	a := tool.Annotations
	if a.ReadOnlyHint == nil || *a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint ||
		a.IdempotentHint == nil || !*a.IdempotentHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations = %+v, want class U (false, false, true, false)", a)
	}
	if a.Title != "Pay bill" {
		t.Errorf("title = %q, want Pay bill", a.Title)
	}
}

// TestBillPay_Validation checks bad input is a tool error and changes
// nothing.
func TestBillPay_Validation(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	id := insertPayBill(t, pool, hh.HouseholdID, ptr(80.0), ptr(15))
	noDay := insertPayBill(t, pool, hh.HouseholdID, ptr(80.0), nil)
	today := householdTodayIn(t, "UTC") // SeedHousehold's timezone
	_, _, pay := NewBillPayTool(pool)

	tests := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"missing bill_id", map[string]any{}, "bill_id is required"},
		{"blank bill_id", map[string]any{"bill_id": " "}, "bill_id is required"},
		{"bill_id not a uuid", map[string]any{"bill_id": "abc"}, "bill_id must be a UUID"},
		{"paid_date US format", map[string]any{"bill_id": id.String(), "paid_date": "11/20/2026"}, "paid_date must be YYYY-MM-DD"},
		{"paid_date with time", map[string]any{"bill_id": id.String(), "paid_date": "2026-01-10T10:00:00Z"}, "is not a date"},
		{"paid_date not a string", map[string]any{"bill_id": id.String(), "paid_date": 20.0}, "paid_date must be a string"},
		{"negative amount", map[string]any{"bill_id": id.String(), "amount": -1.0}, "amount must be between"},
		{"amount not a number", map[string]any{"bill_id": id.String(), "amount": "ten"}, "amount must be a number"},
		{"for_due_date not a date", map[string]any{"bill_id": id.String(), "for_due_date": "Nov 15"}, "for_due_date must be YYYY-MM-DD"},
		{"for_due_date not a string", map[string]any{"bill_id": id.String(), "for_due_date": 15.0}, "for_due_date must be a string"},
		// Checked against the bill: due on the 15th, so the 16th is not one
		// of its due dates, and two months ahead is too far.
		{"for_due_date not a due date", map[string]any{"bill_id": id.String(), "for_due_date": "2026-01-16"},
			"is not a due date of Electric, which is due on day 15"},
		{"for_due_date more than a month ahead", map[string]any{"bill_id": id.String(),
			"for_due_date": dueDateInMonth(today.Year(), today.Month()+2, 15).Format(time.DateOnly)},
			"more than a month after today"},
		{"for_due_date on a bill with no due day", map[string]any{"bill_id": noDay.String(), "for_due_date": "2026-01-15"},
			"has no due day"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, text := callTool(t, ctx, pay, "bill_pay", tc.args)
			if !res.IsError {
				t.Fatalf("want tool error, got %s", text)
			}
			if !strings.Contains(text, tc.wantErr) {
				t.Errorf("error %q does not contain %q", text, tc.wantErr)
			}
		})
	}

	t.Run("no claims", func(t *testing.T) {
		res, _ := callTool(t, context.Background(), pay, "bill_pay", map[string]any{"bill_id": id.String()})
		if !res.IsError {
			t.Fatal("want tool error without claims")
		}
	})

	for _, b := range []uuid.UUID{id, noDay} {
		if d := storedPaidDate(t, pool, b); d != "" {
			t.Errorf("stored paid_date = %q, want NULL", d)
		}
		if d := storedPaidFor(t, pool, b); d != "" {
			t.Errorf("stored paid_for_due_date = %q, want NULL", d)
		}
		if n := updatedEvents(t, pool, b, hh.HouseholdID); n != 0 {
			t.Errorf("bill.updated events = %d, want 0", n)
		}
	}
}

// ── write tools agree with bill_find (#4627) ────────────────────────────

// householdTodayIn returns today in tz as midnight UTC, like householdToday.
func householdTodayIn(t *testing.T, tz string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatalf("load zone %s: %v", tz, err)
	}
	y, m, d := time.Now().In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// listedBill returns the bill_find entry for id.
func listedBill(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) billData {
	t.Helper()
	_, _, list := NewBillFindTool(pool)
	_, text := callTool(t, ctx, list, "bill_find", nil)
	b := findBill(decodeBills(t, text), id)
	if b == nil {
		t.Fatalf("bill_find missing bill %s: %s", id, text)
	}
	if b.NextDueDate == nil || b.DaysUntilDue == nil {
		t.Fatalf("bill_find bill %s has no due fields: %s", id, text)
	}
	return *b
}

// TestBillUpdate_PaidBillMatchesListBills pays a bill due today and then
// changes its amount: bill_update must report next month's due date, as
// bill_find does, not the due date that was just paid.
func TestBillUpdate_PaidBillMatchesListBills(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	const tz = "Pacific/Kiritimati"
	setHouseholdTZ(t, pool, hh.HouseholdID, tz)
	today := householdTodayIn(t, tz)

	_, _, create := NewBillCreateTool(pool)
	res, text := callTool(t, ctx, create, "bill_create", map[string]any{
		"name": "Electric", "amount": 80.0, "due_day": float64(today.Day()),
	})
	if res.IsError {
		t.Fatalf("bill_create returned tool error: %s", text)
	}
	var created billData
	if err := unmarshalData(text, &created); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	if strOrNil(created.NextDueDate) != today.Format(time.DateOnly) || created.DaysUntilDue == nil || *created.DaysUntilDue != 0 {
		t.Fatalf("bill_create next=%s days=%v, want %s 0", strOrNil(created.NextDueDate), created.DaysUntilDue, today.Format(time.DateOnly))
	}
	id := created.ID

	_, _, pay := NewBillPayTool(pool)
	payBill(t, ctx, pay, map[string]any{"bill_id": id})

	_, _, update := NewBillUpdateTool(pool)
	res, text = callTool(t, ctx, update, "bill_update", map[string]any{"bill_id": id, "amount": 95.25})
	if res.IsError {
		t.Fatalf("bill_update returned tool error: %s", text)
	}
	got := decodeUpdated(t, text)
	want := listedBill(t, ctx, pool, id)

	wantNext := dueDateInMonth(today.Year(), today.Month()+1, today.Day()).Format(time.DateOnly)
	if *want.NextDueDate != wantNext {
		t.Fatalf("bill_find next_due_date = %s, want next month %s", *want.NextDueDate, wantNext)
	}
	if got.NextDueDate == nil || got.DaysUntilDue == nil {
		t.Fatalf("bill_update has no due fields: %s", text)
	}
	if *got.NextDueDate != *want.NextDueDate || *got.DaysUntilDue != *want.DaysUntilDue {
		t.Errorf("bill_update next=%s days=%d, bill_find next=%s days=%d",
			*got.NextDueDate, *got.DaysUntilDue, *want.NextDueDate, *want.DaysUntilDue)
	}
	if got.Status != want.Status || got.Status != statusPaid {
		t.Errorf("bill_update status=%q, bill_find status=%q, want %q", got.Status, want.Status, statusPaid)
	}
	if strOrNil(got.PaidDate) != strOrNil(want.PaidDate) || strOrNil(got.PaidForDueDate) != strOrNil(want.PaidForDueDate) {
		t.Errorf("bill_update paid_date=%s paid_for=%s, bill_find paid_date=%s paid_for=%s",
			strOrNil(got.PaidDate), strOrNil(got.PaidForDueDate), strOrNil(want.PaidDate), strOrNil(want.PaidForDueDate))
	}
	if got.Amount == nil || *got.Amount != 95.25 {
		t.Errorf("bill_update amount = %v, want 95.25", got.Amount)
	}
}

// TestBillCreate_FarTimezoneMatchesListBills creates bills in households far
// from UTC: bill_create's next_due_date and days_until_due must equal
// bill_find', which use the household's local today.
func TestBillCreate_FarTimezoneMatchesListBills(t *testing.T) {
	pool := testutil.NewPool(t)
	_, _, create := NewBillCreateTool(pool)

	for _, tz := range []string{"Pacific/Kiritimati", "Pacific/Pago_Pago"} {
		t.Run(tz, func(t *testing.T) {
			ctx, hh := testutil.SeedHousehold(t, pool)
			setHouseholdTZ(t, pool, hh.HouseholdID, tz)
			today := householdTodayIn(t, tz)

			// Due on the household's today: a UTC-clock answer would be
			// today (0 days) or next month, depending on the hour.
			res, text := callTool(t, ctx, create, "bill_create", map[string]any{
				"name": "Water", "amount": 40.0, "due_day": float64(today.Day()),
			})
			if res.IsError {
				t.Fatalf("bill_create returned tool error: %s", text)
			}
			var got billData
			if err := unmarshalData(text, &got); err != nil {
				t.Fatalf("decode %q: %v", text, err)
			}
			want := listedBill(t, ctx, pool, got.ID)
			if strOrNil(got.NextDueDate) != *want.NextDueDate || *got.DaysUntilDue != *want.DaysUntilDue {
				t.Errorf("bill_create next=%s days=%d, bill_find next=%s days=%d",
					strOrNil(got.NextDueDate), *got.DaysUntilDue, *want.NextDueDate, *want.DaysUntilDue)
			}
			if strOrNil(got.NextDueDate) != today.Format(time.DateOnly) {
				t.Errorf("bill_create next_due_date = %s, want household today %s", strOrNil(got.NextDueDate), today.Format(time.DateOnly))
			}
		})
	}
}

// TestBillDueExported checks the exported BillDue matches billDue.
func TestBillDueExported(t *testing.T) {
	today := mustDate(t, "2026-11-15")
	paid := mustDate(t, "2026-11-15")
	got := BillDue(today, 15, &paid, nil)
	if got.Next.Format(time.DateOnly) != "2026-12-15" || got.DaysUntil != 30 || got.Status != statusPaid ||
		got.PaidFor == nil || got.PaidFor.Format(time.DateOnly) != "2026-11-15" {
		t.Errorf("BillDue paid = %+v", got)
	}
	got = BillDue(today, 31, nil, nil)
	if got.Next.Format(time.DateOnly) != "2026-11-30" || got.DaysUntil != 15 || got.Status != statusDue || got.PaidFor != nil {
		t.Errorf("BillDue unpaid = %+v", got)
	}
}

// assertPayMatchesFind checks bill_pay's reply reports the same due fields as
// bill_find does for that bill right after: one rule (BillDue), so the agent
// never tells the family two different dates.
func assertPayMatchesFind(t *testing.T, ctx context.Context, pool *pgxpool.Pool, got billData) {
	t.Helper()
	want := listedBill(t, ctx, pool, got.ID)
	if got.NextDueDate == nil || got.DaysUntilDue == nil || got.Status == "" {
		t.Fatalf("bill_pay reply has no due fields: next=%s days=%v status=%q",
			strOrNil(got.NextDueDate), got.DaysUntilDue, got.Status)
	}
	if *got.NextDueDate != *want.NextDueDate || *got.DaysUntilDue != *want.DaysUntilDue || got.Status != want.Status {
		t.Errorf("bill_pay next=%s days=%d status=%s, bill_find next=%s days=%d status=%s",
			*got.NextDueDate, *got.DaysUntilDue, got.Status, *want.NextDueDate, *want.DaysUntilDue, want.Status)
	}
	if strOrNil(got.NextDueWeekday) != strOrNil(want.NextDueWeekday) {
		t.Errorf("bill_pay next_due_weekday=%s, bill_find %s", strOrNil(got.NextDueWeekday), strOrNil(want.NextDueWeekday))
	}
	if strOrNil(got.PaidDate) != strOrNil(want.PaidDate) || strOrNil(got.PaidForDueDate) != strOrNil(want.PaidForDueDate) {
		t.Errorf("bill_pay paid_date=%s paid_for=%s, bill_find paid_date=%s paid_for=%s",
			strOrNil(got.PaidDate), strOrNil(got.PaidForDueDate), strOrNil(want.PaidDate), strOrNil(want.PaidForDueDate))
	}
	if *got.DaysUntilDue < 0 {
		t.Errorf("bill_pay days_until_due = %d, want >= 0", *got.DaysUntilDue)
	}
}

// TestBillPay_MatchesBillFind pays bills on time, late and back-dated and
// checks bill_pay's next_due_date, days_until_due and status equal
// bill_find's (#4653). Dates are relative to the household's today.
func TestBillPay_MatchesBillFind(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	const tz = "UTC"
	setHouseholdTZ(t, pool, hh.HouseholdID, tz)
	today := householdTodayIn(t, tz)
	late := today.AddDate(0, 0, -5)
	// Two months back, kept on a day every month has so the due day is not
	// clamped and the payment covers exactly that month's due date.
	backY, backM, _ := today.AddDate(0, 0, -today.Day()+1).AddDate(0, -2, 0).Date()
	backPaid := time.Date(backY, backM, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		dueDay     int
		paidDate   time.Time
		wantPaid   string // expected paid_for_due_date
		wantStatus string
	}{
		{"on time", today.Day(), today, today.Format(time.DateOnly), statusPaid},
		// Paid within the grace period, so it covers the due date 5 days
		// ago; the next unpaid one (next month's) is still due.
		{"5 days late", late.Day(), today, late.Format(time.DateOnly), statusDue},
		// Paid on the 1st two months ago: covers that month's 20th; the next
		// unpaid due date is the first 20th on or after today.
		{"back-dated two months", 20, backPaid,
			time.Date(backY, backM, 20, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), statusDue},
	}
	_, _, pay := NewBillPayTool(pool)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := insertPayBill(t, pool, hh.HouseholdID, ptr(60.0), ptr(tc.dueDay))
			got := payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": tc.paidDate.Format(time.DateOnly)})
			if *got.AlreadyPaid || strOrNil(got.PaidDate) != tc.paidDate.Format(time.DateOnly) {
				t.Errorf("already_paid=%v paid_date=%s, want false %s", *got.AlreadyPaid, strOrNil(got.PaidDate), tc.paidDate.Format(time.DateOnly))
			}
			if strOrNil(got.PaidForDueDate) != tc.wantPaid {
				t.Errorf("paid_for_due_date = %s, want %s", strOrNil(got.PaidForDueDate), tc.wantPaid)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
			assertPayMatchesFind(t, ctx, pool, got)
		})
	}
}

// TestBillPay_ForDueDate pays a bill 8 days late (#4655). Without
// for_due_date the grace rule counts it for the NEXT due date, so the
// overdue one vanished; with for_due_date it covers the overdue due date and
// the next one is still due. Dates are relative to the household's today:
// the 20th of last month, paid on the 28th of last month.
func TestBillPay_ForDueDate(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTZ(t, pool, hh.HouseholdID, "UTC")
	today := householdTodayIn(t, "UTC")
	overdue := dueDateInMonth(today.Year(), today.Month()-1, 20)
	paid := overdue.AddDate(0, 0, 8)
	nextDue := nextDueDate(today, 20)
	_, _, pay := NewBillPayTool(pool)

	t.Run("for_due_date covers the overdue due date", func(t *testing.T) {
		id := insertPayBill(t, pool, hh.HouseholdID, ptr(142.5), ptr(20))
		args := map[string]any{"bill_id": id.String(), "paid_date": paid.Format(time.DateOnly), "for_due_date": overdue.Format(time.DateOnly)}
		got := payBill(t, ctx, pay, args)
		if *got.AlreadyPaid || strOrNil(got.PaidForDueDate) != overdue.Format(time.DateOnly) ||
			strOrNil(got.PaidDate) != paid.Format(time.DateOnly) {
			t.Errorf("already_paid=%v paid_date=%s paid_for=%s, want false %s %s", *got.AlreadyPaid,
				strOrNil(got.PaidDate), strOrNil(got.PaidForDueDate), paid.Format(time.DateOnly), overdue.Format(time.DateOnly))
		}
		if strOrNil(got.NextDueDate) != nextDue.Format(time.DateOnly) || got.Status != statusDue {
			t.Errorf("next=%s status=%s, want %s due", strOrNil(got.NextDueDate), got.Status, nextDue.Format(time.DateOnly))
		}
		assertPayMatchesFind(t, ctx, pool, got)
		if d := storedPaidFor(t, pool, id); d != overdue.Format(time.DateOnly) {
			t.Errorf("stored paid_for_due_date = %q, want %s", d, overdue.Format(time.DateOnly))
		}
		if !strings.Contains(paySummary(got, false), "for "+overdue.Format("Jan 2")) {
			t.Errorf("summary %q does not name the due date paid", paySummary(got, false))
		}

		// The same call again changes nothing.
		again := payBill(t, ctx, pay, args)
		if !*again.AlreadyPaid || !again.UpdatedAt.Equal(got.UpdatedAt) || strOrNil(again.PaidForDueDate) != overdue.Format(time.DateOnly) {
			t.Errorf("repeat: already_paid=%v paid_for=%s updated_at changed=%v", *again.AlreadyPaid,
				strOrNil(again.PaidForDueDate), !again.UpdatedAt.Equal(got.UpdatedAt))
		}
		// An earlier due date never moves coverage back.
		earlier := dueDateInMonth(overdue.Year(), overdue.Month()-1, 20)
		older := payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": paid.Format(time.DateOnly), "for_due_date": earlier.Format(time.DateOnly)})
		if !*older.AlreadyPaid || strOrNil(older.PaidForDueDate) != overdue.Format(time.DateOnly) {
			t.Errorf("earlier for_due_date: already_paid=%v paid_for=%s", *older.AlreadyPaid, strOrNil(older.PaidForDueDate))
		}
		if n := updatedEvents(t, pool, id, hh.HouseholdID); n != 1 {
			t.Errorf("bill.updated events = %d, want 1", n)
		}

		// Then paying the next due date moves it on.
		on := payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "for_due_date": nextDue.Format(time.DateOnly)})
		if *on.AlreadyPaid || strOrNil(on.PaidForDueDate) != nextDue.Format(time.DateOnly) || on.Status != statusPaid {
			t.Errorf("next payment: already_paid=%v paid_for=%s status=%s", *on.AlreadyPaid, strOrNil(on.PaidForDueDate), on.Status)
		}
		assertPayMatchesFind(t, ctx, pool, on)
	})

	monthly20, _ := cadence(MonthlySchedule(20))
	t.Run("without for_due_date the grace rule is unchanged", func(t *testing.T) {
		id := insertPayBill(t, pool, hh.HouseholdID, ptr(142.5), ptr(20))
		got := payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "paid_date": paid.Format(time.DateOnly)})
		want := paidForDueDate(paid, monthly20).Format(time.DateOnly) // 8 days late: the next due date
		if strOrNil(got.PaidForDueDate) != want {
			t.Errorf("paid_for_due_date = %s, want %s", strOrNil(got.PaidForDueDate), want)
		}
		if d := storedPaidFor(t, pool, id); d != want {
			t.Errorf("stored paid_for_due_date = %q, want the inferred %s", d, want)
		}
		assertPayMatchesFind(t, ctx, pool, got)
	})

	t.Run("old rows without paid_for_due_date infer it", func(t *testing.T) {
		id := insertPayBill(t, pool, hh.HouseholdID, ptr(142.5), ptr(20))
		if _, err := pool.Exec(context.Background(), `UPDATE bills SET paid_date = $2 WHERE id = $1`, id, paid); err != nil {
			t.Fatal(err)
		}
		// Inferred coverage is the 20th after the payment, so paying the
		// overdue one is already covered and changes nothing.
		got := payBill(t, ctx, pay, map[string]any{"bill_id": id.String(), "for_due_date": overdue.Format(time.DateOnly)})
		if !*got.AlreadyPaid || strOrNil(got.PaidForDueDate) != paidForDueDate(paid, monthly20).Format(time.DateOnly) {
			t.Errorf("already_paid=%v paid_for=%s", *got.AlreadyPaid, strOrNil(got.PaidForDueDate))
		}
		if d := storedPaidFor(t, pool, id); d != "" {
			t.Errorf("stored paid_for_due_date = %q, want NULL (unchanged)", d)
		}
	})
}

// TestBillUpdate_DueDayClearsPaidFor checks bill_update clears a stored
// paid_for_due_date only when due_day really changes (#4655).
func TestBillUpdate_DueDayClearsPaidFor(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, _, update := NewBillUpdateTool(pool)
	id := insertPayBill(t, pool, hh.HouseholdID, ptr(50.0), ptr(20))
	if _, err := pool.Exec(context.Background(),
		`UPDATE bills SET paid_date = '2026-01-28', paid_for_due_date = '2026-01-20' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	for _, args := range []map[string]any{
		{"bill_id": id.String(), "amount": 55.0},
		{"bill_id": id.String(), "due_day": 20.0},
	} {
		if res, text := callTool(t, ctx, update, "bill_update", args); res.IsError {
			t.Fatalf("bill_update %v: %s", args, text)
		}
		if d := storedPaidFor(t, pool, id); d != "2026-01-20" {
			t.Errorf("after %v paid_for_due_date = %q, want 2026-01-20 kept", args, d)
		}
	}

	res, text := callTool(t, ctx, update, "bill_update", map[string]any{"bill_id": id.String(), "due_day": 25.0})
	if res.IsError {
		t.Fatalf("bill_update due_day: %s", text)
	}
	if d := storedPaidFor(t, pool, id); d != "" {
		t.Errorf("paid_for_due_date = %q, want NULL after due_day changed", d)
	}
	got := decodeUpdated(t, text)
	// Coverage is inferred from paid_date for the new day: Jan 28 is 3 days
	// after Jan 25.
	if strOrNil(got.PaidForDueDate) != "2026-01-25" || strOrNil(got.PaidDate) != "2026-01-28" {
		t.Errorf("paid_for=%s paid_date=%s, want 2026-01-25 2026-01-28", strOrNil(got.PaidForDueDate), strOrNil(got.PaidDate))
	}
}
