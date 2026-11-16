package calendar

// #4807: calendar reads follow the household role policy.
//
// GET /api/v1/calendars[/{id}/events] is ResCalendar read = rolesSitter
// (apps/api/internal/auth/roles.go), and since #4485/#4716 every bill has a
// mirror event on the Bills calendar whose description carries
// "Amount: $X" (packages/billcal Description). A housesitter therefore read
// the family's bill names, amounts and due dates through the API while CalDAV
// (apps/calendar/internal/db/access.go, #4718) already hid them.
//
// These tests run against a real Postgres (TEST_DATABASE_URL, every migration
// applied) with the bill mirrored by billcal.SyncByID — the same call the API
// bill repo makes — and ask the calendar routes as every role. They are the
// acceptance test of the ticket: owner/viewer see both calendars and the bill
// event; a housesitter sees only the non-money calendar, gets no bill event
// from /calendars/events, and 404s on /calendars/{billsId}/events.
//
// Run with -p 1.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/billcal"
)

type roleFixture struct {
	pool      *pgxpool.Pool
	router    http.Handler
	household string
	familyCal string
	billsCal  string
	bill      string
	billEvent string
}

// setupRoles seeds a family with a Family calendar holding one ordinary event
// and a Bills calendar holding the mirror of a real bill, then mounts the
// calendar routes with claims taken from the X-Test-Role header the way
// RequireAuth puts them in the request context. The route policy itself is
// covered by cmd/api/routes_test.go; these tests are about what a role's read
// returns.
func setupRoles(t *testing.T) *roleFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var hid string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ('Calendar roles '||gen_random_uuid()) RETURNING id::text`).Scan(&hid))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hid)
	})

	var familyCal, billsCal string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO calendars (household_id, name, type, caldav_uid)
		 VALUES ($1, 'Family', 'family', gen_random_uuid()::text) RETURNING id::text`, hid).Scan(&familyCal))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO calendars (household_id, name, type, caldav_uid)
		 VALUES ($1, 'Bills', 'bills', gen_random_uuid()::text) RETURNING id::text`, hid).Scan(&billsCal))

	// The ordinary, non-money event a housesitter legitimately needs: a
	// dentist appointment on the Family calendar, linked to nothing.
	_, err = pool.Exec(ctx,
		`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, etag, event_type)
		 VALUES (gen_random_uuid(), $1, gen_random_uuid()::text,
		         '{"title":"Dentist","start":"2026-11-20T15:00:00Z","end":"2026-11-20T16:00:00Z","event_type":"custom"}',
		         gen_random_uuid()::text, 'custom')`, familyCal)
	require.NoError(t, err)

	// The bill and its mirror, through billcal — the same call bill.Repo.Create
	// and the worker backfill make, so the event is exactly what the API and
	// the MCP write: entity_type 'bill', event_type 'bill', and a description
	// carrying the amount.
	due := 15
	var billID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO bills (household_id, name, amount, category, frequency, due_day)
		 VALUES ($1, 'Electric', 142.50, 'Utilities', 'monthly', $2) RETURNING id::text`,
		hid, due).Scan(&billID))
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	ch, err := billcal.SyncByID(ctx, tx, hid, billID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	require.Equal(t, billcal.ActionCreate, ch.Action, "the bill should have been mirrored onto the Bills calendar")
	require.NotEmpty(t, ch.EventID)

	// The mirror really does carry the amount in the clear: this is the leak.
	var mirrorData string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT ical_data FROM calendar_objects WHERE id = $1`, ch.EventID).Scan(&mirrorData))
	require.Contains(t, mirrorData, "Amount: $142.50", "fixture: the bill mirror must carry the amount")

	f := &roleFixture{pool: pool, household: hid, familyCal: familyCal,
		billsCal: billsCal, bill: billID, billEvent: ch.EventID}
	f.router = roleRouter(pool)
	return f
}

// roleRouter mounts the calendar read routes with the role from X-Test-Role.
func roleRouter(pool *pgxpool.Pool) http.Handler {
	h := NewHandler(NewRepo(pool))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := &auth.Claims{
				UserID:      "user-" + req.Header.Get("X-Test-Role"),
				HouseholdID: req.Header.Get("X-Test-Household"),
				Role:        req.Header.Get("X-Test-Role"),
			}
			next.ServeHTTP(w, req.WithContext(middleware.WithClaims(req.Context(), c)))
		})
	})
	r.Get("/api/v1/calendars", h.ListCalendars)
	r.Get("/api/v1/calendars/events", h.ListAllEvents)
	r.Get("/api/v1/calendars/{id}/events", h.ListEvents)
	return r
}

// get asks for path as role and returns the status and the decoded body.
func (f *roleFixture) get(t *testing.T, role, path string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, &bytes.Buffer{})
	req.Header.Set("X-Test-Household", f.household)
	req.Header.Set("X-Test-Role", role)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	var body map[string]any
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	}
	return rec.Code, body
}

// rows returns the "data" array of a list response.
func rows(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["data"].([]any)
	require.True(t, ok, "response has no data array: %v", body)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		row, ok := r.(map[string]any)
		require.True(t, ok, "data row is not an object: %v", r)
		out = append(out, row)
	}
	return out
}

func calendarNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	var names []string
	for _, r := range rows(t, body) {
		name, _ := r["name"].(string)
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func eventTitles(t *testing.T, body map[string]any) []string {
	t.Helper()
	var titles []string
	for _, r := range rows(t, body) {
		title, _ := r["title"].(string)
		titles = append(titles, title)
	}
	sort.Strings(titles)
	return titles
}

// TestCalendarReadsFollowRolePolicy is the #4807 acceptance test.
func TestCalendarReadsFollowRolePolicy(t *testing.T) {
	f := setupRoles(t)

	rolesWithMoney := []string{auth.RoleOwner, auth.RoleFamilyManager, auth.RoleFamilyMember, auth.RoleViewer}
	// A housesitter keeps the calendars it may read and loses the money ones.
	rolesWithoutMoney := []string{auth.RoleHousesitter}
	// An unknown role sees nothing at all: the rule fails closed.
	rolesSeingNothing := []string{"something_else", ""}

	t.Run("roles with bill read see the Bills calendar and the bill event", func(t *testing.T) {
		for _, role := range rolesWithMoney {
			code, body := f.get(t, role, "/api/v1/calendars")
			require.Equal(t, http.StatusOK, code, role)
			assert.Equal(t, []string{"Bills", "Family"}, calendarNames(t, body), "GET /calendars as %s", role)

			// The bill event comes back from the calendar it lives on...
			code, body = f.get(t, role, "/api/v1/calendars/"+f.billsCal+"/events")
			require.Equal(t, http.StatusOK, code, role)
			titles := eventTitles(t, body)
			require.Len(t, titles, 1, "GET /calendars/{bills}/events as %s", role)
			assert.True(t, strings.HasPrefix(titles[0], "Electric"), "bill event title as %s: %q", role, titles[0])
			// ...with the amount, which is what they may see.
			desc, _ := rows(t, body)[0]["description"].(string)
			assert.Contains(t, desc, "Amount: $142.50", "bill event description as %s", role)

			// ...and from the household-wide listing.
			code, body = f.get(t, role, "/api/v1/calendars/events")
			require.Equal(t, http.StatusOK, code, role)
			assert.Len(t, eventTitles(t, body), 2, "GET /calendars/events as %s should list both events", role)
		}
	})

	t.Run("roles without bill read see no Bills calendar and no bill event", func(t *testing.T) {
		for _, role := range rolesWithoutMoney {
			// The Bills calendar is not in the list...
			code, body := f.get(t, role, "/api/v1/calendars")
			require.Equal(t, http.StatusOK, code, role)
			assert.Equal(t, []string{"Family"}, calendarNames(t, body), "GET /calendars as %s", role)

			// ...it 404s on direct access (not 403: its existence must not leak)...
			code, _ = f.get(t, role, "/api/v1/calendars/"+f.billsCal+"/events")
			assert.Equal(t, http.StatusNotFound, code, "GET /calendars/{bills}/events as %s", role)

			// ...and no bill event appears in the household-wide listing, while
			// the ordinary event on the calendar they may read is still there.
			code, body = f.get(t, role, "/api/v1/calendars/events")
			require.Equal(t, http.StatusOK, code, role)
			assert.Equal(t, []string{"Dentist"}, eventTitles(t, body), "GET /calendars/events as %s", role)
			for _, r := range rows(t, body) {
				desc, _ := r["description"].(string)
				assert.NotContains(t, desc, "Amount:", "role %s must see no amount", role)
			}
		}
	})

	t.Run("an unknown role sees no calendar and no event", func(t *testing.T) {
		for _, role := range rolesSeingNothing {
			code, body := f.get(t, role, "/api/v1/calendars")
			require.Equal(t, http.StatusOK, code, role)
			assert.Empty(t, calendarNames(t, body), "GET /calendars as %q", role)

			code, _ = f.get(t, role, "/api/v1/calendars/"+f.familyCal+"/events")
			assert.Equal(t, http.StatusNotFound, code, "GET /calendars/{family}/events as %q", role)

			code, body = f.get(t, role, "/api/v1/calendars/events")
			require.Equal(t, http.StatusOK, code, role)
			assert.Empty(t, eventTitles(t, body), "GET /calendars/events as %q", role)
		}
	})

	t.Run("a housesitter still reads the calendars they legitimately need", func(t *testing.T) {
		// The ticket is about money, not about hiding the household: the
		// Family calendar and its events stay readable.
		code, body := f.get(t, auth.RoleHousesitter, "/api/v1/calendars/"+f.familyCal+"/events")
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, []string{"Dentist"}, eventTitles(t, body))

		// The kind is what the event is *about*, not which calendar it sits on
		// (access.go, matching CalDAV): an event a person made on the Family
		// calendar and linked to a pet stays a calendar event and stays
		// visible...
		ctx := context.Background()
		var petID string
		require.NoError(t, f.pool.QueryRow(ctx,
			`INSERT INTO pets (household_id, name, species) VALUES ($1, 'Rex', 'dog') RETURNING id::text`,
			f.household).Scan(&petID))
		_, err := f.pool.Exec(ctx,
			`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
			 VALUES (gen_random_uuid(), $1, gen_random_uuid()::text,
			         '{"title":"Vet visit","start":"2026-11-21T15:00:00Z","end":"2026-11-21T16:00:00Z","event_type":"custom"}',
			         gen_random_uuid()::text, 'custom', 'pet', $2)`, f.familyCal, petID)
		require.NoError(t, err)
		// ...while one linked to a bill is a bill-kind event and is hidden,
		// exactly as CalDAV hides it.
		_, err = f.pool.Exec(ctx,
			`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
			 VALUES (gen_random_uuid(), $1, gen_random_uuid()::text,
			         '{"title":"Pay the electrician","start":"2026-11-22T15:00:00Z","end":"2026-11-22T16:00:00Z","event_type":"custom"}',
			         gen_random_uuid()::text, 'custom', 'bill', $2)`, f.familyCal, f.bill)
		require.NoError(t, err)

		code, body = f.get(t, auth.RoleHousesitter, "/api/v1/calendars/"+f.familyCal+"/events")
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, []string{"Dentist", "Vet visit"}, eventTitles(t, body),
			"a pet-linked family event stays visible; a bill-linked one is a bill")
	})
}

// TestCalendarRepoHidesByKind checks the repo directly, so a handler bug
// cannot mask a query bug, and covers the kinds the acceptance test's fixture
// does not have (Maintenance, Vehicles, and a maintenance task event).
func TestCalendarRepoHidesByKind(t *testing.T) {
	f := setupRoles(t)
	ctx := context.Background()
	repo := NewRepo(f.pool)
	hid := uuid.MustParse(f.household)

	var maintCal, vehCal string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO calendars (household_id, name, type, caldav_uid)
		 VALUES ($1, 'Maintenance', 'maintenance', gen_random_uuid()::text) RETURNING id::text`, f.household).Scan(&maintCal))
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO calendars (household_id, name, type, caldav_uid)
		 VALUES ($1, 'Vehicles', 'vehicles', gen_random_uuid()::text) RETURNING id::text`, f.household).Scan(&vehCal))

	// A maintenance task with a due date, mirrored the way the maintenance
	// handler does it (event_type 'maintenance', entity_type
	// 'maintenance_task'), with a cost the sitter must not see.
	task := mustID(t, f.pool, `INSERT INTO maintenance_tasks (household_id, name, due_date, cost)
		VALUES ($1, 'Fix the boiler', '2026-12-01', 850) RETURNING id`, hid)
	require.NoError(t, repo.UpsertMaintenanceEvent(ctx, hid, task, "Fix the boiler", "Cost: 850.00", mustDate(t, "2026-12-01")))

	calNames := func(role string) []string {
		t.Helper()
		cals, err := repo.ListCalendars(ctx, hid, role)
		require.NoError(t, err)
		var names []string
		for _, c := range cals {
			names = append(names, c.Name)
		}
		sort.Strings(names)
		return names
	}

	// The family and the viewer read everything, whatever the kind.
	for _, role := range []string{auth.RoleOwner, auth.RoleFamilyMember, auth.RoleViewer} {
		assert.Equal(t, []string{"Bills", "Family", "Maintenance", "Vehicles"}, calNames(role), role)
	}
	// A housesitter reads the calendars of kinds they may see (ResCalendar and
	// ResProperties) and nothing of money, maintenance or vehicles.
	assert.Equal(t, []string{"Family"}, calNames(auth.RoleHousesitter))
	// A vendor's calendar route is refused by RequirePolicy (ResCalendar read =
	// rolesSitter), so it never reaches the repo. Were it to, the policy table
	// still grants vendor the maintenance kind (ResMaintenance read =
	// rolesFabric) and nothing else, so the money kinds stay hidden.
	assert.Equal(t, []string{"Maintenance"}, calNames(auth.RoleVendor))
	// An unknown role sees nothing (fail closed).
	assert.Empty(t, calNames(""))
	assert.Empty(t, calNames("something_else"))

	// The maintenance event is hidden from a housesitter even though it lives
	// on a calendar whose kind is the reason, and shown to the family.
	loc := repo.HouseholdLocation(ctx, hid)
	for _, role := range []string{auth.RoleOwner, auth.RoleViewer} {
		events, err := repo.ListEvents(ctx, uuid.MustParse(maintCal), EventRange{}, loc, role)
		require.NoError(t, err)
		require.Len(t, events, 1, role)
		assert.Equal(t, "Fix the boiler", events[0].Title)
	}
	events, err := repo.ListEvents(ctx, uuid.MustParse(maintCal), EventRange{}, loc, auth.RoleHousesitter)
	require.NoError(t, err)
	assert.Empty(t, events, "a housesitter must get no maintenance event")

	// The Bills calendar's own events are hidden from a housesitter at the
	// repo level too, not just by the handler's 404.
	events, err = repo.ListEvents(ctx, uuid.MustParse(f.billsCal), EventRange{}, loc, auth.RoleHousesitter)
	require.NoError(t, err)
	assert.Empty(t, events)

	// GetCalendarByIDForHousehold answers (nil, nil) for a hidden calendar,
	// which is what the handler turns into 404.
	cal, err := repo.GetCalendarByIDForHousehold(ctx, uuid.MustParse(f.billsCal), hid, auth.RoleHousesitter)
	require.NoError(t, err)
	assert.Nil(t, cal)
	cal, err = repo.GetCalendarByIDForHousehold(ctx, uuid.MustParse(f.billsCal), hid, auth.RoleOwner)
	require.NoError(t, err)
	require.NotNil(t, cal)
	assert.Equal(t, "Bills", cal.Name)
}

// TestListAllEventsPropertyFilterKeepsRoleArgument covers the ?property_id=
// path of ListAllEvents, where the role's SQL placeholder is computed before
// rangeFilter appends its own arguments: a mis-numbered placeholder would
// either compare the role to a timestamp or bind the property to the role.
func TestListAllEventsPropertyFilterKeepsRoleArgument(t *testing.T) {
	f := setupRoles(t)
	ctx := context.Background()
	repo := NewRepo(f.pool)
	hid := uuid.MustParse(f.household)

	var propID string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO properties (household_id, name) VALUES ($1, 'Lake House') RETURNING id::text`,
		f.household).Scan(&propID))
	// A property calendar with an ordinary event on it, and a bill bound to the
	// property so its mirror also reaches the property-filtered listing.
	var propCal string
	require.NoError(t, f.pool.QueryRow(ctx,
		`INSERT INTO calendars (household_id, property_id, name, type, caldav_uid)
		 VALUES ($1, $2, 'Lake House', 'properties', gen_random_uuid()::text) RETURNING id::text`,
		f.household, propID).Scan(&propCal))
	_, err := f.pool.Exec(ctx,
		`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, etag, event_type)
		 VALUES (gen_random_uuid(), $1, gen_random_uuid()::text,
		         '{"title":"Cleaners","start":"2026-11-25T15:00:00Z","end":"2026-11-25T16:00:00Z","event_type":"custom"}',
		         gen_random_uuid()::text, 'custom')`, propCal)
	require.NoError(t, err)
	_, err = f.pool.Exec(ctx,
		`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
		 VALUES (gen_random_uuid(), $1, gen_random_uuid()::text,
		         '{"title":"Pay the electrician","start":"2026-11-26T15:00:00Z","end":"2026-11-26T16:00:00Z","event_type":"custom"}',
		         gen_random_uuid()::text, 'custom', 'bill', $2)`, propCal, f.bill)
	require.NoError(t, err)

	propIDParsed := uuid.MustParse(propID)
	loc := repo.HouseholdLocation(ctx, hid)

	// No window at all: the property filter and the role are the only
	// arguments, and a housesitter still loses the bill-linked event.
	events, err := repo.ListAllEvents(ctx, hid, EventRange{}, &propIDParsed, loc, auth.RoleHousesitter)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "Cleaners", events[0].Title)

	// With a window, rangeFilter appends after the role: a wrong placeholder
	// would make Postgres compare a timestamptz to a text role and error.
	from := mustDate(t, "2026-11-20")
	to := mustDate(t, "2026-11-30")
	events, err = repo.ListAllEvents(ctx, hid, EventRange{Start: &from, End: &to}, &propIDParsed, loc, auth.RoleHousesitter)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "Cleaners", events[0].Title)

	// The family sees both, so the filter is the role's and not the window's.
	events, err = repo.ListAllEvents(ctx, hid, EventRange{Start: &from, End: &to}, &propIDParsed, loc, auth.RoleOwner)
	require.NoError(t, err)
	assert.Len(t, events, 2)
}

// TestCalendarPolicyAsksThePolicyTable pins the rule to the one policy table:
// the visibility of each kind for each role must equal auth.CanSeeEntity, so a
// change to roles.go cannot leave the calendar routes behind.
func TestCalendarPolicyAsksThePolicyTable(t *testing.T) {
	f := setupRoles(t)
	ctx := context.Background()
	repo := NewRepo(f.pool)
	hid := uuid.MustParse(f.household)

	kinds := map[string]string{"family": "calendar_event", "custom": "calendar_event",
		"properties": "calendar_event", "bills": "bill", "maintenance": "maintenance_task",
		"vehicles": "vehicle"}
	for _, typ := range []string{"family", "custom", "properties", "bills", "maintenance", "vehicles"} {
		name := "kind " + typ
		var calID string
		require.NoError(t, f.pool.QueryRow(ctx,
			`INSERT INTO calendars (household_id, name, type, caldav_uid)
			 VALUES ($1, $2, $3, gen_random_uuid()::text) RETURNING id::text`, f.household, name, typ).Scan(&calID))
		for _, role := range append(auth.AllRoles, "", "something_else") {
			cal, err := repo.GetCalendarByIDForHousehold(ctx, uuid.MustParse(calID), hid, role)
			require.NoError(t, err)
			want := auth.CanSeeEntity(role, kinds[typ])
			assert.Equal(t, want, cal != nil, "calendar type %q visible to role %q", typ, role)
		}
	}
}

func mustDate(t *testing.T, s string) (out time.Time) {
	t.Helper()
	out, err := time.Parse("2006-01-02", s)
	require.NoError(t, err)
	return out
}
