package bill

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/testutil"
)

// insertBill inserts a monthly bill directly (no loop guard, no outbox).
func insertBill(t *testing.T, pool *pgxpool.Pool, householdID uuid.UUID, name string, amount float64, dueDay int, category *string, autopay bool, notes *string) string {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO bills (household_id, name, amount, due_day, category, is_autopay, notes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		householdID, name, amount, dueDay, category, autopay, notes,
	).Scan(&id); err != nil {
		t.Fatalf("insert bill %s: %v", name, err)
	}
	return id.String()
}

func findBills(t *testing.T, ctx context.Context, pool *pgxpool.Pool, args map[string]any) ([]billData, wireEnvelope) {
	t.Helper()
	_, _, find := NewBillFindTool(pool)
	res, text := callTool(t, ctx, find, "bill_find", args)
	if res.IsError {
		t.Fatalf("bill_find(%v) returned tool error: %s", args, text)
	}
	var bills []billData
	if err := unmarshalData(text, &bills); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return bills, decodeEnvelope(t, text)
}

func names(bills []billData) string {
	out := make([]string, len(bills))
	for i, b := range bills {
		out[i] = b.Name
	}
	return strings.Join(out, ",")
}

// TestBillFind_Filters covers id, query, category (any case), autopay and
// due_within_days, the sort by next due date, and the summary/next hints.
func TestBillFind_Filters(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTZ(t, pool, hh.HouseholdID, "UTC")
	today := householdTodayIn(t, "UTC")

	// Due days relative to today so the order is deterministic whatever the
	// date: soon (today), later (in ~10 days, clamped), and a legacy row.
	soonDay := today.Day()
	laterDate := today.AddDate(0, 0, 10)
	utilities, streaming := ptr("Utilities"), ptr("Streaming")
	soon := insertBill(t, pool, hh.HouseholdID, "Water", 40, soonDay, utilities, false, ptr("city water"))
	later := insertBill(t, pool, hh.HouseholdID, "Netflix", 15.49, laterDate.Day(), streaming, true, nil)
	legacy := insertPayBill(t, pool, hh.HouseholdID, nil, nil).String()

	all, env := findBills(t, ctx, pool, nil)
	if len(all) != 3 || all[0].ID != soon || all[2].ID != legacy {
		t.Fatalf("all bills = %s, want Water first and the legacy bill last", names(all))
	}
	if env.Summary != "Found 3 bills." || len(env.Next) == 0 || !strings.Contains(env.Next[0], "bill_pay bill_id="+soon) {
		t.Errorf("summary/next = %q %v", env.Summary, env.Next)
	}
	if all[0].NextDueWeekday == nil || *all[0].NextDueWeekday != today.Weekday().String()[:3] {
		t.Errorf("next_due_weekday = %v, want %s", all[0].NextDueWeekday, today.Weekday().String()[:3])
	}

	tests := []struct {
		name string
		args map[string]any
		want []string
	}{
		{"id", map[string]any{"id": later}, []string{later}},
		{"query in name, any case", map[string]any{"query": "NETF"}, []string{later}},
		{"query in notes", map[string]any{"query": "city"}, []string{soon}},
		{"query wildcard is literal", map[string]any{"query": "%"}, nil},
		{"category any case", map[string]any{"category": "utilities"}, []string{soon}},
		{"autopay true", map[string]any{"autopay": true}, []string{later}},
		{"is_autopay alias", map[string]any{"is_autopay": true}, []string{later}},
		{"due today", map[string]any{"due_within_days": float64(0)}, []string{soon}},
		{"due within 31 days", map[string]any{"due_within_days": float64(31)}, []string{soon, later}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, env := findBills(t, ctx, pool, tc.args)
			ids := make([]string, len(got))
			for i, b := range got {
				ids[i] = b.ID
			}
			if fmt.Sprint(ids) != fmt.Sprint(tc.want) && !(len(ids) == 0 && len(tc.want) == 0) {
				t.Errorf("got %v (%s), want %v", ids, names(got), tc.want)
			}
			if len(got) == 0 && (!strings.Contains(env.Summary, "No bills") || len(env.Next) == 0) {
				t.Errorf("empty result summary/next = %q %v", env.Summary, env.Next)
			}
		})
	}

	_, env = findBills(t, ctx, pool, map[string]any{"id": later})
	if !strings.HasPrefix(env.Summary, "Found bill Netflix, $15.49 due ") {
		t.Errorf("single-bill summary = %q", env.Summary)
	}
}

// TestBillFind_Pagination pages with limit and next_cursor passed back as
// cursor, still accepts the legacy plain-offset cursor, and rejects a bad
// cursor with an actionable error.
func TestBillFind_Pagination(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	for i := 1; i <= 5; i++ {
		insertBill(t, pool, hh.HouseholdID, fmt.Sprintf("Bill %d", i), float64(i), 15, nil, false, nil)
	}

	page1, env := findBills(t, ctx, pool, map[string]any{"limit": float64(2)})
	if names(page1) != "Bill 1,Bill 2" || !env.Truncated || env.Total == nil || *env.Total != 5 {
		t.Fatalf("page 1 = %s truncated=%v total=%v", names(page1), env.Truncated, env.Total)
	}
	if env.NextCursor != respond.EncodeCursor(2) || env.Summary != "Found 5 bills; showing 1-2." {
		t.Errorf("page 1 narrow/summary = %q / %q", env.Narrow, env.Summary)
	}
	page2, env := findBills(t, ctx, pool, map[string]any{"limit": float64(2), "cursor": env.NextCursor})
	if names(page2) != "Bill 3,Bill 4" || env.NextCursor != respond.EncodeCursor(4) || strings.Contains(env.Narrow, "cursor=") {
		t.Errorf("page 2 = %s next_cursor %q narrow %q", names(page2), env.NextCursor, env.Narrow)
	}
	page3, env := findBills(t, ctx, pool, map[string]any{"limit": float64(2), "cursor": "4"}) // legacy plain offset
	if names(page3) != "Bill 5" || env.Truncated || env.NextCursor != "" {
		t.Errorf("page 3 = %s truncated=%v", names(page3), env.Truncated)
	}

	_, _, find := NewBillFindTool(pool)
	for _, tc := range []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"bad cursor", map[string]any{"cursor": "page-two"}, "cursor"},
		{"negative due_within_days", map[string]any{"due_within_days": float64(-1)}, "due_within_days"},
		{"limit too big", map[string]any{"limit": float64(500)}, "limit"},
		{"id not a uuid", map[string]any{"id": "Electric"}, "id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, text := callTool(t, ctx, find, "bill_find", tc.args)
			env := decodeEnvelope(t, text)
			if !res.IsError || env.Error == nil || env.Error.Field != tc.field || env.Error.Fix == "" || env.Error.Example == nil {
				t.Errorf("want actionable error on %s, got %s", tc.field, text)
			}
		})
	}

	res, text := callTool(t, ctx, find, "bill_find", map[string]any{"id": uuid.NewString()})
	if env := decodeEnvelope(t, text); !res.IsError || !strings.Contains(env.Summary, "No bill with id") || !strings.Contains(env.Error.Fix, "bill_find") {
		t.Errorf("unknown id: %s", text)
	}
}

// TestBillFind_Empty gives the catalog's empty-state hint.
func TestBillFind_Empty(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	got, env := findBills(t, ctx, pool, nil)
	if len(got) != 0 || env.Summary != "No bills yet." || len(env.Next) != 1 ||
		!strings.Contains(env.Next[0], "bill_create") || !strings.Contains(env.Next[0], "calendar") {
		t.Errorf("empty bill_find = %+v", env)
	}
}

// TestBillCreate_LoopGuard returns the first bill with duplicate true when
// the same bill is created again minutes later, and inserts nothing new.
func TestBillCreate_LoopGuard(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, _ := testutil.SeedHousehold(t, pool)
	_, _, create := NewBillCreateTool(pool)
	args := map[string]any{"name": "Internet", "amount": 70.0, "due_day": 5.0}

	_, text := callTool(t, ctx, create, "bill_create", args)
	var first billData
	if err := unmarshalData(text, &first); err != nil {
		t.Fatal(err)
	}
	env := decodeEnvelope(t, text)
	if first.Duplicate || !strings.HasPrefix(env.Summary, "Created bill Internet, $70.00 due ") ||
		len(env.Next) == 0 || !strings.Contains(env.Next[0], "bill_pay bill_id="+first.ID) {
		t.Errorf("first create: %s", text)
	}

	res, text := callTool(t, ctx, create, "bill_create", map[string]any{"name": "internet", "amount": 70.0, "due_day": 5.0})
	var again billData
	if err := unmarshalData(text, &again); err != nil {
		t.Fatal(err)
	}
	if res.IsError || !again.Duplicate || again.ID != first.ID || !strings.Contains(decodeEnvelope(t, text).Next[0], "bill_update") {
		t.Errorf("repeat create: %s", text)
	}
	if n := countBills(t, pool); n != 1 {
		t.Errorf("bills = %d, want 1", n)
	}

	// A different amount is a different bill.
	callTool(t, ctx, create, "bill_create", map[string]any{"name": "Internet", "amount": 75.0, "due_day": 5.0})
	if n := countBills(t, pool); n != 2 {
		t.Errorf("bills = %d, want 2", n)
	}

	// Older than the window: created again.
	if _, err := pool.Exec(context.Background(), `UPDATE bills SET created_at = NOW() - interval '11 minutes'`); err != nil {
		t.Fatal(err)
	}
	callTool(t, ctx, create, "bill_create", args)
	if n := countBills(t, pool); n != 3 {
		t.Errorf("bills = %d, want 3", n)
	}
}

// TestBillCreate_OldParamNames accepts is_autopay and vendor_id as hidden
// aliases for autopay and contact_id.
func TestBillCreate_OldParamNames(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	vendor := insertVendor(t, pool, hh.HouseholdID, "Power Co")
	_, _, create := NewBillCreateTool(pool)
	res, text := callTool(t, ctx, create, "bill_create", map[string]any{
		"name": "Electric", "amount": 99.0, "due_day": 3.0, "is_autopay": true, "vendor_id": vendor.String(),
	})
	var b billData
	if err := unmarshalData(text, &b); err != nil || res.IsError {
		t.Fatalf("create with old names: %s", text)
	}
	if !b.Autopay || b.ContactID == nil || *b.ContactID != vendor.String() {
		t.Errorf("aliases not applied: %s", text)
	}
}

// TestActionableErrors checks one error per tool carries the field, a fix
// and a valid example (the envelope the catalog requires).
func TestActionableErrors(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	id := insertBill(t, pool, hh.HouseholdID, "Gas", 30, 12, nil, false, nil)
	_, _, create := NewBillCreateTool(pool)
	_, _, update := NewBillUpdateTool(pool)
	_, _, del := NewBillDeleteTool(pool)
	_, _, pay := NewBillPayTool(pool)
	future := time.Now().AddDate(0, 0, 3).Format(time.DateOnly)

	tests := []struct {
		tool    string
		handler toolHandler
		args    map[string]any
		field   string
		fixHas  string
	}{
		{"bill_create", create, map[string]any{"name": "Gas", "amount": 30.0, "due_day": 32.0}, "due_day", "1 and 31"},
		{"bill_update", update, map[string]any{"bill_id": id, "contact_id": uuid.NewString()}, "contact_id", "contact_find"},
		{"bill_delete", del, map[string]any{"bill_id": "Gas"}, "bill_id", "bill_find"},
		{"bill_pay", pay, map[string]any{"bill_id": id, "paid_date": future}, "paid_date", "today"},
	}
	for _, tc := range tests {
		t.Run(tc.tool, func(t *testing.T) {
			res, text := callTool(t, ctx, tc.handler, tc.tool, tc.args)
			env := decodeEnvelope(t, text)
			if !res.IsError || env.OK || env.Error == nil {
				t.Fatalf("want isError result, got %s", text)
			}
			if env.Error.Field != tc.field || !strings.Contains(env.Error.Fix, tc.fixHas) || len(env.Error.Example) == 0 {
				t.Errorf("error = %+v, want field %s, fix mentioning %q and an example", env.Error, tc.field, tc.fixHas)
			}
		})
	}
}

// TestWriteSummaries pins the summaries and next hints of update, pay and
// delete.
func TestWriteSummaries(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	id := insertBill(t, pool, hh.HouseholdID, "Gas", 30, 12, nil, false, nil)
	_, _, update := NewBillUpdateTool(pool)
	_, _, del := NewBillDeleteTool(pool)
	_, _, pay := NewBillPayTool(pool)

	_, text := callTool(t, ctx, update, "bill_update", map[string]any{"bill_id": id, "amount": 31.0, "autopay": true})
	if env := decodeEnvelope(t, text); !strings.HasPrefix(env.Summary, "Updated bill Gas (amount, autopay); next due ") ||
		!strings.Contains(env.Next[0], "bill_pay bill_id="+id) {
		t.Errorf("update: %s", text)
	}

	_, text = callTool(t, ctx, pay, "bill_pay", map[string]any{"bill_id": id, "paid_date": "2026-01-12"})
	// The next due date is the next unpaid one as of today, as bill_find
	// reports it (#4653), so it moves with the clock.
	nextDue := listedBill(t, ctx, pool, id).dueText()
	if env := decodeEnvelope(t, text); env.Summary != "Paid Gas $31.00 for Jan 12; next "+nextDue+"." {
		t.Errorf("pay summary = %q, want next %s", env.Summary, nextDue)
	}
	_, text = callTool(t, ctx, pay, "bill_pay", map[string]any{"bill_id": id, "paid_date": "2026-01-12"})
	if env := decodeEnvelope(t, text); env.Summary != "Gas was already paid for Jan 12; nothing changed; next "+nextDue+"." {
		t.Errorf("repeat pay summary = %q, want next %s", env.Summary, nextDue)
	}

	_, text = callTool(t, ctx, del, "bill_delete", map[string]any{"bill_id": id})
	if env := decodeEnvelope(t, text); env.Summary != "Deleted bill Gas." || !strings.Contains(env.Next[0], "bill_find") {
		t.Errorf("delete: %s", text)
	}
}

// TestSummarizeByCategory keeps the old get_bill_summary numbers for
// budget_summary.
func TestSummarizeByCategory(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	_, other := testutil.SeedHousehold(t, pool)
	insertBill(t, pool, hh.HouseholdID, "Water", 40, 1, ptr("Utilities"), false, nil)
	insertBill(t, pool, hh.HouseholdID, "Electric", 110.5, 2, ptr("Utilities"), false, nil)
	insertBill(t, pool, hh.HouseholdID, "Gym", 25, 3, nil, false, nil)
	insertBill(t, pool, other.HouseholdID, "Not mine", 999, 3, nil, false, nil)

	totals, grand, err := SummarizeByCategory(ctx, pool, hh.HouseholdID.String(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(totals) != "[{Utilities 2 150.5} {Uncategorized 1 25}]" || grand != 175.5 {
		t.Errorf("totals = %v grand = %v", totals, grand)
	}

	empty, grand, err := SummarizeByCategory(ctx, pool, uuid.NewString(), time.Now())
	if err != nil || empty == nil || len(empty) != 0 || grand != 0 {
		t.Errorf("empty household = %v %v %v", empty, grand, err)
	}
}
