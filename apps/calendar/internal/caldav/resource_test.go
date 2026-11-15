package caldav

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"home-os/calendar/internal/auth"
	"home-os/calendar/internal/db"
	"home-os/calendar/internal/ical"
)

// TestNormalizeETag covers the quote/weak-prefix stripping used to compare
// client-supplied If-Match values against the bare hex ETags the calendar
// service stores in the database.
func TestNormalizeETag(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare hex", "1718467200000000000", "1718467200000000000"},
		{"quoted strong", `"1718467200000000000"`, "1718467200000000000"},
		{"quoted weak", `W/"1718467200000000000"`, "1718467200000000000"},
		{"surrounding whitespace", `  "1718467200000000000"  `, "1718467200000000000"},
		{"weak with whitespace", `  W/"1718467200000000000"  `, "1718467200000000000"},
		{"empty string", "", ""},
		{"only quotes", `""`, ""},
		{"interior quotes preserved", `"ab"cd"`, `ab"cd`},
		{"single quote not stripped", `"abc`, `"abc`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeETag(tc.in); got != tc.want {
				t.Errorf("normalizeETag(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCheckPUTPreconditions covers the If-Match / If-None-Match decision
// matrix. The function is pure (no DB) so we can exercise every branch.
func TestCheckPUTPreconditions(t *testing.T) {
	const currentETag = "1718467200000000000"

	cases := []struct {
		name        string
		ifMatch     string
		ifNoneMatch string
		currentETag string
		exists      bool
		want        putPreconditionResult
	}{
		// No preconditions: always pass (the "no regression" criterion).
		{
			name:        "no headers, missing resource",
			ifMatch:     "",
			ifNoneMatch: "",
			currentETag: "",
			exists:      false,
			want:        preconditionPass,
		},
		{
			name:        "no headers, existing resource",
			ifMatch:     "",
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},

		// If-None-Match: * — create-only guard.
		{
			name:        "If-None-Match:* on missing resource passes (create allowed)",
			ifMatch:     "",
			ifNoneMatch: "*",
			currentETag: "",
			exists:      false,
			want:        preconditionPass,
		},
		{
			name:        "If-None-Match:* on existing resource fails (exists)",
			ifMatch:     "",
			ifNoneMatch: "*",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionFailExists,
		},

		// If-Match: * — matches any existing resource.
		{
			name:        "If-Match:* on existing resource passes",
			ifMatch:     "*",
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},
		{
			name:        "If-Match:* on missing resource fails (missing)",
			ifMatch:     "*",
			ifNoneMatch: "",
			currentETag: "",
			exists:      false,
			want:        preconditionFailMissing,
		},

		// If-Match with a specific ETag — strong comparison.
		{
			name:        "If-Match quoted ETag matches current",
			ifMatch:     `"1718467200000000000"`,
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},
		{
			name:        "If-Match bare ETag matches current",
			ifMatch:     "1718467200000000000",
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},
		{
			name:        "If-Match weak ETag matches current (normalized)",
			ifMatch:     `W/"1718467200000000000"`,
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},
		{
			name:        "If-Match ETag mismatch fails",
			ifMatch:     `"deadbeef"`,
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionFailMismatch,
		},
		{
			name:        "If-Match on missing resource fails (missing)",
			ifMatch:     `"1718467200000000000"`,
			ifNoneMatch: "",
			currentETag: "",
			exists:      false,
			want:        preconditionFailMissing,
		},

		// Comma-separated If-Match list (RFC 7232 allows multiple ETags).
		{
			name:        "If-Match list with one matching ETag passes",
			ifMatch:     `"deadbeef", "1718467200000000000"`,
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},
		{
			name:        "If-Match list with no matching ETag fails",
			ifMatch:     `"deadbeef", "cafef00d"`,
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionFailMismatch,
		},

		// If-None-Match:* is evaluated before If-Match. If the resource
		// exists, If-None-Match:* short-circuits to preconditionFailExists
		// regardless of what If-Match says.
		{
			name:        "If-None-Match:* takes precedence over If-Match when resource exists",
			ifMatch:     `"1718467200000000000"`,
			ifNoneMatch: "*",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionFailExists,
		},

		// Non-wildcard If-None-Match on a PUT is not meaningful for CalDAV
		// optimistic concurrency and is ignored — the PUT proceeds.
		{
			name:        "non-wildcard If-None-Match ignored",
			ifMatch:     "",
			ifNoneMatch: `"some-other-etag"`,
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},

		// Whitespace-only headers are treated as absent.
		{
			name:        "whitespace-only If-Match treated as absent",
			ifMatch:     "   ",
			ifNoneMatch: "",
			currentETag: currentETag,
			exists:      true,
			want:        preconditionPass,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkPUTPreconditions(tc.ifMatch, tc.ifNoneMatch, tc.currentETag, tc.exists)
			if got != tc.want {
				t.Errorf("checkPUTPreconditions(ifMatch=%q, ifNoneMatch=%q, currentETag=%q, exists=%v) = %d, want %d",
					tc.ifMatch, tc.ifNoneMatch, tc.currentETag, tc.exists, got, tc.want)
			}
		})
	}
}

// rawFixture is an Apple-style VCALENDAR with the properties the JSON
// ical_data drops: a VTIMEZONE (whose STANDARD/DAYLIGHT blocks carry their
// own RRULEs), a weekly RRULE, two EXDATEs, a VALARM, an X- property and a
// folded DESCRIPTION line.
const rawFixture = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//Apple Inc.//macOS 15.1//EN\r\n" +
	"CALSCALE:GREGORIAN\r\n" +
	"BEGIN:VTIMEZONE\r\n" +
	"TZID:America/Chicago\r\n" +
	"BEGIN:DAYLIGHT\r\n" +
	"TZOFFSETFROM:-0600\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU\r\n" +
	"DTSTART:20070311T020000\r\n" +
	"TZNAME:CDT\r\n" +
	"TZOFFSETTO:-0500\r\n" +
	"END:DAYLIGHT\r\n" +
	"BEGIN:STANDARD\r\n" +
	"TZOFFSETFROM:-0500\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU\r\n" +
	"DTSTART:20071104T020000\r\n" +
	"TZNAME:CST\r\n" +
	"TZOFFSETTO:-0600\r\n" +
	"END:STANDARD\r\n" +
	"END:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:evt-roundtrip\r\n" +
	"DTSTAMP:20261101T120000Z\r\n" +
	"SEQUENCE:3\r\n" +
	"SUMMARY:Piano lesson\r\n" +
	"DESCRIPTION:Bring the yellow book and the metronome\\, and practise the \r\n" +
	" scales on page twelve before Tuesday.\r\n" +
	"LOCATION:Music room\r\n" +
	"DTSTART;TZID=America/Chicago:20261103T160000\r\n" +
	"DTEND;TZID=America/Chicago:20261103T170000\r\n" +
	"RRULE:FREQ=WEEKLY;BYDAY=TU;UNTIL=20270601T050000Z\r\n" +
	"EXDATE;TZID=America/Chicago:20261124T160000,20261229T160000\r\n" +
	"X-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC\r\n" +
	"BEGIN:VALARM\r\n" +
	"ACTION:DISPLAY\r\n" +
	"TRIGGER:-PT15M\r\n" +
	"END:VALARM\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

const rawFixtureRRULE = "FREQ=WEEKLY;BYDAY=TU;UNTIL=20270601T050000Z"

// unfold undoes RFC 5545 line folding so two iCalendar texts can be compared
// "modulo line folding".
func unfold(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n ", "")
	return strings.ReplaceAll(s, "\n\t", "")
}

// storedFromPUT builds the calendar object HandlePUT would store for body:
// the JSON from ical.ParseEvent plus ical_raw/rrule from rawForStorage.
func storedFromPUT(t *testing.T, uid, body string) *db.CalendarObject {
	t.Helper()
	ev, err := ical.ParseEvent(body)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	ev.UID = uid
	jsonData, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw, rrule := rawForStorage([]byte(body))
	return &db.CalendarObject{
		UID:       uid,
		ICALData:  string(jsonData),
		ETag:      "etag-1",
		ICALRaw:   raw,
		RRule:     rrule,
		UpdatedAt: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC),
	}
}

func TestRenderObjectICal(t *testing.T) {
	t.Run("raw round trip keeps RRULE EXDATE VALARM", func(t *testing.T) {
		obj := storedFromPUT(t, "evt-roundtrip", rawFixture)
		got, err := renderObjectICal(obj)
		if err != nil {
			t.Fatalf("renderObjectICal: %v", err)
		}
		if got != rawFixture {
			t.Fatalf("rendered text differs from the PUT body:\n got: %q\nwant: %q", got, rawFixture)
		}
		if unfold(got) != unfold(rawFixture) {
			t.Fatalf("rendered text differs modulo folding")
		}
		if obj.RRule == nil || *obj.RRule != rawFixtureRRULE {
			t.Fatalf("rrule = %v, want %q", obj.RRule, rawFixtureRRULE)
		}
	})

	t.Run("NULL ical_raw falls back to generated", func(t *testing.T) {
		obj := storedFromPUT(t, "evt-legacy", rawFixture)
		obj.ICALRaw = nil
		got, err := renderObjectICal(obj)
		if err != nil {
			t.Fatalf("renderObjectICal: %v", err)
		}
		for _, want := range []string{"BEGIN:VCALENDAR", "UID:evt-legacy", "SUMMARY:Piano lesson", "DTSTAMP:20261102T090000Z", "END:VCALENDAR"} {
			if !strings.Contains(got, want) {
				t.Errorf("generated text missing %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "RRULE") {
			t.Errorf("generated text unexpectedly carries RRULE:\n%s", got)
		}
	})

	t.Run("empty ical_raw falls back to generated", func(t *testing.T) {
		obj := storedFromPUT(t, "evt-empty", rawFixture)
		empty := ""
		obj.ICALRaw = &empty
		got, err := renderObjectICal(obj)
		if err != nil {
			t.Fatalf("renderObjectICal: %v", err)
		}
		if !strings.Contains(got, "UID:evt-empty") || strings.Contains(got, "X-APPLE-TRAVEL-ADVISORY-BEHAVIOR") {
			t.Errorf("expected generated text, got:\n%s", got)
		}
	})

	t.Run("stale ical_raw after a JSON-only edit falls back to generated", func(t *testing.T) {
		obj := storedFromPUT(t, "evt-stale", rawFixture)
		var ev ical.CalEvent
		if err := json.Unmarshal([]byte(obj.ICALData), &ev); err != nil {
			t.Fatal(err)
		}
		ev.Title = "Piano lesson (moved)"
		b, _ := json.Marshal(&ev)
		obj.ICALData = string(b)

		got, err := renderObjectICal(obj)
		if err != nil {
			t.Fatalf("renderObjectICal: %v", err)
		}
		if !strings.Contains(got, "SUMMARY:Piano lesson (moved)") {
			t.Errorf("expected the edited title, got:\n%s", got)
		}
		if strings.Contains(got, "X-APPLE-TRAVEL-ADVISORY-BEHAVIOR") {
			t.Errorf("stale raw text was served:\n%s", got)
		}
	})

	t.Run("sequence bump alone keeps raw", func(t *testing.T) {
		obj := storedFromPUT(t, "evt-seq", rawFixture)
		var ev ical.CalEvent
		_ = json.Unmarshal([]byte(obj.ICALData), &ev)
		ev.Sequence = 7
		b, _ := json.Marshal(&ev)
		obj.ICALData = string(b)
		got, err := renderObjectICal(obj)
		if err != nil {
			t.Fatalf("renderObjectICal: %v", err)
		}
		if got != rawFixture {
			t.Errorf("raw not served after a sequence-only change")
		}
	})

	t.Run("unparseable JSON is an error", func(t *testing.T) {
		raw := rawFixture
		obj := &db.CalendarObject{UID: "x", ICALData: "{not json", ICALRaw: &raw}
		if _, err := renderObjectICal(obj); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestExtractRRULE(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"fixture skips VTIMEZONE rules", rawFixture, rawFixtureRRULE},
		{"none", "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:20260101T000000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n", ""},
		{"lower case and LF only", "begin:vcalendar\nbegin:vevent\nrrule:FREQ=DAILY;COUNT=3\nend:vevent\nend:vcalendar\n", "FREQ=DAILY;COUNT=3"},
		{"folded", "BEGIN:VEVENT\r\nRRULE:FREQ=MONTHLY;BYMONTHDAY=1\r\n ;COUNT=12\r\nEND:VEVENT\r\n", "FREQ=MONTHLY;BYMONTHDAY=1;COUNT=12"},
		{"outside VEVENT ignored", "BEGIN:VCALENDAR\r\nRRULE:FREQ=DAILY\r\nEND:VCALENDAR\r\n", ""},
		{"RRULEX not matched", "BEGIN:VEVENT\r\nRRULEX:FREQ=DAILY\r\nEND:VEVENT\r\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractRRULE(tc.in); got != tc.want {
				t.Errorf("extractRRULE = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRawForStorage(t *testing.T) {
	if raw, rrule := rawForStorage([]byte(rawFixture)); raw == nil || *raw != rawFixture || rrule == nil || *rrule != rawFixtureRRULE {
		t.Errorf("fixture: raw=%v rrule=%v", raw != nil, rrule)
	}
	if raw, rrule := rawForStorage([]byte("BEGIN:VEVENT\r\nDTSTART:20260101T000000Z\r\nEND:VEVENT\r\n")); raw == nil || rrule != nil {
		t.Errorf("no RRULE: raw=%v rrule=%v", raw != nil, rrule)
	}
	for name, body := range map[string][]byte{
		"empty":        nil,
		"invalid utf8": []byte("BEGIN:VEVENT\r\nSUMMARY:\xff\xfe\r\nEND:VEVENT\r\n"),
		"NUL byte":     []byte("BEGIN:VEVENT\r\nSUMMARY:a\x00b\r\nEND:VEVENT\r\n"),
	} {
		if raw, rrule := rawForStorage(body); raw != nil || rrule != nil {
			t.Errorf("%s: expected nothing stored", name)
		}
	}
}

func TestWriteReportResponseSplitsCDATAEnd(t *testing.T) {
	var b strings.Builder
	writeReportResponse(&b, "cal", "evt", "e", "DESCRIPTION:a]]>b\r\n")
	out := b.String()
	if !strings.Contains(out, "<![CDATA[DESCRIPTION:a]]]]><![CDATA[>b\r\n]]>") {
		t.Errorf("CDATA terminator not split:\n%s", out)
	}
}

// ----- Integration: real Postgres via testcontainers -----

var (
	pgOnce sync.Once
	pgURL  string
	pgErr  error
)

// testRepo returns a db.Repo on a Postgres 16 container (random host port)
// with every packages/db migration applied. One container per test binary.
// Needs Docker; skipped with -short.
func testRepo(t *testing.T) (*db.Repo, *pgxpool.Pool) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test needs Docker; skipped with -short")
	}
	pgOnce.Do(func() { pgURL, pgErr = startPostgres() })
	if pgErr != nil {
		t.Fatalf("postgres: %v", pgErr)
	}
	ctx := context.Background()
	repo, err := db.New(ctx, pgURL)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	pool, err := pgxpool.New(ctx, pgURL)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(func() { repo.Close(); pool.Close() })
	return repo, pool
}

func startPostgres() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "packages", "db", "migrations")
	files, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil || len(files) == 0 {
		return "", fmt.Errorf("no migrations in %s (err=%v)", dir, err)
	}
	sort.Strings(files)

	c, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("homeos"),
		tcpostgres.WithUsername("homeos"),
		tcpostgres.WithPassword("homeos"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		return "", fmt.Errorf("start container (is Docker running?): %w", err)
	}
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return "", err
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	// Migrations GRANT to the application role, which a fresh database lacks.
	if _, err := conn.Exec(ctx, `DO $$ BEGIN
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app') THEN CREATE ROLE app; END IF;
	END $$`); err != nil {
		return "", fmt.Errorf("create role app: %w", err)
	}
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			return "", fmt.Errorf("apply %s: %w", filepath.Base(f), err)
		}
	}
	return url, nil
}

// seedCalendar inserts a household and a calendar and returns the household
// ID and the calendar's CalDAV UID.
func seedCalendar(t *testing.T, pool *pgxpool.Pool) (householdID, calUID string, calID string) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `INSERT INTO households (name) VALUES ('Test') RETURNING id::text`).Scan(&householdID); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	calUID = "cal-" + householdID[:8]
	if err := pool.QueryRow(ctx,
		`INSERT INTO calendars (household_id, name, type, caldav_uid) VALUES ($1, 'Family', 'family', $2) RETURNING id::text`,
		householdID, calUID).Scan(&calID); err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	return householdID, calUID, calID
}

func davRequest(method, path, householdID, body string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r.WithContext(context.WithValue(r.Context(), auth.HouseholdIDKey, householdID))
}

func TestCalDAVRawRoundTripIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	ctx := context.Background()
	hid, calUID, calID := seedCalendar(t, pool)
	path := "/dav/" + calUID + "/evt-roundtrip.ics"

	// PUT the client body.
	w := httptest.NewRecorder()
	HandlePUT(w, davRequest(http.MethodPut, path, hid, rawFixture, nil), repo, path)
	if w.Code != http.StatusCreated {
		t.Fatalf("PUT status = %d, body %s", w.Code, w.Body)
	}
	putETag := w.Header().Get("ETag")

	// ical_raw and rrule are stored.
	var storedRaw, storedRRule *string
	if err := pool.QueryRow(ctx, `SELECT ical_raw, rrule FROM calendar_objects WHERE calendar_id = $1 AND uid = 'evt-roundtrip'`, calID).
		Scan(&storedRaw, &storedRRule); err != nil {
		t.Fatal(err)
	}
	if storedRaw == nil || *storedRaw != rawFixture {
		t.Errorf("ical_raw not stored verbatim")
	}
	if storedRRule == nil || *storedRRule != rawFixtureRRULE {
		t.Errorf("rrule = %v, want %q", storedRRule, rawFixtureRRULE)
	}

	// GET returns it byte for byte.
	w = httptest.NewRecorder()
	HandleGET(w, davRequest(http.MethodGet, path, hid, "", nil), repo, path)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d", w.Code)
	}
	if got := w.Body.String(); got != rawFixture || unfold(got) != unfold(rawFixture) {
		t.Fatalf("GET body differs from PUT body:\n got: %q\nwant: %q", got, rawFixture)
	}
	if w.Header().Get("ETag") != putETag {
		t.Errorf("GET ETag %q != PUT ETag %q", w.Header().Get("ETag"), putETag)
	}

	// REPORT calendar-multiget and calendar-query serve it too.
	for name, body := range map[string]string{
		"multiget": `<?xml version="1.0"?><C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:getetag/><C:calendar-data/></D:prop><D:href>` + path + `</D:href></C:calendar-multiget>`,
		"query":    `<?xml version="1.0"?><C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:calendar-data/></D:prop><C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"/></C:comp-filter></C:filter></C:calendar-query>`,
		"sync":     `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:sync-token/><D:prop><C:calendar-data/></D:prop></D:sync-collection>`,
	} {
		w = httptest.NewRecorder()
		colPath := "/dav/" + calUID + "/"
		HandleREPORT(w, davRequest("REPORT", colPath, hid, body, nil), repo, colPath)
		if w.Code != http.StatusMultiStatus {
			t.Fatalf("%s REPORT status = %d: %s", name, w.Code, w.Body)
		}
		if !strings.Contains(w.Body.String(), "<![CDATA["+rawFixture+"]]>") {
			t.Errorf("%s REPORT does not carry the raw VCALENDAR:\n%s", name, w.Body)
		}
	}

	// An update PUT replaces the raw text.
	updated := strings.Replace(rawFixture, "TRIGGER:-PT15M", "TRIGGER:-PT30M", 1)
	w = httptest.NewRecorder()
	HandlePUT(w, davRequest(http.MethodPut, path, hid, updated, map[string]string{"If-Match": putETag}), repo, path)
	if w.Code != http.StatusOK {
		t.Fatalf("update PUT status = %d: %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	HandleGET(w, davRequest(http.MethodGet, path, hid, "", nil), repo, path)
	if w.Body.String() != updated {
		t.Fatalf("GET after update did not return the new body")
	}

	// A JSON-only edit (what the API and MCP tools do) must win over the
	// now-stale raw text.
	var jsonData string
	if err := pool.QueryRow(ctx, `SELECT ical_data FROM calendar_objects WHERE calendar_id = $1 AND uid = 'evt-roundtrip'`, calID).Scan(&jsonData); err != nil {
		t.Fatal(err)
	}
	var ev ical.CalEvent
	if err := json.Unmarshal([]byte(jsonData), &ev); err != nil {
		t.Fatal(err)
	}
	ev.Title = "Piano lesson (API edit)"
	b, _ := json.Marshal(&ev)
	if _, err := pool.Exec(ctx, `UPDATE calendar_objects SET ical_data = $1, etag = 'api-etag', updated_at = NOW() WHERE calendar_id = $2 AND uid = 'evt-roundtrip'`, string(b), calID); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	HandleGET(w, davRequest(http.MethodGet, path, hid, "", nil), repo, path)
	if !strings.Contains(w.Body.String(), "SUMMARY:Piano lesson (API edit)") {
		t.Errorf("GET after JSON-only edit served stale raw:\n%s", w.Body)
	}
}

func TestCalDAVNullRawFallbackIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	ctx := context.Background()
	hid, calUID, calID := seedCalendar(t, pool)

	// A row as written before migration 036 (or by the API): JSON only.
	ev := ical.CalEvent{
		Title: "Bin day",
		Start: time.Date(2026, 11, 20, 7, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 11, 20, 7, 30, 0, 0, time.UTC),
	}
	b, _ := json.Marshal(&ev)
	if _, err := pool.Exec(ctx,
		`INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag) VALUES ($1, 'evt-legacy', $2, 'legacy-etag')`,
		calID, string(b)); err != nil {
		t.Fatal(err)
	}

	path := "/dav/" + calUID + "/evt-legacy.ics"
	w := httptest.NewRecorder()
	HandleGET(w, davRequest(http.MethodGet, path, hid, "", nil), repo, path)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d", w.Code)
	}
	got := w.Body.String()
	for _, want := range []string{"BEGIN:VCALENDAR", "UID:evt-legacy", "SUMMARY:Bin day", "DTSTART:20261120T070000Z", "END:VCALENDAR"} {
		if !strings.Contains(got, want) {
			t.Errorf("generated GET body missing %q:\n%s", want, got)
		}
	}
	if w.Header().Get("ETag") != `"legacy-etag"` {
		t.Errorf("ETag = %q", w.Header().Get("ETag"))
	}
}

// overrideVEvent and masterVEvent make up a weekly series plus one
// RECURRENCE-ID override; overrideBodies puts them in both orders, since
// clients may send either.
const (
	overrideVEvent = "BEGIN:VEVENT\r\n" +
		"UID:evt-override\r\n" +
		"RECURRENCE-ID;TZID=America/Chicago:20261116T160000\r\n" +
		"DTSTAMP:20261101T120000Z\r\n" +
		"SUMMARY:Piano lesson (moved)\r\n" +
		"DESCRIPTION:Moved to Tuesday evening\r\n" +
		"LOCATION:Teacher's house\r\n" +
		"DTSTART;TZID=America/Chicago:20261117T180000\r\n" +
		"DTEND;TZID=America/Chicago:20261117T190000\r\n" +
		"END:VEVENT\r\n"
	masterVEvent = "BEGIN:VEVENT\r\n" +
		"UID:evt-override\r\n" +
		"DTSTAMP:20261101T120000Z\r\n" +
		"SUMMARY:Piano lesson\r\n" +
		"DESCRIPTION:Bring the yellow book\r\n" +
		"LOCATION:Music room\r\n" +
		"DTSTART;TZID=America/Chicago:20261102T160000\r\n" +
		"DTEND;TZID=America/Chicago:20261102T170000\r\n" +
		"RRULE:FREQ=WEEKLY;COUNT=8\r\n" +
		"BEGIN:VALARM\r\n" +
		"ACTION:DISPLAY\r\n" +
		"DESCRIPTION:Reminder\r\n" +
		"TRIGGER:-PT15M\r\n" +
		"END:VALARM\r\n" +
		"END:VEVENT\r\n"
	overrideCalHead = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Apple Inc.//macOS 15.1//EN\r\n"
	overrideCalTail = "END:VCALENDAR\r\n"
)

// TestCalDAVOverrideStoresMasterIntegration locks in #4639: PUT of a master
// plus RECURRENCE-ID override stores the MASTER in ical_data (what the API,
// UI, search and MCP read), in either VEVENT order, and GET still serves the
// PUT body byte for byte.
func TestCalDAVOverrideStoresMasterIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	ctx := context.Background()
	hid, calUID, calID := seedCalendar(t, pool)

	for name, body := range map[string]string{
		"master-first":   overrideCalHead + masterVEvent + overrideVEvent + overrideCalTail,
		"override-first": overrideCalHead + overrideVEvent + masterVEvent + overrideCalTail,
	} {
		t.Run(name, func(t *testing.T) {
			// Each order is its own resource, so give each its own UID.
			uid := "evt-override-" + name
			body := strings.ReplaceAll(body, "UID:evt-override\r\n", "UID:"+uid+"\r\n")
			path := "/dav/" + calUID + "/" + uid + ".ics"

			w := httptest.NewRecorder()
			HandlePUT(w, davRequest(http.MethodPut, path, hid, body, nil), repo, path)
			if w.Code != http.StatusCreated {
				t.Fatalf("PUT status = %d, body %s", w.Code, w.Body)
			}

			var jsonData string
			if err := pool.QueryRow(ctx, `SELECT ical_data FROM calendar_objects WHERE calendar_id = $1 AND uid = $2`, calID, uid).Scan(&jsonData); err != nil {
				t.Fatal(err)
			}
			var ev ical.CalEvent
			if err := json.Unmarshal([]byte(jsonData), &ev); err != nil {
				t.Fatal(err)
			}
			if ev.Title != "Piano lesson" {
				t.Errorf("stored title = %q, want the master's %q (json: %s)", ev.Title, "Piano lesson", jsonData)
			}
			if ev.Description != "Bring the yellow book" || ev.Location != "Music room" {
				t.Errorf("stored description/location = %q/%q, want the master's (json: %s)", ev.Description, ev.Location, jsonData)
			}
			// 16:00 CST on 2026-11-02 is 22:00 UTC.
			if want := time.Date(2026, 11, 2, 22, 0, 0, 0, time.UTC); !ev.Start.Equal(want) {
				t.Errorf("stored start = %v, want the master's %v", ev.Start, want)
			}

			w = httptest.NewRecorder()
			HandleGET(w, davRequest(http.MethodGet, path, hid, "", nil), repo, path)
			if w.Code != http.StatusOK {
				t.Fatalf("GET status = %d", w.Code)
			}
			if got := w.Body.String(); got != body {
				t.Fatalf("GET body differs from PUT body:\n got: %q\nwant: %q", got, body)
			}
		})
	}
}
