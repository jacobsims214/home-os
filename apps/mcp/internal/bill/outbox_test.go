package bill

// #5513: a bill change must enqueue its outbox event whoever makes it.
//
// Until migration 088 the enqueue was handler code -- three INSERTs in the
// API's bill repo and a fourth copy in writeOutbox here (called from
// bill_create, bill_update, bill_delete and bill_pay). Handler code fires
// only when that handler runs, which is #5314's finding: the MCP asset tools
// wrote the assets row directly and enqueued nothing, and
// assets_propagate_property() UPDATEs bills.property_id with no handler at
// all, so a moved asset changed its bills silently.
//
// The enqueue is now the bills_outbox / assets_outbox trigger, so these
// tests drive the real MCP tool handlers and assert one row per write. The
// REST path is proved beside it in apps/api/internal/bill/outbox_test.go and
// apps/api/internal/asset/outbox_test.go (a Go module cannot import another
// module's internal package), against the same trigger.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/testutil"
)

// outboxCount counts the events of one type about one entity in a household.
func outboxCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventType, entityID string, householdID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox_events
		  WHERE household_id = $1 AND type = $2 AND payload->>'id' = $3`,
		householdID, eventType, entityID,
	).Scan(&n); err != nil {
		t.Fatalf("count %s events: %v", eventType, err)
	}
	return n
}

// TestBillCreateEnqueuesExactlyOneEvent proves bill_create (the MCP write
// path) leaves exactly one bill.created, carrying what the API used to write.
// "Exactly one" is the other half of the #5513 bug: a trigger left beside the
// old writeOutbox call would double every agent write.
func TestBillCreateEnqueuesExactlyOneEvent(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)

	_, _, create := NewBillCreateTool(pool)
	res, _ := callTool(t, ctx, create, "bill_create", map[string]any{
		"name":    "Electric",
		"amount":  120.50,
		"due_day": 15,
	})
	if res.IsError {
		t.Fatalf("bill_create failed: %s", res.Content[0])
	}

	var (
		billID string
		name   string
	)
	if err := pool.QueryRow(context.Background(),
		`SELECT id::text, name FROM bills WHERE household_id = $1`, hh.HouseholdID,
	).Scan(&billID, &name); err != nil {
		t.Fatalf("read bill: %v", err)
	}

	if n := outboxCount(t, context.Background(), pool, "bill.created", billID, hh.HouseholdID); n != 1 {
		t.Errorf("bill.created events = %d, want 1 (a copy beside the trigger would show here)", n)
	}
	var payloadName string
	if err := pool.QueryRow(context.Background(),
		`SELECT payload->>'name' FROM outbox_events
		  WHERE type = 'bill.created' AND payload->>'id' = $1`, billID,
	).Scan(&payloadName); err != nil {
		t.Fatalf("read bill.created payload: %v", err)
	}
	if payloadName != "Electric" {
		t.Errorf("bill.created payload name = %q, want Electric", payloadName)
	}
}

// TestBillUpdateFromMCPEnqueuesExactlyOneEvent proves bill_update emits one
// bill.updated and no bill.created.
func TestBillUpdateFromMCPEnqueuesExactlyOneEvent(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ctxb := context.Background()

	if _, err := pool.Exec(ctxb,
		`INSERT INTO bills (id, household_id, name, amount, due_day)
		 VALUES ($1, $2, 'Electric', 100.00, 15)`,
		uuid.MustParse("11111111-1111-1111-1111-111111111111"), hh.HouseholdID,
	); err != nil {
		t.Fatalf("seed bill: %v", err)
	}
	billID := "11111111-1111-1111-1111-111111111111"

	_, _, update := NewBillUpdateTool(pool)
	res, _ := callTool(t, ctx, update, "bill_update", map[string]any{
		"bill_id": billID,
		"amount":  150.00,
	})
	if res.IsError {
		t.Fatalf("bill_update failed: %s", res.Content[0])
	}

	if n := outboxCount(t, ctxb, pool, "bill.updated", billID, hh.HouseholdID); n != 1 {
		t.Errorf("bill.updated events = %d, want 1", n)
	}
	// The seed INSERT emitted its own bill.created -- exactly one, and the
	// update must not add a second.
	if n := outboxCount(t, ctxb, pool, "bill.created", billID, hh.HouseholdID); n != 1 {
		t.Errorf("bill.created events = %d, want 1 (from the seed insert, not the update)", n)
	}
}

// TestBillDeleteFromMCPEnqueuesOneEvent: the deleted row's own event comes
// from the trigger, which is why bill.deleted still carries the name of a row
// that no longer exists.
func TestBillDeleteFromMCPEnqueuesOneEvent(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ctxb := context.Background()
	billID := "22222222-2222-2222-2222-222222222222"

	if _, err := pool.Exec(ctxb,
		`INSERT INTO bills (id, household_id, name, amount, due_day)
		 VALUES ($1, $2, 'Water', 40.00, 5)`, uuid.MustParse(billID), hh.HouseholdID,
	); err != nil {
		t.Fatalf("seed bill: %v", err)
	}

	_, _, del := NewBillDeleteTool(pool)
	res, _ := callTool(t, ctx, del, "bill_delete", map[string]any{"bill_id": billID})
	if res.IsError {
		t.Fatalf("bill_delete failed: %s", res.Content[0])
	}

	var events int
	var name string
	if err := pool.QueryRow(ctxb,
		`SELECT COUNT(*), COALESCE(MAX(payload->>'name'), '')
		   FROM outbox_events
		  WHERE household_id = $1 AND type = 'bill.deleted' AND payload->>'id' = $2`,
		hh.HouseholdID, billID,
	).Scan(&events, &name); err != nil {
		t.Fatalf("read bill.deleted: %v", err)
	}
	if events != 1 || name != "Water" {
		t.Errorf("bill.deleted events = %d (name %q), want 1 (Water)", events, name)
	}
}

// TestBillPayEnqueuesOneEventWithPaidDate: bill_pay used to add paid_date to
// the payload by hand. The trigger derives it from the row, so the key must
// still be there -- and there must still be exactly one event.
func TestBillPayEnqueuesOneEventWithPaidDate(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	ctxb := context.Background()
	billID := "33333333-3333-3333-3333-333333333333"

	// Due yesterday so bill_pay has something to pay.
	if _, err := pool.Exec(ctxb,
		`INSERT INTO bills (id, household_id, name, amount, due_day, frequency, due_date)
		 VALUES ($1, $2, 'Gas', 60.00, NULL, 'once', CURRENT_DATE - 1)`,
		uuid.MustParse(billID), hh.HouseholdID,
	); err != nil {
		t.Fatalf("seed bill: %v", err)
	}
	// The seed above already emitted bill.created; count from here.
	before := outboxCount(t, ctxb, pool, "bill.updated", billID, hh.HouseholdID)

	_, _, pay := NewBillPayTool(pool)
	res, text := callTool(t, ctx, pay, "bill_pay", map[string]any{
		"bill_id":   billID,
		"paid_date": time.Now().Format(time.DateOnly),
	})
	if res.IsError {
		t.Fatalf("bill_pay failed: %s", text)
	}

	if n := outboxCount(t, ctxb, pool, "bill.updated", billID, hh.HouseholdID); n != before+1 {
		t.Errorf("bill.updated events = %d, want %d (exactly one more)", n, before+1)
	}
	var paid string
	if err := pool.QueryRow(ctxb,
		`SELECT COALESCE(payload->>'paid_date', '') FROM outbox_events
		  WHERE type = 'bill.updated' AND payload->>'id' = $1
		  ORDER BY created_at DESC, id DESC LIMIT 1`, billID,
	).Scan(&paid); err != nil {
		t.Fatalf("read paid_date: %v", err)
	}
	if paid != time.Now().Format(time.DateOnly) {
		t.Errorf("event paid_date = %q, want today", paid)
	}
}
