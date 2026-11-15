package caldav

import (
	"context"
	"encoding/json"
	"errors"
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
		{"tab folded LF", "BEGIN:VEVENT\nRRULE:FREQ=WEEKLY\n\t;BYDAY=MO\nEND:VEVENT\n", "FREQ=WEEKLY;BYDAY=MO"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractRRULE(tc.in); got != tc.want {
				t.Errorf("extractRRULE = %q, want %q", got, tc.want)
			}
		})
	}
}

// foldedRRULEBody returns a ~size-byte VEVENT whose RRULE is one logical line
// folded into "\r\n ;" continuations, the input that made the old unfold
// quadratic.
func foldedRRULEBody(size int) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:big\r\nRRULE:FREQ=DAILY\r\n")
	for b.Len() < size {
		b.WriteString(" ;\r\n")
	}
	b.WriteString("END:VEVENT\r\nEND:VCALENDAR\r\n")
	return b.String()
}

// TestExtractRRULE_Linear: a 1 MB body of continuation lines took seconds
// with the old per-fold string concatenation.
func TestExtractRRULE_Linear(t *testing.T) {
	body := foldedRRULEBody(1 << 20)
	start := time.Now()
	got := extractRRULE(body)
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("extractRRULE(1 MB folded) took %v, want < 200ms", d)
	}
	if !strings.HasPrefix(got, "FREQ=DAILY;;;") || len(got) < (1<<20)/5 {
		t.Errorf("extractRRULE returned %d bytes starting %.16q", len(got), got)
	}
}

func BenchmarkExtractRRULE1MB(b *testing.B) {
	body := foldedRRULEBody(1 << 20)
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		extractRRULE(body)
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

// simpleEvent returns a minimal VCALENDAR for uid with the given SUMMARY.
func simpleEvent(uid, summary string) string {
	return "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//Test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:" + uid + "\r\n" +
		"DTSTAMP:20261115T120000Z\r\n" +
		"DTSTART:20261120T150000Z\r\n" +
		"DTEND:20261120T160000Z\r\n" +
		"SUMMARY:" + summary + "\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
}

// putResult is one PUT's outcome in a concurrency test.
type putResult struct {
	code    int
	etag    string
	summary string
}

// concurrentPUTs fires one PUT per summary at path at the same moment, each
// with the given headers, and returns their results.
func concurrentPUTs(t *testing.T, repo *db.Repo, path, hid, uid string, hdr map[string]string, summaries ...string) []putResult {
	t.Helper()
	results := make([]putResult, len(summaries))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, s := range summaries {
		wg.Add(1)
		go func(i int, s string) {
			defer wg.Done()
			req := davRequest(http.MethodPut, path, hid, simpleEvent(uid, s), hdr)
			<-start
			w := httptest.NewRecorder()
			HandlePUT(w, req, repo, path)
			results[i] = putResult{code: w.Code, etag: w.Header().Get("ETag"), summary: s}
		}(i, s)
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent PUTs did not finish in 30s (lock wait?)")
	}
	return results
}

// storedEvent returns the stored ETag, SUMMARY, SEQUENCE and raw body of uid.
func storedEvent(t *testing.T, pool *pgxpool.Pool, calID, uid string) (etag, summary string, seq int, raw string) {
	t.Helper()
	var jsonData string
	var rawp *string
	if err := pool.QueryRow(context.Background(),
		`SELECT etag, ical_data, ical_raw FROM calendar_objects WHERE calendar_id = $1 AND uid = $2`,
		calID, uid).Scan(&etag, &jsonData, &rawp); err != nil {
		t.Fatalf("read stored %s: %v", uid, err)
	}
	var ev ical.CalEvent
	if err := json.Unmarshal([]byte(jsonData), &ev); err != nil {
		t.Fatal(err)
	}
	if rawp != nil {
		raw = *rawp
	}
	return etag, ev.Title, ev.Sequence, raw
}

// changeActions returns the calendar_changes actions recorded for uid, oldest first.
func changeActions(t *testing.T, pool *pgxpool.Pool, calID, uid string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT action FROM calendar_changes WHERE calendar_id = $1 AND event_uid = $2 ORDER BY revision`, calID, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func calendarCTag(t *testing.T, pool *pgxpool.Pool, calID string) string {
	t.Helper()
	var ctag string
	if err := pool.QueryRow(context.Background(), `SELECT ctag FROM calendars WHERE id = $1`, calID).Scan(&ctag); err != nil {
		t.Fatal(err)
	}
	return ctag
}

// TestCalDAVConcurrentIfMatchIntegration: two devices save the same event at
// the same time, both carrying the ETag they last saw. Exactly one write may
// win; the other must get 412 so its client refetches instead of silently
// overwriting the winner (#4660). Repeated to give the race many chances.
func TestCalDAVConcurrentIfMatchIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	hid, calUID, calID := seedCalendar(t, pool)

	for round := 0; round < 10; round++ {
		uid := fmt.Sprintf("evt-ifmatch-%d", round)
		path := "/dav/" + calUID + "/" + uid + ".ics"

		w := httptest.NewRecorder()
		HandlePUT(w, davRequest(http.MethodPut, path, hid, simpleEvent(uid, "original"), nil), repo, path)
		if w.Code != http.StatusCreated {
			t.Fatalf("round %d: seed PUT status = %d: %s", round, w.Code, w.Body)
		}
		etag := w.Header().Get("ETag")
		ctagBefore := calendarCTag(t, pool, calID)

		res := concurrentPUTs(t, repo, path, hid, uid, map[string]string{"If-Match": etag}, "phone edit", "laptop edit")

		var winners, failed []putResult
		for _, r := range res {
			switch {
			case r.code == http.StatusOK:
				winners = append(winners, r)
			case r.code == http.StatusPreconditionFailed:
				failed = append(failed, r)
			default:
				t.Fatalf("round %d: unexpected status %d", round, r.code)
			}
		}
		if len(winners) != 1 || len(failed) != 1 {
			t.Fatalf("round %d: got %d x 200 and %d x 412, want exactly one of each (lost update)", round, len(winners), len(failed))
		}
		gotETag, gotSummary, gotSeq, gotRaw := storedEvent(t, pool, calID, uid)
		if `"`+gotETag+`"` != winners[0].etag {
			t.Errorf("round %d: stored etag %q, winner returned %s", round, gotETag, winners[0].etag)
		}
		if gotSummary != winners[0].summary || gotRaw != simpleEvent(uid, winners[0].summary) {
			t.Errorf("round %d: stored summary %q, want winner's %q", round, gotSummary, winners[0].summary)
		}
		if gotSeq != 1 {
			t.Errorf("round %d: SEQUENCE = %d, want 1 (one successful update)", round, gotSeq)
		}
		if got := changeActions(t, pool, calID, uid); strings.Join(got, ",") != "create,update" {
			t.Errorf("round %d: calendar_changes = %v, want [create update]", round, got)
		}
		if calendarCTag(t, pool, calID) == ctagBefore {
			t.Errorf("round %d: ctag not bumped by the winning update", round)
		}

		// The loser retries with the winner's ETag (what a client does after
		// refetching) and succeeds; the old ETag keeps failing.
		w = httptest.NewRecorder()
		HandlePUT(w, davRequest(http.MethodPut, path, hid, simpleEvent(uid, "stale"), map[string]string{"If-Match": etag}), repo, path)
		if w.Code != http.StatusPreconditionFailed {
			t.Errorf("round %d: stale If-Match status = %d, want 412", round, w.Code)
		}
		w = httptest.NewRecorder()
		HandlePUT(w, davRequest(http.MethodPut, path, hid, simpleEvent(uid, failed[0].summary), map[string]string{"If-Match": winners[0].etag}), repo, path)
		if w.Code != http.StatusOK {
			t.Errorf("round %d: retry with fresh If-Match status = %d, want 200", round, w.Code)
		}
	}
}

// TestCalDAVConcurrentIfNoneMatchIntegration: two devices create the same
// event (If-None-Match: *) at the same time. One gets 201, the other 412, and
// the stored event is the creator's.
func TestCalDAVConcurrentIfNoneMatchIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	hid, calUID, calID := seedCalendar(t, pool)

	for round := 0; round < 10; round++ {
		uid := fmt.Sprintf("evt-create-%d", round)
		path := "/dav/" + calUID + "/" + uid + ".ics"

		res := concurrentPUTs(t, repo, path, hid, uid, map[string]string{"If-None-Match": "*"}, "phone create", "laptop create")

		var created, failed []putResult
		for _, r := range res {
			switch r.code {
			case http.StatusCreated:
				created = append(created, r)
			case http.StatusPreconditionFailed:
				failed = append(failed, r)
			default:
				t.Fatalf("round %d: unexpected status %d", round, r.code)
			}
		}
		if len(created) != 1 || len(failed) != 1 {
			t.Fatalf("round %d: got %d x 201 and %d x 412, want exactly one of each", round, len(created), len(failed))
		}
		gotETag, gotSummary, gotSeq, _ := storedEvent(t, pool, calID, uid)
		if `"`+gotETag+`"` != created[0].etag || gotSummary != created[0].summary {
			t.Errorf("round %d: stored (%q, %q), want creator's (%s, %q)", round, gotETag, gotSummary, created[0].etag, created[0].summary)
		}
		if gotSeq != 0 {
			t.Errorf("round %d: SEQUENCE = %d, want 0 for a new event", round, gotSeq)
		}
		if got := changeActions(t, pool, calID, uid); strings.Join(got, ",") != "create" {
			t.Errorf("round %d: calendar_changes = %v, want [create]", round, got)
		}
	}
}

// TestCalDAVUnconditionalPUTIntegration: PUTs without If-Match /
// If-None-Match keep working: create, update, and two concurrent creates of
// the same new event both succeed (one as a create, the other as an update
// after it loses the insert race) instead of one failing.
func TestCalDAVUnconditionalPUTIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	hid, calUID, calID := seedCalendar(t, pool)

	uid := "evt-plain"
	path := "/dav/" + calUID + "/" + uid + ".ics"
	w := httptest.NewRecorder()
	HandlePUT(w, davRequest(http.MethodPut, path, hid, simpleEvent(uid, "first"), nil), repo, path)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	HandlePUT(w, davRequest(http.MethodPut, path, hid, simpleEvent(uid, "second"), nil), repo, path)
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d: %s", w.Code, w.Body)
	}
	etag, summary, seq, _ := storedEvent(t, pool, calID, uid)
	if `"`+etag+`"` != w.Header().Get("ETag") || summary != "second" || seq != 1 {
		t.Errorf("after update: etag %q (resp %s), summary %q, seq %d", etag, w.Header().Get("ETag"), summary, seq)
	}

	for round := 0; round < 10; round++ {
		uid := fmt.Sprintf("evt-plain-race-%d", round)
		path := "/dav/" + calUID + "/" + uid + ".ics"
		res := concurrentPUTs(t, repo, path, hid, uid, nil, "a", "b")
		codes := []int{res[0].code, res[1].code}
		sort.Ints(codes)
		if codes[0] != http.StatusOK || codes[1] != http.StatusCreated {
			t.Fatalf("round %d: statuses %v, want one 200 and one 201", round, codes)
		}
		if got := changeActions(t, pool, calID, uid); strings.Join(got, ",") != "create,update" {
			t.Errorf("round %d: calendar_changes = %v, want [create update]", round, got)
		}
	}

	// A PUT to a calendar of another household is still 404.
	otherHID, _, _ := seedCalendar(t, pool)
	w = httptest.NewRecorder()
	HandlePUT(w, davRequest(http.MethodPut, path, otherHID, simpleEvent(uid, "intruder"), nil), repo, path)
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-household PUT status = %d, want 404", w.Code)
	}
}

// TestPutCalendarObjectPreconditionSentinel: a decide callback's
// db.ErrPreconditionFailed aborts the write and reaches the caller, and
// nothing is written (no row, no change row, no ctag bump).
func TestPutCalendarObjectPreconditionSentinel(t *testing.T) {
	repo, pool := testRepo(t)
	hid, _, calID := seedCalendar(t, pool)
	ctagBefore := calendarCTag(t, pool, calID)

	_, _, err := repo.PutCalendarObject(context.Background(), hid, calID, "evt-sentinel",
		func(cur *db.CalendarObject) (db.ObjectWrite, error) {
			return db.ObjectWrite{}, &preconditionError{result: preconditionFailMissing}
		})
	if !errors.Is(err, db.ErrPreconditionFailed) {
		t.Fatalf("err = %v, want ErrPreconditionFailed", err)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM calendar_objects WHERE calendar_id = $1`, calID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 || len(changeActions(t, pool, calID, "evt-sentinel")) != 0 || calendarCTag(t, pool, calID) != ctagBefore {
		t.Errorf("failed precondition left side effects: rows=%d", n)
	}
}

// ----- DELETE If-Match (#4662) -----

// seedEvent PUTs a new event and returns its path and the ETag the server
// sent back (quoted, as a client would echo it in If-Match).
func seedEvent(t *testing.T, repo *db.Repo, hid, calUID, uid string) (path, etag string) {
	t.Helper()
	path = "/dav/" + calUID + "/" + uid + ".ics"
	w := httptest.NewRecorder()
	HandlePUT(w, davRequest(http.MethodPut, path, hid, simpleEvent(uid, "original"), nil), repo, path)
	if w.Code != http.StatusCreated {
		t.Fatalf("seed PUT %s status = %d: %s", uid, w.Code, w.Body)
	}
	return path, w.Header().Get("ETag")
}

func doDELETE(repo *db.Repo, path, hid string, hdr map[string]string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	HandleDELETE(w, davRequest(http.MethodDelete, path, hid, "", hdr), repo, path)
	return w
}

func objectExists(t *testing.T, pool *pgxpool.Pool, calID, uid string) bool {
	t.Helper()
	var ok bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM calendar_objects WHERE calendar_id = $1 AND uid = $2)`, calID, uid).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// TestCalDAVDeleteIfMatchIntegration covers the DELETE precondition matrix
// against a real database: what is answered, and what is left behind (row,
// calendar_changes tombstone, CTag).
func TestCalDAVDeleteIfMatchIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	hid, calUID, calID := seedCalendar(t, pool)

	t.Run("stale If-Match is 412 and the event stays", func(t *testing.T) {
		uid := "del-stale"
		path, oldETag := seedEvent(t, repo, hid, calUID, uid)
		// Another member edits the event: its ETag changes.
		w := httptest.NewRecorder()
		HandlePUT(w, davRequest(http.MethodPut, path, hid, simpleEvent(uid, "edited"), map[string]string{"If-Match": oldETag}), repo, path)
		if w.Code != http.StatusOK {
			t.Fatalf("edit status = %d", w.Code)
		}
		ctagBefore := calendarCTag(t, pool, calID)

		// The device that has not synced deletes with the old ETag.
		if w := doDELETE(repo, path, hid, map[string]string{"If-Match": oldETag}); w.Code != http.StatusPreconditionFailed {
			t.Fatalf("stale DELETE status = %d, want 412: %s", w.Code, w.Body)
		}
		if !objectExists(t, pool, calID, uid) {
			t.Fatal("event deleted despite stale If-Match")
		}
		if _, summary, _, _ := storedEvent(t, pool, calID, uid); summary != "edited" {
			t.Errorf("summary = %q, want the edit to survive", summary)
		}
		if got := changeActions(t, pool, calID, uid); strings.Join(got, ",") != "create,update" {
			t.Errorf("calendar_changes = %v, want no delete tombstone", got)
		}
		if calendarCTag(t, pool, calID) != ctagBefore {
			t.Error("ctag bumped by a refused DELETE")
		}
	})

	t.Run("matching If-Match is 204", func(t *testing.T) {
		for _, form := range []string{"quoted", "weak", "list"} {
			uid := "del-match-" + form
			path, etag := seedEvent(t, repo, hid, calUID, uid)
			hdr := etag
			switch form {
			case "weak":
				hdr = "W/" + etag
			case "list":
				hdr = `"deadbeef", ` + etag
			}
			ctagBefore := calendarCTag(t, pool, calID)
			if w := doDELETE(repo, path, hid, map[string]string{"If-Match": hdr}); w.Code != http.StatusNoContent {
				t.Fatalf("%s: DELETE status = %d, want 204: %s", form, w.Code, w.Body)
			}
			if objectExists(t, pool, calID, uid) {
				t.Errorf("%s: event still there", form)
			}
			if got := changeActions(t, pool, calID, uid); strings.Join(got, ",") != "create,delete" {
				t.Errorf("%s: calendar_changes = %v, want [create delete]", form, got)
			}
			if calendarCTag(t, pool, calID) == ctagBefore {
				t.Errorf("%s: ctag not bumped", form)
			}
		}
	})

	t.Run("If-Match * deletes an existing event", func(t *testing.T) {
		path, _ := seedEvent(t, repo, hid, calUID, "del-star")
		if w := doDELETE(repo, path, hid, map[string]string{"If-Match": "*"}); w.Code != http.StatusNoContent {
			t.Fatalf("DELETE If-Match:* status = %d, want 204", w.Code)
		}
	})

	t.Run("no If-Match is 204 and idempotent", func(t *testing.T) {
		uid := "del-plain"
		path, _ := seedEvent(t, repo, hid, calUID, uid)
		if w := doDELETE(repo, path, hid, nil); w.Code != http.StatusNoContent {
			t.Fatalf("DELETE status = %d, want 204", w.Code)
		}
		if objectExists(t, pool, calID, uid) {
			t.Fatal("event still there")
		}
		ctagBefore := calendarCTag(t, pool, calID)
		if w := doDELETE(repo, path, hid, nil); w.Code != http.StatusNoContent {
			t.Fatalf("repeat DELETE status = %d, want 204", w.Code)
		}
		if got := changeActions(t, pool, calID, uid); strings.Join(got, ",") != "create,delete" {
			t.Errorf("calendar_changes = %v, want one tombstone only", got)
		}
		if calendarCTag(t, pool, calID) != ctagBefore {
			t.Error("ctag bumped by a no-op DELETE")
		}
	})

	t.Run("If-Match on a missing event is 412", func(t *testing.T) {
		path := "/dav/" + calUID + "/del-missing.ics"
		for _, im := range []string{`"deadbeef"`, "*"} {
			if w := doDELETE(repo, path, hid, map[string]string{"If-Match": im}); w.Code != http.StatusPreconditionFailed {
				t.Errorf("If-Match %s on missing: status = %d, want 412", im, w.Code)
			}
		}
		if w := doDELETE(repo, path, hid, nil); w.Code != http.StatusNoContent {
			t.Errorf("no If-Match on missing: status = %d, want 204", w.Code)
		}
	})

	t.Run("another household gets 404 and nothing is deleted", func(t *testing.T) {
		uid := "del-cross"
		path, etag := seedEvent(t, repo, hid, calUID, uid)
		otherHID, _, _ := seedCalendar(t, pool)
		for _, hdr := range []map[string]string{nil, {"If-Match": etag}} {
			if w := doDELETE(repo, path, otherHID, hdr); w.Code != http.StatusNotFound {
				t.Errorf("cross-household DELETE (%v) status = %d, want 404", hdr, w.Code)
			}
		}
		if !objectExists(t, pool, calID, uid) {
			t.Error("another household deleted the event")
		}
	})

	t.Run("deleting a maintenance event still deletes its task", func(t *testing.T) {
		ctx := context.Background()
		uid := "del-maint"
		path, etag := seedEvent(t, repo, hid, calUID, uid)
		var taskID string
		if err := pool.QueryRow(ctx,
			`INSERT INTO maintenance_tasks (household_id, name) VALUES ($1, 'Replace filter') RETURNING id::text`, hid).Scan(&taskID); err != nil {
			t.Fatalf("insert task: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE calendar_objects SET entity_type = 'maintenance', entity_id = $3 WHERE calendar_id = $1 AND uid = $2`,
			calID, uid, taskID); err != nil {
			t.Fatalf("link task: %v", err)
		}
		// The link does not change the ETag the client holds.
		if w := doDELETE(repo, path, hid, map[string]string{"If-Match": etag}); w.Code != http.StatusNoContent {
			t.Fatalf("DELETE status = %d, want 204: %s", w.Code, w.Body)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM maintenance_tasks WHERE id = $1`, taskID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Error("maintenance task survived the deletion of its calendar event (027/038 trigger)")
		}
	})
}

// TestCalDAVConcurrentPUTDeleteIfMatchIntegration: one device edits an event
// while another deletes it, both carrying the same (current) ETag. Exactly one
// may succeed. If the edit wins, the delete gets 412 and the edit is kept; if
// the delete wins, the edit gets 412 and the event stays deleted.
func TestCalDAVConcurrentPUTDeleteIfMatchIntegration(t *testing.T) {
	repo, pool := testRepo(t)
	hid, calUID, calID := seedCalendar(t, pool)

	putWins, delWins := 0, 0
	for round := 0; round < 20; round++ {
		uid := fmt.Sprintf("evt-putdel-%d", round)
		path, etag := seedEvent(t, repo, hid, calUID, uid)
		hdr := map[string]string{"If-Match": etag}

		putReq := davRequest(http.MethodPut, path, hid, simpleEvent(uid, "edited"), hdr)
		delReq := davRequest(http.MethodDelete, path, hid, "", hdr)
		var putCode, delCode int
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			w := httptest.NewRecorder()
			HandlePUT(w, putReq, repo, path)
			putCode = w.Code
		}()
		go func() {
			defer wg.Done()
			<-start
			// The PUT parses its body before taking the row lock, so with a
			// simultaneous start the DELETE nearly always locks first. Give
			// the PUT a head start on odd rounds so both orders are tested.
			if round%2 == 1 {
				time.Sleep(time.Duration(1+round%4) * time.Millisecond)
			}
			w := httptest.NewRecorder()
			HandleDELETE(w, delReq, repo, path)
			delCode = w.Code
		}()
		close(start)
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("concurrent PUT + DELETE did not finish in 30s (lock wait?)")
		}

		switch {
		case putCode == http.StatusOK && delCode == http.StatusPreconditionFailed:
			putWins++
			if _, summary, _, _ := storedEvent(t, pool, calID, uid); summary != "edited" {
				t.Errorf("round %d: summary = %q, want the winning edit", round, summary)
			}
			if got := changeActions(t, pool, calID, uid); strings.Join(got, ",") != "create,update" {
				t.Errorf("round %d: calendar_changes = %v, want [create update]", round, got)
			}
		case delCode == http.StatusNoContent && putCode == http.StatusPreconditionFailed:
			delWins++
			if objectExists(t, pool, calID, uid) {
				t.Errorf("round %d: event exists after the winning DELETE", round)
			}
			if got := changeActions(t, pool, calID, uid); strings.Join(got, ",") != "create,delete" {
				t.Errorf("round %d: calendar_changes = %v, want [create delete]", round, got)
			}
		default:
			t.Fatalf("round %d: PUT %d, DELETE %d; want exactly one success and one 412", round, putCode, delCode)
		}
	}
	t.Logf("PUT won %d rounds, DELETE won %d", putWins, delWins)
	if putWins == 0 || delWins == 0 {
		t.Logf("only one ordering was exercised (PUT %d, DELETE %d); the outcome checks still held", putWins, delWins)
	}
}
