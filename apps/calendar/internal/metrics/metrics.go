// Package metrics defines the Prometheus metric instruments exposed by the
// calendar service and provides the HTTP middleware that records them.
//
// The service exposes four metric families on /metrics:
//
//   - calendar_http_requests_total{method, path, status} — a counter
//     incremented once per inbound HTTP request after the handler returns.
//   - calendar_http_request_duration_seconds{method, path} — a histogram of
//     the wall-clock time spent inside the handler, observed in seconds.
//   - calendar_auth_failures_total{reason} — a counter incremented by the
//     auth middleware on every rejected Basic-Auth attempt. The "reason"
//     label distinguishes missing-header, bad-format, unknown-user, and
//     bad-password failures so operators can spot credential-spraying vs
//     misconfigured clients.
//   - calendar_db_query_duration_seconds{operation} — a histogram of DB
//     round-trip latency. Currently observed for the /ready ping; future
//     tasks can add observations to individual repo methods.
//
// All metric names carry the `calendar_` prefix to avoid collisions when
// scraped alongside other Home OS services in a shared Prometheus instance.
//
// The package is safe for concurrent use: Prometheus collectors are
// goroutine-safe by construction.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metric instruments. Declared as package-level vars so they are registered
// exactly once at init time. Each uses promauto so the metric is both created
// and registered with the default registry in one step — there is no need to
// call prometheus.Register manually, and double-registration is impossible.
var (
	// HTTPRequestsTotal counts every HTTP request handled by the service,
	// labelled by method, normalised path, and response status code.
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "calendar_http_requests_total",
			Help: "Total number of HTTP requests handled by the calendar service.",
		},
		[]string{"method", "path", "status"},
	)

	// HTTPRequestDurationSeconds observes the wall-clock duration of an HTTP
	// request from the moment the middleware sees it until the handler
	// returns. Buckets are tuned for HTTP request latency (sub-millisecond
	// to ~10s).
	HTTPRequestDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "calendar_http_request_duration_seconds",
			Help:    "Wall-clock duration of HTTP requests handled by the calendar service.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	// AuthFailuresTotal counts every rejected Basic-Auth attempt. The
	// "reason" label surfaces why the credential was rejected so operators
	// can tell credential-spraying apart from misconfigured clients.
	AuthFailuresTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "calendar_auth_failures_total",
			Help: "Total number of rejected CalDAV Basic-Auth attempts.",
		},
		[]string{"reason"},
	)

	// DBQueryDurationSeconds observes DB round-trip latency. The "operation"
	// label names the DB call (e.g. "ping", "list_calendars") so slow
	// queries can be attributed.
	DBQueryDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "calendar_db_query_duration_seconds",
			Help:    "Latency of DB round-trips made by the calendar service.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"operation"},
	)
)

func init() {
	// Register all instruments with the default registry. We use the
	// default registry so promhttp.Handler() exposes them without any
	// additional wiring in main.go.
	prometheus.MustRegister(
		HTTPRequestsTotal,
		HTTPRequestDurationSeconds,
		AuthFailuresTotal,
		DBQueryDurationSeconds,
	)
}

// Handler returns the http.Handler that serves the Prometheus exposition
// format on /metrics. It is backed by the default registry, so any metric
// registered in this package is automatically exposed.
func Handler() http.Handler {
	return promhttp.Handler()
}

// Middleware wraps an http.Handler and records request count and latency
// for every request. The path label is normalised via normalisePath so the
// high-cardinality /dav/{calendarUID}/{eventUID} URLs collapse to a single
// "/dav/" label value — without this, Prometheus would store one series per
// event UID and quickly exhaust memory on a calendar with many events.
//
// The middleware is applied OUTSIDE the auth middleware so requests that
// fail auth (401) are still counted. /metrics is intentionally also wrapped
// so operators can see scrape traffic.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// statusRecorder captures the status code set by the inner handler
		// without otherwise interfering with the response.
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		elapsed := time.Since(start).Seconds()
		path := normalisePath(r.URL.Path)
		method := r.Method

		HTTPRequestDurationSeconds.WithLabelValues(method, path).Observe(elapsed)
		HTTPRequestsTotal.WithLabelValues(method, path, strconv.Itoa(rw.status)).Inc()
	})
}

// RecordAuthFailure increments the auth-failure counter with the given
// reason. The auth middleware calls this on every rejected credential so
// operators can monitor brute-force attempts and misconfigured clients.
// Reason values are stable strings (see auth middleware); new reasons must
// be added deliberately because each becomes its own Prometheus series.
func RecordAuthFailure(reason string) {
	AuthFailuresTotal.WithLabelValues(reason).Inc()
}

// ObserveDBQuery records the duration of a DB round-trip under the given
// operation label. Callers should use stable, snake_case operation names
// (e.g. "ping", "list_calendars") so the cardinality of the operation label
// stays bounded.
func ObserveDBQuery(operation string, duration time.Duration) {
	DBQueryDurationSeconds.WithLabelValues(operation).Observe(duration.Seconds())
}

// normalisePath collapses high-cardinality URL paths to a stable label so
// the path label on HTTPRequestsTotal / HTTPRequestDurationSeconds cannot
// grow unboundedly. CalDAV URLs under /dav/ carry calendar and event UIDs
// in the path — those are mapped to "/dav/". Everything else is passed
// through verbatim because the service only has a fixed set of routes
// (/health, /ready, /metrics, /.well-known/*).
func normalisePath(p string) string {
	if len(p) >= len("/dav/") && p[:len("/dav/")] == "/dav/" {
		return "/dav/"
	}
	// Treat exact "/dav" the same way.
	if p == "/dav" {
		return "/dav/"
	}
	return p
}

// statusRecorder wraps http.ResponseWriter to capture the status code the
// inner handler wrote. It deliberately does NOT implement Hijacker or
// Flusher — CalDAV handlers do not use either, and missing methods cause
// the standard library to fall back to the underlying ResponseWriter via
// the embedded field, preserving those capabilities for callers that need
// them.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status code then delegates to the embedded
// ResponseWriter.
func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
