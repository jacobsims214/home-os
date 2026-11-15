package bill

import (
	"context"
	"encoding/json"
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

// TestCreateBill_VisibleToReadTools creates a bill and checks it shows up in
// list_bills and get_upcoming_bills for its household only.
func TestCreateBill_VisibleToReadTools(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, _ := testutil.SeedHousehold(t, pool)

	// Due today (by the database clock) so get_upcoming_bills must include it.
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
	propertyID := uuid.New()

	_, _, create := NewCreateBillTool(pool)
	res, text := callTool(t, ctx, create, "create_bill", map[string]any{
		"name":        "  Electric  ",
		"amount":      125.5,
		"due_day":     float64(today.Day()),
		"category":    "Utilities",
		"is_autopay":  true,
		"vendor_id":   vendorID.String(),
		"entity_type": "property",
		"entity_id":   propertyID.String(),
		"payment_url": "https://power.example.com/pay",
		"notes":       "Paperless billing",
	})
	if res.IsError {
		t.Fatalf("create_bill returned tool error: %s", text)
	}

	var got createdBill
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	if got.ID == uuid.Nil || got.Name != "Electric" || got.Amount != 125.5 || got.DueDay != today.Day() {
		t.Errorf("unexpected bill: %+v", got)
	}
	if !got.IsAutopay || got.Category == nil || *got.Category != "Utilities" {
		t.Errorf("autopay/category not stored: %+v", got)
	}
	if got.VendorID == nil || *got.VendorID != vendorID || got.EntityID == nil || *got.EntityID != propertyID {
		t.Errorf("vendor/entity not stored: %+v", got)
	}
	if want := today.Format("2006-01-02"); got.NextDueDate != want || got.DaysUntilDue != 0 {
		t.Errorf("next due = %s (+%d days), want %s (+0)", got.NextDueDate, got.DaysUntilDue, want)
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
		if err := json.Unmarshal([]byte(text), &rows); err != nil {
			t.Fatalf("%s: decode %q: %v", name, text, err)
		}
		out := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			out[i] = r.ID
		}
		return out
	}

	_, _, list := NewListBillsTool(pool)
	_, _, upcoming := NewGetUpcomingBillsTool(pool)

	tests := []struct {
		name    string
		ctx     context.Context
		handler toolHandler
		tool    string
		args    map[string]any
		want    int
	}{
		{"list_bills in own household", ctx, list, "list_bills", nil, 1},
		{"list_bills by category", ctx, list, "list_bills", map[string]any{"category": "Utilities"}, 1},
		{"get_upcoming_bills in own household", ctx, upcoming, "get_upcoming_bills", map[string]any{"days": float64(7)}, 1},
		{"list_bills in other household", otherCtx, list, "list_bills", nil, 0},
		{"get_upcoming_bills in other household", otherCtx, upcoming, "get_upcoming_bills", map[string]any{"days": float64(7)}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got2 := ids(t, tc.ctx, tc.handler, tc.tool, tc.args)
			if len(got2) != tc.want {
				t.Fatalf("got %d bills (%v), want %d", len(got2), got2, tc.want)
			}
			if tc.want == 1 && got2[0] != got.ID {
				t.Errorf("got bill %s, want %s", got2[0], got.ID)
			}
		})
	}
}

// TestCreateBill_MinimalFields stores NULL for every optional field.
func TestCreateBill_MinimalFields(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)

	_, _, create := NewCreateBillTool(pool)
	res, text := callTool(t, ctx, create, "create_bill", map[string]any{
		"name": "Netflix", "amount": float64(0), "due_day": float64(15),
	})
	if res.IsError {
		t.Fatalf("create_bill returned tool error: %s", text)
	}
	var got createdBill
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	if got.IsAutopay || got.Category != nil || got.VendorID != nil || got.EntityType != nil ||
		got.EntityID != nil || got.PaymentURL != nil || got.Notes != nil {
		t.Errorf("optional fields should be empty: %s", text)
	}
	if got.NextDueDate == "" {
		t.Error("next_due_date missing")
	}
}

// TestCreateBill_Validation checks that bad input is a tool error and
// inserts nothing.
func TestCreateBill_Validation(t *testing.T) {
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

	_, _, create := NewCreateBillTool(pool)

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
		{"is_autopay not a bool", valid(map[string]any{"is_autopay": "yes"}), "is_autopay must be a boolean"},
		{"vendor_id not a uuid", valid(map[string]any{"vendor_id": "abc"}), "vendor_id must be a UUID"},
		{"vendor from another household", valid(map[string]any{"vendor_id": otherVendor.String()}), "vendor_id not found"},
		{"entity_type without entity_id", valid(map[string]any{"entity_type": "vehicle"}), "must be provided together"},
		{"entity_id without entity_type", valid(map[string]any{"entity_id": uuid.NewString()}), "must be provided together"},
		{"entity_id not a uuid", valid(map[string]any{"entity_type": "pet", "entity_id": "x"}), "entity_id must be a UUID"},
		{"payment_url not http", valid(map[string]any{"payment_url": "javascript:alert(1)"}), "payment_url must be"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, text := callTool(t, ctx, create, "create_bill", tc.args)
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
		res, _ := callTool(t, context.Background(), create, "create_bill", valid(nil))
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
