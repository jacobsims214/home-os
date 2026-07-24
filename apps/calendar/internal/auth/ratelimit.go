// Package auth provides Basic Authentication middleware for the CalDAV service.
//
// This file implements per-IP rate limiting for authentication attempts. A
// successful brute-force attack against the CalDAV app password would require
// running bcrypt.CompareHashAndPassword on every guess; with no throttle an
// attacker could fire thousands of requests per second. The ipRateLimiter
// limits each source IP to a small number of attempts per minute and tracks
// consecutive failures so operators can see (and alert on) repeated bad
// attempts. A successful authentication resets the failure counter so a user
// who fat-fingers their password a few times is not penalised.
package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Per-IP rate limiting constants.
//
// authRatePerMinute is the sustained rate we allow: 120 attempts per minute.
// authRateBurst is the bucket size — 60 immediate attempts are allowed before
// the sustained rate kicks in (one new token every 30 seconds).
// failureLogThreshold is the consecutive-failure count at which we start
// logging "repeated" failures at warning level. Each failure before that is
// still logged at info level with the running count so operators have a full
// audit trail.
// visitorIdleTTL is how long an IP can be idle before its entry is evicted
// from the map. Without eviction the map grows unboundedly as new attackers
// (or rotating NAT pools) hit the service.
const (
	authRatePerMinute    = 120
	authRateBurst        = 60
	failureLogThreshold  = 3
	visitorIdleTTL       = 5 * time.Minute
	visitorCleanupPeriod = time.Minute
)

// visitorEntry is the per-IP state kept by ipRateLimiter.
//
// mu guards failures and lastSeen. The limiter is goroutine-safe on its own
// (rate.Limiter uses an internal mutex), so Allow() may be called without
// holding mu — but to keep things simple and obviously-correct we hold mu
// for the whole check-and-update in allow().
//
// lastSeen is updated on every request that reaches the limiter (whether
// allowed or rejected) so the cleanup goroutine can evict idle entries.
// failures counts consecutive authentication failures for this IP; it is
// incremented at every failure point in AuthMiddleware and reset to 0 on a
// successful authentication.
type visitorEntry struct {
	limiter  *rate.Limiter
	mu       sync.Mutex
	failures int
	lastSeen time.Time
}

// ipRateLimiter tracks per-IP token-bucket limiters and consecutive failure
// counts. It is safe for concurrent use: the underlying sync.Map protects the
// map structure, and each visitorEntry has its own mutex (above) so auth
// checks for different IPs proceed in parallel.
type ipRateLimiter struct {
	visitors sync.Map // map[string]*visitorEntry
}

// newIPRateLimiter constructs an ipRateLimiter and starts a background
// goroutine that periodically evicts idle visitor entries. The goroutine runs
// for the lifetime of the process; because the limiter is constructed once at
// server startup, there is exactly one cleanup goroutine per process.
func newIPRateLimiter() *ipRateLimiter {
	rl := &ipRateLimiter{}
	go rl.cleanupLoop()
	return rl
}

// entryFor returns the visitorEntry for ip, creating a fresh one (with a full
// token bucket) if none exists yet. LoadOrStore handles the race between two
// concurrent first-time requests from the same IP: both may construct a
// visitorEntry, but only the stored one wins, and the loser's entry is
// discarded. The loser's full token bucket is a non-issue — a fresh bucket
// allows 10 immediate attempts, which is exactly what we want for a new IP.
func (rl *ipRateLimiter) entryFor(ip string) *visitorEntry {
	fresh := &visitorEntry{
		limiter:  rate.NewLimiter(rate.Every(time.Minute/authRatePerMinute), authRateBurst),
		lastSeen: time.Now(),
	}
	actual, loaded := rl.visitors.LoadOrStore(ip, fresh)
	if loaded {
		return actual.(*visitorEntry)
	}
	return fresh
}

// allow returns true if the IP may attempt authentication now, false if it
// has exceeded its rate budget (caller should respond 429). It also updates
// the entry's lastSeen timestamp under the entry's mutex.
func (rl *ipRateLimiter) allow(ip string) bool {
	e := rl.entryFor(ip)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastSeen = time.Now()
	return e.limiter.Allow()
}

// recordFailure increments the consecutive-failure counter for ip and returns
// the new count. The caller is expected to log the failure (with email) using
// the returned count to decide info-vs-warning severity.
func (rl *ipRateLimiter) recordFailure(ip string) int {
	e := rl.entryFor(ip)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failures++
	return e.failures
}

// recordSuccess resets the consecutive-failure counter for ip to 0. A user
// who mistypes their password a few times and then authenticates successfully
// is back to a clean slate.
func (rl *ipRateLimiter) recordSuccess(ip string) {
	e := rl.entryFor(ip)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failures = 0
}

// cleanupLoop periodically evicts visitor entries that have been idle for
// longer than visitorIdleTTL. This prevents unbounded map growth from
// rotating client IPs (NAT pools, mobile networks, attackers cycling IPs).
//
// We acquire the entry's mutex while reading lastSeen so we don't race with
// a concurrent allow() update. Deleting an entry that an in-flight request
// still holds a pointer to is safe — that request will finish updating the
// stale entry and the next request will create a fresh one.
func (rl *ipRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(visitorCleanupPeriod)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-visitorIdleTTL)
		rl.visitors.Range(func(key, value any) bool {
			e := value.(*visitorEntry)
			e.mu.Lock()
			idle := e.lastSeen.Before(cutoff)
			e.mu.Unlock()
			if idle {
				rl.visitors.Delete(key)
			}
			return true
		})
	}
}

// clientIP extracts the originating client IP from an HTTP request.
//
// Behind a reverse proxy (the production deployment — ingress-nginx), the
// proxy sets X-Forwarded-For with the client IP as the leftmost entry,
// followed by each proxy that handled the request. We take the leftmost
// entry (the original client) and trim whitespace.
//
// When there is no X-Forwarded-For header (direct connection, e.g. local
// development or a health probe), we fall back to r.RemoteAddr and strip the
// port. If SplitHostPort fails (RemoteAddr had no port) we return it as-is.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For may contain a comma-separated list:
		// "client, proxy1, proxy2". The leftmost entry is the client.
		if idx := strings.Index(xff, ","); idx >= 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
