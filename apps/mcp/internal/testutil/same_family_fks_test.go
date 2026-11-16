package testutil

// Tests for migration 058 (#4697): bills/maintenance vendor_id,
// maintenance_tasks.schedule_id and calendars.property_id are same-family
// composite FKs. See KB decisions/domain-model-v2.md.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sameFamilyRows is one family's vendor, schedule and calendar on top of
// seedV2Family's property.
type sameFamilyRows struct {
	v2Fixture
	vendor, schedule, calendar uuid.UUID
}

func seedSameFamilyRows(t *testing.T, pool *pgxpool.Pool) sameFamilyRows {
	t.Helper()
	f := sameFamilyRows{v2Fixture: seedV2Family(t, pool)}
	f.vendor = insertID(t, pool, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Plumber') RETURNING id`, f.household)
	f.schedule = insertID(t, pool, `INSERT INTO maintenance_schedules (household_id, name, rrule) VALUES ($1, 'Filters', 'FREQ=MONTHLY') RETURNING id`, f.household)
	f.calendar = insertID(t, pool,
		`INSERT INTO calendars (household_id, name, type, caldav_uid, property_id) VALUES ($1, 'House', 'properties', $2, $3) RETURNING id`,
		f.household, uuid.NewString(), f.property)
	return f
}

func TestSameFamilyFKs058_RejectCrossFamilyRefs(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	a := seedSameFamilyRows(t, pool)
	b := seedSameFamilyRows(t, pool)

	billA := insertID(t, pool, `INSERT INTO bills (household_id, name) VALUES ($1, 'Water') RETURNING id`, a.household)
	taskA := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name) VALUES ($1, 'Fix sink') RETURNING id`, a.household)

	rejected := []struct {
		name string
		sql  string
		args []any
	}{
		{"insert bill with another family's vendor",
			`INSERT INTO bills (household_id, name, vendor_id) VALUES ($1, 'x', $2)`, []any{a.household, b.vendor}},
		{"update bill to another family's vendor",
			`UPDATE bills SET vendor_id = $2 WHERE id = $1`, []any{billA, b.vendor}},
		{"insert task with another family's vendor",
			`INSERT INTO maintenance_tasks (household_id, name, vendor_id) VALUES ($1, 'x', $2)`, []any{a.household, b.vendor}},
		{"update task to another family's vendor",
			`UPDATE maintenance_tasks SET vendor_id = $2 WHERE id = $1`, []any{taskA, b.vendor}},
		{"insert schedule with another family's vendor",
			`INSERT INTO maintenance_schedules (household_id, name, rrule, vendor_id) VALUES ($1, 'x', 'FREQ=YEARLY', $2)`, []any{a.household, b.vendor}},
		{"update schedule to another family's vendor",
			`UPDATE maintenance_schedules SET vendor_id = $2 WHERE id = $1`, []any{a.schedule, b.vendor}},
		{"insert task with another family's schedule",
			`INSERT INTO maintenance_tasks (household_id, name, schedule_id) VALUES ($1, 'x', $2)`, []any{a.household, b.schedule}},
		{"update task to another family's schedule",
			`UPDATE maintenance_tasks SET schedule_id = $2 WHERE id = $1`, []any{taskA, b.schedule}},
		{"insert calendar for another family's property",
			`INSERT INTO calendars (household_id, name, type, caldav_uid, property_id) VALUES ($1, 'x', 'properties', $2, $3)`,
			[]any{a.household, uuid.NewString(), b.property}},
		{"update calendar to another family's property",
			`UPDATE calendars SET property_id = $2 WHERE id = $1`, []any{a.calendar, b.property}},
		{"bill with a missing vendor",
			`INSERT INTO bills (household_id, name, vendor_id) VALUES ($1, 'x', $2)`, []any{a.household, uuid.New()}},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tc.sql, tc.args...)
			wantSQLState(t, err, sqlstateFKViolation)
		})
	}

	// Same-family references still work.
	mustExec(t, pool, `UPDATE bills SET vendor_id = $2 WHERE id = $1`, billA, a.vendor)
	mustExec(t, pool, `UPDATE maintenance_tasks SET vendor_id = $2, schedule_id = $3 WHERE id = $1`, taskA, a.vendor, a.schedule)
	mustExec(t, pool, `UPDATE maintenance_schedules SET vendor_id = $2 WHERE id = $1`, a.schedule, a.vendor)
}

// The delete actions are unchanged: vendor and schedule deletes null only
// the reference (never household_id), and a property delete cascades only to
// its own family's calendars.
func TestSameFamilyFKs058_DeleteActions(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	a := seedSameFamilyRows(t, pool)
	b := seedSameFamilyRows(t, pool)

	bill := insertID(t, pool, `INSERT INTO bills (household_id, name, vendor_id) VALUES ($1, 'Water', $2) RETURNING id`, a.household, a.vendor)
	task := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, vendor_id, schedule_id) VALUES ($1, 'Fix', $2, $3) RETURNING id`,
		a.household, a.vendor, a.schedule)
	mustExec(t, pool, `UPDATE maintenance_schedules SET vendor_id = $2 WHERE id = $1`, a.schedule, a.vendor)

	mustExec(t, pool, `DELETE FROM vendors WHERE id = $1`, a.vendor)
	var billVendor, taskVendor, schedVendor *uuid.UUID
	var billHH uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT vendor_id, household_id FROM bills WHERE id = $1`, bill), &billVendor, &billHH)
	mustScan(t, pool.QueryRow(ctx, `SELECT vendor_id FROM maintenance_tasks WHERE id = $1`, task), &taskVendor)
	mustScan(t, pool.QueryRow(ctx, `SELECT vendor_id FROM maintenance_schedules WHERE id = $1`, a.schedule), &schedVendor)
	if billVendor != nil || taskVendor != nil || schedVendor != nil {
		t.Errorf("vendor delete should null vendor_id: bill=%v task=%v schedule=%v", billVendor, taskVendor, schedVendor)
	}
	if billHH != a.household {
		t.Errorf("vendor delete changed bills.household_id to %v", billHH)
	}

	mustExec(t, pool, `DELETE FROM maintenance_schedules WHERE id = $1`, a.schedule)
	var taskSched *uuid.UUID
	mustScan(t, pool.QueryRow(ctx, `SELECT schedule_id FROM maintenance_tasks WHERE id = $1`, task), &taskSched)
	if taskSched != nil {
		t.Errorf("schedule delete should null schedule_id, got %v", taskSched)
	}

	calendars := func(h uuid.UUID) int {
		t.Helper()
		var n int
		mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM calendars WHERE household_id = $1`, h), &n)
		return n
	}
	mustExec(t, pool, `DELETE FROM properties WHERE id = $1`, b.property)
	if n := calendars(a.household); n != 1 {
		t.Errorf("family B's property delete left family A with %d calendars, want 1", n)
	}
	if n := calendars(b.household); n != 0 {
		t.Errorf("family B's property calendar should cascade, %d left", n)
	}
}

// Down to 057, write the cross-family rows the old FKs allowed (including a
// family-A calendar on family B's property with an event), then up/down/up:
// 058 up clears the foreign references, keeps the calendar and its event,
// and family B's property delete no longer reaches family A.
func TestSameFamilyFKs058_RepairAndReversible(t *testing.T) {
	pool := NewPool(t)
	ctx := context.Background()
	a := seedSameFamilyRows(t, pool)
	b := seedSameFamilyRows(t, pool)

	applyMigration(t, pool, "058_same_family_fks.down.sql")

	okBill := insertID(t, pool, `INSERT INTO bills (household_id, name, vendor_id) VALUES ($1, 'ok', $2) RETURNING id`, a.household, a.vendor)
	badBill := insertID(t, pool, `INSERT INTO bills (household_id, name, vendor_id) VALUES ($1, 'bad', $2) RETURNING id`, a.household, b.vendor)
	badTask := insertID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, vendor_id, schedule_id) VALUES ($1, 'bad', $2, $3) RETURNING id`,
		a.household, b.vendor, b.schedule)
	badSched := insertID(t, pool, `INSERT INTO maintenance_schedules (household_id, name, rrule, vendor_id) VALUES ($1, 'bad', 'FREQ=YEARLY', $2) RETURNING id`,
		a.household, b.vendor)
	badCal := insertID(t, pool,
		`INSERT INTO calendars (household_id, name, type, caldav_uid, property_id) VALUES ($1, 'bad', 'properties', $2, $3) RETURNING id`,
		a.household, uuid.NewString(), b.property)
	mustExec(t, pool, `INSERT INTO calendar_objects (calendar_id, uid, etag, ical_data) VALUES ($1, $2, 'e', 'BEGIN:VCALENDAR')`, badCal, uuid.NewString())

	isNull := func(sql string, id uuid.UUID) bool {
		t.Helper()
		var v *uuid.UUID
		mustScan(t, pool.QueryRow(ctx, sql, id), &v)
		return v == nil
	}

	for cycle := 0; cycle < 2; cycle++ {
		applyMigration(t, pool, "058_same_family_fks.up.sql")

		if isNull(`SELECT vendor_id FROM bills WHERE id = $1`, okBill) {
			t.Errorf("cycle %d: same-family bill vendor was cleared", cycle)
		}
		for name, q := range map[string]struct {
			sql string
			id  uuid.UUID
		}{
			"bills.vendor_id":                 {`SELECT vendor_id FROM bills WHERE id = $1`, badBill},
			"maintenance_tasks.vendor_id":     {`SELECT vendor_id FROM maintenance_tasks WHERE id = $1`, badTask},
			"maintenance_tasks.schedule_id":   {`SELECT schedule_id FROM maintenance_tasks WHERE id = $1`, badTask},
			"maintenance_schedules.vendor_id": {`SELECT vendor_id FROM maintenance_schedules WHERE id = $1`, badSched},
			"calendars.property_id":           {`SELECT property_id FROM calendars WHERE id = $1`, badCal},
		} {
			if !isNull(q.sql, q.id) {
				t.Errorf("cycle %d: cross-family %s not cleared", cycle, name)
			}
		}

		if cycle == 0 {
			applyMigration(t, pool, "058_same_family_fks.down.sql")
		}
	}

	mustExec(t, pool, `DELETE FROM properties WHERE id = $1`, b.property)
	var cals, events int
	mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM calendars WHERE household_id = $1`, a.household), &cals)
	mustScan(t, pool.QueryRow(ctx, `SELECT count(*) FROM calendar_objects WHERE calendar_id = $1`, badCal), &events)
	if cals != 2 || events != 1 {
		t.Errorf("family A after B's property delete: %d calendars (want 2), %d events (want 1)", cals, events)
	}
}
