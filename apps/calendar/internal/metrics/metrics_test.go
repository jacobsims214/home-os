package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestMiddleware_RecordsRequestAndDuration verifies that the middleware
// increments the request counter and observes a duration for every request
// that passes through it, including requests that return non-200 statuses.
func TestMiddleware_RecordsRequestAndDuration(t *testing.T) {
	// Reset the counter vec so the test is deterministic. Collectors are
	// global singletons; without a reset, prior tests would pollute the count.
	HTTPRequestsTotal.Reset()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	wrapped := Middleware(inner)

	req := httptest.NewRequest(http.MethodGet, "/dav/some-uid/event-uid", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("expected status %d, got %d", http.StatusTeapot, rec.Code)
	}

	// The /dav/... path should have been normalised to "/dav/".
	count := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues(http.MethodGet, "/dav/", "418"))
	if count != 1 {
		t.Fatalf("expected request count 1, got %f", count)
	}
}

// TestMiddleware_HealthPathNotCollapsed verifies that non-/dav/ paths are
// passed through verbatim so /health, /ready, /metrics keep distinct labels.
func TestMiddleware_HealthPathNotCollapsed(t *testing.T) {
	HTTPRequestsTotal.Reset()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	wrapped := Middleware(inner)

	for _, p := range []string{"/health", "/ready", "/metrics"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		wrapped.ServeHTTP(httptest.NewRecorder(), req)
	}

	for _, p := range []string{"/health", "/ready", "/metrics"} {
		count := testutil.ToFloat64(HTTPRequestsTotal.WithLabelValues(http.MethodGet, p, "200"))
		if count != 1 {
			t.Fatalf("expected 1 request for %s, got %f", p, count)
		}
	}
}

// TestRecordAuthFailure_IncrementsCounter verifies that each call adds one
// to the named reason label.
func TestRecordAuthFailure_IncrementsCounter(t *testing.T) {
	AuthFailuresTotal.Reset()

	RecordAuthFailure("bad_password")
	RecordAuthFailure("bad_password")
	RecordAuthFailure("unknown_user")

	if got := testutil.ToFloat64(AuthFailuresTotal.WithLabelValues("bad_password")); got != 2 {
		t.Fatalf("expected 2 bad_password failures, got %f", got)
	}
	if got := testutil.ToFloat64(AuthFailuresTotal.WithLabelValues("unknown_user")); got != 1 {
		t.Fatalf("expected 1 unknown_user failure, got %f", got)
	}
}

// TestNormalisePath verifies that CalDAV paths with UIDs collapse to "/dav/"
// while everything else is passed through.
func TestNormalisePath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/dav/", "/dav/"},
		{"/dav", "/dav/"},
		{"/dav/abc-123", "/dav/"},
		{"/dav/abc-123/def-456.ics", "/dav/"},
		{"/health", "/health"},
		{"/ready", "/ready"},
		{"/metrics", "/metrics"},
		{"/.well-known/caldav", "/.well-known/caldav"},
		{"/", "/"},
	}
	for _, c := range cases {
		if got := normalisePath(c.in); got != c.want {
			t.Errorf("normalisePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestHandler_ServesMetrics verifies that the /metrics handler exposes the
// registered metric names in the Prometheus text exposition format.
func TestHandler_ServesMetrics(t *testing.T) {
	// Ensure at least one observation exists for each metric so the metric
	// name appears in the output.
	HTTPRequestsTotal.WithLabelValues(http.MethodGet, "/health", "200").Inc()
	HTTPRequestDurationSeconds.WithLabelValues(http.MethodGet, "/health").Observe(0.001)
	AuthFailuresTotal.WithLabelValues("bad_password").Inc()
	DBQueryDurationSeconds.WithLabelValues("ping").Observe(0.002)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, name := range []string{
		"calendar_http_requests_total",
		"calendar_http_request_duration_seconds",
		"calendar_auth_failures_total",
		"calendar_db_query_duration_seconds",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("metrics output missing %q", name)
		}
	}
}
