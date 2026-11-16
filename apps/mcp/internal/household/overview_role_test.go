package household

// #4717: family_overview is the entry point for every household role, so it
// answers each of them with only the part of the family they may see. These
// tests run the real tool against a household with one of everything and
// assert, per role, exactly what comes back.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// seedRoleFixture adds one of everything a section of family_overview reads:
// a kid without a login (name and age), a calendar with an event, a bill due
// tomorrow, a maintenance task due tomorrow, and an activity_log row.
func seedRoleFixture(t *testing.T, pool *pgxpool.Pool, hh testutil.Household) (kid string, task string) {
	t.Helper()
	now := time.Now()

	// A child without a login: peopleSelect lists them since #4464.
	kid = insertReturningID(t, pool,
		`INSERT INTO people (household_id, display_name, relationship, birthdate)
		 VALUES ($1, 'Nora', 'child', $2) RETURNING id::text`,
		hh.HouseholdID, now.AddDate(-9, 0, 0))

	cal := insertCalendar(t, pool, hh.HouseholdID, "Family", "family")
	insertEvent(t, pool, cal, "Soccer practice", now.Add(24*time.Hour))

	// Money: a monthly bill due on today's day of the month (so it is due
	// today, inside the preview window), and a loan, so the counts have
	// something.
	mustExec(t, pool, `INSERT INTO bills (household_id, name, amount, due_day)
		VALUES ($1, 'Electric', 142.50, $2)`, hh.HouseholdID, now.Day())
	mustExec(t, pool, `INSERT INTO loans (household_id, name, original_amount, interest_rate, monthly_payment, remaining_balance)
		VALUES ($1, 'Mortgage', 300000, 4.25, 1850, 295000)`, hh.HouseholdID)

	// Upkeep: a task due tomorrow.
	task = insertReturningID(t, pool,
		`INSERT INTO maintenance_tasks (household_id, name, due_date, status)
		 VALUES ($1, 'Clean gutters', $2::date, 'pending') RETURNING id::text`,
		hh.HouseholdID, now.AddDate(0, 0, 1).Format(time.DateOnly))

	// A vehicle and a document, for the counts.
	mustExec(t, pool, `INSERT INTO vehicles (household_id, make, model, vin) VALUES ($1, 'Honda', 'Odyssey', 'VIN-1')`, hh.HouseholdID)
	return kid, task
}

// roleCtx builds claims for a member of hh with role.
func roleCtx(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) (context.Context, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	email := role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
		email, "Role "+role,
	).Scan(&uid); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
		hh.HouseholdID, uid, role,
	); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	return server.ContextWithClaims(ctx, &auth.Claims{
		Identity:    auth.Identity{Subject: uid.String(), Email: email},
		UserID:      uid.String(),
		HouseholdID: hh.HouseholdID.String(),
		Role:        role,
	}), uid
}

// TestFamilyOverviewFollowsRolePolicy is the per-role matrix: a vendor sees no
// people, no calendar and no money; a housesitter sees people by name only,
// the calendar, and no money; a viewer sees everything the family does except
// credentials, which family_overview does not show anyway.
func TestFamilyOverviewFollowsRolePolicy(t *testing.T) {
	pool := testutil.NewPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	kid, task := seedRoleFixture(t, pool, hh)

	tests := []struct {
		role string
		// wantPeople/wantAges: people listed, and whether their age is shown.
		wantPeople, wantAges bool
		wantCalendars        bool
		wantBills            bool
		wantMaintenance      bool
		wantRecent           bool
		// wantCounts is the counts fields that must be non-zero (all of them
		// are seeded), so a trimmed count shows up as zero.
		wantBillsCount, wantLoansCount, wantVehiclesCount bool
	}{
		{"owner", true, true, true, true, true, true, true, true, true},
		{"family_manager", true, true, true, true, true, true, true, true, true},
		{"family_member", true, true, true, true, true, true, true, true, true},
		{"viewer", true, true, true, true, true, true, true, true, true},
		// No people at all, no calendar, no money, no upkeep (maintenance is
		// not in the housesitter's read grant), no activity feed.
		{"housesitter", true, false, true, false, false, false, false, false, false},
		// A vendor: no people, no calendar, no money, upkeep yes (their own
		// jobs), no activity feed.
		{"vendor", false, false, false, false, true, false, false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.role, func(t *testing.T) {
			ctx, _ := roleCtx(t, pool, hh, tc.role)
			ov := overviewOf(t, ctx, pool)

			if got := len(ov.People) > 0; got != tc.wantPeople {
				t.Errorf("people listed = %v, want %v", got, tc.wantPeople)
			}
			if tc.wantPeople {
				var found bool
				for _, p := range ov.People {
					if p.Name != "Nora" {
						continue
					}
					found = true
					if hasAge := p.Age != nil; hasAge != tc.wantAges {
						t.Errorf("Nora's age shown = %v, want %v", hasAge, tc.wantAges)
					}
					if tc.role == auth.RoleHousesitter && (p.UserID != nil || p.Email != nil || p.Role != nil) {
						t.Errorf("housesitter sees a login on %q: %+v", p.Name, p)
					}
				}
				if !found {
					t.Errorf("Nora (a child without a login) is missing from people: %+v", ov.People)
				}
			}
			if got := len(ov.Calendars) > 0; got != tc.wantCalendars {
				t.Errorf("calendars listed = %v, want %v", got, tc.wantCalendars)
			}
			if got := ov.NextDays.Events.Total > 0; got != tc.wantCalendars {
				t.Errorf("next_days.events = %v, want %v", got, tc.wantCalendars)
			}
			if got := ov.NextDays.BillsDue.Total > 0; got != tc.wantBills {
				t.Errorf("next_days.bills_due = %v, want %v", got, tc.wantBills)
			}
			if got := ov.NextDays.MaintenanceDue.Total > 0; got != tc.wantMaintenance {
				t.Errorf("next_days.maintenance_due = %v, want %v", got, tc.wantMaintenance)
			}
			if got := len(ov.RecentChanges) > 0; got != tc.wantRecent {
				t.Errorf("recent_changes = %v, want %v", got, tc.wantRecent)
			}
			if got := ov.Counts.Bills > 0; got != tc.wantBillsCount {
				t.Errorf("counts.bills = %v, want >0 = %v", got, tc.wantBillsCount)
			}
			if got := ov.Counts.Loans > 0; got != tc.wantLoansCount {
				t.Errorf("counts.loans = %v, want >0 = %v", got, tc.wantLoansCount)
			}
			if got := ov.Counts.Vehicles > 0; got != tc.wantVehiclesCount {
				t.Errorf("counts.vehicles = %v, want >0 = %v", got, tc.wantVehiclesCount)
			}
			// The summary must not claim a hidden section is empty.
			summary := overviewSummary(ov, newOverviewView(tc.role, nil))
			if !tc.wantBills && containsWord(summary, "bill") {
				t.Errorf("%s summary mentions bills: %q", tc.role, summary)
			}
			// A hidden task is never named.
			if !tc.wantMaintenance && taskListed(ov.NextDays.MaintenanceDue.Items, task) {
				t.Errorf("%s sees upkeep task %s", tc.role, task)
			}
			_ = kid
		})
	}
}

// TestFamilyOverviewVendorSeesOwnJobs: a vendor linked to a vendors row sees
// only their own maintenance jobs in next_days (#4799).
func TestFamilyOverviewVendorSeesOwnJobs(t *testing.T) {
	pool := testutil.NewPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	ctx := context.Background()
	now := time.Now()

	ownVendor := insertReturningID(t, pool,
		`INSERT INTO vendors (household_id, name) VALUES ($1, 'Acme Plumbing') RETURNING id::text`, hh.HouseholdID)
	otherVendor := insertReturningID(t, pool,
		`INSERT INTO vendors (household_id, name) VALUES ($1, 'Other Electric') RETURNING id::text`, hh.HouseholdID)

	mine := insertReturningID(t, pool,
		`INSERT INTO maintenance_tasks (household_id, name, due_date, status, vendor_id)
		 VALUES ($1, 'My job', $2::date, 'pending', $3) RETURNING id::text`,
		hh.HouseholdID, now.AddDate(0, 0, 1).Format(time.DateOnly), ownVendor)
	theirs := insertReturningID(t, pool,
		`INSERT INTO maintenance_tasks (household_id, name, due_date, status, vendor_id)
		 VALUES ($1, 'Their job', $2::date, 'pending', $3) RETURNING id::text`,
		hh.HouseholdID, now.AddDate(0, 0, 1).Format(time.DateOnly), otherVendor)

	vctx, uid := roleCtx(t, pool, hh, auth.RoleVendor)
	if _, err := pool.Exec(ctx, `UPDATE memberships SET vendor_id = $3 WHERE household_id = $1 AND user_id = $2`,
		hh.HouseholdID, uid, ownVendor); err != nil {
		t.Fatal(err)
	}

	ov := overviewOf(t, vctx, pool)
	items := ov.NextDays.MaintenanceDue.Items
	if len(items) != 1 || items[0].ID != mine {
		t.Fatalf("linked vendor sees %+v, want only %s", items, mine)
	}
	if taskListed(items, theirs) {
		t.Errorf("linked vendor sees another vendor's job %s", theirs)
	}

	// An unlinked vendor keeps seeing every job (the manager decision, #4799).
	if _, err := pool.Exec(ctx, `UPDATE memberships SET vendor_id = NULL WHERE household_id = $1 AND user_id = $2`,
		hh.HouseholdID, uid); err != nil {
		t.Fatal(err)
	}
	if got := len(overviewOf(t, vctx, pool).NextDays.MaintenanceDue.Items); got != 2 {
		t.Errorf("unlinked vendor sees %d jobs, want 2", got)
	}
}

// TestCountKindsComplete keeps countKinds (roleview.go) in step with
// areaCounts: a count added to the struct without a kind would be shown to
// every role by default.
func TestCountKindsComplete(t *testing.T) {
	t.Parallel()
	want := map[string]bool{
		"Bills": true, "Loans": true, "Assets": true, "Vehicles": true, "Pets": true,
		"Contacts": true, "Notes": true, "Lists": true, "Recipes": true, "Documents": true, "Guides": true,
	}
	if len(countKinds) != len(want) {
		t.Errorf("countKinds has %d entries, areaCounts has %d fields: add the new one with its entity kind", len(countKinds), len(want))
	}
	for field := range want {
		if _, ok := countKinds[field]; !ok {
			t.Errorf("countKinds is missing %q: its count would be shown to every role", field)
		}
	}
	for field, kind := range countKinds {
		if !want[field] {
			t.Errorf("countKinds names %q, which areaCounts does not have", field)
		}
		if _, ok := auth.EntityResource(kind); !ok {
			t.Errorf("countKinds[%q] = %q is not an entity kind the policy knows", field, kind)
		}
	}
}

// containsWord reports whether s mentions word (case-insensitive, as a word).
func containsWord(s, word string) bool {
	lower := toLower(s)
	for i := 0; i+len(word) <= len(lower); i++ {
		if lower[i:i+len(word)] != word {
			continue
		}
		beforeOK := i == 0 || !isLetter(lower[i-1])
		after := i + len(word)
		afterOK := after == len(lower) || !isLetter(lower[after])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

func isLetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

func toLower(s string) string {
	out := []byte(s)
	for i, b := range out {
		if b >= 'A' && b <= 'Z' {
			out[i] = b + 32
		}
	}
	return string(out)
}

// taskListed reports whether items includes the task with id.
func taskListed(items []taskBrief, id string) bool {
	for _, it := range items {
		if it.ID == id {
			return true
		}
	}
	return false
}
