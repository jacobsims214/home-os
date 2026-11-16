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
// The buckets live in memory in each pod (see internal/ratelimit, shared
// with the /mcp limits of #4693). The client IP comes from
// ratelimit.ClientIP: X-Forwarded-For is believed only when the TCP peer is
// a trusted proxy (MCP_TRUSTED_PROXIES), and then the rightmost untrusted
// hop is the client.

import (
	"net/http"
	"net/netip"
	"time"

	"golang.org/x/time/rate"

	"home-os/mcp/internal/config"
	"home-os/mcp/internal/ratelimit"
)

// registerLimiter holds the per-IP and global /register buckets. Safe for
// concurrent use.
type registerLimiter struct {
	trusted []netip.Prefix
	perIP   *ratelimit.Keyed
	global  *rate.Limiter
	now     func() time.Time
}

func newRegisterLimiter(cfg *config.Config) *registerLimiter {
	return newRegisterLimiterAt(cfg, time.Now)
}

func newRegisterLimiterAt(cfg *config.Config, now func() time.Time) *registerLimiter {
	perMinute := cfg.RegisterPerMinute
	if perMinute <= 0 {
		perMinute = config.DefaultRegisterPerMinute
	}
	perHour := cfg.RegisterPerHour
	if perHour <= 0 {
		perHour = config.DefaultRegisterPerHour
	}
	return &registerLimiter{
		trusted: cfg.TrustedProxies,
		perIP:   ratelimit.NewKeyed(ratelimit.PerMinute(perMinute), perMinute, ratelimit.WithClock(now)),
		global:  rate.NewLimiter(rate.Every(time.Hour/time.Duration(perHour)), perHour),
		now:     now,
	}
}

// allowIP spends one token from key's bucket. When none is left it returns
// false and how long until one is.
func (l *registerLimiter) allowIP(key string) (bool, time.Duration) {
	return l.perIP.Allow(key)
}

// allowGlobal spends one token from the global bucket.
func (l *registerLimiter) allowGlobal() (bool, time.Duration) {
	now := l.now()
	if l.global.AllowN(now, 1) {
		return true, 0
	}
	return false, ratelimit.WaitForToken(l.global, now)
}

// clientIP is ratelimit.ClientIP (kept as a name for the handler).
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	return ratelimit.ClientIP(r, trusted)
}
