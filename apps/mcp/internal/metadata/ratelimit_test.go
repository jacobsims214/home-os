package metadata

import (
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"home-os/mcp/internal/config"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{
		netip.MustParsePrefix("10.244.0.0/16"),
		netip.MustParsePrefix("173.245.48.0/20"), // a Cloudflare range
	}
	tests := []struct {
		name    string
		remote  string
		xff     []string
		trusted []netip.Prefix
		want    string
	}{
		{"no proxies trusted: peer, XFF ignored", "198.51.100.7:4000", []string{"203.0.113.9"}, nil, "198.51.100.7"},
		{"untrusted peer: XFF ignored", "198.51.100.7:4000", []string{"203.0.113.9"}, trusted, "198.51.100.7"},
		{"trusted peer, no XFF: peer", "10.244.1.5:4000", nil, trusted, "10.244.1.5"},
		{"trusted chain: rightmost untrusted hop", "10.244.1.5:4000", []string{"203.0.113.9, 173.245.48.10, 10.244.2.2"}, trusted, "203.0.113.9"},
		{"spoofed leftmost entry ignored", "10.244.1.5:4000", []string{"1.2.3.4, 203.0.113.9, 10.244.2.2"}, trusted, "203.0.113.9"},
		{"repeated headers are concatenated", "10.244.1.5:4000", []string{"1.2.3.4", "203.0.113.9", "10.244.2.2"}, trusted, "203.0.113.9"},
		{"all trusted: leftmost", "10.244.1.5:4000", []string{"10.244.3.3, 10.244.2.2"}, trusted, "10.244.3.3"},
		{"unparsable entry stops the walk", "10.244.1.5:4000", []string{"203.0.113.9, garbage, 10.244.2.2"}, trusted, "10.244.2.2"},
		{"ipv6 keyed by /64", "[2001:db8:1:2:aaaa::1]:4000", nil, nil, "2001:db8:1:2::/64"},
		{"ipv4-mapped peer unmapped", "[::ffff:198.51.100.7]:4000", nil, nil, "198.51.100.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/register", nil)
			r.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := clientIP(r, tt.trusted); got != tt.want {
				t.Errorf("clientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

// fakeClock is a settable time source for the limiter.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(perMinute, perHour int) (*registerLimiter, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := newRegisterLimiter(&config.Config{RegisterPerMinute: perMinute, RegisterPerHour: perHour})
	l.now = clock.now
	return l, clock
}

func TestRegisterLimiter_PerIP(t *testing.T) {
	l, clock := newTestLimiter(5, 60)

	for i := range 5 {
		if ok, _ := l.allowIP("198.51.100.7"); !ok {
			t.Fatalf("request %d denied within burst", i+1)
		}
	}
	ok, wait := l.allowIP("198.51.100.7")
	if ok {
		t.Fatal("6th request allowed past the burst")
	}
	if wait <= 0 || wait > 12*time.Second {
		t.Errorf("wait = %v, want (0, 12s] at 5/min", wait)
	}

	if ok, _ := l.allowIP("203.0.113.9"); !ok {
		t.Error("another IP was limited by the first one's bucket")
	}

	clock.advance(12 * time.Second)
	if ok, _ := l.allowIP("198.51.100.7"); !ok {
		t.Error("no token after one refill interval")
	}
	if ok, _ := l.allowIP("198.51.100.7"); ok {
		t.Error("more than one token after one refill interval")
	}
}

func TestRegisterLimiter_Global(t *testing.T) {
	l, clock := newTestLimiter(5, 2)

	for i := range 2 {
		if ok, _ := l.allowGlobal(); !ok {
			t.Fatalf("global request %d denied within burst", i+1)
		}
	}
	ok, wait := l.allowGlobal()
	if ok {
		t.Fatal("global request allowed past the ceiling")
	}
	if wait <= 0 || wait > 30*time.Minute {
		t.Errorf("wait = %v, want (0, 30m] at 2/hour", wait)
	}
	clock.advance(30 * time.Minute)
	if ok, _ := l.allowGlobal(); !ok {
		t.Error("no global token after one refill interval")
	}
}

func TestRegisterLimiter_EvictsIdleFullBuckets(t *testing.T) {
	l, clock := newTestLimiter(5, 60)

	l.allowIP("198.51.100.7") // will refill and go idle
	for range 6 {
		l.allowIP("203.0.113.9") // stays empty
	}
	clock.advance(visitorIdleTTL - 30*time.Second)
	for range 6 {
		l.allowIP("203.0.113.9") // keep it drained and recent
	}
	clock.advance(time.Minute)
	l.allowIP("192.0.2.1") // a new key triggers the sweep

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.visitors["198.51.100.7"]; ok {
		t.Error("idle, refilled entry was not evicted")
	}
	if _, ok := l.visitors["203.0.113.9"]; !ok {
		t.Error("recently used entry was evicted")
	}
}

func TestRegisterLimiter_MapIsBounded(t *testing.T) {
	l, _ := newTestLimiter(5, 60)
	for i := range maxVisitors {
		l.visitors[netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}).String()] = &visitor{
			lim: rate.NewLimiter(l.perIP, l.burst), lastSeen: l.now(),
		}
	}
	if ok, _ := l.allowIP("203.0.113.9"); ok {
		t.Error("new IP admitted while the map is full of active entries")
	}
	if len(l.visitors) > maxVisitors {
		t.Errorf("map grew past maxVisitors: %d", len(l.visitors))
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	for _, tt := range []struct {
		d    time.Duration
		want int
	}{{0, 1}, {200 * time.Millisecond, 1}, {12 * time.Second, 12}, {12*time.Second + time.Millisecond, 13}} {
		if got := retryAfterSeconds(tt.d); got != tt.want {
			t.Errorf("retryAfterSeconds(%v) = %d, want %d", tt.d, got, tt.want)
		}
	}
}
