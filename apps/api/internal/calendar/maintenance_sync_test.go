package calendar

// Integration tests for the maintenance calendar sync. They mirror
// apps/mcp/internal/maintenance/tools_test.go (TestCreate,
// TestCreate_NoMaintenanceCalendar, TestUpdate, TestDelete): the api and the
// MCP must write the same event — all-day, Maintenance calendar (fallback:
// first calendar), entity_type 'maintenance_task', a calendar_changes row on
// every write — so phones and agents agree. Change both together.
//
// Requires TEST_DATABASE_URL pointing at a database with all migrations
// applied; skipped otherwise. Run with -p 1.

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func mustID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id
}

func countOf(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// seedHousehold creates a household (deleted on cleanup) and a maintenance
// task with a due date.
func seedHousehold(t *testing.T, pool *pgxpool.Pool) (household, task uuid.UUID, due time.Time) {
	t.Helper()
	household = mustID(t, pool, `INSERT INTO households (name) VALUES ('Maint sync '||gen_random_uuid()) RETURNING id`)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, household)
	})
	due = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	task = mustID(t, pool, `INSERT INTO maintenance_tasks (household_id, name, due_date) VALUES ($1, 'Replace furnace filter', $2) RETURNING id`, household, due)
	return household, task, due
}

func addCalendar(t *testing.T, pool *pgxpool.Pool, household uuid.UUID, name, typ string) uuid.UUID {
	t.Helper()
	return mustID(t, pool, `INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, $2, $3, gen_random_uuid()::text) RETURNING id`, household, name, typ)
}

func TestMaintenanceSync_CreateOnMaintenanceCalendarAllDay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewRepo(pool)
	hh, task, due := seedHousehold(t, pool)
	addCalendar(t, pool, hh, "Family", "family")
	maint := addCalendar(t, pool, hh, "Maintenance", "maintenance")

	if err := repo.UpsertMaintenanceEvent(ctx, hh, task, "Replace furnace filter", "", due); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var calID uuid.UUID
	var ical, entityType string
	if err := pool.QueryRow(ctx, `SELECT calendar_id, ical_data, entity_type FROM calendar_objects WHERE entity_id = $1`, task).
		Scan(&calID, &ical, &entityType); err != nil {
		t.Fatalf("calendar event: %v", err)
	}
	if calID != maint || entityType != "maintenance_task" {
		t.Errorf("event on %s kind %s, want Maintenance calendar %s, maintenance_task", calID, entityType, maint)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(ical), &got); err != nil {
		t.Fatalf("ical json: %v", err)
	}
	if got["all_day"] != true || got["start"] != "2026-11-20T00:00:00Z" || got["end"] != "2026-11-21T00:00:00Z" ||
		got["title"] != "Replace furnace filter" || got["event_type"] != "maintenance" {
		t.Errorf("event json = %s", ical)
	}
	if n := countOf(t, pool, `SELECT COUNT(*) FROM calendar_changes WHERE calendar_id = $1 AND action = 'create'`, maint); n != 1 {
		t.Errorf("create changes = %d, want 1", n)
	}
}

func TestMaintenanceSync_FallbackAndNoCalendar(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewRepo(pool)
	hh, task, due := seedHousehold(t, pool)

	// No calendar at all: no event, no error.
	if err := repo.UpsertMaintenanceEvent(ctx, hh, task, "Replace furnace filter", "", due); err != nil {
		t.Fatalf("upsert without calendar: %v", err)
	}
	if n := countOf(t, pool, `SELECT COUNT(*) FROM calendar_objects WHERE entity_id = $1`, task); n != 0 {
		t.Errorf("events = %d without any calendar", n)
	}

	// No Maintenance calendar: the first calendar.
	fam := addCalendar(t, pool, hh, "Family", "family")
	addCalendar(t, pool, hh, "Bills", "bills")
	if err := repo.UpsertMaintenanceEvent(ctx, hh, task, "Replace furnace filter", "", due); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if n := countOf(t, pool, `SELECT COUNT(*) FROM calendar_objects WHERE entity_id = $1 AND calendar_id = $2`, task, fam); n != 1 {
		t.Errorf("event on Family calendar = %d, want 1", n)
	}
}

func TestMaintenanceSync_UpdateKeepsEventAndRecordsChange(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewRepo(pool)
	hh, task, due := seedHousehold(t, pool)
	maint := addCalendar(t, pool, hh, "Maintenance", "maintenance")
	if err := repo.UpsertMaintenanceEvent(ctx, hh, task, "Replace furnace filter", "", due); err != nil {
		t.Fatalf("create: %v", err)
	}
	// The CalDAV service keeps `sequence` in the JSON; it must survive.
	if _, err := pool.Exec(ctx, `UPDATE calendar_objects SET ical_data = jsonb_set(ical_data::jsonb, '{sequence}', '3')::text WHERE entity_id = $1`, task); err != nil {
		t.Fatalf("set sequence: %v", err)
	}

	if err := repo.UpsertMaintenanceEvent(ctx, hh, task, "Replace furnace filter (MERV 13)", "20x25x1", due.AddDate(0, 0, 2)); err != nil {
		t.Fatalf("update: %v", err)
	}
	if n := countOf(t, pool, `SELECT COUNT(*) FROM calendar_objects WHERE entity_id = $1`, task); n != 1 {
		t.Fatalf("events = %d, want 1", n)
	}
	var ical string
	if err := pool.QueryRow(ctx, `SELECT ical_data FROM calendar_objects WHERE entity_id = $1`, task).Scan(&ical); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(ical), &got)
	if got["title"] != "Replace furnace filter (MERV 13)" || got["description"] != "20x25x1" ||
		got["start"] != "2026-11-22T00:00:00Z" || got["all_day"] != true || got["sequence"] != float64(4) {
		t.Errorf("updated json = %s", ical)
	}
	if n := countOf(t, pool, `SELECT COUNT(*) FROM calendar_changes WHERE calendar_id = $1 AND action = 'update'`, maint); n != 1 {
		t.Errorf("update changes = %d, want 1", n)
	}
}

func TestMaintenanceSync_DeleteRemovesTaskAndRecordsChange(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewRepo(pool)
	hh, task, due := seedHousehold(t, pool)
	maint := addCalendar(t, pool, hh, "Maintenance", "maintenance")
	if err := repo.UpsertMaintenanceEvent(ctx, hh, task, "Replace furnace filter", "", due); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Another family can't delete it.
	other, _, _ := seedHousehold(t, pool)
	if err := repo.DeleteMaintenanceEvent(ctx, other, task); err != nil {
		t.Fatalf("delete as other family: %v", err)
	}
	if n := countOf(t, pool, `SELECT COUNT(*) FROM calendar_objects WHERE entity_id = $1`, task); n != 1 {
		t.Fatalf("other family deleted the event")
	}

	if err := repo.DeleteMaintenanceEvent(ctx, hh, task); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n := countOf(t, pool, `SELECT COUNT(*) FROM calendar_objects WHERE entity_id = $1`, task); n != 0 {
		t.Error("calendar event still there")
	}
	// 027 trigger (AFTER DELETE since 038): the task goes with its event.
	if n := countOf(t, pool, `SELECT COUNT(*) FROM maintenance_tasks WHERE id = $1`, task); n != 0 {
		t.Error("task still there after its event was deleted")
	}
	if n := countOf(t, pool, `SELECT COUNT(*) FROM calendar_changes WHERE calendar_id = $1 AND action = 'delete'`, maint); n != 1 {
		t.Errorf("delete changes = %d, want 1", n)
	}

	// A repeat finds nothing and records nothing.
	if err := repo.DeleteMaintenanceEvent(ctx, hh, task); err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
	if n := countOf(t, pool, `SELECT COUNT(*) FROM calendar_changes WHERE calendar_id = $1 AND action = 'delete'`, maint); n != 1 {
		t.Errorf("delete changes after repeat = %d, want 1", n)
	}
}

// TestMaintenanceSync_DeletingTaskDeletesEvent covers the other direction:
// deleting the task removes its event through the 038 rehome trigger.
func TestMaintenanceSync_DeletingTaskDeletesEvent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewRepo(pool)
	hh, task, due := seedHousehold(t, pool)
	addCalendar(t, pool, hh, "Maintenance", "maintenance")
	if err := repo.UpsertMaintenanceEvent(ctx, hh, task, "Replace furnace filter", "", due); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM maintenance_tasks WHERE id = $1`, task); err != nil {
		t.Fatalf("delete task: %v", err)
	}
	if n := countOf(t, pool, `SELECT COUNT(*) FROM calendar_objects WHERE entity_id = $1`, task); n != 0 {
		t.Error("event still there after its task was deleted")
	}
}
