package vendors

import (
	"context"
	"strings"
	"testing"

	"home-os/mcp/internal/testutil"
)

// TestContactFindUsesVendorIDIndexes pins #4766: contact_find's per-row
// bills and open-maintenance counts can use 065's idx_bills_vendor_id and
// idx_maintenance_tasks_vendor_id. With enable_seqscan off the planner still
// picks a seq scan when nothing else can serve the predicate, so a plan
// without "Seq Scan on bills/maintenance_tasks" proves the indexes apply.
// (Family-sized tables may still be seq-scanned by choice; that is cheap.)
func TestContactFindUsesVendorIDIndexes(t *testing.T) {
	t.Parallel()
	pool := testutil.NewPool(t)
	_, h := testutil.SeedHousehold(t, pool)
	ctx := context.Background()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}

	rows, err := tx.Query(ctx, "EXPLAIN "+contactSelect, h.HouseholdID.String(), nil, "", "", "", "", defaultLimit, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(plan, "\n")
	for _, bad := range []string{"Seq Scan on bills", "Seq Scan on maintenance_tasks"} {
		if strings.Contains(text, bad) {
			t.Errorf("plan has %q:\n%s", bad, text)
		}
	}
	for _, idx := range []string{"idx_bills_vendor_id", "idx_maintenance_tasks_vendor_id"} {
		if !strings.Contains(text, idx) {
			t.Errorf("plan does not use %s:\n%s", idx, text)
		}
	}
}
