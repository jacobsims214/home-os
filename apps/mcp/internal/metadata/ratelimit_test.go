package metadata

import (
	"testing"
	"time"

	"home-os/mcp/internal/config"
)

// fakeClock is a settable time source for the limiter.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(perMinute, perHour int) (*registerLimiter, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := newRegisterLimiterAt(&config.Config{RegisterPerMinute: perMinute, RegisterPerHour: perHour}, clock.now)
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
