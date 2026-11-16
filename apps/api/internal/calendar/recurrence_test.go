package calendar

// Recurrence tests (#4458). The unit tests run everywhere; the integration
// tests need TEST_DATABASE_URL (all migrations applied, see
// maintenance_sync_test.go) and are skipped otherwise. Run with -p 1.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/icalx"
)

func TestNormalizeRRule(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ""},
		{in: "  FREQ=DAILY ", want: "FREQ=DAILY"},
		{in: "RRULE:FREQ=WEEKLY;BYDAY=TU", want: "FREQ=WEEKLY;BYDAY=TU"},
		{in: "rrule:FREQ=WEEKLY", want: "FREQ=WEEKLY"},
		{in: "FREQ=DAILY\r\nATTENDEE:mailto:x@example.com", wantErr: true},
		{in: "FREQ=DAILY;UNTIL=20261231T000000Z:X", wantErr: true},
		{in: "FREQ=DAILY;" + strings.Repeat("X", maxRRuleLen), wantErr: true},
	}
	for _, tt := range tests {
		got, err := normalizeRRule(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("normalizeRRule(%q) = %q, %v; want %q, err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestApplyToObjectRejectsBadRules(t *testing.T) {
	start := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		rrule string
		want  string
	}{
		{"secondly", "FREQ=SECONDLY", "unsupported recurrence rule"},
		{"unreachable lattice", "FREQ=HOURLY;INTERVAL=6;BYHOUR=1", "unsupported recurrence rule"},
		{"never matches", "FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30", "unsupported recurrence rule"},
		{"malformed", "FREQ=FORTNIGHTLY", "invalid rrule"},
		{"bad until", "FREQ=DAILY;UNTIL=tomorrow", "invalid rrule"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := &Event{Title: "x", Start: start, End: start.Add(time.Hour), RRule: tt.rrule}
			_, _, err := applyToObject(&icalx.Object{UID: "u"}, ev, 0)
			msg, ok := IsInvalidEvent(err)
			if !ok || !strings.Contains(msg, tt.want) {
				t.Fatalf("err = %v; want invalid-event containing %q", err, tt.want)
			}
		})
	}
}

func TestApplyToObjectRoundTrip(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	ev := &Event{
		Title: "Piano, lesson; Tue", Description: "Bring\nbooks", Location: "Studio",
		Start: time.Date(2026, 10, 27, 13, 0, 0, 0, time.UTC), End: time.Date(2026, 10, 27, 14, 0, 0, 0, time.UTC),
		RRule: "FREQ=WEEKLY;BYDAY=TU", TimeZone: "America/New_York",
		ExDates:   []time.Time{time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC)},
		Reminders: []Reminder{{MinutesBefore: 15}}, remindersSet: true,
	}
	raw, rrule, err := applyToObject(&icalx.Object{UID: "u1"}, ev, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rrule == nil || *rrule != "FREQ=WEEKLY;BYDAY=TU" {
		t.Fatalf("rrule = %v", rrule)
	}
	if currentObject(ev, &raw) == nil {
		t.Fatalf("freshly built raw is not current for its own JSON:\n%s", raw)
	}
	obj, _ := icalx.Parse([]byte(raw))
	occ, err := obj.Expand(time.Date(2026, 10, 26, 0, 0, 0, 0, ny), time.Date(2026, 11, 18, 0, 0, 0, 0, ny), ny)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range occ {
		got = append(got, o.Start.In(ny).Format("Jan 2 15:04"))
	}
	want := "Oct 27 09:00,Nov 3 09:00,Nov 17 09:00" // Nov 10 excluded; 09:00 across DST
	if strings.Join(got, ",") != want {
		t.Fatalf("occurrences = %v; want %s", got, want)
	}
	if len(obj.Master.Alarms) != 1 || obj.Master.Alarms[0].Trigger != -15*time.Minute {
		t.Fatalf("alarms = %+v", obj.Master.Alarms)
	}
}

// ── integration ──────────────────────────────────────────────────────────

type recurrenceFixture struct {
	pool     *pgxpool.Pool
	router   http.Handler
	calendar uuid.UUID
	ctx      context.Context
}

func newRecurrenceFixture(t *testing.T, tz string) *recurrenceFixture {
	t.Helper()
	pool := testPool(t)
	hh := mustID(t, pool, `INSERT INTO households (name, timezone) VALUES ('Recurrence '||gen_random_uuid(), $1) RETURNING id`, tz)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, hh)
	})
	cal := addCalendar(t, pool, hh, "Family", "family")

	h := NewHandler(NewRepo(pool))
	r := chi.NewRouter()
	// A family role: since #4807 calendar reads ask role_can_see_entity, and
	// an unknown role sees nothing (fails closed), so the fixture needs a real
	// role from the policy table.
	claims := &auth.Claims{UserID: "local-dev", HouseholdID: hh.String(), Role: auth.RoleOwner}
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(middleware.WithClaims(req.Context(), claims)))
		})
	})
	r.Get("/api/v1/calendars/events", h.ListAllEvents)
	r.Get("/api/v1/calendars/{id}/events", h.ListEvents)
	r.Post("/api/v1/calendars/{id}/events", h.CreateEvent)
	r.Patch("/api/v1/calendars/{id}/events/{eventId}", h.UpdateEvent)
	return &recurrenceFixture{pool: pool, router: r, calendar: cal, ctx: context.Background()}
}

func (f *recurrenceFixture) do(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (f *recurrenceFixture) eventsPath() string {
	return "/api/v1/calendars/" + f.calendar.String() + "/events"
}

func decodeOne(t *testing.T, b []byte) Event {
	t.Helper()
	var out struct{ Data Event }
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return out.Data
}

func decodeList(t *testing.T, b []byte) []Event {
	t.Helper()
	var out struct{ Data []Event }
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return out.Data
}

type storedRow struct {
	icalData string
	icalRaw  *string
	rrule    *string
}

func (f *recurrenceFixture) row(t *testing.T, id string) storedRow {
	t.Helper()
	var r storedRow
	if err := f.pool.QueryRow(f.ctx, `SELECT ical_data, ical_raw, rrule FROM calendar_objects WHERE id = $1`, id).
		Scan(&r.icalData, &r.icalRaw, &r.rrule); err != nil {
		t.Fatalf("load row: %v", err)
	}
	return r
}

func startsUTC(evs []Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Start.UTC().Format("2006-01-02T15:04Z"))
	}
	return out
}

func TestRecurrence_CreateStoresRawAndRangeExpands(t *testing.T) {
	f := newRecurrenceFixture(t, "America/New_York")

	// Tue/Thu 09:00 New York, no explicit timezone: the household's zone is
	// used, so the series stays at 09:00 local across the Nov 1 DST change.
	code, body := f.do(t, http.MethodPost, f.eventsPath(), map[string]any{
		"title": "Piano", "start": "2026-10-27T09:00:00-04:00", "end": "2026-10-27T10:00:00-04:00",
		"rrule": "FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261231T235959Z", "reminders": []map[string]int{{"minutes_before": 15}},
		"exdates": []string{"2026-11-05T09:00:00-05:00"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	created := decodeOne(t, body)
	if created.TimeZone != "America/New_York" || !created.Recurring {
		t.Fatalf("created = %+v", created)
	}

	row := f.row(t, created.ID)
	if row.rrule == nil || *row.rrule != "FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261231T235959Z" {
		t.Fatalf("rrule column = %v", row.rrule)
	}
	if row.icalRaw == nil {
		t.Fatal("ical_raw not stored")
	}
	for _, want := range []string{"RRULE:FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261231T235959Z", "DTSTART;TZID=America/New_York:20261027T090000",
		"EXDATE;TZID=America/New_York:20261105T090000", "BEGIN:VALARM", "TRIGGER:-PT15M", "BEGIN:VTIMEZONE", "STATUS:CONFIRMED"} {
		if !strings.Contains(*row.icalRaw, want) {
			t.Errorf("ical_raw lacks %q:\n%s", want, *row.icalRaw)
		}
	}
	// The JSON holds the series start, as CalDAV PUT stores it.
	if !strings.Contains(row.icalData, `"start":"2026-10-27T13:00:00Z"`) {
		t.Errorf("ical_data = %s", row.icalData)
	}

	// Range listing: Oct 27, Oct 29 (EDT, 13:00Z), Nov 3 (EST, 14:00Z);
	// Nov 5 is excluded.
	q := "?start=2026-10-26T00:00:00Z&end=2026-11-07T00:00:00Z"
	for _, path := range []string{f.eventsPath() + q, "/api/v1/calendars/events" + q} {
		code, body = f.do(t, http.MethodGet, path, nil)
		if code != http.StatusOK {
			t.Fatalf("list %s: %d %s", path, code, body)
		}
		evs := decodeList(t, body)
		got := strings.Join(startsUTC(evs), ",")
		if got != "2026-10-27T13:00Z,2026-10-29T13:00Z,2026-11-03T14:00Z" {
			t.Fatalf("%s occurrences = %s", path, got)
		}
		for _, e := range evs {
			if e.ID != created.ID || e.RecurrenceID == nil || !e.RecurrenceID.Equal(e.Start) || e.End.Sub(e.Start) != time.Hour {
				t.Errorf("occurrence = %+v", e)
			}
			if len(e.Reminders) != 1 || e.Reminders[0].MinutesBefore != 15 {
				t.Errorf("reminders = %+v", e.Reminders)
			}
		}
	}

	// A window entirely after the series start still finds it (the stored
	// ends_at is only the first occurrence's end).
	code, body = f.do(t, http.MethodGet, f.eventsPath()+"?start=2026-12-28T00:00:00Z&end=2027-01-05T00:00:00Z", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	if got := strings.Join(startsUTC(decodeList(t, body)), ","); got != "2026-12-29T14:00Z,2026-12-31T14:00Z" {
		t.Fatalf("late window = %s", got)
	}

	// Without a range the series is returned once, with its rule.
	code, body = f.do(t, http.MethodGet, f.eventsPath(), nil)
	evs := decodeList(t, body)
	if code != http.StatusOK || len(evs) != 1 || evs[0].RRule == "" || evs[0].RecurrenceID != nil {
		t.Fatalf("unranged list: %d %+v", code, evs)
	}
}

func TestRecurrence_SingleEventWritesRawWithoutRRule(t *testing.T) {
	f := newRecurrenceFixture(t, "UTC")
	code, body := f.do(t, http.MethodPost, f.eventsPath(), map[string]any{
		"title": "Dentist", "start": "2026-11-20T15:00:00Z",
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	ev := decodeOne(t, body)
	row := f.row(t, ev.ID)
	if row.rrule != nil || row.icalRaw == nil || strings.Contains(*row.icalRaw, "RRULE") {
		t.Fatalf("row = %+v raw=%v", row, row.icalRaw)
	}
	if !ev.End.Equal(ev.Start.Add(time.Hour)) {
		t.Errorf("default end = %v", ev.End)
	}
	if c := currentObject(&ev, row.icalRaw); c == nil {
		t.Error("stored raw is not current for the stored JSON")
	}

	// Zero-length: stored as one hour, the way CalDAV reads a VEVENT
	// without DTEND, so the raw stays current.
	code, body = f.do(t, http.MethodPost, f.eventsPath(), map[string]any{
		"title": "Call 🎹", "start": "2026-11-20T15:00:00Z", "end": "2026-11-20T15:00:00Z",
	})
	if code != http.StatusCreated {
		t.Fatalf("create zero-length: %d %s", code, body)
	}
	zero := decodeOne(t, body)
	if zr := f.row(t, zero.ID); !zero.End.Equal(zero.Start.Add(time.Hour)) || currentObject(&zero, zr.icalRaw) == nil {
		t.Errorf("zero-length event: end=%v raw current=%v", zero.End, currentObject(&zero, zr.icalRaw) != nil)
	}
	if code, body := f.do(t, http.MethodPost, f.eventsPath(), map[string]any{
		"title": "Backwards", "start": "2026-11-20T15:00:00Z", "end": "2026-11-20T14:00:00Z",
	}); code != http.StatusBadRequest {
		t.Errorf("end before start: %d %s", code, body)
	}
	code, body = f.do(t, http.MethodGet, f.eventsPath()+"?start=2026-11-20T00:00:00Z&end=2026-11-21T00:00:00Z", nil)
	if evs := decodeList(t, body); code != http.StatusOK || len(evs) != 2 || evs[0].Recurring || evs[0].RecurrenceID != nil {
		t.Fatalf("list: %d %s", code, body)
	}
}

func TestRecurrence_AllDaySeriesInHouseholdZone(t *testing.T) {
	f := newRecurrenceFixture(t, "Pacific/Auckland")
	code, body := f.do(t, http.MethodPost, f.eventsPath(), map[string]any{
		"title": "Bins", "start": "2026-11-02T00:00:00Z", "all_day": true, "rrule": "FREQ=WEEKLY;COUNT=3",
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	row := f.row(t, decodeOne(t, body).ID)
	if !strings.Contains(*row.icalRaw, "DTSTART;VALUE=DATE:20261102") {
		t.Fatalf("raw:\n%s", *row.icalRaw)
	}
	code, body = f.do(t, http.MethodGet, f.eventsPath()+"?start=2026-10-30T00:00:00Z&end=2026-12-30T00:00:00Z", nil)
	evs := decodeList(t, body)
	if code != http.StatusOK || strings.Join(startsUTC(evs), ",") != "2026-11-02T00:00Z,2026-11-09T00:00Z,2026-11-16T00:00Z" {
		t.Fatalf("all-day occurrences: %d %v", code, startsUTC(evs))
	}
	for _, e := range evs {
		if !e.AllDay || e.End.Sub(e.Start) != 24*time.Hour {
			t.Errorf("occurrence = %+v", e)
		}
	}
}

func TestRecurrence_RejectsUnsupportedRules(t *testing.T) {
	f := newRecurrenceFixture(t, "UTC")
	for _, rule := range []string{"FREQ=SECONDLY", "FREQ=HOURLY;INTERVAL=6;BYHOUR=1", "FREQ=FORTNIGHTLY"} {
		code, body := f.do(t, http.MethodPost, f.eventsPath(), map[string]any{
			"title": "Bad", "start": "2026-11-02T00:00:00Z", "rrule": rule,
		})
		if code != http.StatusBadRequest || !strings.Contains(string(body), "rule") {
			t.Errorf("rrule %q: %d %s; want 400", rule, code, body)
		}
	}
	for _, bad := range []map[string]any{
		{"title": "Bad tz", "start": "2026-11-02T09:00:00Z", "rrule": "FREQ=DAILY", "timezone": "Mars/Olympus"},
		{"title": "Bad reminder", "start": "2026-11-02T09:00:00Z", "reminders": []map[string]int{{"minutes_before": -5}}},
	} {
		if code, body := f.do(t, http.MethodPost, f.eventsPath(), bad); code != http.StatusBadRequest {
			t.Errorf("%v: %d %s; want 400", bad, code, body)
		}
	}
	if n := countOf(t, f.pool, `SELECT count(*) FROM calendar_objects WHERE calendar_id = $1`, f.calendar); n != 0 {
		t.Fatalf("rejected creates stored %d rows", n)
	}

	// Updates are validated the same way and leave the row untouched.
	code, body := f.do(t, http.MethodPost, f.eventsPath(), map[string]any{"title": "Ok", "start": "2026-11-02T09:00:00Z", "rrule": "FREQ=DAILY"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	ev := decodeOne(t, body)
	before := f.row(t, ev.ID)
	code, body = f.do(t, http.MethodPatch, f.eventsPath()+"/"+ev.ID, map[string]any{"rrule": "FREQ=SECONDLY"})
	if code != http.StatusBadRequest {
		t.Fatalf("patch: %d %s; want 400", code, body)
	}
	if after := f.row(t, ev.ID); *after.rrule != *before.rrule || *after.icalRaw != *before.icalRaw {
		t.Fatal("rejected patch changed the row")
	}
}

func TestRecurrence_RangeValidation(t *testing.T) {
	f := newRecurrenceFixture(t, "UTC")
	for _, q := range []string{
		"?start=yesterday",
		"?start=2026-11-02T00:00:00Z&end=2026-11-01T00:00:00Z",
		"?start=2020-01-01T00:00:00Z&end=2030-01-01T00:00:00Z",
	} {
		if code, body := f.do(t, http.MethodGet, f.eventsPath()+q, nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s; want 400", q, code, body)
		}
	}
}

func TestRecurrence_PatchKeepsRawConsistent(t *testing.T) {
	f := newRecurrenceFixture(t, "UTC")
	code, body := f.do(t, http.MethodPost, f.eventsPath(), map[string]any{
		"title": "Standup", "start": "2026-11-02T09:00:00Z", "end": "2026-11-02T09:15:00Z", "rrule": "FREQ=DAILY;COUNT=5",
		"reminders": []map[string]int{{"minutes_before": 5}},
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	ev := decodeOne(t, body)
	path := f.eventsPath() + "/" + ev.ID

	// Title only: rule and reminders survive, JSON and raw agree.
	code, body = f.do(t, http.MethodPatch, path, map[string]any{"title": "Team standup"})
	if code != http.StatusOK {
		t.Fatalf("patch: %d %s", code, body)
	}
	got := decodeOne(t, body)
	if got.Title != "Team standup" || got.RRule != "FREQ=DAILY;COUNT=5" || len(got.Reminders) != 1 {
		t.Fatalf("patched = %+v", got)
	}
	row := f.row(t, ev.ID)
	if !strings.Contains(*row.icalRaw, "SUMMARY:Team standup") || !strings.Contains(*row.icalRaw, "SEQUENCE:1") ||
		!strings.Contains(row.icalData, `"title":"Team standup"`) || !strings.Contains(row.icalData, `"sequence":1`) {
		t.Fatalf("row after title patch: %s\n%s", row.icalData, *row.icalRaw)
	}
	if n := countOf(t, f.pool, `SELECT count(*) FROM calendar_changes WHERE event_uid = $1 AND action = 'update'`, ev.ID); n != 1 {
		t.Errorf("update change rows = %d", n)
	}

	// Change the rule; the listing follows.
	code, body = f.do(t, http.MethodPatch, path, map[string]any{"rrule": "FREQ=WEEKLY;COUNT=2", "reminders": []any{}})
	if code != http.StatusOK {
		t.Fatalf("patch rule: %d %s", code, body)
	}
	row = f.row(t, ev.ID)
	if *row.rrule != "FREQ=WEEKLY;COUNT=2" || strings.Contains(*row.icalRaw, "VALARM") {
		t.Fatalf("row after rule patch: rrule=%v\n%s", *row.rrule, *row.icalRaw)
	}
	code, body = f.do(t, http.MethodGet, f.eventsPath()+"?start=2026-11-01T00:00:00Z&end=2026-12-01T00:00:00Z", nil)
	if got := strings.Join(startsUTC(decodeList(t, body)), ","); code != http.StatusOK || got != "2026-11-02T09:00Z,2026-11-09T09:00Z" {
		t.Fatalf("after rule patch: %d %s", code, got)
	}

	// Remove the rule: a single event again, rrule column cleared.
	code, body = f.do(t, http.MethodPatch, path, map[string]any{"rrule": ""})
	if code != http.StatusOK {
		t.Fatalf("patch clear: %d %s", code, body)
	}
	row = f.row(t, ev.ID)
	if row.rrule != nil || strings.Contains(*row.icalRaw, "RRULE") {
		t.Fatalf("row after clearing: %v\n%s", row.rrule, *row.icalRaw)
	}

	// Another calendar's id or an unknown event is 404.
	if code, _ := f.do(t, http.MethodPatch, f.eventsPath()+"/"+uuid.NewString(), map[string]any{"title": "x"}); code != http.StatusNotFound {
		t.Errorf("unknown event: %d", code)
	}
	if code, _ := f.do(t, http.MethodPatch, "/api/v1/calendars/"+uuid.NewString()+"/events/"+ev.ID, map[string]any{"title": "x"}); code != http.StatusNotFound {
		t.Errorf("foreign calendar: %d", code)
	}
}

// phoneRaw is a series as an iPhone might PUT it: a VTIMEZONE, an ATTENDEE,
// an EXDATE and one moved instance (RECURRENCE-ID override).
const phoneRaw = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Apple Inc.//iOS 18//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:phone-series-1\r\nDTSTAMP:20261101T000000Z\r\nSUMMARY:Swim\r\n" +
	"DTSTART:20261102T170000Z\r\nDTEND:20261102T180000Z\r\nRRULE:FREQ=DAILY;COUNT=5\r\n" +
	"EXDATE:20261104T170000Z\r\nATTENDEE;CN=Coach:mailto:coach@example.com\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:phone-series-1\r\nDTSTAMP:20261101T000000Z\r\nSUMMARY:Swim (late)\r\n" +
	"RECURRENCE-ID:20261103T170000Z\r\nDTSTART:20261103T190000Z\r\nDTEND:20261103T200000Z\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func TestRecurrence_ClientRawOverridesAndStaleness(t *testing.T) {
	f := newRecurrenceFixture(t, "UTC")
	id := uuid.NewString()
	jsonData := `{"title":"Swim","description":"","start":"2026-11-02T17:00:00Z","end":"2026-11-02T18:00:00Z","all_day":false,"location":"","color":"","event_type":"custom","sequence":0}`
	if _, err := f.pool.Exec(f.ctx,
		`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, etag, event_type, ical_raw, rrule)
		 VALUES ($1, $2, 'phone-series-1', $3, 'e', 'custom', $4, 'FREQ=DAILY;COUNT=5')`,
		id, f.calendar, jsonData, phoneRaw); err != nil {
		t.Fatal(err)
	}

	list := func() []Event {
		code, body := f.do(t, http.MethodGet, f.eventsPath()+"?start=2026-11-01T00:00:00Z&end=2026-11-10T00:00:00Z", nil)
		if code != http.StatusOK {
			t.Fatalf("list: %d %s", code, body)
		}
		return decodeList(t, body)
	}

	// Nov 2, 3 (moved to 19:00, override title), 5, 6; Nov 4 excluded.
	evs := list()
	if got := strings.Join(startsUTC(evs), ","); got != "2026-11-02T17:00Z,2026-11-03T19:00Z,2026-11-05T17:00Z,2026-11-06T17:00Z" {
		t.Fatalf("occurrences = %s", got)
	}
	if evs[1].Title != "Swim (late)" || !evs[1].RecurrenceID.Equal(time.Date(2026, 11, 3, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("override occurrence = %+v", evs[1])
	}

	// An API edit keeps the phone's override, EXDATE and ATTENDEE.
	code, body := f.do(t, http.MethodPatch, f.eventsPath()+"/"+id, map[string]any{"location": "Pool"})
	if code != http.StatusOK {
		t.Fatalf("patch: %d %s", code, body)
	}
	row := f.row(t, id)
	for _, want := range []string{"LOCATION:Pool", "RECURRENCE-ID:20261103T170000Z", "SUMMARY:Swim (late)", "EXDATE:20261104T170000Z", "ATTENDEE;CN=Coach:mailto:coach@example.com", "UID:phone-series-1"} {
		if !strings.Contains(*row.icalRaw, want) {
			t.Errorf("raw after patch lacks %q:\n%s", want, *row.icalRaw)
		}
	}
	if evs := list(); len(evs) != 4 || evs[0].Location != "Pool" {
		t.Fatalf("after patch: %+v", evs)
	}

	// A JSON-only writer (MCP) changes the title without touching the raw:
	// the raw is stale, CalDAV generates from the JSON, and so does the API
	// (a single event, no recurrence).
	if _, err := f.pool.Exec(f.ctx, `UPDATE calendar_objects SET ical_data = jsonb_set(ical_data::jsonb, '{title}', '"Gym"')::text WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	evs = list()
	if len(evs) != 1 || evs[0].Title != "Gym" || evs[0].Recurring || evs[0].RecurrenceID != nil {
		t.Fatalf("stale raw listing = %+v", evs)
	}
}

func TestMaintenanceSync_UpdateClearsRaw(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	repo := NewRepo(pool)
	hh, task, due := seedHousehold(t, pool)
	addCalendar(t, pool, hh, "Maintenance", "maintenance")
	if err := repo.UpsertMaintenanceEvent(ctx, hh, task, "Filter", "", due); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE calendar_objects SET ical_raw = 'BEGIN:VCALENDAR', rrule = 'FREQ=DAILY' WHERE entity_id = $1`, task); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertMaintenanceEvent(ctx, hh, task, "Filter (new)", "", due); err != nil {
		t.Fatal(err)
	}
	if n := countOf(t, pool, `SELECT count(*) FROM calendar_objects WHERE entity_id = $1 AND ical_raw IS NULL AND rrule IS NULL`, task); n != 1 {
		t.Fatalf("raw not cleared on maintenance update")
	}
}

// Create-event accepts only a registered entity kind of the caller's family
// (migration 070, #4708); a legacy 'maintenance' or another family's record
// is refused and nothing is stored.
func TestCreateEvent_EntityReference(t *testing.T) {
	f := newRecurrenceFixture(t, "UTC")
	var hh uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT household_id FROM calendars WHERE id = $1`, f.calendar).Scan(&hh); err != nil {
		t.Fatal(err)
	}
	other := mustID(t, f.pool, `INSERT INTO households (name) VALUES ('Other '||gen_random_uuid()) RETURNING id`)
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM households WHERE id = $1`, other) })
	ownPet := mustID(t, f.pool, `INSERT INTO pets (household_id, name) VALUES ($1, 'Rex') RETURNING id`, hh)
	otherPet := mustID(t, f.pool, `INSERT INTO pets (household_id, name) VALUES ($1, 'Fido') RETURNING id`, other)

	ev := func(kind, id string) map[string]any {
		return map[string]any{"title": "Vet", "start": "2026-11-02T09:00:00Z", "entity_type": kind, "entity_id": id}
	}
	for _, tc := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"legacy maintenance", ev("maintenance", uuid.NewString()), http.StatusBadRequest},
		{"unknown kind", ev("calendar_alarm", uuid.NewString()), http.StatusBadRequest},
		{"type without id", ev("pet", ""), http.StatusBadRequest},
		{"id not a uuid", ev("pet", "rex"), http.StatusBadRequest},
		{"other family's pet", ev("pet", otherPet.String()), http.StatusNotFound},
		{"missing pet", ev("pet", uuid.NewString()), http.StatusNotFound},
	} {
		if code, body := f.do(t, http.MethodPost, f.eventsPath(), tc.body); code != tc.want {
			t.Errorf("%s: %d %s; want %d", tc.name, code, body, tc.want)
		}
	}
	if n := countOf(t, f.pool, `SELECT count(*) FROM calendar_objects WHERE calendar_id = $1`, f.calendar); n != 0 {
		t.Fatalf("rejected creates stored %d rows", n)
	}
	code, body := f.do(t, http.MethodPost, f.eventsPath(), ev("pet", ownPet.String()))
	if code != http.StatusCreated {
		t.Fatalf("own pet: %d %s", code, body)
	}
	if got := decodeOne(t, body); got.EntityType != "pet" || got.EntityID != ownPet.String() {
		t.Errorf("own pet: stored (%s, %s)", got.EntityType, got.EntityID)
	}
}
