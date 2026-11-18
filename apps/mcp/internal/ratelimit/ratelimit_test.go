package ratelimit

import (
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestKeyed(perMinute int) (*Keyed, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	return NewKeyed(PerMinute(perMinute), perMinute, WithClock(clock.now)), clock
}

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
			if got := ClientIP(r, tt.trusted); got != tt.want {
				t.Errorf("ClientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKeyed_AllowAndIsolation(t *testing.T) {
	k, clock := newTestKeyed(5)
	for i := range 5 {
		if ok, _ := k.Allow("a"); !ok {
			t.Fatalf("request %d denied within burst", i+1)
		}
	}
	ok, wait := k.Allow("a")
	if ok {
		t.Fatal("6th request allowed past the burst")
	}
	if wait <= 0 || wait > 12*time.Second {
		t.Errorf("wait = %v, want (0, 12s] at 5/min", wait)
	}
	if ok, _ := k.Allow("b"); !ok {
		t.Error("another key was limited by the first one's bucket")
	}
	clock.advance(12 * time.Second)
	if ok, _ := k.Allow("a"); !ok {
		t.Error("no token after one refill interval")
	}
}

func TestKeyed_ExhaustedDoesNotSpend(t *testing.T) {
	k, _ := newTestKeyed(2)
	if ex, _ := k.Exhausted("a"); ex {
		t.Fatal("unknown key reported exhausted")
	}
	for range 10 {
		k.Exhausted("a")
	}
	if k.Len() != 0 {
		t.Error("Exhausted created an entry")
	}
	k.Allow("a")
	k.Allow("a")
	ex, wait := k.Exhausted("a")
	if !ex || wait <= 0 {
		t.Errorf("Exhausted = %v, %v after draining; want true, >0", ex, wait)
	}
}

func TestKeyed_EvictsIdleFullBuckets(t *testing.T) {
	k, clock := newTestKeyed(5)

	k.Allow("198.51.100.7") // will refill and go idle
	for range 6 {
		k.Allow("203.0.113.9") // stays empty
	}
	clock.advance(IdleTTL - 30*time.Second)
	for range 6 {
		k.Allow("203.0.113.9") // keep it drained and recent
	}
	clock.advance(time.Minute)
	k.Allow("192.0.2.1") // a new key triggers the sweep

	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.entries["198.51.100.7"]; ok {
		t.Error("idle, refilled entry was not evicted")
	}
	if _, ok := k.entries["203.0.113.9"]; !ok {
		t.Error("recently used entry was evicted")
	}
}

func TestKeyed_MapIsBounded(t *testing.T) {
	k, _ := newTestKeyed(5)
	for i := range MaxKeys {
		k.entries[netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}).String()] = &entry{
			lim: rate.NewLimiter(k.limit, k.burst), lastSeen: k.now(),
		}
	}
	if ok, _ := k.Allow("203.0.113.9"); ok {
		t.Error("new key admitted while the map is full of active entries")
	}
	if ex, _ := k.Exhausted("203.0.113.9"); !ex {
		t.Error("Exhausted = false for a new key while the map is full")
	}
	if k.Len() > MaxKeys {
		t.Errorf("map grew past MaxKeys: %d", k.Len())
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	for _, tt := range []struct {
		d    time.Duration
		want int
	}{{0, 1}, {200 * time.Millisecond, 1}, {12 * time.Second, 12}, {12*time.Second + time.Millisecond, 13}} {
		if got := RetryAfterSeconds(tt.d); got != tt.want {
			t.Errorf("RetryAfterSeconds(%v) = %d, want %d", tt.d, got, tt.want)
		}
	}
}
