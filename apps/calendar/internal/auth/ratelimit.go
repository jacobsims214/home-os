// Package auth provides Basic Authentication middleware for the CalDAV service.
//
// This file implements the rate limiting in front of the credential check.
// Every password check runs a cost-12 bcrypt compare (~250 ms of CPU), so an
// unthrottled caller could both brute-force app passwords and pin the CPU.
// Three token buckets guard it:
//
//   - requests: every request that reaches the credential check, per client
//     IP (default 120/min, burst 60). CalDAV clients sync in bursts of
//     PROPFIND/REPORT requests, each of which re-authenticates, so this one is
//     deliberately generous.
//   - ipFailures: failed password checks per client IP (default 10/min, burst
//     10). Only failures that ran bcrypt (unknown email, deactivated account,
//     no app password, wrong password) spend a token; a missing or malformed
//     Authorization header costs nothing and is what clients send first to
//     get the 401 challenge.
//   - accountFailures: failed password checks per account email (default
//     30/hour, burst 30), so an attacker spreading guesses over many IPs still
//     gets a bounded number per account. A successful login refills it.
//
// When a bucket is empty the middleware answers 429 with Retry-After before
// parsing credentials further, looking the user up or running bcrypt.
//
// The client IP comes from clientIP: X-Forwarded-For is only believed when
// the direct peer is a configured trusted proxy, and then the rightmost
// untrusted hop is used. The leftmost entry is whatever the client sent and
// must never be used as a rate-limit key.
package auth

import (
	"math"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimitConfig sets the auth rate limits and the proxies whose
// X-Forwarded-For entries are trusted. Zero or negative rates and bursts are
// replaced by the defaults (see DefaultRateLimitConfig).
type RateLimitConfig struct {
	// TrustedProxies are the networks of reverse proxies in front of the
	// service (in Kubernetes: the pod CIDR, which covers Envoy and the
	// ingress controller). Empty means X-Forwarded-For is ignored and the
	// TCP peer address is the client.
	TrustedProxies []netip.Prefix

	// RequestsPerMinute / RequestBurst bound all requests per client IP that
	// reach the credential check.
	RequestsPerMinute int
	RequestBurst      int

	// IPFailuresPerMinute / IPFailureBurst bound failed password checks per
	// client IP.
	IPFailuresPerMinute int
	IPFailureBurst      int

	// AccountFailuresPerHour / AccountFailureBurst bound failed password
	// checks per account email, across all IPs.
	AccountFailuresPerHour int
	AccountFailureBurst    int
}

// Default limits.
const (
	DefaultRequestsPerMinute      = 120
	DefaultRequestBurst           = 60
	DefaultIPFailuresPerMinute    = 10
	DefaultIPFailureBurst         = 10
	DefaultAccountFailuresPerHour = 30
	DefaultAccountFailureBurst    = 30
)

// DefaultRateLimitConfig returns the default limits with no trusted proxies.
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		RequestsPerMinute:      DefaultRequestsPerMinute,
		RequestBurst:           DefaultRequestBurst,
		IPFailuresPerMinute:    DefaultIPFailuresPerMinute,
		IPFailureBurst:         DefaultIPFailureBurst,
		AccountFailuresPerHour: DefaultAccountFailuresPerHour,
		AccountFailureBurst:    DefaultAccountFailureBurst,
	}
}

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

const (
	// failureLogThreshold is the consecutive-failure count at which failures
	// are logged at warning level instead of info.
	failureLogThreshold = 3
	// visitorIdleTTL is how long an entry must be idle (and its bucket full)
	// before it is evicted.
	visitorIdleTTL       = 5 * time.Minute
	visitorCleanupPeriod = time.Minute
	// maxAccountKeyLen caps the account key so a huge username can't make a
	// huge map key. Longer names share a bucket by their prefix.
	maxAccountKeyLen = 320
)

// limiterEntry is the state for one key of a keyedLimiter. mu guards every
// field. failures is the consecutive-failure count used for log severity.
type limiterEntry struct {
	mu       sync.Mutex
	lim      *rate.Limiter
	failures int
	lastSeen time.Time
}

// keyedLimiter is a set of token buckets, one per key, safe for concurrent
// use.
type keyedLimiter struct {
	limit   rate.Limit
	burst   int
	entries sync.Map // map[string]*limiterEntry
}

func newKeyedLimiter(limit rate.Limit, burst int) *keyedLimiter {
	return &keyedLimiter{limit: limit, burst: burst}
}

// entry returns key's entry, creating one with a full bucket.
func (k *keyedLimiter) entry(key string, now time.Time) *limiterEntry {
	if v, ok := k.entries.Load(key); ok {
		return v.(*limiterEntry)
	}
	fresh := &limiterEntry{lim: rate.NewLimiter(k.limit, k.burst), lastSeen: now}
	actual, _ := k.entries.LoadOrStore(key, fresh)
	return actual.(*limiterEntry)
}

// retryAfter is how long until the bucket holds one token, given its current
// token count.
func (k *keyedLimiter) retryAfter(tokens float64) time.Duration {
	if tokens >= 1 || k.limit <= 0 {
		return 0
	}
	return time.Duration((1 - tokens) / float64(k.limit) * float64(time.Second))
}

// take spends one token for key. When none is available it returns false and
// how long until one is.
func (k *keyedLimiter) take(key string, now time.Time) (bool, time.Duration) {
	e := k.entry(key, now)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastSeen = now
	if e.lim.AllowN(now, 1) {
		return true, 0
	}
	return false, k.retryAfter(e.lim.TokensAt(now))
}

// blocked reports whether key's bucket is empty, without spending a token.
// An unknown key is never blocked and no entry is created for it, so checks
// alone can't grow the map.
func (k *keyedLimiter) blocked(key string, now time.Time) (bool, time.Duration) {
	v, ok := k.entries.Load(key)
	if !ok {
		return false, 0
	}
	e := v.(*limiterEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	tokens := e.lim.TokensAt(now)
	if tokens >= 1 {
		return false, 0
	}
	return true, k.retryAfter(tokens)
}

// fail spends a token for key if one is left (a failure that empties the
// bucket blocks the next attempt) and returns the consecutive-failure count.
func (k *keyedLimiter) fail(key string, now time.Time) int {
	e := k.entry(key, now)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastSeen = now
	e.lim.AllowN(now, 1)
	e.failures++
	return e.failures
}

// countFailure bumps the consecutive-failure count without spending a token.
func (k *keyedLimiter) countFailure(key string, now time.Time) int {
	e := k.entry(key, now)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastSeen = now
	e.failures++
	return e.failures
}

// resetCount zeroes key's consecutive-failure count; the bucket is unchanged.
func (k *keyedLimiter) resetCount(key string) {
	if v, ok := k.entries.Load(key); ok {
		e := v.(*limiterEntry)
		e.mu.Lock()
		e.failures = 0
		e.mu.Unlock()
	}
}

// forget drops key, which refills its bucket.
func (k *keyedLimiter) forget(key string) {
	k.entries.Delete(key)
}

// evictIdle removes entries idle since before cutoff whose bucket has
// refilled. A refilled entry behaves exactly like a missing one, so eviction
// never lets anyone skip a wait, however slowly the bucket refills.
func (k *keyedLimiter) evictIdle(now, cutoff time.Time) {
	k.entries.Range(func(key, value any) bool {
		e := value.(*limiterEntry)
		e.mu.Lock()
		idle := e.lastSeen.Before(cutoff) && e.lim.TokensAt(now) >= float64(k.burst)
		e.mu.Unlock()
		if idle {
			k.entries.Delete(key)
		}
		return true
	})
}

// authLimiter holds the three auth buckets and the trusted-proxy list.
type authLimiter struct {
	trusted         []netip.Prefix
	requests        *keyedLimiter // per client IP, every request
	ipFailures      *keyedLimiter // per client IP, failed password checks
	accountFailures *keyedLimiter // per account email, failed password checks
}

// newAuthLimiter builds the limiter without starting the cleanup goroutine
// (tests use it directly).
func newAuthLimiter(cfg RateLimitConfig) *authLimiter {
	rpm := orDefault(cfg.RequestsPerMinute, DefaultRequestsPerMinute)
	fpm := orDefault(cfg.IPFailuresPerMinute, DefaultIPFailuresPerMinute)
	fph := orDefault(cfg.AccountFailuresPerHour, DefaultAccountFailuresPerHour)
	return &authLimiter{
		trusted:         cfg.TrustedProxies,
		requests:        newKeyedLimiter(rate.Every(time.Minute/time.Duration(rpm)), orDefault(cfg.RequestBurst, DefaultRequestBurst)),
		ipFailures:      newKeyedLimiter(rate.Every(time.Minute/time.Duration(fpm)), orDefault(cfg.IPFailureBurst, DefaultIPFailureBurst)),
		accountFailures: newKeyedLimiter(rate.Every(time.Hour/time.Duration(fph)), orDefault(cfg.AccountFailureBurst, DefaultAccountFailureBurst)),
	}
}

// startAuthLimiter builds the limiter and starts its cleanup goroutine, which
// runs for the life of the process (the middleware is built once).
func startAuthLimiter(cfg RateLimitConfig) *authLimiter {
	l := newAuthLimiter(cfg)
	go func() {
		ticker := time.NewTicker(visitorCleanupPeriod)
		defer ticker.Stop()
		for now := range ticker.C {
			l.evictIdle(now)
		}
	}()
	return l
}

func (l *authLimiter) evictIdle(now time.Time) {
	cutoff := now.Add(-visitorIdleTTL)
	l.requests.evictIdle(now, cutoff)
	l.ipFailures.evictIdle(now, cutoff)
	l.accountFailures.evictIdle(now, cutoff)
}

// accountKey normalises an email for the per-account bucket: case and
// surrounding space don't make a new bucket.
func accountKey(email string) string {
	k := strings.ToLower(strings.TrimSpace(email))
	if len(k) > maxAccountKeyLen {
		k = k[:maxAccountKeyLen]
	}
	return k
}

// retryAfterSeconds renders a wait as a Retry-After value: whole seconds,
// rounded up, at least 1.
func retryAfterSeconds(d time.Duration) int {
	s := int(math.Ceil(d.Seconds()))
	if s < 1 {
		s = 1
	}
	return s
}

// clientIP returns the rate-limit key for the client that sent r.
//
// The TCP peer (r.RemoteAddr) is the only address the client can't forge.
// If it is not a trusted proxy, it is the client and X-Forwarded-For is
// ignored. If it is, X-Forwarded-For is read right to left: each trusted
// proxy appended the address of the peer it received the request from, so
// the first entry that is not a trusted proxy is the real client. Entries to
// the left of it were supplied by the client and are never used. If every
// entry is a trusted proxy, the leftmost one is the client (an in-cluster
// caller). An unparsable entry stops the walk at the last trusted hop.
//
// In production the chain is client -> Traefik -> Envoy -> calendar, Traefik
// and Envoy both in the pod CIDR, so TrustedProxies = pod CIDR yields the
// address Traefik saw.
//
// IPv6 clients are keyed by their /64, since one host usually controls a
// whole /64 and could otherwise rotate addresses to get fresh buckets.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	client := peer
	if isTrusted(peer, trusted) {
		hops := forwardedFor(r)
		for i := len(hops) - 1; i >= 0; i-- {
			addr, err := netip.ParseAddr(hops[i])
			if err != nil {
				break
			}
			client = addr.Unmap()
			if !isTrusted(client, trusted) {
				break
			}
		}
	}
	return ipKey(client)
}

// forwardedFor returns every X-Forwarded-For entry in order, across repeated
// headers, trimmed (zone suffixes and brackets are not valid in XFF and make
// the entry unparsable).
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

func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// ipKey is the bucket key for addr: the address for IPv4, the /64 for IPv6.
func ipKey(addr netip.Addr) string {
	if addr.Is6() {
		p, err := addr.Prefix(64)
		if err == nil {
			return p.String()
		}
	}
	return addr.String()
}

// ParseTrustedProxies parses a comma-separated list of CIDRs or bare IPs
// (a bare IP is a /32 or /128). Empty input gives an empty list.
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
				return nil, err
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, err
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}
