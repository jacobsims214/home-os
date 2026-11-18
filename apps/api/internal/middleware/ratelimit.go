// This file implements per-client-IP rate limiting for the API's
// unauthenticated routes (#4685): invite accept (signup), household create
// (onboarding), forgot-password and reset-password. They are reachable from
// the internet once Home OS is public (Cloudflare -> Traefik -> Envoy -> API),
// so each client IP gets a token bucket (golang.org/x/time/rate). An empty
// bucket answers 429 with Retry-After before the handler runs: no body is
// read, no token verified, no bcrypt or Dex call made.
//
// The buckets are in memory, per pod. With api.replicas=2 a client can get up
// to twice the configured budget if Envoy spreads its requests over both
// pods. That is acceptable for throttling signup/reset abuse and enumeration;
// a shared store (Redis, Postgres) would be needed for an exact global limit.
//
// Request bodies on these routes are already capped by the handlers (#4684),
// so this file only limits request rate.
//
// The client IP is chosen by ClientIP: X-Forwarded-For is believed only when
// the TCP peer is a configured trusted proxy, and then the rightmost hop
// outside the trusted networks is the client. The leftmost entry is whatever
// the client sent and is never used. The calendar service applies the same
// rule (apps/calendar/internal/auth/ratelimit.go); the code is deliberately
// not shared across apps.
package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"home-os/api/pkg/apierr"
)

// Default budget for the unauthenticated routes, per client IP.
const (
	DefaultPublicRatePerMinute = 10
	DefaultPublicRateBurst     = 5
)

const (
	// rateLimitIdleTTL is how long an entry must be idle (and its bucket
	// full again) before eviction.
	rateLimitIdleTTL = 10 * time.Minute
	// rateLimitCleanupPeriod is how often idle entries are evicted.
	rateLimitCleanupPeriod = time.Minute
)

// RateLimiter is a set of token buckets keyed by client IP, safe for
// concurrent use. Build it with NewRateLimiter, start eviction with Run, and
// wrap handlers with Middleware.
type RateLimiter struct {
	limit   rate.Limit
	burst   int
	trusted []netip.Prefix
	now     func() time.Time // replaced in tests

	mu      sync.Mutex
	entries map[string]*rateEntry
}

// rateEntry is one client's bucket. lastSeen is guarded by RateLimiter.mu.
type rateEntry struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter returns a limiter allowing perMinute requests per client IP
// with the given burst. Zero or negative values get the defaults. trusted are
// the networks of the reverse proxies in front of the API (see ClientIP).
func NewRateLimiter(perMinute, burst int, trusted []netip.Prefix) *RateLimiter {
	if perMinute <= 0 {
		perMinute = DefaultPublicRatePerMinute
	}
	if burst <= 0 {
		burst = DefaultPublicRateBurst
	}
	return &RateLimiter{
		limit:   rate.Every(time.Minute / time.Duration(perMinute)),
		burst:   burst,
		trusted: trusted,
		now:     time.Now,
		entries: make(map[string]*rateEntry),
	}
}

// Run evicts idle entries every minute until ctx is done. Call it in its own
// goroutine.
func (l *RateLimiter) Run(ctx context.Context) {
	t := time.NewTicker(rateLimitCleanupPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.evictIdle(l.now())
		}
	}
}

// Allow spends one token for key. When none is left it returns false and how
// long until one is.
func (l *RateLimiter) Allow(key string) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		e = &rateEntry{lim: rate.NewLimiter(l.limit, l.burst)}
		l.entries[key] = e
	}
	e.lastSeen = now
	if e.lim.AllowN(now, 1) {
		return true, 0
	}
	tokens := e.lim.TokensAt(now)
	if tokens >= 1 || l.limit <= 0 {
		return false, time.Second
	}
	return false, time.Duration((1 - tokens) / float64(l.limit) * float64(time.Second))
}

// evictIdle drops entries idle since before now-rateLimitIdleTTL whose bucket
// has refilled. A full bucket behaves exactly like a missing one, so eviction
// never shortens anyone's wait.
func (l *RateLimiter) evictIdle(now time.Time) {
	cutoff := now.Add(-rateLimitIdleTTL)
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, e := range l.entries {
		if e.lastSeen.Before(cutoff) && e.lim.TokensAt(now) >= float64(l.burst) {
			delete(l.entries, k)
		}
	}
}

// size reports the number of tracked clients (tests).
func (l *RateLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// Middleware rate-limits next per client IP. Past the budget it answers 429
// with a Retry-After header (whole seconds, rounded up) and a JSON error.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r, l.trusted)
		ok, wait := l.Allow(ip)
		if !ok {
			secs := int(math.Ceil(wait.Seconds()))
			if secs < 1 {
				secs = 1
			}
			slog.Info("rate limited", "path", r.URL.Path, "client_ip", ip, "retry_after_s", secs)
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			apierr.JSON(w, http.StatusTooManyRequests, apierr.ErrorResponse{Error: apierr.ErrorDetail{
				Code:    "RATE_LIMITED",
				Message: "too many requests, try again later",
			}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ClientIP returns the rate-limit key for the client that sent r.
//
// The TCP peer (r.RemoteAddr) is the only address a client can't forge. If it
// is not in trusted, it is the client and X-Forwarded-For is ignored. If it
// is, X-Forwarded-For is read right to left: each proxy appends the address
// it received the request from, so the first entry outside trusted is the
// real client. Entries to its left came from the client and are never used.
// If every entry is trusted, the leftmost is used (an in-cluster caller). An
// unparsable entry stops the walk at the last trusted hop.
//
// In production the chain is client -> Cloudflare -> Traefik -> Envoy -> API.
// With trusted = the pod CIDR plus Cloudflare's published ranges, this yields
// the address Cloudflare's edge saw.
//
// IPv6 clients are keyed by their /64, since one host usually controls a
// whole /64 and could otherwise rotate addresses to get fresh buckets.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	client := peer.Unmap()
	if inPrefixes(client, trusted) {
		hops := forwardedFor(r)
		for i := len(hops) - 1; i >= 0; i-- {
			addr, err := netip.ParseAddr(hops[i])
			if err != nil {
				break
			}
			client = addr.Unmap()
			if !inPrefixes(client, trusted) {
				break
			}
		}
	}
	if client.Is6() {
		if p, err := client.Prefix(64); err == nil {
			return p.String()
		}
	}
	return client.String()
}

// forwardedFor returns every X-Forwarded-For entry in order, across repeated
// headers, trimmed.
func forwardedFor(r *http.Request) []string {
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hops = append(hops, h)
			}
		}
	}
	return hops
}

func inPrefixes(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// ParseTrustedProxies parses a comma-separated list of CIDRs or bare IPs (a
// bare IP is a /32 or /128). Empty input gives an empty list, meaning
// X-Forwarded-For is never believed.
func ParseTrustedProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			p, err := netip.ParsePrefix(part)
			if err != nil {
				return nil, fmt.Errorf("trusted proxy %q: %w", part, err)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", part, err)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}
