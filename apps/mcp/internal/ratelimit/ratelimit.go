// Package ratelimit holds the MCP server's in-memory, per-key token buckets
// and the trusted-proxy client-IP helper. It is shared by the /register
// limit (#4691, internal/metadata) and the /mcp limits (#4693,
// internal/server).
//
// Buckets live in memory in each pod. That is deliberate: there is one MCP
// replica, and with N replicas every budget is simply N times larger, still
// bounded. Nothing is shared or persisted, and a restart refills the buckets.
package ratelimit

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

const (
	// IdleTTL is how long an entry must be idle (and its bucket full)
	// before it is dropped. A full bucket behaves exactly like a missing
	// entry, so eviction never shortens anyone's wait.
	IdleTTL = 5 * time.Minute
	// SweepEvery is how often, at most, an insert sweeps idle entries.
	SweepEvery = time.Minute
	// MaxKeys bounds each Keyed map. If it is still full after a sweep, new
	// keys are refused (fail closed): a flood of distinct keys that large is
	// an attack, not traffic.
	MaxKeys = 10000
)

type entry struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// Keyed is a set of token buckets, one per key (a client IP, an agent token
// id, ...), all with the same rate and burst. Idle, refilled buckets are
// swept so the map stays bounded. Safe for concurrent use.
type Keyed struct {
	limit rate.Limit
	burst int
	now   func() time.Time

	mu        sync.Mutex
	entries   map[string]*entry
	lastSweep time.Time
}

// Option configures a Keyed.
type Option func(*Keyed)

// WithClock replaces time.Now (tests).
func WithClock(now func() time.Time) Option { return func(k *Keyed) { k.now = now } }

// NewKeyed returns buckets that refill at limit and hold at most burst
// tokens.
func NewKeyed(limit rate.Limit, burst int, opts ...Option) *Keyed {
	k := &Keyed{limit: limit, burst: burst, now: time.Now, entries: map[string]*entry{}}
	for _, o := range opts {
		o(k)
	}
	return k
}

// PerMinute is the rate.Limit for n events a minute.
func PerMinute(n int) rate.Limit { return rate.Every(time.Minute / time.Duration(n)) }

// Allow spends one token from key's bucket. When none is left it returns
// false and how long until one is.
func (k *Keyed) Allow(key string) (bool, time.Duration) {
	now := k.now()
	k.mu.Lock()
	defer k.mu.Unlock()

	e, ok := k.entries[key]
	if !ok {
		if now.Sub(k.lastSweep) >= SweepEvery || len(k.entries) >= MaxKeys {
			k.sweepLocked(now)
		}
		if len(k.entries) >= MaxKeys {
			return false, SweepEvery
		}
		e = &entry{lim: rate.NewLimiter(k.limit, k.burst)}
		k.entries[key] = e
	}
	e.lastSeen = now
	if e.lim.AllowN(now, 1) {
		return true, 0
	}
	return false, WaitForToken(e.lim, now)
}

// Exhausted reports, without spending anything, whether key's bucket is
// empty, and if so how long until it holds a token. A key with no bucket
// yet is not exhausted (unless the map is full of active keys).
func (k *Keyed) Exhausted(key string) (bool, time.Duration) {
	now := k.now()
	k.mu.Lock()
	defer k.mu.Unlock()

	e, ok := k.entries[key]
	if !ok {
		if len(k.entries) >= MaxKeys {
			k.sweepLocked(now)
			if len(k.entries) >= MaxKeys {
				return true, SweepEvery
			}
		}
		return false, 0
	}
	if e.lim.TokensAt(now) >= 1 {
		return false, 0
	}
	return true, WaitForToken(e.lim, now)
}

// Len is the number of keys currently tracked.
func (k *Keyed) Len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.entries)
}

// sweepLocked drops entries idle since before now-IdleTTL whose bucket has
// refilled. k.mu must be held.
func (k *Keyed) sweepLocked(now time.Time) {
	cutoff := now.Add(-IdleTTL)
	for key, e := range k.entries {
		if e.lastSeen.Before(cutoff) && e.lim.TokensAt(now) >= float64(k.burst) {
			delete(k.entries, key)
		}
	}
	k.lastSweep = now
}

// WaitForToken is how long until lim holds one token.
func WaitForToken(lim *rate.Limiter, now time.Time) time.Duration {
	tokens := lim.TokensAt(now)
	if tokens >= 1 || lim.Limit() <= 0 {
		return 0
	}
	return time.Duration((1 - tokens) / float64(lim.Limit()) * float64(time.Second))
}

// RetryAfterSeconds renders a wait as a Retry-After value: whole seconds,
// rounded up, at least 1.
func RetryAfterSeconds(d time.Duration) int {
	return max(1, int(math.Ceil(d.Seconds())))
}

// ClientIP returns the rate-limit key for the client that sent r. Same
// algorithm as the API's ClientIP (apps/api/internal/middleware/ratelimit.go,
// #4685) and the calendar's.
//
// The TCP peer (r.RemoteAddr) is the only address the client can't forge.
// If it is not a trusted proxy (MCP_TRUSTED_PROXIES), it is the client and
// X-Forwarded-For is ignored. If it is, X-Forwarded-For is read right to
// left: each trusted proxy appended the address it received the request
// from, so the first entry that is not a trusted proxy is the real client.
// Entries to the left of it were supplied by the client and are never used.
// If every entry is trusted, the leftmost one is the client (an in-cluster
// caller). An unparsable entry stops the walk at the last trusted hop.
//
// In production the chain is client -> Cloudflare -> Traefik -> Envoy ->
// mcp. With the chart's trusted list (pod CIDR plus Cloudflare's edge
// ranges) this yields the address Cloudflare saw.
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
	if isTrusted(client, trusted) {
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
		if p, err := addr.Prefix(64); err == nil {
			return p.String()
		}
	}
	return addr.String()
}
