package secret

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// Wrong master-password attempts are limited per household: after
// verifyMaxFailures wrong /verify calls within verifyFailWindow, further
// /verify calls answer 429 until the window that began with the first failure
// ends. A correct password clears the count. The check runs before argon2id,
// so a locked household also costs the server nothing.
//
// The limiter is in memory, per API process. With N replicas an attacker
// gets at most N x verifyMaxFailures guesses per window, each still costing
// PBKDF2 (100k, client side) plus argon2id.
const (
	verifyMaxFailures = 10
	verifyFailWindow  = 15 * time.Minute
	// limiterPruneAt is the map size at which expired entries are dropped.
	limiterPruneAt = 4096
)

type failWindow struct {
	start time.Time
	count int
}

type failLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	max     int
	window  time.Duration
	entries map[uuid.UUID]*failWindow
}

func newFailLimiter() *failLimiter {
	return &failLimiter{
		now: time.Now, max: verifyMaxFailures, window: verifyFailWindow,
		entries: map[uuid.UUID]*failWindow{},
	}
}

// retryAfter returns how long the household must wait, or 0 when it may try.
func (l *failLimiter) retryAfter(hid uuid.UUID) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[hid]
	if e == nil {
		return 0
	}
	elapsed := l.now().Sub(e.start)
	if elapsed >= l.window {
		delete(l.entries, hid)
		return 0
	}
	if e.count < l.max {
		return 0
	}
	return l.window - elapsed
}

// fail records one wrong attempt.
func (l *failLimiter) fail(hid uuid.UUID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.entries[hid]
	if e == nil || now.Sub(e.start) >= l.window {
		if len(l.entries) >= limiterPruneAt {
			for k, v := range l.entries {
				if now.Sub(v.start) >= l.window {
					delete(l.entries, k)
				}
			}
		}
		l.entries[hid] = &failWindow{start: now, count: 1}
		return
	}
	e.count++
}

// reset clears the household's count after a correct password.
func (l *failLimiter) reset(hid uuid.UUID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, hid)
}
