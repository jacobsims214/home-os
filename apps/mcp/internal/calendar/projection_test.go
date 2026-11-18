package calendar

// Projection ownership (#5301), war game R2 F-AGENT-2.
//
// Asked "what bills are due", an assistant answered that the bill-sync entries
// on the Bills calendar were "duplicates I added" and offered to delete them.
// Ground truth: every one of them is a bill's projection (#4943), written by
// packages/billcal when the bill was created, rewritten whenever the bill
// changes, and older than every session of the agent that claimed it. A parent
// saying "yes, delete them" would have destroyed the family's calendar bill
// view.
//
// These tests pin the fix at the tool layer, which is where the offer came
// from: event_find gave the model "Remove it with event_delete event_id=…" for
// an entry nothing said it did not own, and event_delete deleted it. So:
//
//   - an entry a record keeps reports managed_by/managed_id, and event_find's
//     hints never offer event_delete for it;
//   - event_delete refuses it (nothing was deleted) and names the record's
//     tools;
//   - an ordinary event, an event merely LINKED to a record, and a legacy
//     bill-titled entry nothing keeps are all still the family's to delete.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/testutil"
)

// projectionEnv is a household with a Bills and a Family calendar, ready for
// the ownership tests.
type projectionEnv struct {
	pool                       *pgxpool.Pool
	ctx                        context.Context
	household                  uuid.UUID
	family, bills, maintenance string
	find, del, update          handlerFunc
}

func newProjectionEnv(t *testing.T) projectionEnv {
	t.Helper()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	env := projectionEnv{
		pool: pool, ctx: ctx, household: hh.HouseholdID,
		find: bind(t, NewEventFindTool, pool), del: bind(t, NewEventDeleteTool, pool),
		update: bind(t, NewEventUpdateTool, pool),
	}
	env.family = seedCalendar(t, pool, hh.HouseholdID)
	for _, c := range []struct {
		name, kind string
		dst        *string
	}{{"Bills", "bills", &env.bills}, {"Maintenance", "maintenance", &env.maintenance}} {
		if err := pool.QueryRow(context.Background(),
			`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			hh.HouseholdID, c.name, c.kind, uuid.NewString()).Scan(c.dst); err != nil {
			t.Fatalf("insert %s calendar: %v", c.name, err)
		}
	}
	return env
}

// seedOwnedEvent writes a row exactly as one of the mirror writers does.
// entityType/entityID is the link the writer finds its own entry by (empty for
// no link); eventType is what that writer stores. It returns the new row's id.
func (e projectionEnv) seedOwnedEvent(t *testing.T, calendarID, title, entityType, entityID, eventType string) string {
	t.Helper()
	data := `{"title":"` + title + `","description":"Amount: $180.00","start":"2026-11-18T00:00:00Z",` +
		`"end":"2026-11-19T00:00:00Z","all_day":true,"event_type":"` + eventType + `"}`
	// entity_kind is a domain with no empty value, and the composite FK
	// requires the record to exist in the same family, so an unlinked entry
	// passes NULL and a linked one names a real row.
	var et, id *string
	if entityType != "" {
		et, id = &entityType, &entityID
	}
	var got string
	if err := e.pool.QueryRow(context.Background(),
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`,
		calendarID, uuid.NewString(), data, uuid.NewString(), eventType, et, id,
	).Scan(&got); err != nil {
		t.Fatalf("seed %s: %v", title, err)
	}
	return got
}

// newBill inserts a bill and returns its id, for an entry's entity link.
func (e projectionEnv) newBill(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := e.pool.QueryRow(context.Background(),
		`INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, $2, 180, 18) RETURNING id::text`,
		e.household, name,
	).Scan(&id); err != nil {
		t.Fatalf("insert bill: %v", err)
	}
	return id
}

// newTask inserts an upkeep task and returns its id.
func (e projectionEnv) newTask(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := e.pool.QueryRow(context.Background(),
		`INSERT INTO maintenance_tasks (household_id, name, due_date, status) VALUES ($1, $2, current_date + 3, 'pending') RETURNING id::text`,
		e.household, name,
	).Scan(&id); err != nil {
		t.Fatalf("insert upkeep task: %v", err)
	}
	return id
}

// oneEvent decodes the single event an id lookup returns (event_find answers
// with a list, even for one id).
func oneEvent(t *testing.T, text string) Event {
	t.Helper()
	evs := eventItems(t, text)
	if len(evs) != 1 {
		t.Fatalf("event_find returned %d events, want 1: %s", len(evs), text)
	}
	return evs[0]
}

// exists reports whether the calendar row is still there.
func (e projectionEnv) exists(t *testing.T, eventID string) bool {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM calendar_objects WHERE id = $1`, eventID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n == 1
}

// TestBillProjectionIsNotTheAssistantsToDelete is the ticket's case: the
// bill-sync entries F-AGENT-2 called "duplicates I added".
//
// On the old code every assertion here fails: the entries had no managed_by,
// event_find handed back "Remove it with event_delete", and event_delete
// deleted the row — exactly the offer, and the destruction, the ticket reports.
func TestBillProjectionIsNotTheAssistantsToDelete(t *testing.T) {
	env := newProjectionEnv(t)
	billID := env.newBill(t, "Electric")
	// The exact shape billcal.Sync writes: entity_type 'bill', event_type
	// 'bill', title "<bill name> due".
	projection := env.seedOwnedEvent(t, env.bills, "Electric due", EntityTypeBill, billID, EntityTypeBill)

	t.Run("event_find says which record keeps it, and never offers event_delete", func(t *testing.T) {
		text, isErr := callTool(t, env.find, env.ctx, map[string]any{"id": projection})
		if isErr {
			t.Fatalf("event_find: %s", text)
		}
		ev := oneEvent(t, text)
		if ev.ManagedBy != EntityTypeBill || ev.ManagedID != billID {
			t.Errorf("managed_by=%q managed_id=%q, want %q %q: an entry a record keeps must say so, or a model guesses from the title",
				ev.ManagedBy, ev.ManagedID, EntityTypeBill, billID)
		}
		// The next-step hint is the offer the war game recorded. It must not be
		// there, and the bill's own tools must be.
		if strings.Contains(text, "event_delete event_id="+projection) {
			t.Errorf("event_find still offers to delete a bill's projection: %s", text)
		}
		if !strings.Contains(text, "bill_id="+billID) || !strings.Contains(text, "bill_update") {
			t.Errorf("the hint does not name the bill as the thing to change: %s", text)
		}
	})

	t.Run("event_delete refuses and leaves the entry alone", func(t *testing.T) {
		text, isErr := callTool(t, env.del, env.ctx, map[string]any{"event_id": projection, "scope": "all"})
		if !isErr {
			t.Fatalf("event_delete deleted a bill's projection (the F-AGENT-2 failure): %s", text)
		}
		if !strings.Contains(text, billID) || !strings.Contains(text, "bill_delete") || !strings.Contains(text, "bill_update") {
			t.Errorf("the refusal does not name the bill and its tools: %s", text)
		}
		if !env.exists(t, projection) {
			t.Error("the entry was deleted even though the call was refused")
		}
	})

	t.Run("event_update refuses too, so a stale amount cannot be written back", func(t *testing.T) {
		text, isErr := callTool(t, env.update, env.ctx, map[string]any{"event_id": projection, "title": "Electric due (edited)"})
		if !isErr {
			t.Fatalf("event_update edited a bill's projection: %s", text)
		}
		if !strings.Contains(text, "bill_update") || !strings.Contains(text, billID) {
			t.Errorf("the refusal does not point at bill_update bill_id=<id>: %s", text)
		}
		var title string
		if err := env.pool.QueryRow(context.Background(),
			`SELECT ical_data::jsonb->>'title' FROM calendar_objects WHERE id = $1`, projection).Scan(&title); err != nil {
			t.Fatalf("read title: %v", err)
		}
		if title != "Electric due" {
			t.Errorf("title = %q, want the stored one: a refused edit changed the row", title)
		}
	})
}

// TestOrdinaryEventsStayTheFamilysToDelete is the other half of the ownership
// test: the guard must not over-reach. These three are NOT projections even
// though they look like bills or are linked to records, and the family must
// still be able to delete them here.
func TestOrdinaryEventsStayTheFamilysToDelete(t *testing.T) {
	env := newProjectionEnv(t)
	// A legacy "bill due" entry from round 1 (#4943/#5140): event_type 'bill'
	// with NO entity link. Nothing keeps it, so nothing owns it. This is the
	// shape migration 073 could not match, and retiring it is the family's
	// call, not a refusal here.
	legacy := env.seedOwnedEvent(t, env.bills, "Water bill due — $60", "", "", "bill")
	// An event a person made and linked to a bill (#4485's documented case):
	// entity_type 'bill' but event_type 'event', so billcal.Sync never touches
	// it. "Only rows with entity_type 'bill' AND event_type 'bill' are ever
	// read, changed or deleted here, so an event a person made and linked to a
	// bill is never touched" (packages/billcal/billcal.go).
	linked := env.seedOwnedEvent(t, env.family, "Pay the plumber", EntityTypeBill, env.newBill(t, "Plumber"), "event")
	// A plain event on the Bills calendar.
	plain := env.seedOwnedEvent(t, env.bills, "Call the water company", "", "", "event")

	tests := []struct {
		name, id, why string
	}{
		{"legacy bill-titled entry with no record", legacy, "no entity link: nothing keeps it"},
		{"event linked to a bill but kept by nobody", linked, "event_type 'event': billcal never rewrites it"},
		{"plain event on the Bills calendar", plain, "no link at all"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, isErr := callTool(t, env.find, env.ctx, map[string]any{"id": tt.id})
			if isErr {
				t.Fatalf("event_find: %s", text)
			}
			ev := oneEvent(t, text)
			if ev.ManagedBy != "" || ev.ManagedID != "" {
				t.Errorf("managed_by=%q managed_id=%q, want empty (%s)", ev.ManagedBy, ev.ManagedID, tt.why)
			}
			if !strings.Contains(text, "event_delete event_id="+tt.id) {
				t.Errorf("the family was not told it can remove this entry: %s", text)
			}
			if text, isErr := callTool(t, env.del, env.ctx, map[string]any{"event_id": tt.id}); isErr {
				t.Fatalf("event_delete refused an ordinary event (%s): %s", tt.why, text)
			}
			if env.exists(t, tt.id) {
				t.Error("the event was not deleted")
			}
		})
	}
}

// TestUpkeepTaskProjectionIsOwnedToo pins the second owner: a maintenance
// task's calendar entry (apps/mcp/internal/maintenance/task.go) is kept by the
// task, so deleting the entry here would be undone by the next task save.
func TestUpkeepTaskProjectionIsOwnedToo(t *testing.T) {
	env := newProjectionEnv(t)
	taskID := env.newTask(t, "Clean gutters")
	projection := env.seedOwnedEvent(t, env.maintenance, "Clean gutters", EntityTypeMaintenanceTask, taskID, "maintenance")

	text, isErr := callTool(t, env.find, env.ctx, map[string]any{"id": projection})
	if isErr {
		t.Fatalf("event_find: %s", text)
	}
	ev := oneEvent(t, text)
	if ev.ManagedBy != EntityTypeMaintenanceTask || ev.ManagedID != taskID {
		t.Errorf("managed_by=%q managed_id=%q, want %q %q", ev.ManagedBy, ev.ManagedID, EntityTypeMaintenanceTask, taskID)
	}
	if text, isErr := callTool(t, env.del, env.ctx, map[string]any{"event_id": projection}); !isErr {
		t.Fatalf("event_delete deleted an upkeep task's calendar entry: %s", text)
	} else if !strings.Contains(text, "maintenance_delete") || !strings.Contains(text, taskID) {
		t.Errorf("the refusal does not name maintenance_delete task_id=<id>: %s", text)
	}
	if !env.exists(t, projection) {
		t.Error("the entry was deleted even though the call was refused")
	}
}

// TestProjectionOwnershipUnit pins the predicate itself, without a database:
// ownership comes from the entity link alone, and needs both halves.
func TestProjectionOwnershipUnit(t *testing.T) {
	bill, task := uuid.NewString(), uuid.NewString()
	tests := []struct {
		name, entityType, entityID, eventType string
		wantKind                              string
	}{
		{"bill mirror", "bill", bill, "bill", "bill"},
		{"upkeep mirror", "maintenance_task", task, "maintenance", "maintenance_task"},
		{"person link", "person", uuid.NewString(), "event", ""},
		{"pet link", "pet", uuid.NewString(), "event", ""},
		{"vehicle link", "vehicle", uuid.NewString(), "event", ""},
		{"legacy bill entry, event_type bill, no link", "", "", "bill", ""},
		{"event linked to a bill, as a person made it", "bill", bill, "event", ""},
		{"a type with no record", "bill", "", "bill", ""},
		{"unknown kind", "spaceship", uuid.NewString(), "event", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var et, id *string
			if tt.entityType != "" {
				et = &tt.entityType
			}
			if tt.entityID != "" {
				id = &tt.entityID
			}
			owner, ok := managedBy(et, id, tt.eventType)
			if ok != (tt.wantKind != "") || owner.kind != tt.wantKind {
				t.Errorf("managedBy(%v, %v, %q) = %q, %v; want %q, %v",
					et, id, tt.eventType, owner.kind, ok, tt.wantKind, tt.wantKind != "")
			}
		})
	}
}
