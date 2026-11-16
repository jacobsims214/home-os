package metadata

// Rate limiting for dynamic client registration (#4691).
//
// /register (public at /oauth/register) is unauthenticated by design
// (RFC 7591 open registration), and every accepted request creates a
// permanent public client in Dex's storage. Two token buckets bound it:
//
//   - per client IP: MCP_REGISTER_PER_MINUTE (default 5/min, burst the
//     same), spent by every POST before the body is read, so junk requests
//     cost the sender too;
//   - global: MCP_REGISTER_PER_HOUR (default 60/hour, burst the same),
//     spent only by requests that passed validation and are about to reach
//     Dex, so one noisy IP sending junk can't use up everyone's budget,
//     while a spread-out (many-IP) flood still creates at most that many
//     Dex clients an hour.
//
// Past either budget the handler answers 429 with Retry-After and a JSON
// error body, before anything reaches Dex.
//
// The buckets live in memory in each pod. That is deliberate and acceptable
// here: there is one MCP replica, and with N replicas the effective budget
// is simply N times larger, still bounded. Nothing is shared or persisted,
// and a restart refills the buckets.
//
// The client IP comes from clientIP, which matches the API's ClientIP
// (apps/api/internal/middleware/ratelimit.go, #4685) and the calendar's:
// X-Forwarded-For is believed only when the TCP peer is a trusted proxy
// (MCP_TRUSTED_PROXIES), and then the rightmost untrusted hop is the client.

import (
	"math"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"home-os/mcp/internal/config"
)

const (
	// visitorIdleTTL is how long a per-IP entry must be idle (and its bucket
	// full) before it is dropped. A full bucket behaves exactly like a
	// missing entry, so eviction never shortens anyone's wait.
	visitorIdleTTL = 5 * time.Minute
	// sweepEvery is how often, at most, an insert sweeps idle entries.
	sweepEvery = time.Minute
	// maxVisitors bounds the per-IP map. If it is still full after a sweep,
	// new IPs are refused (fail closed): Dex is what we are protecting, and
	// the global bucket would refuse a flood that large anyway.
	maxVisitors = 10000
)

type visitor struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// registerLimiter holds the per-IP and global /register buckets. Safe for
// concurrent use.
type registerLimiter struct {
	trusted []netip.Prefix
	perIP   rate.Limit
	burst   int
	global  *rate.Limiter
	now     func() time.Time

	mu        sync.Mutex
	visitors  map[string]*visitor
	lastSweep time.Time
}

func newRegisterLimiter(cfg *config.Config) *registerLimiter {
	perMinute := cfg.RegisterPerMinute
	if perMinute <= 0 {
		perMinute = config.DefaultRegisterPerMinute
	}
	perHour := cfg.RegisterPerHour
	if perHour <= 0 {
		perHour = config.DefaultRegisterPerHour
	}
	return &registerLimiter{
		trusted:  cfg.TrustedProxies,
		perIP:    rate.Every(time.Minute / time.Duration(perMinute)),
		burst:    perMinute,
		global:   rate.NewLimiter(rate.Every(time.Hour/time.Duration(perHour)), perHour),
		now:      time.Now,
		visitors: map[string]*visitor{},
	}
}

// allowIP spends one token from key's bucket. When none is left it returns
// false and how long until one is.
func (l *registerLimiter) allowIP(key string) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	v, ok := l.visitors[key]
	if !ok {
		if now.Sub(l.lastSweep) >= sweepEvery || len(l.visitors) >= maxVisitors {
			l.sweepLocked(now)
		}
		if len(l.visitors) >= maxVisitors {
			return false, sweepEvery
		}
		v = &visitor{lim: rate.NewLimiter(l.perIP, l.burst)}
		l.visitors[key] = v
	}
	v.lastSeen = now
	if v.lim.AllowN(now, 1) {
		return true, 0
	}
	return false, waitForToken(v.lim, now)
}

// allowGlobal spends one token from the global bucket.
func (l *registerLimiter) allowGlobal() (bool, time.Duration) {
	now := l.now()
	if l.global.AllowN(now, 1) {
		return true, 0
	}
	return false, waitForToken(l.global, now)
}

// sweepLocked drops entries idle since before now-visitorIdleTTL whose
// bucket has refilled. l.mu must be held.
func (l *registerLimiter) sweepLocked(now time.Time) {
	cutoff := now.Add(-visitorIdleTTL)
	for k, v := range l.visitors {
		if v.lastSeen.Before(cutoff) && v.lim.TokensAt(now) >= float64(l.burst) {
			delete(l.visitors, k)
		}
	}
	l.lastSweep = now
}

// waitForToken is how long until lim holds one token.
func waitForToken(lim *rate.Limiter, now time.Time) time.Duration {
	tokens := lim.TokensAt(now)
	if tokens >= 1 || lim.Limit() <= 0 {
		return 0
	}
	return time.Duration((1 - tokens) / float64(lim.Limit()) * float64(time.Second))
}

// retryAfterSeconds renders a wait as a Retry-After value: whole seconds,
// rounded up, at least 1.
func retryAfterSeconds(d time.Duration) int {
	return max(1, int(math.Ceil(d.Seconds())))
}

// clientIP returns the rate-limit key for the client that sent r. Same
// algorithm as the API's ClientIP (#4685).
//
// The TCP peer (r.RemoteAddr) is the only address the client can't forge.
// If it is not a trusted proxy, it is the client and X-Forwarded-For is
// ignored. If it is, X-Forwarded-For is read right to left: each trusted
// proxy appended the address it received the request from, so the first
// entry that is not a trusted proxy is the real client. Entries to the left
// of it were supplied by the client and are never used. If every entry is
// trusted, the leftmost one is the client (an in-cluster caller). An
// unparsable entry stops the walk at the last trusted hop.
//
// In production the chain is client -> Cloudflare -> Traefik -> Envoy ->
// mcp. With the chart's trusted list (pod CIDR plus Cloudflare's edge
// ranges) this yields the address Cloudflare saw.
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
