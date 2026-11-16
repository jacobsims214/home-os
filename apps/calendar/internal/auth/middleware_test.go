package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"home-os/calendar/internal/db"
	"home-os/calendar/internal/metrics"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.org/x/crypto/bcrypt"
)

// TestMain swaps the cost-12 dummy hash for a MinCost one so the many
// rejected-login requests in these tests stay fast. The production cost is
// checked against newDummyHash in TestAuthMiddleware_EveryCredentialRejectionRunsOneBcrypt.
func TestMain(m *testing.M) {
	cheap, err := bcrypt.GenerateFromPassword([]byte("dummy-for-tests"), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	dummyHash = func() []byte { return cheap }
	os.Exit(m.Run())
}

// fakeRepo is an in-memory caldavRepo used by the middleware tests. It avoids
// the need for a live PostgreSQL connection. The fields are intentionally
// minimal — each test populates only what its code path needs.
type fakeRepo struct {
	// user is returned by GetUserByEmailForCalDAV. nil means "no such user".
	user *db.CalDAVUser
	// err, when non-nil, is returned instead of user (simulates a DB error).
	err error
	// householdIDs is returned by ListMembershipsForUser (already in the
	// repo's deterministic order: oldest membership first).
	householdIDs []string
	// roles maps household_id -> role for ListMembershipsForUser (default
	// owner).
	roles map[string]string
	// calendarOwners maps caldav_uid -> household_id for GetCalendarHouseholdID.
	calendarOwners map[string]string
	// calendarLookups counts GetCalendarHouseholdID calls.
	calendarLookups int
}

func (f *fakeRepo) GetUserByEmailForCalDAV(_ context.Context, _ string) (*db.CalDAVUser, error) {
	return f.user, f.err
}

func (f *fakeRepo) ListMembershipsForUser(_ context.Context, _ string) ([]db.Membership, error) {
	var out []db.Membership
	for _, id := range f.householdIDs {
		role := f.roles[id]
		if role == "" {
			role = "owner"
		}
		out = append(out, db.Membership{HouseholdID: id, Role: role})
	}
	return out, nil
}

func (f *fakeRepo) GetCalendarHouseholdID(_ context.Context, uid string) (string, error) {
	f.calendarLookups++
	return f.calendarOwners[uid], nil
}

// basicAuth encodes an email:password pair into a Basic Authorization header
// value.
func basicAuth(t *testing.T, email, password string) string {
	t.Helper()
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+password))
}

// doAuth fires a single GET /dav/ through the middleware with the given
// Authorization header and returns the recorder.
func doAuth(t *testing.T, mw func(http.Handler) http.Handler, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/dav/", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	return rec
}

// TestAuthMiddleware_RecordsEachFailureReason verifies that every auth-failure
// branch in the middleware increments calendar_auth_failures_total with the
// expected stable reason label. We exercise all 9 credential-rejection paths
// and confirm the matching reason counter went up by exactly 1 while no other
// reason label was touched. Rate-limit (429) and DB-error (500) paths are NOT
// expected to record an auth failure — those are covered by separate cases
// below.
func TestAuthMiddleware_RecordsEachFailureReason(t *testing.T) {
	// Hash a real password once for the bad_password case.
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-horse-battery-staple"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt hash: %v", err)
	}
	hashStr := string(hash)

	cases := []struct {
		name     string
		authHdr  string
		repoUser *db.CalDAVUser
		repoErr  error
		wantCode int
		want     string // expected reason label, "" means no increment expected
	}{
		{
			name:     "missing header",
			authHdr:  "",
			wantCode: http.StatusUnauthorized,
			want:     "missing_header",
		},
		{
			name:     "non-basic scheme",
			authHdr:  "Bearer xyz",
			wantCode: http.StatusUnauthorized,
			want:     "non_basic",
		},
		{
			name:     "invalid base64",
			authHdr:  "Basic !!!not-base64!!!",
			wantCode: http.StatusUnauthorized,
			want:     "bad_base64",
		},
		{
			name:     "malformed credentials (no colon)",
			authHdr:  "Basic " + base64.StdEncoding.EncodeToString([]byte("nocolonhere")),
			wantCode: http.StatusUnauthorized,
			want:     "malformed_credentials",
		},
		{
			name:     "empty credentials",
			authHdr:  "Basic " + base64.StdEncoding.EncodeToString([]byte(":")),
			wantCode: http.StatusUnauthorized,
			want:     "empty_credentials",
		},
		{
			name:     "unknown user",
			authHdr:  basicAuth(t, "nobody@example.com", "pw"),
			repoUser: nil,
			wantCode: http.StatusUnauthorized,
			want:     "unknown_user",
		},
		{
			name:    "no caldav password set",
			authHdr: basicAuth(t, "user@example.com", "pw"),
			repoUser: &db.CalDAVUser{
				ID:                 "u-1",
				Email:              "user@example.com",
				CalDAVPasswordHash: nil,
				Active:             true,
			},
			wantCode: http.StatusUnauthorized,
			want:     "no_caldav_password",
		},
		{
			name:    "bad password",
			authHdr: basicAuth(t, "user@example.com", "wrong-password"),
			repoUser: &db.CalDAVUser{
				ID:                 "u-1",
				Email:              "user@example.com",
				CalDAVPasswordHash: &hashStr,
				Active:             true,
			},
			wantCode: http.StatusUnauthorized,
			want:     "bad_password",
		},
		{
			// Correct app password, but the account is deactivated.
			name:    "deactivated user",
			authHdr: basicAuth(t, "user@example.com", "correct-horse-battery-staple"),
			repoUser: &db.CalDAVUser{
				ID:                 "u-1",
				Email:              "user@example.com",
				CalDAVPasswordHash: &hashStr,
				Active:             false,
			},
			wantCode: http.StatusUnauthorized,
			want:     "inactive_user",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset the counter so prior cases don't pollute this one.
			metrics.AuthFailuresTotal.Reset()

			repo := &fakeRepo{user: tc.repoUser, err: tc.repoErr}
			rec := doAuth(t, AuthMiddleware(repo), tc.authHdr)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}

			if tc.want == "" {
				t.Fatalf("test case %q has want == \"\" — every case in this table must expect an increment", tc.name)
			}

			got := testutil.ToFloat64(metrics.AuthFailuresTotal.WithLabelValues(tc.want))
			if got != 1 {
				t.Fatalf("reason %q count = %v, want 1", tc.want, got)
			}

			// Ensure no other reason label was touched — total across the
			// whole vec should be exactly 1.
			// We sample the known reason set; an unexpected extra increment
			// would show up as a non-zero count on some other label.
			for _, other := range []string{
				"missing_header", "non_basic", "bad_base64", "malformed_credentials",
				"empty_credentials", "unknown_user", "no_caldav_password", "bad_password",
				"inactive_user",
			} {
				if other == tc.want {
					continue
				}
				if got := testutil.ToFloat64(metrics.AuthFailuresTotal.WithLabelValues(other)); got != 0 {
					t.Fatalf("unexpected increment for reason %q: got %v, want 0", other, got)
				}
			}
		})
	}
}

// TestAuthMiddleware_RateLimitDoesNotRecordAuthFailure verifies that a 429
// rate-limit response does NOT increment calendar_auth_failures_total — a
// throttled request is not a credential rejection.
func TestAuthMiddleware_RateLimitDoesNotRecordAuthFailure(t *testing.T) {
	metrics.AuthFailuresTotal.Reset()

	repo := &fakeRepo{}
	mw := AuthMiddleware(repo)

	// Exhaust the per-IP request bucket so the next request is throttled.
	// Missing-header requests don't spend failed-login tokens, so it is the
	// request bucket (burst DefaultRequestBurst) that runs out.
	for i := 0; i < DefaultRequestBurst; i++ {
		doAuth(t, mw, "")
	}

	// This one should be rate-limited (429) and must not record an auth
	// failure reason.
	rec := doAuth(t, mw, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exhausting burst, got %d", rec.Code)
	}

	// The missing_header reason was incremented `burst` times by the prior
	// requests. The 429 response must not have added another. Verify the
	// count equals burst exactly.
	got := testutil.ToFloat64(metrics.AuthFailuresTotal.WithLabelValues("missing_header"))
	if got != float64(DefaultRequestBurst) {
		t.Fatalf("missing_header count = %v, want %d (no extra increment from the 429)", got, DefaultRequestBurst)
	}
}

// TestAuthMiddleware_DBErrorDoesNotRecordAuthFailure verifies that a 500
// DB-error response does NOT increment calendar_auth_failures_total — a
// transient DB failure is not a credential rejection.
func TestAuthMiddleware_DBErrorDoesNotRecordAuthFailure(t *testing.T) {
	metrics.AuthFailuresTotal.Reset()

	repo := &fakeRepo{
		err: context.DeadlineExceeded, // any non-nil error → 500 path
	}
	rec := doAuth(t, AuthMiddleware(repo), basicAuth(t, "user@example.com", "pw"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on DB error, got %d", rec.Code)
	}

	for _, reason := range []string{
		"missing_header", "non_basic", "bad_base64", "malformed_credentials",
		"empty_credentials", "unknown_user", "no_caldav_password", "bad_password",
		"inactive_user",
	} {
		if got := testutil.ToFloat64(metrics.AuthFailuresTotal.WithLabelValues(reason)); got != 0 {
			t.Fatalf("DB error path incremented reason %q: got %v, want 0", reason, got)
		}
	}
}

// TestAuthMiddleware_SuccessDoesNotRecordAuthFailure verifies that a
// successful authentication does NOT increment the auth-failure counter —
// the metric is for rejected credentials only.
func TestAuthMiddleware_SuccessDoesNotRecordAuthFailure(t *testing.T) {
	metrics.AuthFailuresTotal.Reset()

	hash, err := bcrypt.GenerateFromPassword([]byte("correct-horse-battery-staple"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt hash: %v", err)
	}
	hashStr := string(hash)

	repo := &fakeRepo{
		user: &db.CalDAVUser{
			ID:                 "u-1",
			Email:              "user@example.com",
			CalDAVPasswordHash: &hashStr,
			Active:             true,
		},
		householdIDs: []string{"h-1"},
	}

	rec := doAuth(t, AuthMiddleware(repo), basicAuth(t, "user@example.com", "correct-horse-battery-staple"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on successful auth, got %d", rec.Code)
	}

	for _, reason := range []string{
		"missing_header", "non_basic", "bad_base64", "malformed_credentials",
		"empty_credentials", "unknown_user", "no_caldav_password", "bad_password",
		"inactive_user",
	} {
		if got := testutil.ToFloat64(metrics.AuthFailuresTotal.WithLabelValues(reason)); got != 0 {
			t.Fatalf("successful auth incremented reason %q: got %v, want 0", reason, got)
		}
	}
}

// authedUser returns a fakeRepo user with a real bcrypt hash plus the matching
// Basic auth header.
func authedUser(t *testing.T) (*db.CalDAVUser, string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("pw-123"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt hash: %v", err)
	}
	h := string(hash)
	return &db.CalDAVUser{ID: "u-1", Email: "user@example.com", CalDAVPasswordHash: &h, Active: true},
		basicAuth(t, "user@example.com", "pw-123")
}

// seen captures what the CalDAV handler would see after the middleware ran.
type seen struct {
	called    bool
	path      string
	household string
	scope     string
	all       []string
	base      string
}

func serveThrough(t *testing.T, repo *fakeRepo, header, path string) (*httptest.ResponseRecorder, seen) {
	t.Helper()
	var got seen
	req := httptest.NewRequest("PROPFIND", path, nil)
	req.RemoteAddr = "192.0.2.50:1234"
	req.Header.Set("Authorization", header)
	rec := httptest.NewRecorder()
	AuthMiddleware(repo)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = seen{
			called:    true,
			path:      r.URL.Path,
			household: HouseholdIDFromContext(r.Context()),
			scope:     HouseholdScopeFromContext(r.Context()),
			all:       HouseholdIDsFromContext(r.Context()),
			base:      DAVBase(r.Context()),
		}
		w.WriteHeader(http.StatusMultiStatus)
	})).ServeHTTP(rec, req)
	return rec, got
}

// TestAuthMiddleware_HouseholdResolution covers the default household, the
// /dav/households/<id>/ scope, and routing an unscoped calendar UID to the
// household that owns it.
func TestAuthMiddleware_HouseholdResolution(t *testing.T) {
	user, header := authedUser(t)
	twoFamilies := []string{"h-old", "h-new"}
	owners := map[string]string{
		"cal-old":     "h-old",
		"cal-new":     "h-new",
		"cal-foreign": "h-stranger",
	}

	cases := []struct {
		name       string
		households []string
		path       string
		wantCode   int
		wantPath   string
		wantHH     string
		wantScope  string
		wantBase   string
		wantLookup int
	}{
		{"default is first (oldest) membership", twoFamilies, "/dav/", 207, "/dav/", "h-old", "", "/dav/", 0},
		{"home set uses default", twoFamilies, "/dav/calendars/", 207, "/dav/calendars/", "h-old", "", "/dav/", 0},
		{"no memberships yields empty household", nil, "/dav/", 207, "/dav/", "", "", "/dav/", 0},
		{"scoped principal", twoFamilies, "/dav/households/h-new/", 207, "/dav/", "h-new", "h-new", "/dav/households/h-new/", 0},
		{"scoped without trailing slash", twoFamilies, "/dav/households/h-new", 207, "/dav/", "h-new", "h-new", "/dav/households/h-new/", 0},
		{"scoped home set", twoFamilies, "/dav/households/h-new/calendars/", 207, "/dav/calendars/", "h-new", "h-new", "/dav/households/h-new/", 0},
		{"scoped event", twoFamilies, "/dav/households/h-old/cal-old/e1.ics", 207, "/dav/cal-old/e1.ics", "h-old", "h-old", "/dav/households/h-old/", 0},
		{"scoped to a household the user is not in", twoFamilies, "/dav/households/h-stranger/", 404, "", "", "", "", 0},
		{"unscoped calendar in second household", twoFamilies, "/dav/cal-new/", 207, "/dav/cal-new/", "h-new", "", "/dav/", 1},
		{"unscoped event in second household", twoFamilies, "/dav/cal-new/e1.ics", 207, "/dav/cal-new/e1.ics", "h-new", "", "/dav/", 1},
		{"foreign calendar falls back to default", twoFamilies, "/dav/cal-foreign/", 207, "/dav/cal-foreign/", "h-old", "", "/dav/", 1},
		{"unknown calendar falls back to default", twoFamilies, "/dav/cal-missing/", 207, "/dav/cal-missing/", "h-old", "", "/dav/", 1},
		{"single household skips the calendar lookup", []string{"h-only"}, "/dav/cal-new/", 207, "/dav/cal-new/", "h-only", "", "/dav/", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{user: user, householdIDs: tc.households, calendarOwners: owners}
			rec, got := serveThrough(t, repo, header, tc.path)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantCode)
			}
			if repo.calendarLookups != tc.wantLookup {
				t.Errorf("calendar lookups = %d, want %d", repo.calendarLookups, tc.wantLookup)
			}
			if tc.wantCode != 207 {
				if got.called {
					t.Fatalf("handler ran for a rejected request")
				}
				return
			}
			if got.path != tc.wantPath || got.household != tc.wantHH || got.scope != tc.wantScope || got.base != tc.wantBase {
				t.Errorf("got path=%q household=%q scope=%q base=%q, want path=%q household=%q scope=%q base=%q",
					got.path, got.household, got.scope, got.base, tc.wantPath, tc.wantHH, tc.wantScope, tc.wantBase)
			}
			if len(got.all) != len(tc.households) {
				t.Errorf("household list = %v, want %v", got.all, tc.households)
			}
		})
	}
}

// TestAuthMiddleware_DefaultHouseholdIsStable: the same user lands in the same
// default household on every request.
func TestAuthMiddleware_DefaultHouseholdIsStable(t *testing.T) {
	user, header := authedUser(t)
	repo := &fakeRepo{user: user, householdIDs: []string{"h-a", "h-b", "h-c"}}
	for i := 0; i < 5; i++ {
		_, got := serveThrough(t, repo, header, "/dav/")
		if got.household != "h-a" {
			t.Fatalf("request %d: household = %q, want h-a", i, got.household)
		}
	}
}

// TestAuthMiddleware_DeactivatedUserRejectedEverywhere: a deactivated user's
// valid app password gets 401 on every authenticated path, including
// household-scoped ones, and the response can't be told apart from an
// unknown email's (same status, header and body; next handler never runs).
func TestAuthMiddleware_DeactivatedUserRejectedEverywhere(t *testing.T) {
	user, header := authedUser(t)
	user.Active = false
	inactive := &fakeRepo{user: user, householdIDs: []string{"h-1", "h-2"}}
	unknown := &fakeRepo{}

	paths := []string{
		"/dav/",
		"/dav/calendars/",
		"/dav/households/h-1/",
		"/dav/households/h-2/calendars/",
		"/dav/cal-uid/",
		"/dav/cal-uid/event.ics",
	}
	for _, method := range []string{"PROPFIND", http.MethodGet, http.MethodPut, "REPORT", http.MethodDelete} {
		for _, p := range paths {
			// Fresh middleware per request so the per-IP limiter never
			// turns a 401 into a 429.
			got := serveOnce(t, inactive, method, p, header)
			want := serveOnce(t, unknown, method, p, header)
			if got.called {
				t.Fatalf("%s %s: next handler ran for a deactivated user", method, p)
			}
			if got.rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s = %d, want 401", method, p, got.rec.Code)
			}
			if got.rec.Body.String() != want.rec.Body.String() ||
				got.rec.Header().Get("WWW-Authenticate") != want.rec.Header().Get("WWW-Authenticate") {
				t.Fatalf("%s %s: deactivated response (%q) differs from unknown-user response (%q)",
					method, p, got.rec.Body.String(), want.rec.Body.String())
			}
		}
	}
	if inactive.calendarLookups != 0 {
		t.Fatalf("calendar lookups for a deactivated user = %d, want 0", inactive.calendarLookups)
	}

	// No app password set doesn't change the answer either.
	user.CalDAVPasswordHash = nil
	got := serveOnce(t, inactive, "PROPFIND", "/dav/", header)
	want := serveOnce(t, unknown, "PROPFIND", "/dav/", header)
	if got.rec.Code != http.StatusUnauthorized || got.rec.Body.String() != want.rec.Body.String() {
		t.Fatalf("deactivated user without app password = %d %q, want 401 %q",
			got.rec.Code, got.rec.Body.String(), want.rec.Body.String())
	}
}

type onceResult struct {
	rec    *httptest.ResponseRecorder
	called bool
}

func serveOnce(t *testing.T, repo *fakeRepo, method, path, header string) onceResult {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "192.0.2.77:1234"
	req.Header.Set("Authorization", header)
	rec := httptest.NewRecorder()
	called := false
	AuthMiddleware(repo)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	return onceResult{rec: rec, called: called}
}

// rejectionCases returns the four credential rejections that happen after the
// user lookup, each against its own repo. All four send the same password so
// only the account state differs. The wrong-password case's stored hash is a
// real hash of a different password.
func rejectionCases(t *testing.T) (header string, cases []struct {
	name   string
	repo   *fakeRepo
	reason string
}) {
	t.Helper()
	user, _ := authedUser(t) // stored hash is of "pw-123"
	header = basicAuth(t, user.Email, "not-the-password")

	inactive := *user
	inactive.Active = false
	noPassword := *user
	noPassword.CalDAVPasswordHash = nil
	emptyPassword := *user
	empty := ""
	emptyPassword.CalDAVPasswordHash = &empty

	cases = []struct {
		name   string
		repo   *fakeRepo
		reason string
	}{
		{"unknown email", &fakeRepo{}, "unknown_user"},
		{"deactivated user", &fakeRepo{user: &inactive}, "inactive_user"},
		{"no app password", &fakeRepo{user: &noPassword}, "no_caldav_password"},
		{"empty app password hash", &fakeRepo{user: &emptyPassword}, "no_caldav_password"},
		{"wrong password", &fakeRepo{user: user}, "bad_password"},
	}
	return header, cases
}

// TestAuthMiddleware_CredentialRejectionsAreIdentical verifies that an unknown
// email, a deactivated account, an account without an app password and a
// wrong password get byte-identical 401 responses (status, every header,
// body), so the response can't be used to enumerate accounts.
func TestAuthMiddleware_CredentialRejectionsAreIdentical(t *testing.T) {
	header, cases := rejectionCases(t)

	var (
		wantName   string
		wantHeader http.Header
		wantBody   string
	)
	for _, tc := range cases {
		got := serveOnce(t, tc.repo, "PROPFIND", "/dav/", header)
		if got.called {
			t.Fatalf("%s: next handler ran", tc.name)
		}
		if got.rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d, want 401", tc.name, got.rec.Code)
		}
		if body := got.rec.Body.String(); body != "Invalid credentials\n" {
			t.Fatalf("%s: body = %q, want %q", tc.name, body, "Invalid credentials\n")
		}
		if got.rec.Header().Get("WWW-Authenticate") != `Basic realm="Home OS CalDAV"` {
			t.Fatalf("%s: WWW-Authenticate = %q", tc.name, got.rec.Header().Get("WWW-Authenticate"))
		}
		if wantHeader == nil {
			wantName, wantHeader, wantBody = tc.name, got.rec.Header().Clone(), got.rec.Body.String()
			continue
		}
		if !reflect.DeepEqual(got.rec.Header(), wantHeader) {
			t.Fatalf("%s headers %v differ from %s headers %v", tc.name, got.rec.Header(), wantName, wantHeader)
		}
		if got.rec.Body.String() != wantBody {
			t.Fatalf("%s body %q differs from %s body %q", tc.name, got.rec.Body.String(), wantName, wantBody)
		}
	}
}

// TestAuthMiddleware_EveryCredentialRejectionRunsOneBcrypt verifies the timing
// side of account enumeration without wall-clock measurements: every
// credential rejection runs exactly one bcrypt compare. Unknown, deactivated
// and no-app-password logins compare against the process's dummy hash; a
// wrong password compares against the user's own hash. The production dummy
// hash has the stored app-password cost (12, apps/api DefaultCost), so all
// four take the same time.
func TestAuthMiddleware_EveryCredentialRejectionRunsOneBcrypt(t *testing.T) {
	// The production dummy hash must cost the same as a stored app password
	// hash, or the dummy compare would be faster than a real one.
	prod := newDummyHash()
	if cost, err := bcrypt.Cost(prod); err != nil || cost != appPasswordCost {
		t.Fatalf("production dummy hash cost = %d (err %v), want %d", cost, err, appPasswordCost)
	}
	if bcrypt.CompareHashAndPassword(prod, []byte("")) == nil {
		t.Fatal("production dummy hash matches the empty password")
	}

	header, cases := rejectionCases(t)

	type call struct{ hash string }
	var calls []call
	orig := compareHash
	t.Cleanup(func() { compareHash = orig })
	compareHash = func(hash, password []byte) error {
		calls = append(calls, call{hash: string(hash)})
		return orig(hash, password)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metrics.AuthFailuresTotal.Reset()
			calls = nil

			got := serveOnce(t, tc.repo, "PROPFIND", "/dav/", header)
			if got.rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", got.rec.Code)
			}
			if len(calls) != 1 {
				t.Fatalf("bcrypt compares = %d, want exactly 1", len(calls))
			}
			wantHash := string(dummyHash())
			if tc.reason == "bad_password" {
				wantHash = *tc.repo.user.CalDAVPasswordHash
			}
			if calls[0].hash != wantHash {
				t.Fatalf("compared against %q, want %q", calls[0].hash, wantHash)
			}
			// The server-side reason label still tells the cases apart.
			if n := testutil.ToFloat64(metrics.AuthFailuresTotal.WithLabelValues(tc.reason)); n != 1 {
				t.Fatalf("reason %q count = %v, want 1", tc.reason, n)
			}
		})
	}

	// A successful login also runs exactly one compare (the real one).
	calls = nil
	user, okHeader := authedUser(t)
	if got := serveOnce(t, &fakeRepo{user: user, householdIDs: []string{"h-1"}}, "PROPFIND", "/dav/", okHeader); !got.called {
		t.Fatalf("valid login rejected: %d %q", got.rec.Code, got.rec.Body.String())
	}
	if len(calls) != 1 || calls[0].hash != *user.CalDAVPasswordHash {
		t.Fatalf("successful login compares = %+v, want one against the user's hash", calls)
	}
}

// ---- Rate limiting and client IP ----

// podCIDR is the trusted proxy network used by these tests, matching the
// cluster's pod CIDR (Traefik and Envoy both run there).
var podCIDR = []netip.Prefix{netip.MustParsePrefix("10.244.0.0/16")}

// envoyAddr is a RemoteAddr inside podCIDR: the request came via Envoy.
const envoyAddr = "10.244.3.17:41234"

// TestClientIP covers the trusted-proxy rules: X-Forwarded-For is ignored
// unless the TCP peer is a trusted proxy, and then the rightmost untrusted hop
// is the client; the client-supplied leftmost entries are never used.
func TestClientIP(t *testing.T) {
	cases := []struct {
		name    string
		xff     []string // one entry per header line
		remote  string
		trusted []netip.Prefix
		want    string
	}{
		{"no xff uses remote addr host", nil, "198.51.100.7:40000", podCIDR, "198.51.100.7"},
		{"no xff, no trusted proxies", nil, "198.51.100.7:40000", nil, "198.51.100.7"},
		{"no xff and no port returns remote as-is", nil, "192.0.2.3", nil, "192.0.2.3"},
		{"ipv4-mapped remote is unmapped", nil, "[::ffff:192.0.2.9]:80", nil, "192.0.2.9"},
		{"ipv6 remote keyed by /64", nil, "[2001:db8:1:2:aaaa::1]:443", nil, "2001:db8:1:2::/64"},
		{"xff ignored when peer untrusted", []string{"203.0.113.5"}, "198.51.100.7:1", podCIDR, "198.51.100.7"},
		{"xff ignored when no trusted proxies", []string{"203.0.113.5"}, envoyAddr, nil, "10.244.3.17"},
		{"trusted peer, single hop", []string{"203.0.113.5"}, envoyAddr, podCIDR, "203.0.113.5"},
		{"spoofed leftmost entries ignored", []string{"1.2.3.4, 5.6.7.8, 203.0.113.5"}, envoyAddr, podCIDR, "203.0.113.5"},
		{"trusted hops skipped from the right", []string{"9.9.9.9, 203.0.113.5, 10.244.1.4"}, envoyAddr, podCIDR, "203.0.113.5"},
		{"repeated headers read in order", []string{"9.9.9.9", "203.0.113.5"}, envoyAddr, podCIDR, "203.0.113.5"},
		{"whitespace trimmed", []string{"  9.9.9.9 ,  203.0.113.5  "}, envoyAddr, podCIDR, "203.0.113.5"},
		{"all hops trusted gives leftmost", []string{"10.244.9.9, 10.244.1.4"}, envoyAddr, podCIDR, "10.244.9.9"},
		{"garbage stops at last trusted hop", []string{"203.0.113.5, not-an-ip"}, envoyAddr, podCIDR, "10.244.3.17"},
		{"ipv6 client in xff keyed by /64", []string{"2001:db8:5:6::99"}, envoyAddr, podCIDR, "2001:db8:5:6::/64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/dav/", nil)
			req.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			if got := clientIP(req, tc.trusted); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseTrustedProxies(t *testing.T) {
	got, err := ParseTrustedProxies(" 10.244.0.0/16, 192.0.2.7 ,2001:db8::/32,, 10.1.2.3/8")
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("10.244.0.0/16"),
		netip.MustParsePrefix("192.0.2.7/32"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("10.0.0.0/8"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, err := ParseTrustedProxies(""); err != nil || len(got) != 0 {
		t.Fatalf("empty input = %v, %v; want none", got, err)
	}
	for _, bad := range []string{"10.244.0.0/99", "nope", "10.0.0.1/"} {
		if _, err := ParseTrustedProxies(bad); err == nil {
			t.Fatalf("ParseTrustedProxies(%q) accepted invalid input", bad)
		}
	}
}

// TestKeyedLimiter_TakeRefillAndRetryAfter: the bucket admits burst requests,
// then rejects with the time until the next token, and refills at the rate.
func TestKeyedLimiter_TakeRefillAndRetryAfter(t *testing.T) {
	l := newAuthLimiter(DefaultRateLimitConfig()).requests // 120/min: one token per 500ms
	now := time.Now()
	for i := 0; i < DefaultRequestBurst; i++ {
		if ok, _ := l.take("a", now); !ok {
			t.Fatalf("request %d rejected within burst", i+1)
		}
	}
	ok, wait := l.take("a", now)
	if ok {
		t.Fatal("request beyond burst allowed")
	}
	if wait <= 0 || wait > 500*time.Millisecond {
		t.Fatalf("retry after = %v, want (0, 500ms]", wait)
	}
	if ok, _ := l.take("b", now); !ok {
		t.Fatal("another key shares the exhausted bucket")
	}
	if ok, _ := l.take("a", now.Add(500*time.Millisecond)); !ok {
		t.Fatal("no token after one refill period")
	}
}

// TestKeyedLimiter_EvictOnlyRefilledEntries: idle eviction must not drop a
// bucket that is still draining, or waiting out the idle TTL would reset it.
func TestKeyedLimiter_EvictOnlyRefilledEntries(t *testing.T) {
	l := newAuthLimiter(DefaultRateLimitConfig())
	start := time.Now()
	for i := 0; i < DefaultAccountFailureBurst; i++ {
		l.accountFailures.fail("victim@example.com", start)
	}
	l.accountFailures.fail("other@example.com", start)

	// 10 minutes later both are idle; "other" has refilled its one token
	// (one every 2 minutes); "victim" has 5 of 30 back.
	later := start.Add(10 * time.Minute)
	l.evictIdle(later)
	if _, ok := l.accountFailures.entries.Load("victim@example.com"); !ok {
		t.Fatal("draining account bucket was evicted (attempts reset by waiting out the idle TTL)")
	}
	if _, ok := l.accountFailures.entries.Load("other@example.com"); ok {
		t.Fatal("refilled idle entry was not evicted")
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	for d, want := range map[time.Duration]int{0: 1, time.Millisecond: 1, time.Second: 1, 5500 * time.Millisecond: 6, 2 * time.Minute: 120} {
		if got := retryAfterSeconds(d); got != want {
			t.Fatalf("retryAfterSeconds(%v) = %d, want %d", d, got, want)
		}
	}
}

// limitedRequest is one request through mw from RemoteAddr remote with the
// given X-Forwarded-For (empty = none) and Authorization header.
func limitedRequest(mw http.Handler, remote, xff, header string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PROPFIND", "/dav/", nil)
	req.RemoteAddr = remote
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	return rec
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

// countBcrypt replaces compareHash with a counting wrapper for the test.
func countBcrypt(t *testing.T) *int {
	t.Helper()
	n := 0
	orig := compareHash
	t.Cleanup(func() { compareHash = orig })
	compareHash = func(hash, pw []byte) error {
		n++
		return orig(hash, pw)
	}
	return &n
}

// TestAuthMiddleware_SpoofedXFFSharesOneBucket is the regression test for the
// bypass: every request carries a different, attacker-chosen leftmost
// X-Forwarded-For, but the same address appended by the trusted proxy. They
// must share one failed-login bucket and get 429 (with Retry-After and no
// bcrypt) once the burst is spent. A different real client is unaffected.
func TestAuthMiddleware_SpoofedXFFSharesOneBucket(t *testing.T) {
	cfg := DefaultRateLimitConfig()
	cfg.TrustedProxies = podCIDR
	mw := AuthMiddlewareWithConfig(&fakeRepo{}, cfg)(okHandler())
	bcrypts := countBcrypt(t)

	const attacker = "203.0.113.66"
	for i := 0; i < DefaultIPFailureBurst; i++ {
		// A different victim email each time, so only the IP bucket can stop it.
		header := basicAuth(t, fmt.Sprintf("victim%d@example.com", i), "guess")
		xff := fmt.Sprintf("198.18.%d.%d, %s", i/250, i%250+1, attacker)
		if rec := limitedRequest(mw, envoyAddr, xff, header); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d, want 401", i+1, rec.Code)
		}
	}
	if *bcrypts != DefaultIPFailureBurst {
		t.Fatalf("bcrypt compares = %d, want %d", *bcrypts, DefaultIPFailureBurst)
	}

	rec := limitedRequest(mw, envoyAddr, "100.64.0.1, "+attacker, basicAuth(t, "fresh@example.com", "guess"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt after burst with a new spoofed XFF: status %d, want 429", rec.Code)
	}
	// 10 failures/min: the next token is 6s away.
	if got := rec.Header().Get("Retry-After"); got != "6" {
		t.Fatalf("Retry-After = %q, want \"6\"", got)
	}
	if *bcrypts != DefaultIPFailureBurst {
		t.Fatalf("a rate-limited request ran bcrypt (compares = %d)", *bcrypts)
	}

	if rec := limitedRequest(mw, envoyAddr, attacker+", 203.0.113.67", basicAuth(t, "a@example.com", "pw")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("other client behind the same proxy: status %d, want 401", rec.Code)
	}
}

// TestAuthMiddleware_UntrustedPeerXFFIgnored: a client connecting directly
// can't pick its bucket with X-Forwarded-For; its RemoteAddr host is the key.
func TestAuthMiddleware_UntrustedPeerXFFIgnored(t *testing.T) {
	cfg := DefaultRateLimitConfig()
	cfg.TrustedProxies = podCIDR
	mw := AuthMiddlewareWithConfig(&fakeRepo{}, cfg)(okHandler())

	for i := 0; i < DefaultIPFailureBurst; i++ {
		limitedRequest(mw, "198.51.100.9:5000", fmt.Sprintf("192.0.2.%d", i+1), basicAuth(t, "x@example.com"+fmt.Sprint(i), "guess"))
	}
	// No XFF at all: the same RemoteAddr host (any port) is the same bucket.
	if rec := limitedRequest(mw, "198.51.100.9:6000", "", basicAuth(t, "y@example.com", "guess")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429 (RemoteAddr host is the key)", rec.Code)
	}
	if rec := limitedRequest(mw, "198.51.100.10:5000", "", basicAuth(t, "y@example.com", "guess")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("different RemoteAddr: status %d, want 401", rec.Code)
	}
}

// TestAuthMiddleware_AccountFailureLimit: guesses at one account from many
// IPs are bounded per account, before the lookup or bcrypt; a different
// account is unaffected; a successful login refills the account's bucket.
func TestAuthMiddleware_AccountFailureLimit(t *testing.T) {
	user, okHeader := authedUser(t)
	repo := &fakeRepo{user: user, householdIDs: []string{"h-1"}}
	cfg := DefaultRateLimitConfig()
	cfg.AccountFailureBurst = 3
	mw := AuthMiddlewareWithConfig(repo, cfg)(okHandler())
	bcrypts := countBcrypt(t)

	bad := basicAuth(t, user.Email, "wrong")
	for i := 0; i < 2; i++ {
		if rec := limitedRequest(mw, fmt.Sprintf("192.0.2.%d:1", i+1), "", bad); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: status %d, want 401", i+1, rec.Code)
		}
	}
	// The right password still works and refills the bucket.
	if rec := limitedRequest(mw, "192.0.2.50:1", "", okHeader); rec.Code != http.StatusOK {
		t.Fatalf("valid login: status %d, want 200", rec.Code)
	}
	for i := 0; i < 3; i++ {
		if rec := limitedRequest(mw, fmt.Sprintf("192.0.2.%d:1", i+10), "", bad); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d after refill: status %d, want 401", i+1, rec.Code)
		}
	}
	before := *bcrypts
	// Case and spaces don't make a new bucket.
	rec := limitedRequest(mw, "192.0.2.99:1", "", basicAuth(t, " "+strings.ToUpper(user.Email), "wrong"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("account over its limit from a new IP: status %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	if *bcrypts != before {
		t.Fatal("account-limited request ran bcrypt")
	}
	if rec := limitedRequest(mw, "192.0.2.99:1", "", basicAuth(t, "someone-else@example.com", "wrong")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("other account: status %d, want 401", rec.Code)
	}
}

// TestAuthMiddleware_HeaderFailuresDontSpendLoginTokens: the unauthenticated
// challenge request CalDAV clients send first (and other header failures,
// which run no bcrypt) must not use up the failed-login budget.
func TestAuthMiddleware_HeaderFailuresDontSpendLoginTokens(t *testing.T) {
	user, okHeader := authedUser(t)
	mw := AuthMiddleware(&fakeRepo{user: user, householdIDs: []string{"h-1"}})(okHandler())
	for i := 0; i < 3*DefaultIPFailureBurst; i++ {
		if rec := limitedRequest(mw, "192.0.2.1:1", "", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("challenge %d: status %d, want 401", i+1, rec.Code)
		}
	}
	if rec := limitedRequest(mw, "192.0.2.1:1", "", okHeader); rec.Code != http.StatusOK {
		t.Fatalf("login after challenges: status %d, want 200", rec.Code)
	}
}

// TestAuthMiddleware_SuccessDoesNotRefillIPFailures: a valid login must not
// buy an IP more guesses at other accounts.
func TestAuthMiddleware_SuccessDoesNotRefillIPFailures(t *testing.T) {
	user, okHeader := authedUser(t)
	mw := AuthMiddleware(&fakeRepo{user: user, householdIDs: []string{"h-1"}})(okHandler())
	for i := 0; i < DefaultIPFailureBurst-1; i++ {
		limitedRequest(mw, "192.0.2.1:1", "", basicAuth(t, fmt.Sprintf("v%d@example.com", i), "guess"))
	}
	if rec := limitedRequest(mw, "192.0.2.1:1", "", okHeader); rec.Code != http.StatusOK {
		t.Fatalf("valid login: status %d", rec.Code)
	}
	limitedRequest(mw, "192.0.2.1:1", "", basicAuth(t, "last@example.com", "guess"))
	if rec := limitedRequest(mw, "192.0.2.1:1", "", basicAuth(t, "more@example.com", "guess")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429 (success must not refill the IP bucket)", rec.Code)
	}
}
