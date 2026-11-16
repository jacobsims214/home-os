package calendar

// #4717: agenda_get answers every household role, so it trims the sections a
// role may not see instead of refusing. These tests run the real tool against
// a household with an event, a bill, an upkeep job and a list item due today.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// agendaEnv seeds an event, a bill due today, an upkeep job due today and an
// open list item due today, and returns the tool handler.
func agendaEnv(t *testing.T) (*pgxpool.Pool, testutil.Household, handlerFunc) {
	t.Helper()
	pool := testutil.NewPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	ctx := context.Background()
	now := time.Now()

	var cal string
	if err := pool.QueryRow(ctx,
		`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', $2) RETURNING id::text`,
		hh.HouseholdID, uuid.NewString()).Scan(&cal); err != nil {
		t.Fatal(err)
	}
	// Noon of today, not now+2h: the household is on UTC here and agenda_get
	// reads one local day, so an event two hours out lands on tomorrow when
	// the suite runs after 22:00 UTC and the events section comes back empty.
	insertAgendaEvent(t, pool, cal, "Soccer",
		time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC))

	if _, err := pool.Exec(ctx,
		`INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Electric', 142.50, $2)`,
		hh.HouseholdID, now.Day()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO maintenance_tasks (household_id, name, due_date, status) VALUES ($1, 'Clean gutters', $2::date, 'pending')`,
		hh.HouseholdID, now.Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	_, h := NewAgendaGetTool(pool)
	return pool, hh, h
}

func insertAgendaEvent(t *testing.T, pool *pgxpool.Pool, calendarID, title string, start time.Time) string {
	t.Helper()
	data, _ := json.Marshal(map[string]any{
		"title": title, "start": start.UTC().Format(time.RFC3339),
		"end": start.Add(time.Hour).UTC().Format(time.RFC3339), "all_day": false,
	})
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type)
		 VALUES ($1, $2, $3, 'e1', 'event') RETURNING id::text`,
		calendarID, uuid.NewString(), string(data)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// agendaAsRole builds claims for a new member of hh with role.
func agendaAsRole(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) (context.Context, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	email := "agenda-" + role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
		email, "Agenda "+role).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
		hh.HouseholdID, uid, role); err != nil {
		t.Fatal(err)
	}
	return server.ContextWithClaims(ctx, &auth.Claims{
		Identity:    auth.Identity{Subject: uid.String(), Email: email},
		UserID:      uid.String(),
		HouseholdID: hh.HouseholdID.String(),
		Role:        role,
	}), uid
}

// TestAgendaGetFollowsRolePolicy is the per-role matrix for agenda_get: money
// (bills) and upkeep are dropped for the roles that may not see them, and the
// sections they may see are unchanged.
func TestAgendaGetFollowsRolePolicy(t *testing.T) {
	pool, hh, h := agendaEnv(t)

	tests := []struct {
		role                     string
		wantEvents, wantBills    bool
		wantMaintenance, wantSum bool // wantSum: the summary mentions upkeep
	}{
		{"owner", true, true, true, true},
		{"family_manager", true, true, true, true},
		{"family_member", true, true, true, true},
		{"viewer", true, true, true, true},
		// A housesitter keeps the calendar and loses money and upkeep.
		{"housesitter", true, false, false, false},
		// A vendor has no calendar at all; upkeep yes (their own jobs).
		{"vendor", false, false, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.role, func(t *testing.T) {
			ctx, _ := agendaAsRole(t, pool, hh, tc.role)
			text, isErr := callTool(t, h, ctx, map[string]any{})
			if isErr {
				t.Fatalf("agenda_get refused %s: %s", tc.role, text)
			}
			var res agendaResult
			if err := json.Unmarshal(data(t, text), &res); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := len(res.Events) > 0; got != tc.wantEvents {
				t.Errorf("events = %d (%v), want present=%v", len(res.Events), res.Areas, tc.wantEvents)
			}
			if got := len(res.Bills) > 0; got != tc.wantBills {
				t.Errorf("bills = %d, want present=%v", len(res.Bills), tc.wantBills)
			}
			if got := len(res.Maintenance) > 0; got != tc.wantMaintenance {
				t.Errorf("maintenance = %d, want present=%v", len(res.Maintenance), tc.wantMaintenance)
			}
			// A dropped area is not advertised as included.
			has := func(a string) bool {
				for _, x := range res.Areas {
					if x == a {
						return true
					}
				}
				return false
			}
			if has("bills") != tc.wantBills {
				t.Errorf("areas = %v, bills included = %v, want %v", res.Areas, has("bills"), tc.wantBills)
			}
			if has("maintenance") != tc.wantMaintenance {
				t.Errorf("areas = %v, maintenance included = %v, want %v", res.Areas, has("maintenance"), tc.wantMaintenance)
			}
		})
	}
}

// TestAgendaGetVendorSeesOwnJobs: a linked vendor's maintenance section holds
// only their own jobs, and another vendor's task is invisible (#4799).
func TestAgendaGetVendorSeesOwnJobs(t *testing.T) {
	pool, hh, h := agendaEnv(t)
	ctx := context.Background()
	now := time.Now()

	var mine, theirs string
	if err := pool.QueryRow(ctx, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Acme') RETURNING id::text`, hh.HouseholdID).Scan(&mine); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO vendors (household_id, name) VALUES ($1, 'Other') RETURNING id::text`, hh.HouseholdID).Scan(&theirs); err != nil {
		t.Fatal(err)
	}
	var myTask string
	if err := pool.QueryRow(ctx,
		`INSERT INTO maintenance_tasks (household_id, name, due_date, status, vendor_id)
		 VALUES ($1, 'Mine', $2::date, 'pending', $3) RETURNING id::text`,
		hh.HouseholdID, now.Format(time.DateOnly), mine).Scan(&myTask); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO maintenance_tasks (household_id, name, due_date, status, vendor_id)
		 VALUES ($1, 'Theirs', $2::date, 'pending', $3)`,
		hh.HouseholdID, now.Format(time.DateOnly), theirs); err != nil {
		t.Fatal(err)
	}

	vctx, uid := agendaAsRole(t, pool, hh, auth.RoleVendor)
	if _, err := pool.Exec(ctx, `UPDATE memberships SET vendor_id = $3 WHERE household_id = $1 AND user_id = $2`,
		hh.HouseholdID, uid, mine); err != nil {
		t.Fatal(err)
	}

	text, isErr := callTool(t, h, vctx, map[string]any{"areas": []string{"maintenance"}})
	if isErr {
		t.Fatalf("agenda_get: %s", text)
	}
	var res agendaResult
	if err := json.Unmarshal(data(t, text), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Maintenance) != 1 || res.Maintenance[0].TaskID != myTask {
		t.Fatalf("linked vendor's maintenance = %+v, want only %s", res.Maintenance, myTask)
	}
}
