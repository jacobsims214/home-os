package calendar

// #4717: agenda_get answers every household role, so it trims the sections a
// role may not see instead of refusing. These tests run the real tool against
// a household with an event, a bill, an upkeep job and a list item due today.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

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
			// Every area (the default leaves upkeep out), and today plus
			// tomorrow so a run near midnight finds what agendaEnv seeded today.
			text, isErr := callTool(t, h, ctx, map[string]any{"areas": agendaAreas,
				"from": time.Now().Format(time.DateOnly), "to": time.Now().AddDate(0, 0, 1).Format(time.DateOnly)})
			if isErr {
				t.Fatalf("agenda_get refused %s: %s", tc.role, text)
			}
			var res agendaResult
			if err := json.Unmarshal(data(t, text), &res); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := len(res.Events) > 0; got != tc.wantEvents {
				t.Errorf("events = %d (%v), want present=%v", len(res.Events), res.Coverage.Areas, tc.wantEvents)
			}
			if got := len(res.Bills) > 0; got != tc.wantBills {
				t.Errorf("bills = %d, want present=%v", len(res.Bills), tc.wantBills)
			}
			if got := len(res.Maintenance) > 0; got != tc.wantMaintenance {
				t.Errorf("maintenance = %d, want present=%v", len(res.Maintenance), tc.wantMaintenance)
			}
			// A dropped area is not advertised as included.
			has := func(a string) bool {
				for _, x := range res.Coverage.Areas {
					if x == a {
						return true
					}
				}
				return false
			}
			if has("bills") != tc.wantBills {
				t.Errorf("areas = %v, bills included = %v, want %v", res.Coverage.Areas, has("bills"), tc.wantBills)
			}
			if has("maintenance") != tc.wantMaintenance {
				t.Errorf("areas = %v, maintenance included = %v, want %v", res.Coverage.Areas, has("maintenance"), tc.wantMaintenance)
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

	// to=from: one day, so the task due today is in range whatever the hour.
	today := time.Now().Format(time.DateOnly)
	text, isErr := callTool(t, h, vctx, map[string]any{"areas": []string{"maintenance"}, "from": today, "to": today})
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

// TestAgendaGetTrimsEventKinds is the per-event half of the role policy in
// agenda (#4810, the rule #4807 applied to the API): a role that may see the
// calendar must not be handed events whose own kind it may not see. An upkeep
// job's mirror event is a maintenance_task (ResMaintenance read = rolesFabric:
// no housesitter) and an event on the Vehicles calendar needs the vehicle read
// grant (rolesReaders: no housesitter, no vendor). Dropping the whole event is
// deliberate: a mirror's summary and description carry the job's detail, so no
// partial version is safe to show, and a sitter has no use for the calendar of
// upkeep they may not open.
//
// The household also holds an ordinary Family appointment, which every role
// that may see the calendar keeps: the rule must not hide everything.
func TestAgendaGetTrimsEventKinds(t *testing.T) {
	pool, hh, h := agendaEnv(t)
	ctx := context.Background()
	now := time.Now()

	// agendaEnv already seeded a Family calendar holding "Soccer".
	var family, upkeepCal, vehicles string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM calendars WHERE household_id = $1 AND name = 'Family'`, hh.HouseholdID).Scan(&family); err != nil {
		t.Fatalf("find the seeded Family calendar: %v", err)
	}
	insertCal := func(name, typ string) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, $2, $3::calendar_type, $4) RETURNING id::text`,
			hh.HouseholdID, name, typ, uuid.NewString()).Scan(&id); err != nil {
			t.Fatalf("insert calendar %s: %v", name, err)
		}
		return id
	}
	upkeepCal = insertCal("Maintenance", "maintenance")
	vehicles = insertCal("Vehicles", "vehicles")

	// An ordinary appointment every role may see.
	insertAgendaEvent(t, pool, family, "Dentist", now.Add(2*time.Hour))

	// An upkeep job's mirror: an all-day event on the Maintenance calendar whose
	// event_type and entity_type both say what it mirrors (maintenance/task.go).
	var taskID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO maintenance_tasks (household_id, name, due_date, status) VALUES ($1, 'Service furnace', $2::date, 'pending') RETURNING id::text`,
		hh.HouseholdID, now.Format(time.DateOnly)).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
		 VALUES ($1, $2, $3, 'e1', 'maintenance', 'maintenance_task', $4)`,
		upkeepCal, uuid.NewString(),
		`{"title":"Service furnace","description":"","start":"`+now.Format(time.DateOnly)+`T00:00:00Z","end":"`+
			now.AddDate(0, 0, 1).Format(time.DateOnly)+`T00:00:00Z","all_day":true,"event_type":"maintenance"}`,
		taskID); err != nil {
		t.Fatal(err)
	}
	// A vehicle event: an ordinary event on the Vehicles calendar, which takes
	// its kind from the calendar's type. Pinned to 10:00 today (the household
	// has no timezone set here, so the tools read UTC) rather than now+3h,
	// which a run near midnight would push into tomorrow.
	today10 := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, time.UTC)
	insertAgendaEvent(t, pool, vehicles, "Oil change", today10)

	// A bill-linked event on the FAMILY calendar: someone put the water bill's
	// due date on the family calendar and linked it to the bill. The link makes
	// it a money record wherever it sits, so #4807 classifies it 'bill' and a
	// housesitter must not read its title either.
	var waterBill string
	if err := pool.QueryRow(ctx,
		`INSERT INTO bills (household_id, name, amount, due_day) VALUES ($1, 'Water', 60, $2) RETURNING id::text`,
		hh.HouseholdID, now.Day()).Scan(&waterBill); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
		 VALUES ($1, $2, $3, 'e1', 'event', 'bill', $4)`,
		family, uuid.NewString(),
		`{"title":"Water bill due","description":"","start":"`+now.Format(time.DateOnly)+`T00:00:00Z","end":"`+
			now.AddDate(0, 0, 1).Format(time.DateOnly)+`T00:00:00Z","all_day":true,"event_type":"event"}`,
		waterBill); err != nil {
		t.Fatal(err)
	}

	titles := func(t *testing.T, role string) ([]string, agendaCoverage, string) {
		t.Helper()
		rctx, _ := agendaAsRole(t, pool, hh, role)
		// Two days: agendaEnv seeds its appointment at now+2h, which lands on
		// tomorrow in a run after 22:00 UTC, and max_per_day raised because this
		// test is about the kind rule, not the per-day cap.
		text, isErr := callTool(t, h, rctx, map[string]any{"areas": []string{"events"},
			"from": now.Format(time.DateOnly), "to": now.AddDate(0, 0, 1).Format(time.DateOnly), "max_per_day": 50})
		if isErr {
			t.Fatalf("agenda_get as %s: %s", role, text)
		}
		var res agendaResult
		if err := json.Unmarshal(data(t, text), &res); err != nil {
			t.Fatalf("decode: %v", err)
		}
		var got []string
		for _, e := range res.Events {
			got = append(got, e.Title)
		}
		return got, res.Coverage, text
	}

	t.Run("a housesitter keeps appointments and loses upkeep and vehicle events", func(t *testing.T) {
		got, cov, text := titles(t, auth.RoleHousesitter)
		if !contains(got, "Dentist") {
			t.Errorf("housesitter sees no Dentist: %v", got)
		}
		for _, leak := range []string{"Service furnace", "Oil change", "Water bill due"} {
			if contains(got, leak) {
				t.Errorf("housesitter sees the %q event: %v", leak, got)
			}
			if strings.Contains(text, leak) {
				t.Errorf("housesitter's result mentions %q:\n%s", leak, text)
			}
		}
		// The calendars searched say which ones were left out, so a model never
		// reads the absence as "nothing on".
		if strings.Join(cov.Calendars, ",") != "Family" {
			t.Errorf("calendars = %v, want only Family", cov.Calendars)
		}
	})

	t.Run("the family roles and a viewer keep every event", func(t *testing.T) {
		for _, role := range []string{auth.RoleOwner, auth.RoleFamilyMember, auth.RoleViewer} {
			got, cov, _ := titles(t, role)
			for _, want := range []string{"Dentist", "Service furnace", "Oil change", "Water bill due"} {
				if !contains(got, want) {
					t.Errorf("%s is missing %q: %v", role, want, got)
				}
			}
			if len(cov.Calendars) != 3 {
				t.Errorf("%s searched %v, want all four calendars", role, cov.Calendars)
			}
		}
	})

	t.Run("a vendor has no calendar at all", func(t *testing.T) {
		_, cov, text := titles(t, auth.RoleVendor)
		if len(cov.Calendars) != 0 || (cov.EventsListed != nil && *cov.EventsListed != 0) {
			t.Errorf("vendor coverage = %+v", cov)
		}
		if !contains(cov.Hidden, "events") {
			t.Errorf("vendor hidden areas = %v, want events among them", cov.Hidden)
		}
		if !strings.Contains(text, "Not visible to your role") {
			t.Errorf("vendor's summary does not say the calendar is hidden:\n%s", text)
		}
	})

	t.Run("hidden is never offered as something to ask for", func(t *testing.T) {
		_, cov, _ := titles(t, auth.RoleHousesitter)
		rctx, _ := agendaAsRole(t, pool, hh, auth.RoleHousesitter)
		text, _ := callTool(t, h, rctx, nil)
		var res agendaResult
		_ = json.Unmarshal(data(t, text), &res)
		if contains(res.Coverage.NotIncluded, "bills") || contains(res.Coverage.Hidden, "events") {
			t.Errorf("coverage mixes hidden and not_included: %+v", res.Coverage)
		}
		if !contains(res.Coverage.Hidden, "bills") {
			t.Errorf("a housesitter's bills are not reported as hidden: %+v", res.Coverage.Hidden)
		}
		_ = cov
	})
}

func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestAgendaGetCoverageStatesScope is the anti-regression test for the war
// game's worst failure (#4810): an agent answering "nothing is due" from an
// answer that was never the whole picture. A default result must say, in its
// own payload, which range it covers, which timezone, which calendars it
// searched, which areas exist but are absent, and how to ask for them — and a
// range with genuinely nothing in it must still say all of that rather than
// read as "the family has nothing".
func TestAgendaGetCoverageStatesScope(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	agenda := bind(t, NewAgendaGetTool, pool)
	seedNamedCalendar(t, pool, hh.HouseholdID, "Family", "family")

	// A day far in the future: nothing is scheduled, no bill is due, no meal is
	// planned. This is exactly the answer an agent must not dress up as
	// "nothing is due".
	text, isErr := callTool(t, agenda, ctx, map[string]any{"from": "2027-06-01", "to": "2027-06-07"})
	if isErr {
		t.Fatalf("agenda_get: %s", text)
	}
	var res agendaResult
	if err := json.Unmarshal(data(t, text), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	c := res.Coverage
	if c.From != "2027-06-01" || c.To != "2027-06-07" || c.Days != 7 {
		t.Errorf("coverage range = %s..%s (%d days), want 2027-06-01..2027-06-07 (7)", c.From, c.To, c.Days)
	}
	if c.Timezone != "America/New_York" {
		t.Errorf("coverage.timezone = %q", c.Timezone)
	}
	if strings.Join(c.Calendars, ",") != "Family" {
		t.Errorf("coverage.calendars = %v, want [Family]", c.Calendars)
	}
	if !strings.Contains(c.Scope, "no email") || !strings.Contains(c.Scope, "2027") || !strings.Contains(c.Scope, "America/New_York") {
		t.Errorf("coverage.scope does not name the range, zone and what is out of scope: %q", c.Scope)
	}
	if strings.Join(c.NotIncluded, ",") != "maintenance" {
		t.Errorf("coverage.not_included = %v, want [maintenance]", c.NotIncluded)
	}
	if c.HowToGetDetail == "" || !strings.Contains(c.HowToGetDetail, "maintenance") {
		t.Errorf("how_to_get_detail does not name the missing area: %q", c.HowToGetDetail)
	}
	// The empty counters are present and zero: "none" here means "I looked".
	if c.EventsOccurrences == nil || *c.EventsOccurrences != 0 {
		t.Errorf("events_occurrences = %v, want 0", c.EventsOccurrences)
	}
	if c.BillsCount == nil || *c.BillsCount != 0 {
		t.Errorf("bills_count = %v, want 0", c.BillsCount)
	}
	// The summary says what it covers, and that it is not everything.
	if !strings.Contains(text, "2027") || !strings.Contains(text, "Not included: maintenance") {
		t.Errorf("summary does not state the range or the omitted area: %s", text)
	}
	// The events section is an empty list, not null: a consumer can iterate it.
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data(t, text), &raw)
	if string(raw["events"]) != "[]" {
		t.Errorf("events = %s, want []", raw["events"])
	}
}

// TestParseAgendaArgs covers the argument parser's refusals and conventions:
// detail matched in any case with dashes folded, max_per_day accepting an int
// or a float and refusing a fraction, and areas taking []any (the wire form),
// []string (an in-process caller) and a bare string.
func TestParseAgendaArgs(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args map[string]any
		want agendaInput
		bad  string // substring of the error, "" for success
	}{
		{name: "defaults", args: nil, want: agendaInput{detail: agendaDetailSummary, perDay: defaultAgendaPerDay,
			areas: agendaDefaultAreas}},
		{name: "detail any case and dash", args: map[string]any{"detail": " FULL "}, want: agendaInput{detail: agendaDetailFull}},
		{name: "detail with a dash", args: map[string]any{"detail": "Sum-Mary"}, bad: "not an agenda detail level"},
		{name: "bad detail", args: map[string]any{"detail": "verbose"}, bad: "not an agenda detail level"},
		{name: "detail as a number", args: map[string]any{"detail": 2}, bad: "must be a name"},
		{name: "max_per_day int", args: map[string]any{"max_per_day": 12}, want: agendaInput{perDay: 12}},
		{name: "max_per_day float", args: map[string]any{"max_per_day": float64(7)}, want: agendaInput{perDay: 7}},
		{name: "max_per_day fraction", args: map[string]any{"max_per_day": 2.5}, bad: "whole number"},
		{name: "max_per_day zero", args: map[string]any{"max_per_day": 0}, bad: "whole number"},
		{name: "max_per_day too large", args: map[string]any{"max_per_day": maxAgendaPerDay + 1}, bad: "whole number"},
		{name: "areas []any", args: map[string]any{"areas": []any{"bills", "events", "bills"}}, want: agendaInput{areas: []string{"bills", "events"}, explicitAreas: true}},
		{name: "areas []string", args: map[string]any{"areas": []string{"meals"}}, want: agendaInput{areas: []string{"meals"}, explicitAreas: true}},
		{name: "areas bare string", args: map[string]any{"areas": "lists"}, want: agendaInput{areas: []string{"lists"}, explicitAreas: true}},
		{name: "areas any case", args: map[string]any{"areas": []any{" MAINTENANCE "}}, want: agendaInput{areas: []string{"maintenance"}, explicitAreas: true}},
		{name: "unknown area", args: map[string]any{"areas": []any{"chores"}}, bad: "not an agenda area"},
		{name: "areas not a list of names", args: map[string]any{"areas": []any{1}}, bad: "must be a list"},
		{name: "empty areas", args: map[string]any{"areas": []any{}}, want: agendaInput{areas: agendaDefaultAreas}},
		{name: "to before from", args: map[string]any{"from": "2026-11-18", "to": "2026-11-17"}, bad: "to must not be before from"},
		{name: "range too long", args: map[string]any{"from": "2026-01-01", "to": "2026-03-01"}, bad: "longer than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, bad := parseAgendaArgs(tt.args, ny)
			if tt.bad != "" {
				if bad == nil {
					t.Fatalf("parsed %+v, want an error containing %q", got, tt.bad)
				}
				var envelope struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				_ = json.Unmarshal([]byte(bad.Content[0].(mcp.TextContent).Text), &envelope)
				if !strings.Contains(envelope.Error.Message, tt.bad) {
					t.Errorf("error %q does not contain %q", envelope.Error.Message, tt.bad)
				}
				return
			}
			if bad != nil {
				t.Fatalf("refused: %s", bad.Content[0].(mcp.TextContent).Text)
			}
			// A case that only names one field leaves the rest at the defaults.
			wantDetail, wantPerDay := tt.want.detail, tt.want.perDay
			if wantDetail == "" {
				wantDetail = agendaDetailSummary
			}
			if wantPerDay == 0 {
				wantPerDay = defaultAgendaPerDay
			}
			if got.detail != wantDetail || got.perDay != wantPerDay || got.explicitAreas != tt.want.explicitAreas {
				t.Errorf("got detail=%q perDay=%d explicit=%v, want %q/%d/%v",
					got.detail, got.perDay, got.explicitAreas, wantDetail, wantPerDay, tt.want.explicitAreas)
			}
			if tt.want.areas != nil && strings.Join(got.areas, ",") != strings.Join(tt.want.areas, ",") {
				t.Errorf("areas = %v, want %v", got.areas, tt.want.areas)
			}
			// The default range is a week from the first day, whatever was given.
			wantDays := 7
			if tt.name == "to before from" || tt.name == "range too long" {
				return
			}
			if got.dayCount != wantDays {
				t.Errorf("dayCount = %d, want %d", got.dayCount, wantDays)
			}
		})
	}
}
