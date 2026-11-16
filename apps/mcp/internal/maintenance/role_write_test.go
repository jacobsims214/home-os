package maintenance

// #4717: the role policy lets a vendor update a maintenance task's status,
// completion and notes (auth ActUpdateStatus) and nothing else, and only for
// their own jobs when the membership is linked to a vendors row (#4799).

import (
	"context"
	"strings"
	"testing"

	"home-os/mcp/internal/testutil"
)

// TestVendorChangesStatusAndNotesOnly: a vendor sets a job in progress with a
// note; every other field is refused with a corrective message; another
// vendor's job is not found.
func TestVendorChangesStatusAndNotesOnly(t *testing.T) {
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool, "America/Chicago")
	var userID string
	if err := pool.QueryRow(f.ctx, `SELECT user_id::text FROM memberships WHERE household_id = $1`, f.id).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	plumber := insertID(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Plumber') RETURNING id`, f.id)
	electrician := insertID(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Electrician') RETURNING id`, f.id)
	sink := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, vendor_id, due_date) VALUES ($1, 'Fix sink', $2, $3::date) RETURNING id`, f.id, plumber, f.day(3))
	outlet := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, vendor_id) VALUES ($1, 'Replace outlet', $2) RETURNING id`, f.id, electrician)

	exec(t, pool, `UPDATE memberships SET role = 'vendor', vendor_id = $3 WHERE household_id = $1 AND user_id = $2`, f.id, userID, plumber)

	// Status and notes are the narrow write.
	env := mustOK(t, f.ctx, pool, "maintenance_update", map[string]any{
		"task_id": sink, "status": "in_progress", "notes": "Parts ordered",
	})
	if env.Data.Status != statusInProgress {
		t.Errorf("status = %q, want in_progress", env.Data.Status)
	}
	if env.Data.Notes == nil || *env.Data.Notes != "Parts ordered" {
		t.Errorf("notes = %v", env.Data.Notes)
	}

	// done sets the completion date.
	env = mustOK(t, f.ctx, pool, "maintenance_update", map[string]any{"task_id": sink, "status": "done"})
	if env.Data.Status != statusDone || env.Data.CompletedDate == nil {
		t.Errorf("done: status %q completed %v", env.Data.Status, env.Data.CompletedDate)
	}
	// Back to open clears it.
	env = mustOK(t, f.ctx, pool, "maintenance_update", map[string]any{"task_id": sink, "status": "pending"})
	if env.Data.CompletedDate != nil {
		t.Errorf("reopened task still has a completion date: %v", env.Data.CompletedDate)
	}

	// Every other field is refused, naming the role and the field.
	for _, tc := range []struct {
		arg string
		val any
	}{
		{"name", "Fix the sink properly"}, {"due_date", f.day(9)}, {"description", "new"},
	} {
		env := call(t, f.ctx, pool, "maintenance_update", map[string]any{"task_id": sink, tc.arg: tc.val})
		if env.OK || env.Error == nil {
			t.Fatalf("%s was accepted by a vendor: %+v", tc.arg, env)
		}
		msg, _ := (*env.Error)["message"].(string)
		if !strings.Contains(msg, "vendor") || !strings.Contains(msg, tc.arg) {
			t.Errorf("%s: message %q does not name the role and the field", tc.arg, msg)
		}
	}
	var name string
	if err := pool.QueryRow(context.Background(), `SELECT name FROM maintenance_tasks WHERE id = $1`, sink).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Fix sink" {
		t.Errorf("a refused field changed the task: name = %q", name)
	}

	// Another vendor's job is treated as absent, on both write tools.
	if env := call(t, f.ctx, pool, "maintenance_update", map[string]any{"task_id": outlet, "status": "in_progress"}); env.OK {
		t.Errorf("vendor updated another vendor's job: %+v", env)
	}
	if env := call(t, f.ctx, pool, "maintenance_complete", map[string]any{"task_id": outlet}); env.OK {
		t.Errorf("vendor completed another vendor's job: %+v", env)
	}

	// Their own job completes, and the completion is recorded.
	env = mustOK(t, f.ctx, pool, "maintenance_complete", map[string]any{"task_id": sink})
	if env.Data.Status != statusDone {
		t.Errorf("complete: status %q", env.Data.Status)
	}
}

// TestHousesitterCannotWriteMaintenance: a housesitter has no maintenance
// write at all.
func TestHousesitterCannotWriteMaintenance(t *testing.T) {
	pool := testutil.NewPool(t)
	f := seedFamily(t, pool, "America/Chicago")
	var userID string
	if err := pool.QueryRow(f.ctx, `SELECT user_id::text FROM memberships WHERE household_id = $1`, f.id).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name) VALUES ($1, 'Clean gutters') RETURNING id`, f.id)
	exec(t, pool, `UPDATE memberships SET role = 'housesitter', vendor_id = NULL WHERE household_id = $1 AND user_id = $2`, f.id, userID)

	// The handler-level guard: even without the server gate, a housesitter
	// has neither the write nor the update_status grant.
	if roleCanWrite("housesitter") || statusOnly("housesitter") {
		t.Fatal("a housesitter must have no maintenance write grant")
	}
	// And the vendor path is what statusOnly selects, not a family role.
	if !statusOnly("vendor") || roleCanWrite("vendor") {
		t.Fatal("a vendor must be the status-only role")
	}
	_ = task
}
