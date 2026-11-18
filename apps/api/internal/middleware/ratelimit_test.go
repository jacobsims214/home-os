package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is a settable clock for the limiter.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(t *testing.T, perMinute, burst int, trusted string) (*RateLimiter, *fakeClock) {
	t.Helper()
	prefixes, err := ParseTrustedProxies(trusted)
	require.NoError(t, err)
	l := NewRateLimiter(perMinute, burst, prefixes)
	clk := &fakeClock{t: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}
	l.now = clk.now
	return l, clk
}

// do sends one POST through the limited handler and returns the recorder.
func do(h http.Handler, remote string, xff ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/invites/accept", nil)
	req.RemoteAddr = remote
	for _, v := range xff {
		req.Header.Add("X-Forwarded-For", v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
})

func TestRateLimiter_BurstThen429WithRetryAfter(t *testing.T) {
	l, _ := newTestLimiter(t, 10, 5, "")
	h := l.Middleware(okHandler)

	for i := 0; i < 5; i++ {
		rec := do(h, "203.0.113.7:4000")
		require.Equal(t, http.StatusNoContent, rec.Code, "request %d within burst", i+1)
	}
	rec := do(h, "203.0.113.7:4000")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	// 10/min = one token per 6s, bucket empty: wait 6s.
	assert.Equal(t, "6", rec.Header().Get("Retry-After"))
	assert.Contains(t, rec.Body.String(), "RATE_LIMITED")

	// Another client IP has its own bucket.
	assert.Equal(t, http.StatusNoContent, do(h, "198.51.100.9:4000").Code)
}

func TestRateLimiter_Refill(t *testing.T) {
	l, clk := newTestLimiter(t, 10, 5, "")
	h := l.Middleware(okHandler)
	for i := 0; i < 5; i++ {
		do(h, "203.0.113.7:1")
	}
	require.Equal(t, http.StatusTooManyRequests, do(h, "203.0.113.7:1").Code)

	clk.advance(2 * time.Second)
	rec := do(h, "203.0.113.7:1")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	require.NoError(t, err)
	assert.Equal(t, 4, retry, "Retry-After shrinks as the bucket refills")

	clk.advance(4 * time.Second)
	assert.Equal(t, http.StatusNoContent, do(h, "203.0.113.7:1").Code, "one token after 6s")
	assert.Equal(t, http.StatusTooManyRequests, do(h, "203.0.113.7:1").Code, "and only one")

	clk.advance(time.Minute)
	for i := 0; i < 5; i++ {
		assert.Equal(t, http.StatusNoContent, do(h, "203.0.113.7:1").Code, "full burst after a minute")
	}
}

func TestRateLimiter_SpoofedXFFFromUntrustedPeerIgnored(t *testing.T) {
	// Only the pod CIDR is trusted; the attacker connects directly.
	l, _ := newTestLimiter(t, 10, 5, "10.244.0.0/16")
	h := l.Middleware(okHandler)
	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusNoContent, do(h, "203.0.113.7:1", "192.0.2."+strconv.Itoa(i)).Code)
	}
	// A fresh spoofed XFF value doesn't buy a fresh bucket.
	assert.Equal(t, http.StatusTooManyRequests, do(h, "203.0.113.7:1", "192.0.2.200").Code)
	assert.Equal(t, 1, l.size(), "every request landed in the peer's bucket")
}

func TestRateLimiter_LeftmostXFFNeverUsedBehindTrustedProxy(t *testing.T) {
	l, _ := newTestLimiter(t, 10, 5, "10.244.0.0/16")
	h := l.Middleware(okHandler)
	// Via Envoy (trusted). The client prepends a random hop each time; the
	// rightmost untrusted hop (the real client) stays the same.
	for i := 0; i < 5; i++ {
		require.Equal(t, http.StatusNoContent, do(h, "10.244.1.5:1", "192.0.2."+strconv.Itoa(i)+", 203.0.113.7, 10.244.2.9").Code)
	}
	assert.Equal(t, http.StatusTooManyRequests, do(h, "10.244.1.5:1", "192.0.2.99, 203.0.113.7").Code)
	// A different real client behind the same proxy is unaffected.
	assert.Equal(t, http.StatusNoContent, do(h, "10.244.1.5:1", "203.0.113.8").Code)
}

func TestClientIP(t *testing.T) {
	trusted, err := ParseTrustedProxies("10.244.0.0/16, 173.245.48.0/20, 2606:4700::/32")
	require.NoError(t, err)

	tests := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"untrusted peer, no xff", "203.0.113.7:5000", nil, "203.0.113.7"},
		{"untrusted peer, xff ignored", "203.0.113.7:5000", []string{"192.0.2.1"}, "203.0.113.7"},
		{"trusted peer, no xff", "10.244.0.3:5000", nil, "10.244.0.3"},
		{"trusted peer, rightmost untrusted", "10.244.0.3:5000", []string{"1.1.1.1, 203.0.113.7, 10.244.0.9"}, "203.0.113.7"},
		{"cloudflare hop skipped", "10.244.0.3:5000", []string{"203.0.113.7, 173.245.48.10"}, "203.0.113.7"},
		{"repeated headers joined", "10.244.0.3:5000", []string{"1.1.1.1", "203.0.113.7"}, "203.0.113.7"},
		{"all trusted -> leftmost", "10.244.0.3:5000", []string{"10.244.5.5, 10.244.0.9"}, "10.244.5.5"},
		{"unparsable stops at last trusted", "10.244.0.3:5000", []string{"203.0.113.7, garbage, 10.244.0.9"}, "10.244.0.9"},
		{"ipv6 keyed by /64", "[2001:db8:1:2:3:4:5:6]:443", nil, "2001:db8:1:2::/64"},
		{"ipv4-mapped peer unmapped", "[::ffff:203.0.113.7]:443", nil, "203.0.113.7"},
		{"cloudflare v6 hop skipped", "10.244.0.3:1", []string{"2001:db8:9::1, 2606:4700::1"}, "2001:db8:9::/64"},
		{"no port", "203.0.113.7", nil, "203.0.113.7"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			assert.Equal(t, tc.want, ClientIP(req, trusted))
		})
	}
}

func TestParseTrustedProxies(t *testing.T) {
	got, err := ParseTrustedProxies(" 10.244.0.0/16 , 192.0.2.5,, 2606:4700::/32 ")
	require.NoError(t, err)
	assert.Equal(t, []netip.Prefix{
		netip.MustParsePrefix("10.244.0.0/16"),
		netip.MustParsePrefix("192.0.2.5/32"),
		netip.MustParsePrefix("2606:4700::/32"),
	}, got)

	empty, err := ParseTrustedProxies("")
	require.NoError(t, err)
	assert.Empty(t, empty)

	_, err = ParseTrustedProxies("10.0.0.0/33")
	assert.Error(t, err)
	_, err = ParseTrustedProxies("not-an-ip")
	assert.Error(t, err)
}

func TestRateLimiter_EvictIdle(t *testing.T) {
	l, clk := newTestLimiter(t, 10, 5, "")
	l.Allow("a")
	for i := 0; i < 5; i++ {
		l.Allow("b")
	}
	clk.advance(rateLimitIdleTTL + time.Second)
	l.Allow("c") // recent
	l.evictIdle(clk.now())
	// a and b were idle and have refilled; c is recent.
	assert.Equal(t, 1, l.size())

	// An empty bucket that is idle but not yet refilled is kept.
	slow, clk2 := newTestLimiter(t, 1, 100, "")
	for i := 0; i < 100; i++ {
		slow.Allow("x")
	}
	clk2.advance(rateLimitIdleTTL + time.Second)
	slow.evictIdle(clk2.now())
	assert.Equal(t, 1, slow.size(), "evicting a non-full bucket would reset its wait")
}

func TestRateLimiter_Defaults(t *testing.T) {
	l := NewRateLimiter(0, -1, nil)
	assert.Equal(t, DefaultPublicRateBurst, l.burst)
	assert.InDelta(t, float64(DefaultPublicRatePerMinute)/60, float64(l.limit), 1e-9)
}

func TestCORSFor(t *testing.T) {
	h := CORSFor(OriginOf("https://homeos.simstech.cloud/"), false)(okHandler)
	hDev := CORSFor(OriginOf("https://homeos.simstech.cloud"), true)(okHandler)

	tests := []struct {
		name   string
		h      http.Handler
		origin string
		want   bool
	}{
		{"public origin", h, "https://homeos.simstech.cloud", true},
		{"other site", h, "https://evil.example", false},
		{"suffix trick", h, "https://homeos.simstech.cloud.evil.example", false},
		{"http downgrade", h, "http://homeos.simstech.cloud", false},
		{"localhost in prod", h, "http://localhost:3000", false},
		{"localhost in dev", hDev, "http://localhost:3000", true},
		{"127.0.0.1 in dev", hDev, "https://127.0.0.1:8443", true},
		{"other site in dev", hDev, "https://evil.example", false},
		{"localhost lookalike in dev", hDev, "http://localhost.evil.example", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
			req.Header.Set("Origin", tc.origin)
			rec := httptest.NewRecorder()
			tc.h.ServeHTTP(rec, req)
			if tc.want {
				assert.Equal(t, tc.origin, rec.Header().Get("Access-Control-Allow-Origin"))
			} else {
				assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
			}
		})
	}
	assert.Equal(t, "", OriginOf(""))
	assert.Equal(t, "", OriginOf("ftp://x"))
	assert.Equal(t, "http://localhost:8000", OriginOf("http://LOCALHOST:8000/app"))
}
