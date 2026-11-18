package secret

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// Wrong master-password attempts are limited at two levels:
//
//   - per member (household + user): after verifyMaxFailures wrong /verify
//     calls within verifyFailWindow, that member's further /verify calls answer
//     429 until the window that began with their first failure ends. A correct
//     password clears that member's count. One member therefore cannot lock the
//     rest of the family out of unlocking.
//   - per household: verifyHouseholdMaxFailures wrong attempts in total, from
//     all members, within verifyFailWindow lock the whole household. A correct
//     password does not clear this count, so the ceiling on guesses per
//     household stays no matter how many member accounts an attacker holds.
//
// Check and reservation are one atomic step: begin, under the mutex, refuses
// when failures + attempts already in flight reach a limit and otherwise
// reserves an in-flight slot on both levels. The slot then becomes a failure
// (fail), is cleared (succeed), or is given back (release) on every other exit
// path. Concurrent requests can therefore never evaluate more guesses than the
// limits allow; the check runs before argon2id, so a refused request also costs
// the server nothing.
//
// The limiter is in memory, per API process. With N replicas an attacker
// gets at most N x the limits per window, each guess still costing PBKDF2
// (100k, client side) plus argon2id.
const (
	verifyMaxFailures          = 10
	verifyHouseholdMaxFailures = 30
	verifyFailWindow           = 15 * time.Minute
	// limiterPruneAt is the map size at which idle entries are dropped.
	limiterPruneAt = 4096
	// inflightRetry is the Retry-After for a request refused only because
	// other attempts are still being evaluated; they finish within seconds.
	inflightRetry = time.Second
)

// limitKey identifies one counter: a member (household + user) or, with
// household set, the whole household.
type limitKey struct {
	hid       uuid.UUID
	uid       uuid.UUID
	household bool
}

type failWindow struct {
	start    time.Time // first failure of the current window; zero when count is 0
	count    int       // failures recorded in the window
	inflight int       // attempts reserved by begin and not yet resolved
}

type failLimiter struct {
	mu           sync.Mutex
	now          func() time.Time
	max          int // per member
	householdMax int
	window       time.Duration
	entries      map[limitKey]*failWindow
}

func newFailLimiter() *failLimiter {
	return &failLimiter{
		now: time.Now, max: verifyMaxFailures, householdMax: verifyHouseholdMaxFailures,
		window: verifyFailWindow, entries: map[limitKey]*failWindow{},
	}
}

// verifyAttempt is one reserved guess. Exactly one of fail, succeed or release
// takes effect; later calls are no-ops, so release can always be deferred.
type verifyAttempt struct {
	l      *failLimiter
	member limitKey
	house  limitKey
	done   bool
}

// entry returns the live entry for k, expiring a finished window. Callers
// hold l.mu.
func (l *failLimiter) entry(k limitKey, now time.Time) *failWindow {
	e := l.entries[k]
	if e == nil {
		return nil
	}
	if e.count > 0 && now.Sub(e.start) >= l.window {
		e.count, e.start = 0, time.Time{}
	}
	if e.count == 0 && e.inflight == 0 {
		delete(l.entries, k)
		return nil
	}
	return e
}

// wait reports how long k must wait before another attempt fits under limit,
// or 0 when one fits now. Callers hold l.mu.
func (l *failLimiter) wait(k limitKey, limit int, now time.Time) time.Duration {
	e := l.entry(k, now)
	if e == nil || e.count+e.inflight < limit {
		return 0
	}
	if e.count >= limit {
		return l.window - now.Sub(e.start)
	}
	return inflightRetry
}

// begin atomically checks the member and household limits and, when both
// allow it, reserves an in-flight attempt on each. On refusal it returns nil
// and how long to wait.
func (l *failLimiter) begin(hid, uid uuid.UUID) (*verifyAttempt, time.Duration) {
	member := limitKey{hid: hid, uid: uid}
	house := limitKey{hid: hid, household: true}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	w := l.wait(member, l.max, now)
	if hw := l.wait(house, l.householdMax, now); hw > w {
		w = hw
	}
	if w > 0 {
		return nil, w
	}
	if len(l.entries) >= limiterPruneAt {
		l.prune(now)
	}
	for _, k := range []limitKey{member, house} {
		e := l.entries[k]
		if e == nil {
			e = &failWindow{}
			l.entries[k] = e
		}
		e.inflight++
	}
	return &verifyAttempt{l: l, member: member, house: house}, 0
}

// prune drops idle entries. Callers hold l.mu.
func (l *failLimiter) prune(now time.Time) {
	for k := range l.entries {
		l.entry(k, now)
	}
}

// settle resolves the reservation on k: a failure is counted when failed.
// Callers hold l.mu.
func (l *failLimiter) settle(k limitKey, failed bool, now time.Time) {
	e := l.entries[k]
	if e == nil {
		return // cannot happen while a reservation is held
	}
	if e.inflight > 0 {
		e.inflight--
	}
	if failed {
		if e.count == 0 || now.Sub(e.start) >= l.window {
			e.start, e.count = now, 0
		}
		e.count++
	}
	l.entry(k, now)
}

func (a *verifyAttempt) finish(fn func(now time.Time)) {
	if a == nil || a.done {
		return
	}
	a.done = true
	a.l.mu.Lock()
	defer a.l.mu.Unlock()
	fn(a.l.now())
}

// fail records the attempt as a wrong password for the member and household.
func (a *verifyAttempt) fail() {
	a.finish(func(now time.Time) {
		a.l.settle(a.member, true, now)
		a.l.settle(a.house, true, now)
	})
}

// succeed clears the member's failure count after a correct password. The
// household's failures stay counted.
func (a *verifyAttempt) succeed() {
	a.finish(func(now time.Time) {
		if e := a.l.entries[a.member]; e != nil {
			e.count, e.start = 0, time.Time{}
		}
		a.l.settle(a.member, false, now)
		a.l.settle(a.house, false, now)
	})
}

// release gives the reservation back without counting it (server error,
// cancelled request). Safe to defer after fail or succeed.
func (a *verifyAttempt) release() {
	a.finish(func(now time.Time) {
		a.l.settle(a.member, false, now)
		a.l.settle(a.house, false, now)
	})
}
