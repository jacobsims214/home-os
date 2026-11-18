// Package alarms turns VALARMs in stored calendar events into in-app
// notifications.
//
// Every tick the job reads the calendar_objects rows that carry full
// iCalendar text (calendar_objects.ical_raw, written by CalDAV since
// migration 036), asks packages/icalx which alarms fired in the window
// (now-Lookback, now], and inserts one `calendar_alarm` notification per
// household member who may see the event for each firing.
//
// # Deduplication
//
// A firing is identified by (calendar object, occurrence, firing instant):
// the occurrence is its RECURRENCE-ID (or start, for a single event), and the
// firing instant distinguishes several alarms and REPEAT firings on the same
// occurrence. That key is hashed into a deterministic UUIDv5 and stored in
// notifications.dedupe_key (migration 056), which is unique per
// (user_id, dedupe_key), so a row is only inserted when the member has no
// notification for that firing yet. The notification's entity is the event
// itself (entity_type 'calendar_event', entity_id = the calendar object id),
// so a client can open the event from the reminder. Because the key lives in
// the notifications table itself, a restart
// (or a second replica, serialised by an advisory lock) never re-notifies a
// firing that was already delivered, however often the window overlaps it.
//
// Editing an event so its alarm fires at a new time produces a new key, and
// so a new notification, which is what a user who moved a reminder expects.
//
// # Who is told
//
// A reminder is about the event, so it goes only to members whose role may
// read that event: the same rule CalDAV (#4718), the API (#4807) and the MCP
// apply on their read paths. It is asked of the database, not repeated here:
// role_can_see_entity(role, kind) from migration 061 (the SQL copy of
// auth.CanSeeEntity) on both the event's kind and its calendar's kind, so a
// housesitter no longer receives a reminder for a bill (its name, amount and
// due date) or for a maintenance task, and a vendor linked to a vendors row
// (memberships.vendor_id, #4799) hears only about its own jobs. The kinds are
// derived exactly as apps/calendar/internal/db/access.go and
// apps/api/internal/calendar/access.go derive them (objectKindSQL,
// calendarKindSQL); change the three together.
//
// # Time zones
//
// Events with a TZID or a UTC time fire at their own instant. Floating and
// all-day events are placed in the household's time zone (households.timezone;
// UTC when unset or unknown), so a floating 09:00 event with a 30-minute alarm
// fires at 08:30 household time.
//
// # Bill events
//
// Bill mirror events on the Bills calendar (event_type 'bill', #4485) are left
// out. Their VALARM stays in ical_raw so phones keep their native reminder,
// but the in-app reminder for a bill belongs to the worker's bill_due job: it
// knows whether the bill is already paid (the mirror event does not change on
// bill_pay) and it already notifies the family 3 days before the due date, so
// an alarm notification here would be a duplicate, or a reminder for a paid
// bill. The match is on event_type, which every bill mirror writer sets.
package alarms

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/icalx"
)

const (
	// DefaultInterval is how often the job looks for alarms that fired.
	DefaultInterval = 30 * time.Second
	// DefaultLookback is how far back each run looks. It is much longer than
	// the interval so a slow run, a brief DB outage or a restart does not
	// drop a reminder; dedupe makes the overlap harmless. Alarms that fired
	// longer ago than this (for example during a long outage) are skipped on
	// purpose: a reminder for something that already started is noise.
	DefaultLookback = 15 * time.Minute

	// NotificationType is written on every alarm notification; EntityType
	// names what it is about (the calendar object), the same reference the
	// item_changed notifications of migration 054 use for events.
	NotificationType = "calendar_alarm"
	EntityType       = "calendar_event"

	// advisoryLockKey serialises runs across worker replicas.
	advisoryLockKey int64 = 0x616c61726d73 // "alarms"
)

// dedupeNamespace is the UUIDv5 namespace for alarm firing keys.
var dedupeNamespace = uuid.MustParse("6b1e2a8c-3f0d-4c55-9a51-2d1f0e7c4a10")

// Job finds fired alarms and writes notifications.
type Job struct {
	pool     *pgxpool.Pool
	interval time.Duration
	lookback time.Duration
	now      func() time.Time
}

// Option configures a Job.
type Option func(*Job)

// WithInterval sets the polling interval.
func WithInterval(d time.Duration) Option {
	return func(j *Job) {
		if d > 0 {
			j.interval = d
		}
	}
}

// WithLookback sets how far back each run looks for firings.
func WithLookback(d time.Duration) Option {
	return func(j *Job) {
		if d > 0 {
			j.lookback = d
		}
	}
}

// WithClock replaces time.Now (tests).
func WithClock(now func() time.Time) Option {
	return func(j *Job) { j.now = now }
}

// New returns a Job using pool.
func New(pool *pgxpool.Pool, opts ...Option) *Job {
	j := &Job{pool: pool, interval: DefaultInterval, lookback: DefaultLookback, now: time.Now}
	for _, o := range opts {
		o(j)
	}
	return j
}

// Run processes alarms every interval until ctx is cancelled, then returns
// ctx.Err(). A failed run is logged and retried on the next tick.
func (j *Job) Run(ctx context.Context) error {
	slog.Info("alarm job started", "interval", j.interval, "lookback", j.lookback)
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		if n, err := j.Process(ctx, j.now()); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Error("alarm job run failed", "error", err)
		} else if n > 0 {
			slog.Info("alarm notifications created", "count", n)
		}
		select {
		case <-ctx.Done():
			slog.Info("alarm job stopped")
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// candidate is a calendar object that may hold alarms.
type candidate struct {
	ObjectID    uuid.UUID
	HouseholdID uuid.UUID
	Timezone    string
	ICalRaw     string
}

// Firing is one alarm firing that should produce notifications.
type Firing struct {
	ObjectID    uuid.UUID
	HouseholdID uuid.UUID
	Key         uuid.UUID // dedupe key, stored as notifications.dedupe_key
	At          time.Time
	Title       string
	Body        string
}

// candidatesSQL selects objects with stored iCalendar text that mention a
// VALARM. Rows without ical_raw predate migration 036 and carry no alarms.
// Bill mirror events (event_type 'bill') are skipped: bill_due owns those
// reminders (see the package doc). Retired entries (superseded_at set, #4943:
// a bill is the record for them) are skipped too, or a phone alarm would still
// fire for the stale duplicate a briefing must not read.
const candidatesSQL = `
	SELECT co.id, c.household_id, h.timezone, co.ical_raw
	FROM calendar_objects co
	JOIN calendars c ON c.id = co.calendar_id
	JOIN households h ON h.id = c.household_id
	WHERE co.ical_raw IS NOT NULL
	  AND co.ical_raw ILIKE '%BEGIN:VALARM%'
	  AND co.event_type IS DISTINCT FROM 'bill'
	  AND co.superseded_at IS NULL
`

// calendarKindSQL maps a calendars row (aliased c) to a role_can_see_entity
// kind. Copied from apps/calendar/internal/db/access.go and
// apps/api/internal/calendar/access.go; keep the three in step.
const calendarKindSQL = `(CASE c.type::text
		WHEN 'bills' THEN 'bill'
		WHEN 'maintenance' THEN 'maintenance_task'
		WHEN 'vehicles' THEN 'vehicle'
		ELSE 'calendar_event' END)`

// objectKindSQL maps a calendar_objects row (aliased o) to a
// role_can_see_entity kind: an event is what it mirrors. Copied from
// apps/calendar/internal/db/access.go and
// apps/api/internal/calendar/access.go; keep the three in step.
const objectKindSQL = `(CASE
		WHEN o.event_type = 'bill' OR o.entity_type::text = 'bill' THEN 'bill'
		WHEN o.event_type = 'maintenance' OR o.entity_type::text IN ('maintenance_task', 'maintenance') THEN 'maintenance_task'
		ELSE 'calendar_event' END)`

// insertSQL writes one notification per household member who may see the
// event, for a firing about the event ($6), skipping members who already have
// a notification for this firing key ($7). The partial unique index
// idx_notifications_dedupe (migration 056) makes the skip race-free.
//
// The membership filter is the role policy (see the package doc):
// role_can_see_entity on the event's kind and on its calendar's kind, plus
// the vendor own-jobs narrowing of apps/calendar/internal/db/access.go
// objectVisibleSQL. A member the policy hides the event from gets no
// reminder: a housesitter is not told about a bill or a maintenance task,
// and a linked vendor only about its own jobs.
//
// Only active users are notified: a deactivated account (users.active = false,
// the flag auth and CalDAV already reject, migration 037) can no longer sign
// in, so a reminder it cannot act on would only pile up unread. The users
// JOIN also covers the case of a membership left behind by a deactivated user
// (#4873). The role filter above is not a substitute: a deactivated member
// whose role may see the event is still skipped, and a housesitter who is
// active is still filtered out by the role policy.
const insertSQL = `
	INSERT INTO notifications (household_id, user_id, type, title, body, entity_type, entity_id, dedupe_key)
	SELECT $1::uuid, m.user_id, $2::varchar, $3::varchar, $4::text, $5::varchar, $6::uuid, $7::text
	FROM memberships m
	JOIN users u ON u.id = m.user_id AND u.active
	JOIN calendar_objects o ON o.id = $6::uuid
	JOIN calendars c ON c.id = o.calendar_id
	WHERE m.household_id = $1::uuid
	  AND c.household_id = m.household_id
	  AND role_can_see_entity(m.role::text, ` + objectKindSQL + `)
	  AND role_can_see_entity(m.role::text, ` + calendarKindSQL + `)
	  AND (m.role <> 'vendor' OR m.vendor_id IS NULL
	       OR (o.entity_type::text = 'maintenance_task' AND EXISTS (
	           SELECT 1 FROM maintenance_tasks t
	            WHERE t.id = o.entity_id AND t.household_id = c.household_id
	              AND t.vendor_id = m.vendor_id)))
	ON CONFLICT (user_id, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING
`

// Process creates notifications for every alarm that fired in
// (now-lookback, now] and returns how many notification rows it inserted.
func (j *Job) Process(ctx context.Context, now time.Time) (int, error) {
	tx, err := j.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, advisoryLockKey); err != nil {
		return 0, fmt.Errorf("advisory lock: %w", err)
	}

	rows, err := tx.Query(ctx, candidatesSQL)
	if err != nil {
		return 0, fmt.Errorf("query calendar objects: %w", err)
	}
	cands, err := pgx.CollectRows(rows, pgx.RowToStructByPos[candidate])
	if err != nil {
		return 0, fmt.Errorf("collect calendar objects: %w", err)
	}

	var firings []Firing
	for _, c := range cands {
		fs, err := FiringsFor(c.ObjectID, c.HouseholdID, c.ICalRaw, householdLocation(c.Timezone), now, j.lookback)
		if err != nil {
			// One bad event must not stop everyone else's reminders.
			slog.Warn("alarm job: skipping calendar object", "calendar_object_id", c.ObjectID, "error", err)
			continue
		}
		firings = append(firings, fs...)
	}

	inserted := 0
	for _, f := range firings {
		tag, err := tx.Exec(ctx, insertSQL, f.HouseholdID, NotificationType, f.Title, f.Body, EntityType, f.ObjectID, f.Key.String())
		if err != nil {
			return 0, fmt.Errorf("insert alarm notification for %s: %w", f.ObjectID, err)
		}
		inserted += int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return inserted, nil
}

// FiringsFor parses one calendar object and returns its alarm firings in
// (now-lookback, now], with times and text in loc.
func FiringsFor(objectID, householdID uuid.UUID, raw string, loc *time.Location, now time.Time, lookback time.Duration) ([]Firing, error) {
	obj, err := icalx.Parse([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	// icalx windows are [from, to); shift by 1ns for (now-lookback, now].
	from := now.Add(-lookback).Add(time.Nanosecond)
	to := now.Add(time.Nanosecond)
	afs, err := obj.Alarms(from, to, loc)
	if err != nil {
		// Includes icalx.ErrTooManyOccurrences for runaway rules.
		return nil, fmt.Errorf("expand alarms: %w", err)
	}
	out := make([]Firing, 0, len(afs))
	seen := make(map[uuid.UUID]bool, len(afs))
	for _, af := range afs {
		key := DedupeKey(objectID, af.Occurrence, af.At)
		if seen[key] {
			continue // two alarms at the same instant: one reminder
		}
		seen[key] = true
		title, body := message(af, loc)
		out = append(out, Firing{
			ObjectID:    objectID,
			HouseholdID: householdID,
			Key:         key,
			At:          af.At,
			Title:       title,
			Body:        body,
		})
	}
	return out, nil
}

// DedupeKey identifies one firing of one alarm on one occurrence.
func DedupeKey(objectID uuid.UUID, occ icalx.Occurrence, at time.Time) uuid.UUID {
	instance := occ.RecurrenceID
	if instance.IsZero() {
		instance = occ.Start
	}
	name := fmt.Sprintf("%s|%d|%d", objectID, instance.UTC().Unix(), at.UTC().Unix())
	return uuid.NewSHA1(dedupeNamespace, []byte(name))
}

// householdLocation loads the household's IANA zone, falling back to UTC.
func householdLocation(tz string) *time.Location {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		slog.Warn("alarm job: unknown household timezone, using UTC", "timezone", tz)
		return time.UTC
	}
	return loc
}

// message builds the notification title and body for a firing.
func message(af icalx.AlarmFiring, loc *time.Location) (string, string) {
	ev := af.Occurrence.Event
	title := ""
	if ev != nil {
		title = strings.TrimSpace(ev.Summary)
	}
	if title == "" {
		title = strings.TrimSpace(af.Alarm.Summary)
	}
	if title == "" {
		title = "Upcoming event"
	}

	start := af.Occurrence.Start.In(loc)
	var when string
	if af.Occurrence.AllDay {
		when = "All day " + start.Format("Mon Jan 2")
	} else {
		when = "Starts " + start.Format("Mon Jan 2, 3:04 PM MST")
	}
	parts := []string{when}
	if ev != nil && strings.TrimSpace(ev.Location) != "" {
		parts = append(parts, "at "+strings.TrimSpace(ev.Location))
	}
	body := strings.Join(parts, " ")
	if d := strings.TrimSpace(af.Alarm.Description); d != "" && !strings.EqualFold(d, "reminder") && d != title {
		body = d + " — " + body
	}
	if r := []rune(title); len(r) > 500 { // notifications.title is VARCHAR(500)
		title = string(r[:500])
	}
	return title, body
}
