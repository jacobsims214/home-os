package caldav

// #4943: a calendar entry Home OS retired because a bill is the record for it
// must be invisible to every CalDAV read, or a phone's Bills calendar keeps
// showing "Electric bill due — $180" beside the bill's own projection and the
// next briefing reads the stale one (war game R1 G2 + G5).
//
// The rows are made through the real trigger (migration 073's reconcile on a
// bills INSERT), not by setting superseded_at by hand, so this covers the path
// a live household goes through.

import (
	"context"
	"testing"

	"home-os/calendar/internal/db"
)

func TestSupersededEntriesAreInvisibleToCalDAV(t *testing.T) {
	repo, pool := testRepo(t)
	ctx := context.Background()

	var hh string
	if err := pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('CalDAV supersede '||gen_random_uuid()) RETURNING id::text`).Scan(&hh); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1::uuid`, hh) })

	var billsCal, famCal string
	for _, c := range []struct {
		name, typ string
		dst       *string
	}{
		{"Bills", "bills", &billsCal},
		{"Family", "family", &famCal},
	} {
		if err := pool.QueryRow(ctx,
			`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, $2, $3, gen_random_uuid()::text) RETURNING id::text`,
			hh, c.name, c.typ).Scan(c.dst); err != nil {
			t.Fatal(err)
		}
	}

	// The round-1 entry, and the bill that is the record for it. The bill's
	// INSERT trigger absorbs the entry.
	if _, err := pool.Exec(ctx,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
		 VALUES ($1, 'legacy-electric', '{"title":"Electric bill due — $180","description":"","start":"2026-11-18T00:00:00Z","end":"2026-11-19T00:00:00Z","all_day":true}', 'e', 'event')`,
		billsCal); err != nil {
		t.Fatal(err)
	}
	// An ordinary appointment on the Family calendar, which must stay readable.
	if _, err := pool.Exec(ctx,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
		 VALUES ($1, 'dentist', '{"title":"Dentist","description":"","start":"2026-11-18T15:00:00Z","end":"2026-11-18T16:00:00Z","all_day":false}', 'e', 'event')`,
		famCal); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1::uuid, 'Electric', 180, 18)`, hh); err != nil {
		t.Fatal(err)
	}

	acc := db.Access{Role: "owner"}

	// The Bills calendar's listing: the retired entry is gone, so a REPORT or
	// PROPFIND shows nothing for it.
	objects, err := repo.ListCalendarObjects(ctx, hh, billsCal, acc)
	if err != nil {
		t.Fatalf("ListCalendarObjects: %v", err)
	}
	for _, o := range objects {
		if o.UID == "legacy-electric" {
			t.Errorf("the retired entry is still listed on the Bills calendar")
		}
	}
	// Direct access by uid (what a phone GET does): absent, so a 404.
	obj, err := repo.GetCalendarObjectByUID(ctx, hh, billsCal, "legacy-electric", acc)
	if err != nil {
		t.Fatalf("GetCalendarObjectByUID: %v", err)
	}
	if obj != nil {
		t.Errorf("the retired entry is still readable by uid: %+v", obj)
	}
	// The time-range REPORT path.
	inRange, err := repo.ListCalendarObjectsInRange(ctx, hh, billsCal, acc)
	if err != nil {
		t.Fatalf("ListCalendarObjectsInRange: %v", err)
	}
	for _, o := range inRange {
		if o.UID == "legacy-electric" {
			t.Errorf("the retired entry is still in the range REPORT")
		}
	}

	// The ordinary event is untouched, on both paths.
	fam, err := repo.ListCalendarObjects(ctx, hh, famCal, acc)
	if err != nil {
		t.Fatalf("list Family: %v", err)
	}
	var found bool
	for _, o := range fam {
		if o.UID == "dentist" {
			found = true
		}
	}
	if !found {
		t.Error("the supersede filter took an ordinary event with it")
	}

	// The row itself survives with its text, so nothing a family wrote is lost.
	var title string
	var supersededAt *string
	if err := pool.QueryRow(ctx,
		`SELECT ical_data::jsonb->>'title', superseded_at::text FROM calendar_objects WHERE calendar_id = $1 AND uid = 'legacy-electric'`,
		billsCal).Scan(&title, &supersededAt); err != nil {
		t.Fatalf("the retired row was deleted: %v", err)
	}
	if supersededAt == nil || title != "Electric bill due — $180" {
		t.Errorf("retired row = %q superseded_at=%v", title, supersededAt)
	}
}
