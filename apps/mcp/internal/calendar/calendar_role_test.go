package calendar

// #4808: event_find, calendar_find, agenda_get and calendar_find_free_time are
// readable by a housesitter (ResCalendar read is rolesSitter), and every bill
// has a mirror event on the Bills calendar whose title is the bill's NAME and
// whose description is built by billcal.Description: "Amount: $X", the
// category, the autopay flag and the payment URL. The API (#4807) and CalDAV
// (#4718) already drop those records for a housesitter; this is the MCP half,
// and it uses the same rule — ask the policy table per record kind and drop the
// whole record, because no partial version of a bill mirror is safe.
//
// The role view is exercised directly (eventView) so the rule is pinned per
// kind and per role, and through the real tools so the wiring is pinned too.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/billcal"
	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/bill"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// calendarAsRole builds a member of hh with role and a context carrying it.
func calendarAsRole(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) context.Context {
	t.Helper()
	ctx := context.Background()
	email := "cal-" + role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
		email, "Calendar "+role).Scan(&uid); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
		hh.HouseholdID, uid, role); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	return server.ContextWithClaims(ctx, &auth.Claims{
		Identity:    auth.Identity{Subject: uid.String(), Email: email},
		UserID:      uid.String(),
		HouseholdID: hh.HouseholdID.String(),
		Role:        role,
	})
}

// calendarRoleEnv is a household with a Family calendar holding a dentist
// appointment and a Bills calendar holding a real bill mirror (written through
// billcal, the same call bill_create makes), plus the tool handlers.
type calendarRoleEnv struct {
	pool      *pgxpool.Pool
	hh        testutil.Household
	loc       *time.Location
	family    string
	bills     string
	dentist   string
	billEvent string
	billID    string
	find      handlerFunc
	calFind   handlerFunc
	agenda    handlerFunc
	freeTime  handlerFunc
}

// ownerCtx is the seeded owner's context: the write tools are family-only, so
// the fixture's bill is created the way a family member would.
func ownerCtx(t *testing.T, hh testutil.Household) context.Context {
	t.Helper()
	return server.ContextWithClaims(context.Background(), &auth.Claims{
		Identity:    auth.Identity{Subject: hh.UserID.String(), Email: hh.Email},
		UserID:      hh.UserID.String(),
		HouseholdID: hh.HouseholdID.String(),
		Role:        "owner",
	})
}

func newCalendarRoleEnv(t *testing.T) calendarRoleEnv {
	t.Helper()
	pool := testutil.NewPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	env := calendarRoleEnv{pool: pool, hh: hh, loc: mustLoc(t, "America/New_York")}

	env.family = seedNamedCalendar(t, pool, hh.HouseholdID, "Family", "family")
	env.bills = seedNamedCalendar(t, pool, hh.HouseholdID, "Bills", "bills")
	env.dentist = seedEvent(t, pool, env.family, uuid.NewString(),
		`{"title":"Dentist","start":"2026-11-18T19:00:00Z","end":"2026-11-18T20:00:00Z"}`)

	// A real bill and its mirror event: bill_create writes both in one
	// transaction (billcal.Sync), which is what makes the description carry
	// "Amount: $142.50".
	billCreate := bindBill(bill.NewBillCreateTool, pool)
	text, isErr := callTool(t, billCreate, ownerCtx(t, hh), map[string]any{
		"name": "Electric", "amount": 142.5, "due_day": 18, "category": "Utilities"})
	if isErr {
		t.Fatalf("bill_create: %s", text)
	}
	env.billID = billID(t, text)

	var eventID string
	if err := pool.QueryRow(context.Background(),
		`SELECT id::text FROM calendar_objects WHERE entity_type = 'bill' AND entity_id = $1`, env.billID).Scan(&eventID); err != nil {
		t.Fatalf("bill mirror event: %v", err)
	}
	env.billEvent = eventID

	env.find = bind(t, NewEventFindTool, pool)
	env.calFind = bind(t, NewCalendarFindTool, pool)
	env.agenda = bind(t, NewAgendaGetTool, pool)
	env.freeTime = bind(t, NewCalendarFindFreeTimeTool, pool)
	return env
}

// TestCalendarKindMapping pins the two CASE rules the whole view rests on: the
// calendar's kind from calendars.type, and the record's kind from what it
// mirrors. It is the same mapping as apps/api/internal/calendar/access.go
// (#4807) and apps/calendar/internal/db/access.go (#4718).
func TestCalendarKindMapping(t *testing.T) {
	for calendarType, want := range map[string]string{
		"bills":       "bill",
		"maintenance": "maintenance_task",
		"vehicles":    "vehicle",
		"family":      "calendar_event",
		"properties":  "calendar_event",
		"custom":      "calendar_event",
		"":            "calendar_event",
		"nonsense":    "calendar_event",
	} {
		if got := calendarKind(calendarType); got != want {
			t.Errorf("calendarKind(%q) = %q, want %q", calendarType, got, want)
		}
	}
	ptr := func(s string) *string { return &s }
	for _, tc := range []struct {
		eventType  string
		entityType *string
		want       string
	}{
		{"bill", nil, "bill"},
		{"event", ptr("bill"), "bill"},
		{"bill", ptr("bill"), "bill"},
		{"maintenance", nil, "maintenance_task"},
		{"event", ptr("maintenance_task"), "maintenance_task"},
		{"event", ptr("maintenance"), "maintenance_task"},
		{"event", nil, "calendar_event"},
		{"custom", ptr("person"), "calendar_event"},
		{"custom", ptr("vehicle"), "calendar_event"},
	} {
		if got := recordKind(tc.eventType, tc.entityType); got != tc.want {
			t.Errorf("recordKind(%q, %v) = %q, want %q", tc.eventType, tc.entityType, got, tc.want)
		}
	}
}

// TestEventViewFollowsThePolicyTable walks every role and every kind of
// calendar and record against auth.CanSeeEntity, so a change to the policy
// table cannot leave the MCP calendar behind, and an unknown role sees nothing.
func TestEventViewFollowsThePolicyTable(t *testing.T) {
	ptr := func(s string) *string { return &s }
	calendars := map[string]string{"family": "family", "bills": "bills", "maintenance": "maintenance", "vehicles": "vehicles", "custom": "custom"}
	records := map[string]struct {
		eventType  string
		entityType *string
	}{"event": {"event", nil}, "bill": {"bill", nil}, "bill-linked": {"event", ptr("bill")},
		"maintenance": {"maintenance", nil}, "maintenance-linked": {"event", ptr("maintenance_task")}}

	for _, role := range append(auth.AllRoles, "something_else") {
		view := eventView{role: role}
		for calType, cals := range calendars {
			want := auth.CanSeeEntity(role, calendarKind(calType))
			if got := view.calendarVisible(calType); got != want {
				t.Errorf("%s: calendarVisible(%s) = %v, want %v", role, cals, got, want)
			}
			for name, r := range records {
				want := auth.CanSeeEntity(role, calendarKind(calType)) && auth.CanSeeEntity(role, recordKind(r.eventType, r.entityType))
				if got := view.eventVisible(calType, r.eventType, r.entityType); got != want {
					t.Errorf("%s: eventVisible(%s, %s) = %v, want %v", role, cals, name, got, want)
				}
			}
		}
	}
}

// TestEventFindHidesBillMirrorsByRole runs the real event_find, calendar_find
// and agenda_get for every role.
func TestEventFindHidesBillMirrorsByRole(t *testing.T) {
	env := newCalendarRoleEnv(t)

	// What a family role must be able to see, so "hidden" is proved against a
	// fixture that really carries the amount.
	for _, role := range []string{"owner", "family_manager", "family_member", "viewer"} {
		ctx := calendarAsRole(t, env.pool, env.hh, role)
		text, isErr := callTool(t, env.find, ctx, map[string]any{"from": "2026-11-16", "to": "2026-11-22"})
		if isErr {
			t.Fatalf("%s: event_find: %s", role, text)
		}
		if !strings.Contains(text, "Dentist") || !strings.Contains(text, "Amount: $142.50") {
			t.Fatalf("%s: the fixture must show the dentist and the bill's amount: %s", role, text)
		}
	}

	t.Run("housesitter", func(t *testing.T) {
		ctx := calendarAsRole(t, env.pool, env.hh, "housesitter")
		// event_find by range: the dentist stays, the bill mirror is gone.
		text, isErr := callTool(t, env.find, ctx, map[string]any{"from": "2026-11-16", "to": "2026-11-22"})
		if isErr {
			t.Fatalf("event_find: %s", text)
		}
		if !strings.Contains(text, "Dentist") {
			t.Errorf("housesitter lost an ordinary event: %s", text)
		}
		for _, leak := range []string{"Amount:", "Electric", "142.50", "autopay", "payments.example.com"} {
			if strings.Contains(text, leak) {
				t.Errorf("event_find leaks %q to a housesitter: %s", leak, text)
			}
		}
		// event_find by id: the bill mirror is absent, like a calendar of
		// another household (404-style isError), and the dentist still opens.
		if got, isErr := callTool(t, env.find, ctx, map[string]any{"id": env.billEvent}); !isErr || !strings.Contains(got, "No event with id") {
			t.Errorf("event_find id=<bill mirror> for a housesitter = %s", got)
		}
		if got, isErr := callTool(t, env.find, ctx, map[string]any{"id": env.dentist}); isErr || !strings.Contains(got, "Dentist") {
			t.Errorf("event_find id=<dentist> for a housesitter = %s", got)
		}
		// event_find asking for the Bills calendar by name: nothing.
		if got, _ := callTool(t, env.find, ctx, map[string]any{"from": "2026-11-16", "to": "2026-11-22", "calendar_id": env.bills}); strings.Contains(got, "Amount:") {
			t.Errorf("event_find calendar_id=<Bills> leaks the amount: %s", got)
		}
		// calendar_find: no Bills calendar.
		if got, isErr := callTool(t, env.calFind, ctx, map[string]any{}); isErr || strings.Contains(got, `"Bills"`) || !strings.Contains(got, `"Family"`) {
			t.Errorf("calendar_find for a housesitter = %s", got)
		}
		// agenda_get: no bill mirror among the events.
		text, isErr = callTool(t, env.agenda, ctx, map[string]any{"from": "2026-11-18", "to": "2026-11-18"})
		if isErr {
			t.Fatalf("agenda_get: %s", text)
		}
		for _, leak := range []string{"Amount:", "Electric", "142.50"} {
			if strings.Contains(text, leak) {
				t.Errorf("agenda_get leaks %q to a housesitter: %s", leak, text)
			}
		}
	})

	t.Run("vendor sees no calendar at all", func(t *testing.T) {
		ctx := calendarAsRole(t, env.pool, env.hh, "vendor")
		// #4717's gate refuses event_find to a vendor; the view agrees.
		view, _ := eventViewFor(ctx, env.pool, server.ClaimsFromContext(ctx))
		if view.anyEvent() {
			t.Error("a vendor may see calendar events through the view")
		}
		if got, isErr := callTool(t, env.calFind, ctx, map[string]any{}); isErr || strings.Contains(got, `"Family"`) || strings.Contains(got, `"Bills"`) {
			t.Errorf("calendar_find for a vendor = %s", got)
		}
	})

	t.Run("viewer keeps everything", func(t *testing.T) {
		ctx := calendarAsRole(t, env.pool, env.hh, "viewer")
		if got, isErr := callTool(t, env.calFind, ctx, map[string]any{}); isErr || !strings.Contains(got, `"Bills"`) {
			t.Errorf("calendar_find for a viewer = %s", got)
		}
	})
}

// TestCalendarFindFreeTimeHidesBillMirrors proves the free-time search cannot
// be used to learn about a bill: an all-day mirror is not free-time input
// either way, but the role view drops the row before it can be named in a
// conflict or a note.
func TestCalendarFindFreeTimeHidesBillMirrors(t *testing.T) {
	env := newCalendarRoleEnv(t)
	ctx := calendarAsRole(t, env.pool, env.hh, "housesitter")
	// duration_minutes is a JSON number, so float64: the MCP request decoder
	// never yields an int and getFloat64Arg ignores one.
	text, isErr := callTool(t, env.freeTime, ctx, map[string]any{"from": "2026-11-18", "to": "2026-11-18", "duration_minutes": 60.0})
	if isErr {
		t.Fatalf("calendar_find_free_time: %s", text)
	}
	for _, leak := range []string{"Amount:", "Electric", "142.50"} {
		if strings.Contains(text, leak) {
			t.Errorf("calendar_find_free_time leaks %q to a housesitter: %s", leak, text)
		}
	}
}

// TestQueryOccurrencesAsDropsRowsBeforeParsing checks the read itself, not just
// the tools: a restricted view returns no occurrence of the bill series, while
// the family view returns them (a recurring mirror is many occurrences of one
// row, and all of them must go).
func TestQueryOccurrencesAsDropsRowsBeforeParsing(t *testing.T) {
	env := newCalendarRoleEnv(t)
	from := time.Date(2026, 11, 16, 0, 0, 0, 0, env.loc)
	to := time.Date(2026, 11, 22, 0, 0, 0, 0, env.loc)

	for _, tc := range []struct {
		role    string
		wantIDs map[string]bool
	}{
		{"owner", map[string]bool{env.dentist: true, env.billEvent: true}},
		{"housesitter", map[string]bool{env.dentist: true}},
	} {
		events, _, err := queryOccurrencesAs(context.Background(), env.pool, eventView{role: tc.role},
			env.hh.HouseholdID.String(), env.loc, nil, from, to)
		if err != nil {
			t.Fatalf("%s: %v", tc.role, err)
		}
		seen := map[string]bool{}
		for _, ev := range events {
			seen[ev.ID] = true
		}
		for id, want := range tc.wantIDs {
			if seen[id] != want {
				t.Errorf("%s: event %s present=%v, want %v", tc.role, id, seen[id], want)
			}
		}
		if len(seen) != len(tc.wantIDs) {
			t.Errorf("%s: got %d distinct events, want %d (%v)", tc.role, len(seen), len(tc.wantIDs), seen)
		}
	}
}

// TestBillcalDescriptionCarriesTheAmount is the reason the whole record must be
// dropped rather than stripped: the mirror's description is the bill's money.
func TestBillcalDescriptionCarriesTheAmount(t *testing.T) {
	amount := 142.5
	desc := billcal.Description(billcal.Bill{Name: "Electric", Amount: &amount})
	if !strings.Contains(desc, "Amount: $142.50") {
		t.Fatalf("billcal.Description = %q, want the amount in it: this is why the record is dropped whole", desc)
	}
}