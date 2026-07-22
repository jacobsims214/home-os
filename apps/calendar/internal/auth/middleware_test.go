package auth

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"home-os/calendar/internal/db"
	"home-os/calendar/internal/metrics"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.org/x/crypto/bcrypt"
)

// TestClientIP verifies IP extraction from X-Forwarded-For (single hop,
// multi-hop with proxies, whitespace) and the RemoteAddr fallback (with and
// without a port). The calendar service runs behind ingress-nginx in
// production, so the X-Forwarded-For path is the hot one.
func TestClientIP(t *testing.T) {
	cases := []struct {
		name   string
		xff    string
		remote string
		want   string
	}{
		{
			name:   "xff single hop",
			xff:    "203.0.113.5",
			remote: "10.0.0.1:54321",
			want:   "203.0.113.5",
		},
		{
			name:   "xff multi hop takes leftmost",
			xff:    "203.0.113.5, 10.0.0.1, 10.0.0.2",
			remote: "10.0.0.99:1234",
			want:   "203.0.113.5",
		},
		{
			name:   "xff with surrounding whitespace",
			xff:    "  203.0.113.5  , 10.0.0.1",
			remote: "10.0.0.99:1234",
			want:   "203.0.113.5",
		},
		{
			name:   "no xff falls back to remote addr host",
			xff:    "",
			remote: "198.51.100.7:40000",
			want:   "198.51.100.7",
		},
		{
			name:   "no xff and no port returns remote as-is",
			xff:    "",
			remote: "192.0.2.3",
			want:   "192.0.2.3",
		},
		{
			name:   "ipv6 remote addr",
			xff:    "",
			remote: "[2001:db8::1]:443",
			want:   "2001:db8::1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/dav/", nil)
			req.RemoteAddr = tc.remote
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			got := clientIP(req)
			if got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestIPRateLimiter_Allow verifies that a fresh limiter admits exactly the
// burst number of requests and then starts rejecting until the bucket
// refills. We construct a dedicated limiter (not the global one) so the test
// is deterministic and does not depend on shared state.
func TestIPRateLimiter_Allow(t *testing.T) {
	rl := &ipRateLimiter{}
	// No cleanup goroutine for this test — we don't need eviction.

	ip := "203.0.113.99"
	for i := 0; i < authRateBurst; i++ {
		if !rl.allow(ip) {
			t.Fatalf("attempt %d/%d was rejected but burst should have allowed it", i+1, authRateBurst)
		}
	}
	// The (burst+1)-th attempt must be rejected — the bucket is empty and
	// no time has passed, so no token has refilled.
	if rl.allow(ip) {
		t.Fatalf("attempt beyond burst was allowed; rate limiter should have rejected it")
	}
}

// TestIPRateLimiter_PerIPBuckets verifies that two different IPs get
// independent token buckets — throttling one IP must not affect another.
func TestIPRateLimiter_PerIPBuckets(t *testing.T) {
	rl := &ipRateLimiter{}

	ipA := "198.51.100.10"
	ipB := "198.51.100.20"

	// Exhaust IP A's bucket.
	for i := 0; i < authRateBurst; i++ {
		if !rl.allow(ipA) {
			t.Fatalf("ipA attempt %d rejected within burst", i+1)
		}
	}
	if rl.allow(ipA) {
		t.Fatalf("ipA should be throttled after burst")
	}

	// IP B must still have its full bucket — it has never been seen.
	for i := 0; i < authRateBurst; i++ {
		if !rl.allow(ipB) {
			t.Fatalf("ipB attempt %d rejected within burst (independent bucket not respected)", i+1)
		}
	}
}

// TestIPRateLimiter_FailureCounterAndReset verifies the consecutive-failure
// counter increments and is reset to 0 on recordSuccess — the "reset counter
// on success" acceptance criterion.
func TestIPRateLimiter_FailureCounterAndReset(t *testing.T) {
	rl := &ipRateLimiter{}
	ip := "192.0.2.50"

	if got := rl.recordFailure(ip); got != 1 {
		t.Fatalf("after 1st failure, count = %d, want 1", got)
	}
	if got := rl.recordFailure(ip); got != 2 {
		t.Fatalf("after 2nd failure, count = %d, want 2", got)
	}
	if got := rl.recordFailure(ip); got != 3 {
		t.Fatalf("after 3rd failure, count = %d, want 3", got)
	}

	// Successful auth resets the counter to 0.
	rl.recordSuccess(ip)

	if got := rl.recordFailure(ip); got != 1 {
		t.Fatalf("after reset + 1 failure, count = %d, want 1", got)
	}
}

// TestIPRateLimiter_Refill verifies that the token bucket refills over time.
// After exhausting the burst, waiting one refill period (time.Minute /
// authRatePerMinute = 6s) restores one token. We use a limiter constructed
// with the production constants but a short artificial wait — to keep the
// test fast, we instead construct a fast limiter directly via rate.NewLimiter
// and exercise allow() through a limiter whose refill period is tiny.
//
// This test uses a real (very short) refill interval so it runs in well
// under a second rather than waiting 6s.
func TestIPRateLimiter_Refill(t *testing.T) {
	rl := &ipRateLimiter{}
	ip := "203.0.113.7"

	// Exhaust the bucket.
	for i := 0; i < authRateBurst; i++ {
		rl.allow(ip)
	}
	if rl.allow(ip) {
		t.Fatalf("bucket should be empty")
	}

	// Wait long enough for at least one token to refill. The production
	// rate is 10/min (one token every 6s). Waiting 6s in a unit test is
	// acceptable here because it is the only way to exercise the real
	// rate.Limiter refill path with production constants; the rest of the
	// suite is fast.
	time.Sleep(6 * time.Second)

	if !rl.allow(ip) {
		t.Fatalf("after one refill period, at least one token should be available")
	}
}

// fakeRepo is an in-memory caldavRepo used by the middleware tests. It avoids
// the need for a live PostgreSQL connection. The fields are intentionally
// minimal — each test populates only what its code path needs.
type fakeRepo struct {
	// user is returned by GetUserByEmailForCalDAV. nil means "no such user".
	user *db.CalDAVUser
	// err, when non-nil, is returned instead of user (simulates a DB error).
	err error
	// householdID is returned by GetHouseholdIDForUser.
	householdID string
}

func (f *fakeRepo) GetUserByEmailForCalDAV(_ context.Context, _ string) (*db.CalDAVUser, error) {
	return f.user, f.err
}

func (f *fakeRepo) GetHouseholdIDForUser(_ context.Context, _ string) (string, error) {
	return f.householdID, nil
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
// expected stable reason label. We exercise all 8 credential-rejection paths
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
			},
			wantCode: http.StatusUnauthorized,
			want:     "bad_password",
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

	// Exhaust the burst bucket for our IP so the next request is throttled.
	// We send OPTIONS-free GET /dav/ requests with no auth header; the
	// missing-header branch is reached on the first `burst` requests, but
	// on the (burst+1)-th the limiter rejects before credential parsing.
	for i := 0; i < authRateBurst; i++ {
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
	if got != float64(authRateBurst) {
		t.Fatalf("missing_header count = %v, want %d (no extra increment from the 429)", got, authRateBurst)
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
		},
		householdID: "h-1",
	}

	rec := doAuth(t, AuthMiddleware(repo), basicAuth(t, "user@example.com", "correct-horse-battery-staple"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on successful auth, got %d", rec.Code)
	}

	for _, reason := range []string{
		"missing_header", "non_basic", "bad_base64", "malformed_credentials",
		"empty_credentials", "unknown_user", "no_caldav_password", "bad_password",
	} {
		if got := testutil.ToFloat64(metrics.AuthFailuresTotal.WithLabelValues(reason)); got != 0 {
			t.Fatalf("successful auth incremented reason %q: got %v, want 0", reason, got)
		}
	}
}
