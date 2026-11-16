// Package db provides PostgreSQL database access for the calendar service.
// It reads from the calendars and calendar_objects tables shared with the
// core API. The same DATABASE_URL environment variable is used.
package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// querier is the minimal subset of pgx operations used by the repo methods
// below. Both *pgxpool.Pool and pgx.Tx satisfy it, which lets the SQL for a
// given operation live in exactly one place while still being callable from
// either a connection pool or a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ETagGen generates a unique ETag value for a calendar object.
//
// It uses a randomly-generated UUID (v4), which has negligible collision
// probability and matches the ETag generation on the API side
// (apps/api/internal/calendar/repo.go). This is required for If-Match
// optimistic concurrency to be reliable: a previous implementation used
// time.Now().UnixNano() hex, which could collide on two writes landing in the
// same nanosecond (fast hardware, clock skew, NTP adjustments) and silently
// let a concurrent PUT pass If-Match against the wrong ETag.
func ETagGen() string {
	return uuid.New().String()
}

// CalDAVUser holds the user data needed for CalDAV Basic Auth.
type CalDAVUser struct {
	ID                 string
	Email              string
	CalDAVPasswordHash *string
	// Active mirrors users.active. A deactivated user (Active=false) must be
	// rejected by CalDAV auth even with a valid app password. The zero value
	// is false so a CalDAVUser built without it fails closed.
	Active bool
}

// GetUserByEmailForCalDAV looks up a user by email for CalDAV Basic Auth.
// Returns nil, nil if the user is not found.
//
// The match is case-insensitive (lower(email) = lower($1)), like the API's
// member resolver (apps/api/internal/auth/resolve.go): users.email keeps the
// capitalisation the account was created with, and phone clients send
// whatever the member typed. users.email's UNIQUE constraint is
// case-sensitive, so two rows could differ only by case. If one of them is an
// exact match it is used. If none is exact, the login is ambiguous and is
// refused (nil, nil, the same as an unknown email) rather than picking one.
func (r *Repo) GetUserByEmailForCalDAV(ctx context.Context, email string) (*CalDAVUser, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, email, caldav_password_hash, active
		FROM users
		WHERE lower(email) = lower($1)
		ORDER BY (email = $1) DESC, id
		LIMIT 2
	`, email)
	if err != nil {
		return nil, fmt.Errorf("db: get user by email for caldav: %w", err)
	}
	defer rows.Close()

	var found []CalDAVUser
	for rows.Next() {
		var u CalDAVUser
		if err := rows.Scan(&u.ID, &u.Email, &u.CalDAVPasswordHash, &u.Active); err != nil {
			return nil, fmt.Errorf("db: get user by email for caldav: %w", err)
		}
		found = append(found, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: get user by email for caldav: %w", err)
	}

	switch {
	case len(found) == 0:
		return nil, nil
	case found[0].Email == email || len(found) == 1:
		return &found[0], nil
	default:
		// Two or more accounts differ only by case and none matches exactly.
		return nil, nil
	}
}

// GetHouseholdIDForUser returns the user's default household: the one from
// their oldest membership (ties broken by household_id so the choice is
// stable). Returns empty string if the user has no memberships.
func (r *Repo) GetHouseholdIDForUser(ctx context.Context, userID string) (string, error) {
	ids, err := r.ListHouseholdIDsForUser(ctx, userID)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", nil
	}
	return ids[0], nil
}

// ListHouseholdIDsForUser returns every household the user belongs to, in a
// deterministic order: oldest membership first, ties broken by household_id.
// The first element is the user's default CalDAV household. Returns an empty
// slice if the user has no memberships.
func (r *Repo) ListHouseholdIDsForUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT household_id::text FROM memberships
		WHERE user_id = $1
		ORDER BY created_at ASC, household_id ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("db: list households for user: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("db: scan household id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: rows iteration: %w", err)
	}
	return ids, nil
}

// GetCalendarHouseholdID returns the household that owns the calendar with the
// given CalDAV UID (caldav_uid is globally unique). Returns empty string if no
// such calendar exists. Callers must check the result against the
// authenticated user's memberships before trusting it.
func (r *Repo) GetCalendarHouseholdID(ctx context.Context, caldavUID string) (string, error) {
	var householdID string
	err := r.pool.QueryRow(ctx, `
		SELECT household_id::text FROM calendars WHERE caldav_uid = $1
	`, caldavUID).Scan(&householdID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("db: get calendar household: %w", err)
	}
	return householdID, nil
}

// Calendar represents a row from the calendars table.
type Calendar struct {
	ID          string    `json:"id"`
	HouseholdID string    `json:"household_id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Color       *string   `json:"color,omitempty"`
	CalDAVUID   string    `json:"caldav_uid"`
	CTag        string    `json:"ctag"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// Writable is whether the caller's role may change this calendar and
	// its events (Access.CanWrite). It drives the advertised
	// current-user-privilege-set, so phones show read-only calendars as such.
	Writable bool `json:"-"`
}

// CalendarObject represents a row from the calendar_objects table.
type CalendarObject struct {
	ID         string    `json:"id"`
	CalendarID string    `json:"calendar_id"`
	UID        string    `json:"uid"`
	ICALData   string    `json:"ical_data"`
	ETag       string    `json:"etag"`
	EntityType *string   `json:"entity_type,omitempty"`
	EntityID   *string   `json:"entity_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// ICALRaw is the complete VCALENDAR text as received by CalDAV PUT
	// (migration 036). NULL for rows written before 036, and for rows written
	// by JSON-only writers (the API and MCP tools), which do not know about
	// it. When set and still in step with ICALData, CalDAV serves it verbatim
	// so RRULE, EXDATE, VALARM and other properties survive the round trip.
	ICALRaw *string `json:"ical_raw,omitempty"`
	// RRule is the event's RRULE value, extracted from ICALRaw at PUT time.
	RRule *string `json:"rrule,omitempty"`
}

// CalendarChangeAction is the type of mutation recorded against a calendar
// object. The string values match the CHECK constraint on the
// calendar_changes.action column and must never diverge from it.
type CalendarChangeAction string

const (
	// ChangeCreate is recorded when a new calendar object is inserted.
	ChangeCreate CalendarChangeAction = "create"
	// ChangeUpdate is recorded when an existing calendar object is updated.
	ChangeUpdate CalendarChangeAction = "update"
	// ChangeDelete is recorded when a calendar object is deleted. The
	// corresponding event_uid is no longer present in calendar_objects, so
	// sync-collection emits a 404 tombstone for it.
	ChangeDelete CalendarChangeAction = "delete"
)

// CalendarChange represents a row from the calendar_changes table. Each row
// records a single create/update/delete of a calendar object together with a
// globally-monotonic revision. The sync-collection REPORT (RFC 6578) uses
// these rows to compute the delta since a client's last sync-token.
type CalendarChange struct {
	ID         int64                `json:"id"`
	CalendarID string               `json:"calendar_id"`
	EventUID   string               `json:"event_uid"`
	Action     CalendarChangeAction `json:"action"`
	Revision   int64                `json:"revision"`
	CreatedAt  time.Time            `json:"created_at"`
}

// Repo provides database operations for the calendar service.
type Repo struct {
	pool *pgxpool.Pool
}

// New connects to PostgreSQL and returns a Repo. Call Close when done.
func New(ctx context.Context, databaseURL string) (*Repo, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return &Repo{pool: pool}, nil
}

// Close shuts down the connection pool.
func (r *Repo) Close() {
	r.pool.Close()
}

// Ping verifies the database connection is alive by round-tripping a lightweight
// SELECT 1 against the pool. It is used by the /ready endpoint to decide whether
// the service is ready to accept traffic: a 503 is returned when Ping fails so
// the load balancer can stop sending requests to this instance. /health stays
// shallow (always 200 when the process is up) and does not call Ping — that
// keeps liveness probes from cascading a DB outage into a pod restart storm.
func (r *Repo) Ping(ctx context.Context) error {
	return r.pool.Ping(ctx)
}

// ListCalendars returns all calendars belonging to the given household.
// householdID is the authenticated user's household — every row is filtered
// by it so a CalDAV credential from one household can never see another's
// calendars. An empty householdID returns no rows (defensive — should never
// happen because auth middleware rejects unauthenticated requests).
//
// Only calendars acc's role may read are returned (see access.go); the rest
// are absent from discovery.
func (r *Repo) ListCalendars(ctx context.Context, householdID string, acc Access) ([]Calendar, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, household_id, name, type, color, caldav_uid, ctag, created_at, updated_at
		FROM calendars c
		WHERE household_id = $1 AND `+calendarVisibleSQL("$2")+`
		ORDER BY created_at ASC
	`, householdID, acc.Role)
	if err != nil {
		return nil, fmt.Errorf("db: list calendars: %w", err)
	}
	defer rows.Close()

	var calendars []Calendar
	for rows.Next() {
		var c Calendar
		if err := rows.Scan(&c.ID, &c.HouseholdID, &c.Name, &c.Type, &c.Color, &c.CalDAVUID, &c.CTag, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("db: scan calendar: %w", err)
		}
		c.Writable = acc.CanWrite()
		calendars = append(calendars, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: rows iteration: %w", err)
	}
	return calendars, nil
}

// GetCalendarByCalDAVUID returns a single calendar by its CalDAV UID, but only
// if it belongs to the given household. This enforces tenant isolation: a
// credential from household A cannot resolve (and therefore cannot read/write)
// a calendar owned by household B.
// Returns nil, nil if not found OR if the calendar belongs to a different
// household — callers treat both the same (404), which avoids leaking the
// existence of cross-tenant calendars. A calendar acc's role may not read is
// likewise nil, nil (absent, not forbidden).
func (r *Repo) GetCalendarByCalDAVUID(ctx context.Context, householdID string, uid string, acc Access) (*Calendar, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, household_id, name, type, color, caldav_uid, ctag, created_at, updated_at
		FROM calendars c
		WHERE caldav_uid = $1 AND household_id = $2 AND `+calendarVisibleSQL("$3")+`
	`, uid, householdID, acc.Role)

	var c Calendar
	err := row.Scan(&c.ID, &c.HouseholdID, &c.Name, &c.Type, &c.Color, &c.CalDAVUID, &c.CTag, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("db: get calendar by uid: %w", err)
	}
	c.Writable = acc.CanWrite()
	return &c, nil
}

// ListCalendarObjects returns all calendar objects for a given calendar ID,
// but only if that calendar belongs to the given household. The household
// check is enforced via an INNER JOIN with calendars — if the calendar is
// owned by a different household, the join yields no rows. Events acc may not
// read are left out (see access.go).
func (r *Repo) ListCalendarObjects(ctx context.Context, householdID string, calendarID string, acc Access) ([]CalendarObject, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT o.id, o.calendar_id, o.uid, o.ical_data, o.etag, o.entity_type, o.entity_id, o.created_at, o.updated_at, o.ical_raw, o.rrule
		FROM calendar_objects o
		INNER JOIN calendars c ON c.id = o.calendar_id
		WHERE o.calendar_id = $1 AND c.household_id = $2
		  AND `+calendarVisibleSQL("$3")+` AND `+objectVisibleSQL("$3", "$4")+`
		ORDER BY o.created_at ASC
	`, calendarID, householdID, acc.Role, acc.vendorArg())
	if err != nil {
		return nil, fmt.Errorf("db: list calendar objects: %w", err)
	}
	defer rows.Close()

	var objects []CalendarObject
	for rows.Next() {
		var o CalendarObject
		if err := rows.Scan(&o.ID, &o.CalendarID, &o.UID, &o.ICALData, &o.ETag, &o.EntityType, &o.EntityID, &o.CreatedAt, &o.UpdatedAt, &o.ICALRaw, &o.RRule); err != nil {
			return nil, fmt.Errorf("db: scan calendar object: %w", err)
		}
		objects = append(objects, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: rows iteration: %w", err)
	}
	return objects, nil
}

// GetCalendarObjectByUID returns a single calendar object by its calendar ID
// and UID, but only if the calendar belongs to the given household. The
// household check is enforced via an INNER JOIN with calendars.
// Returns nil, nil if not found OR if the calendar belongs to a different
// household, or if acc may not read the event (see access.go).
func (r *Repo) GetCalendarObjectByUID(ctx context.Context, householdID string, calendarID string, uid string, acc Access) (*CalendarObject, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT o.id, o.calendar_id, o.uid, o.ical_data, o.etag, o.entity_type, o.entity_id, o.created_at, o.updated_at, o.ical_raw, o.rrule
		FROM calendar_objects o
		INNER JOIN calendars c ON c.id = o.calendar_id
		WHERE o.calendar_id = $1 AND o.uid = $2 AND c.household_id = $3
		  AND `+calendarVisibleSQL("$4")+` AND `+objectVisibleSQL("$4", "$5")+`
	`, calendarID, uid, householdID, acc.Role, acc.vendorArg())

	var o CalendarObject
	err := row.Scan(&o.ID, &o.CalendarID, &o.UID, &o.ICALData, &o.ETag, &o.EntityType, &o.EntityID, &o.CreatedAt, &o.UpdatedAt, &o.ICALRaw, &o.RRule)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("db: get calendar object by uid: %w", err)
	}
	return &o, nil
}

// UpsertCalendarObject inserts or updates a calendar object by calendar_id and
// uid, but only if the calendar belongs to the given household. The household
// check is enforced via a CTE that gates both the INSERT and the ON CONFLICT
// UPDATE: if no calendar row matches (id, household_id), the SELECT yields no
// rows and nothing is inserted/updated — the function returns nil, nil (callers
// should treat this as a 404). This is defense-in-depth: handlers always
// resolve the calendar via GetCalendarByCalDAVUID first, but this guard
// prevents a write if a calendar_id is ever passed without that check.
// Returns the created or updated calendar object, including the new ETag and
// timestamps.
//
// This method runs against the connection pool. To run inside an explicit
// transaction (e.g. paired with a CTag bump), use UpsertCalendarObjectTx with
// a pgx.Tx, or call the high-level UpsertCalendarObjectWithCTag wrapper which
// performs the upsert + CTag bump atomically.
func (r *Repo) UpsertCalendarObject(ctx context.Context, householdID string, calendarID string, uid string, icalData string, etag string) (*CalendarObject, error) {
	return upsertCalendarObjectQ(ctx, r.pool, householdID, calendarID, uid, icalData, etag, nil, nil)
}

// UpsertCalendarObjectTx inserts or updates a calendar object within the given
// transaction. The caller is responsible for Commit/Rollback. Pair with
// IncrementCalendarCTagTx to make a write + CTag bump atomic, or use the
// UpsertCalendarObjectWithCTag wrapper which does both in one tx.
//
// The household ownership guard is enforced inside the SQL just like the pool
// variant; if the calendar is not owned by the household the function returns
// nil, nil (and the caller should rollback and return 404).
func (r *Repo) UpsertCalendarObjectTx(ctx context.Context, tx pgx.Tx, householdID string, calendarID string, uid string, icalData string, etag string) (*CalendarObject, error) {
	return upsertCalendarObjectQ(ctx, tx, householdID, calendarID, uid, icalData, etag, nil, nil)
}

// upsertCalendarObjectQ is the single implementation of the upsert SQL shared
// by the pool and tx variants. The querier may be either *pgxpool.Pool or pgx.Tx.
// Returns (nil, nil) when the calendar is not owned by the household — callers
// must handle this (rollback the tx and return 404).
//
// icalRaw and rrule are written as given on both insert and update, so a nil
// value clears them: a writer that only has the JSON must not leave a stale
// raw VCALENDAR behind for CalDAV to serve.
func upsertCalendarObjectQ(ctx context.Context, q querier, householdID string, calendarID string, uid string, icalData string, etag string, icalRaw *string, rrule *string) (*CalendarObject, error) {
	row := q.QueryRow(ctx, `
		WITH owned AS (
			SELECT id FROM calendars WHERE id = $1 AND household_id = $5
		)
		INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, ical_raw, rrule)
		SELECT $1, $2, $3, $4, $6, $7 FROM owned
		ON CONFLICT (calendar_id, uid)
		DO UPDATE SET ical_data = $3, etag = $4, ical_raw = $6, rrule = $7, updated_at = NOW()
		WHERE EXISTS (SELECT 1 FROM owned)
		RETURNING id, calendar_id, uid, ical_data, etag, entity_type, entity_id, created_at, updated_at, ical_raw, rrule
	`, calendarID, uid, icalData, etag, householdID, icalRaw, rrule)

	var o CalendarObject
	err := row.Scan(&o.ID, &o.CalendarID, &o.UID, &o.ICALData, &o.ETag, &o.EntityType, &o.EntityID, &o.CreatedAt, &o.UpdatedAt, &o.ICALRaw, &o.RRule)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("db: upsert calendar object: %w", err)
	}
	return &o, nil
}

// DeleteCalendarObject deletes a calendar object by calendar ID and UID, but
// only if the calendar belongs to the given household. The household check is
// enforced via a WHERE EXISTS subquery on calendars; if the calendar is owned
// by another household, no row is deleted (idempotent — callers still get nil).
//
// This method runs against the connection pool. To run inside an explicit
// transaction (e.g. paired with a CTag bump), use DeleteCalendarObjectTx with
// a pgx.Tx, or call the high-level DeleteCalendarObjectWithCTag wrapper which
// performs the delete + CTag bump atomically.
func (r *Repo) DeleteCalendarObject(ctx context.Context, householdID string, calendarID string, uid string) error {
	return deleteCalendarObjectQ(ctx, r.pool, householdID, calendarID, uid)
}

// DeleteCalendarObjectTx deletes a calendar object within the given
// transaction. The caller is responsible for Commit/Rollback. Pair with
// IncrementCalendarCTagTx to make a delete + CTag bump atomic, or use the
// DeleteCalendarObjectWithCTag wrapper which does both in one tx.
//
// The household ownership guard is enforced inside the SQL just like the pool
// variant; if the calendar is not owned by the household no row is deleted
// (idempotent nil) and the caller should rollback and return 404.
func (r *Repo) DeleteCalendarObjectTx(ctx context.Context, tx pgx.Tx, householdID string, calendarID string, uid string) error {
	return deleteCalendarObjectQ(ctx, tx, householdID, calendarID, uid)
}

// deleteCalendarObjectQ is the single implementation of the delete SQL shared
// by the pool and tx variants. The querier may be either *pgxpool.Pool or pgx.Tx.
func deleteCalendarObjectQ(ctx context.Context, q querier, householdID string, calendarID string, uid string) error {
	_, err := q.Exec(ctx, `
		DELETE FROM calendar_objects
		WHERE calendar_id = $1 AND uid = $2
		AND EXISTS (
			SELECT 1 FROM calendars
			WHERE id = calendar_objects.calendar_id AND household_id = $3
		)
	`, calendarID, uid, householdID)
	if err != nil {
		return fmt.Errorf("db: delete calendar object: %w", err)
	}
	return nil
}

// IncrementCalendarCTag updates the ctag for a calendar, generating a new random value.
// CTag changes whenever any object in the calendar collection changes.
//
// The calendar_id is expected to have already been verified as belonging to the
// caller's household (e.g. via GetCalendarByCalDAVUID) before this is called, so
// no household guard is applied here. The upsert/delete that pair with this in a
// transaction DO carry the household guard.
//
// This method runs against the connection pool. To run inside an explicit
// transaction (e.g. paired with an event write), use IncrementCalendarCTagTx
// with a pgx.Tx, or use the UpsertCalendarObjectWithCTag /
// DeleteCalendarObjectWithCTag wrappers which pair the CTag bump with the
// write atomically.
func (r *Repo) IncrementCalendarCTag(ctx context.Context, calendarID string) error {
	return incrementCalendarCTagQ(ctx, r.pool, calendarID)
}

// IncrementCalendarCTagTx bumps the calendar CTag within the given transaction.
// The caller is responsible for Commit/Rollback.
func (r *Repo) IncrementCalendarCTagTx(ctx context.Context, tx pgx.Tx, calendarID string) error {
	return incrementCalendarCTagQ(ctx, tx, calendarID)
}

// incrementCalendarCTagQ is the single implementation of the CTag bump SQL
// shared by the pool and tx variants. The querier may be either *pgxpool.Pool
// or pgx.Tx.
func incrementCalendarCTagQ(ctx context.Context, q querier, calendarID string) error {
	_, err := q.Exec(ctx, `
		UPDATE calendars SET ctag = gen_random_uuid()::text, updated_at = NOW()
		WHERE id = $1
	`, calendarID)
	if err != nil {
		return fmt.Errorf("db: increment ctag: %w", err)
	}
	return nil
}

// UpsertCalendarObjectWithCTag upserts the calendar object, records a
// calendar_changes row, and bumps the calendar CTag inside a single pgx
// transaction. If any operation fails the whole transaction is rolled back
// and the error is returned. It writes unconditionally: CalDAV PUT, which
// must honour If-Match / If-None-Match, uses PutCalendarObject instead. A
// stale CTag (event written but CTag not bumped)
// would cause Apple Calendar to silently miss the change forever, and a
// missing calendar_changes row would cause sync-collection to never
// deliver the change to incremental-sync clients.
//
// The change row's action is "create" if the event did not previously exist
// in this calendar, or "update" if it did. The existence check is done
// inside the same transaction before the upsert so it reflects the
// pre-mutation state.
//
// If the calendar is not owned by the given household, returns (nil, nil) and
// the transaction is rolled back — callers should treat this as 404.
//
// icalRaw is the VCALENDAR text exactly as the client sent it and rrule the
// event's RRULE value; either may be nil (nil clears the stored value).
func (r *Repo) UpsertCalendarObjectWithCTag(ctx context.Context, householdID string, calendarID string, uid string, icalData string, etag string, icalRaw *string, rrule *string) (*CalendarObject, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("db: begin tx (upsert+ctag): %w", err)
	}
	// Defer rollback; if Commit succeeds, the returned error is a sentinel
	// that we ignore (pgx.Rollback returns ErrTxClosed after Commit). This is
	// the idiomatic pgx v5 pattern.
	defer func() { _ = tx.Rollback(ctx) }()

	// Determine whether this is a create or an update by checking for an
	// existing row with the same (calendar_id, uid) BEFORE the upsert runs.
	// The check happens inside the same transaction so the read is consistent
	// with the write that follows.
	var existed bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM calendar_objects WHERE calendar_id = $1 AND uid = $2)`,
		calendarID, uid,
	).Scan(&existed); err != nil {
		return nil, fmt.Errorf("db: check existing object: %w", err)
	}

	obj, err := upsertCalendarObjectQ(ctx, tx, householdID, calendarID, uid, icalData, etag, icalRaw, rrule)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		// Calendar not owned by this household. Nothing was written — roll
		// back and signal 404 to the caller. The deferred rollback handles it.
		return nil, nil
	}

	action := ChangeCreate
	if existed {
		action = ChangeUpdate
	}
	if err := recordChangeQ(ctx, tx, calendarID, uid, action); err != nil {
		return nil, err
	}
	if err := incrementCalendarCTagQ(ctx, tx, calendarID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("db: commit (upsert+ctag): %w", err)
	}
	return obj, nil
}

// ErrPreconditionFailed is the sentinel a conditional write returns when the
// stored state of the object does not satisfy the caller's precondition
// (If-Match / If-None-Match). CalDAV handlers map it to 412 Precondition
// Failed. Callbacks passed to PutCalendarObject return it (or an error that
// wraps it) to abort the write; the transaction is rolled back.
var ErrPreconditionFailed = errors.New("db: precondition failed")

// ObjectWrite is the new state a conditional write stores for a calendar
// object. ICALRaw and RRule may be nil (nil clears the stored value), with the
// same meaning as in UpsertCalendarObjectWithCTag.
type ObjectWrite struct {
	ICALData string
	ETag     string
	ICALRaw  *string
	RRule    *string
}

// maxPutAttempts bounds how often PutCalendarObject re-reads the row after
// losing an insert race. Each retry only happens when another transaction
// committed a row with the same (calendar_id, uid) between our locking read
// and our INSERT, so two attempts are enough in practice; the third is slack.
const maxPutAttempts = 3

// inCalendarTx runs fn inside one transaction after checking that calendarID
// belongs to householdID. It commits when fn returns nil and rolls back
// otherwise, returning fn's error unchanged (so sentinels such as
// ErrPreconditionFailed reach the caller as-is). owned is false, and fn is not
// called, when the calendar is missing or belongs to another household:
// callers treat that as 404.
//
// This is the shared shape for every conditional object write (PUT now,
// DELETE with If-Match next): fn locks the object row with
// lockCalendarObjectQ, evaluates the precondition against what it locked, and
// writes, so no concurrent writer can slip between the check and the write.
func (r *Repo) inCalendarTx(ctx context.Context, householdID string, calendarID string, fn func(tx pgx.Tx) error) (owned bool, err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("db: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM calendars WHERE id = $1 AND household_id = $2)`,
		calendarID, householdID,
	).Scan(&owned); err != nil {
		return false, fmt.Errorf("db: check calendar ownership: %w", err)
	}
	if !owned {
		return false, nil
	}
	if err := fn(tx); err != nil {
		return true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return true, fmt.Errorf("db: commit: %w", err)
	}
	return true, nil
}

// lockCalendarObjectQ reads the (calendar_id, uid) row and locks it FOR
// UPDATE until the transaction ends. Returns nil, nil when there is no such
// row; in that case nothing is locked, so a create must still rely on the
// UNIQUE (calendar_id, uid) constraint (see insertCalendarObjectIfAbsentQ).
//
// Under READ COMMITTED, if another transaction holds the lock, this waits for
// it and then returns the row as that transaction committed it, so the
// precondition is always evaluated against the latest committed state.
func lockCalendarObjectQ(ctx context.Context, q querier, calendarID string, uid string) (*CalendarObject, error) {
	var o CalendarObject
	err := q.QueryRow(ctx, `
		SELECT id, calendar_id, uid, ical_data, etag, entity_type, entity_id, created_at, updated_at, ical_raw, rrule
		FROM calendar_objects
		WHERE calendar_id = $1 AND uid = $2
		FOR UPDATE
	`, calendarID, uid).Scan(&o.ID, &o.CalendarID, &o.UID, &o.ICALData, &o.ETag, &o.EntityType, &o.EntityID, &o.CreatedAt, &o.UpdatedAt, &o.ICALRaw, &o.RRule)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("db: lock calendar object: %w", err)
	}
	return &o, nil
}

// insertCalendarObjectIfAbsentQ inserts a new row and returns it, or returns
// nil, nil when a row with the same (calendar_id, uid) already exists
// (including one a concurrent transaction committed while we waited on it).
func insertCalendarObjectIfAbsentQ(ctx context.Context, q querier, calendarID string, uid string, w ObjectWrite) (*CalendarObject, error) {
	var o CalendarObject
	err := q.QueryRow(ctx, `
		INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag, ical_raw, rrule)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (calendar_id, uid) DO NOTHING
		RETURNING id, calendar_id, uid, ical_data, etag, entity_type, entity_id, created_at, updated_at, ical_raw, rrule
	`, calendarID, uid, w.ICALData, w.ETag, w.ICALRaw, w.RRule).Scan(&o.ID, &o.CalendarID, &o.UID, &o.ICALData, &o.ETag, &o.EntityType, &o.EntityID, &o.CreatedAt, &o.UpdatedAt, &o.ICALRaw, &o.RRule)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("db: insert calendar object: %w", err)
	}
	return &o, nil
}

// updateLockedCalendarObjectQ overwrites a row the caller has locked with
// lockCalendarObjectQ in the same transaction.
func updateLockedCalendarObjectQ(ctx context.Context, q querier, calendarID string, uid string, w ObjectWrite) (*CalendarObject, error) {
	var o CalendarObject
	err := q.QueryRow(ctx, `
		UPDATE calendar_objects
		SET ical_data = $3, etag = $4, ical_raw = $5, rrule = $6, updated_at = NOW()
		WHERE calendar_id = $1 AND uid = $2
		RETURNING id, calendar_id, uid, ical_data, etag, entity_type, entity_id, created_at, updated_at, ical_raw, rrule
	`, calendarID, uid, w.ICALData, w.ETag, w.ICALRaw, w.RRule).Scan(&o.ID, &o.CalendarID, &o.UID, &o.ICALData, &o.ETag, &o.EntityType, &o.EntityID, &o.CreatedAt, &o.UpdatedAt, &o.ICALRaw, &o.RRule)
	if err != nil {
		return nil, fmt.Errorf("db: update calendar object: %w", err)
	}
	return &o, nil
}

// PutCalendarObject is the conditional create-or-update CalDAV PUT uses. In a
// single transaction it:
//
//  1. checks the calendar belongs to householdID (not owned: returns
//     nil, false, nil and the caller answers 404);
//  2. locks the existing (calendar_id, uid) row FOR UPDATE, or finds none;
//  3. calls decide with that row (nil when absent). decide evaluates the
//     request's preconditions against it and returns the state to store, or
//     an error (ErrPreconditionFailed for a failed If-Match /
//     If-None-Match) that aborts and rolls back the write and is returned
//     unchanged;
//  4. updates the locked row, or inserts with ON CONFLICT DO NOTHING. When
//     the insert finds a row a concurrent transaction just committed, it goes
//     back to step 2 and calls decide again with that row, so a create-only
//     PUT (If-None-Match: *) gets ErrPreconditionFailed and an unconditional
//     PUT becomes an update instead of failing;
//  5. records the calendar_changes row (create/update, for sync-collection)
//     and bumps the calendar CTag, then commits.
//
// Because the precondition is evaluated against the locked row inside the
// write transaction, two clients PUTting with the same If-Match cannot both
// succeed: the second waits on the row lock, then sees the first one's new
// ETag and fails. decide may be called more than once and must not have side
// effects.
//
// created reports whether the row was inserted (201) rather than updated
// (200).
func (r *Repo) PutCalendarObject(ctx context.Context, householdID string, calendarID string, uid string, decide func(existing *CalendarObject) (ObjectWrite, error)) (obj *CalendarObject, created bool, err error) {
	owned, err := r.inCalendarTx(ctx, householdID, calendarID, func(tx pgx.Tx) error {
		for attempt := 0; attempt < maxPutAttempts; attempt++ {
			existing, err := lockCalendarObjectQ(ctx, tx, calendarID, uid)
			if err != nil {
				return err
			}
			w, err := decide(existing)
			if err != nil {
				return err
			}
			action := ChangeUpdate
			if existing != nil {
				obj, err = updateLockedCalendarObjectQ(ctx, tx, calendarID, uid, w)
				if err != nil {
					return err
				}
				created = false
			} else {
				obj, err = insertCalendarObjectIfAbsentQ(ctx, tx, calendarID, uid, w)
				if err != nil {
					return err
				}
				if obj == nil {
					// Lost the insert race: re-read the winner's row and
					// re-evaluate the precondition against it.
					continue
				}
				created = true
				action = ChangeCreate
			}
			if err := recordChangeQ(ctx, tx, calendarID, uid, action); err != nil {
				return err
			}
			return incrementCalendarCTagQ(ctx, tx, calendarID)
		}
		return fmt.Errorf("db: put calendar object %q: row kept changing under %d attempts", uid, maxPutAttempts)
	})
	if err != nil {
		return nil, false, err
	}
	if !owned {
		return nil, false, nil
	}
	return obj, created, nil
}

// DeleteCalendarObjectWithCTag deletes the calendar object unconditionally,
// records a calendar_changes row (action=delete) and bumps the calendar CTag
// in one transaction. It is DeleteCalendarObjectIf with no precondition; see
// that method for the details. A calendar of another household, or an event
// that is already gone, is a no-op that returns nil (DELETE is idempotent).
func (r *Repo) DeleteCalendarObjectWithCTag(ctx context.Context, householdID string, calendarID string, uid string) error {
	_, _, err := r.DeleteCalendarObjectIf(ctx, householdID, calendarID, uid, nil)
	return err
}

// DeleteCalendarObjectIf is the conditional delete CalDAV DELETE uses
// (If-Match, #4662). In a single transaction it:
//
//  1. checks the calendar belongs to householdID (not owned: returns
//     owned=false and the caller answers 404);
//  2. locks the (calendar_id, uid) row FOR UPDATE, or finds none;
//  3. calls check with that row (nil when absent), when check is non-nil.
//     check evaluates the request's precondition against the locked row and
//     returns an error (ErrPreconditionFailed, or one wrapping it) to abort;
//     the error is returned unchanged and nothing is written;
//  4. if a row exists, deletes it, records a calendar_changes row
//     (action=delete, the sync-collection tombstone) and bumps the CTag.
//
// Because check sees the locked row, a stale If-Match cannot delete an event
// another device just updated: a concurrent PUT holding the lock commits
// first, and the delete then sees the new ETag and fails (or the delete wins
// and the PUT, which re-reads under the lock, sees no row).
//
// A missing row that passes check is not an error (deleted=false): CalDAV
// DELETE without If-Match is idempotent, and no tombstone or CTag bump is
// written for it, so repeated DELETEs do not pollute the change stream.
//
// The delete is a plain DELETE on calendar_objects, so its row triggers run as
// before (038's AFTER DELETE maintenance_calendar_sync_trigger removes the
// maintenance task linked to the event). Lock order matches PutCalendarObject:
// object row first, then the calendars row via the CTag UPDATE.
func (r *Repo) DeleteCalendarObjectIf(ctx context.Context, householdID string, calendarID string, uid string, check func(existing *CalendarObject) error) (owned bool, deleted bool, err error) {
	owned, err = r.inCalendarTx(ctx, householdID, calendarID, func(tx pgx.Tx) error {
		existing, err := lockCalendarObjectQ(ctx, tx, calendarID, uid)
		if err != nil {
			return err
		}
		if check != nil {
			if err := check(existing); err != nil {
				return err
			}
		}
		if existing == nil {
			return nil
		}
		tag, err := tx.Exec(ctx, `DELETE FROM calendar_objects WHERE calendar_id = $1 AND uid = $2`, calendarID, uid)
		if err != nil {
			return fmt.Errorf("db: delete calendar object: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// Cannot happen while we hold the row lock; refuse rather than
			// write a tombstone for a row we did not delete.
			return fmt.Errorf("db: delete calendar object %q: locked row vanished", uid)
		}
		deleted = true
		if err := recordChangeQ(ctx, tx, calendarID, uid, ChangeDelete); err != nil {
			return err
		}
		return incrementCalendarCTagQ(ctx, tx, calendarID)
	})
	if err != nil {
		return owned, false, err
	}
	return owned, deleted, nil
}

// UpdateCalendarProps updates the name and/or color of a calendar, but only
// if the calendar belongs to the given household. The household guard is
// enforced via the WHERE clause; a calendar owned by a different household
// matches no rows and the function returns (false, nil) — callers should
// treat this as 404 (defense-in-depth: handlers always resolve the calendar
// via GetCalendarByCalDAVUID first, but this guard prevents a write if a
// calendar_id is ever passed without that check).
//
// nil values leave the corresponding column unchanged. An empty color string
// clears the column (color = NULL) so a PROPPATCH remove on calendar-color
// persists as "no color". The name column is NOT NULL so callers must not
// pass a pointer to an empty string for name; the PROPPATCH handler skips
// the name update when the value is empty.
//
// This is the operation CalDAV PROPPATCH uses to persist displayname and
// calendar-color changes from Apple Calendar. Previously PROPPATCH returned
// 207 with an empty body — silently accepting and discarding all changes,
// so user renames appeared to work locally but reverted on next sync.
//
// Returns (true, nil) on a successful update, (false, nil) if no row matched
// (calendar missing or not owned by the household), and (false, err) on a
// database error.
func (r *Repo) UpdateCalendarProps(ctx context.Context, householdID string, calendarID string, name *string, color *string) (bool, error) {
	// Nothing to do. Avoid issuing an empty UPDATE that would still bump
	// updated_at for no reason.
	if name == nil && color == nil {
		// We did not actually touch the row, but the calendar is presumed to
		// exist (caller resolved it). Return true so the handler reports 200.
		return true, nil
	}

	var sets []string
	var args []any
	idx := 1
	if name != nil {
		sets = append(sets, fmt.Sprintf("name = $%d", idx))
		args = append(args, *name)
		idx++
	}
	if color != nil {
		if *color == "" {
			// Empty string means "clear the color". The column is nullable,
			// so we explicitly set it to NULL rather than storing "".
			sets = append(sets, "color = NULL")
		} else {
			sets = append(sets, fmt.Sprintf("color = $%d", idx))
			args = append(args, *color)
			idx++
		}
	}
	sets = append(sets, "updated_at = NOW()")

	args = append(args, calendarID, householdID)
	query := fmt.Sprintf(`
		UPDATE calendars SET %s
		WHERE id = $%d AND household_id = $%d
	`, strings.Join(sets, ", "), idx, idx+1)

	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("db: update calendar props: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// No row matched (calendar_id wrong or not owned by household).
		return false, nil
	}
	return true, nil
}

// ListCalendarObjectsInRange returns all calendar objects for a given calendar
// ID, but only if the calendar belongs to the given household. Date-range
// filtering is done in-app since events are stored as serialized JSON in the
// ical_data column. For a home OS with typical volumes (<1000 events), this is
// acceptable. The household check is enforced via an INNER JOIN with calendars.
// Events acc may not read are left out (see access.go).
func (r *Repo) ListCalendarObjectsInRange(ctx context.Context, householdID string, calendarID string, acc Access) ([]CalendarObject, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT o.id, o.calendar_id, o.uid, o.ical_data, o.etag, o.entity_type, o.entity_id, o.created_at, o.updated_at, o.ical_raw, o.rrule
		FROM calendar_objects o
		INNER JOIN calendars c ON c.id = o.calendar_id
		WHERE o.calendar_id = $1 AND c.household_id = $2
		  AND `+calendarVisibleSQL("$3")+` AND `+objectVisibleSQL("$3", "$4")+`
		ORDER BY o.created_at ASC
	`, calendarID, householdID, acc.Role, acc.vendorArg())
	if err != nil {
		return nil, fmt.Errorf("db: list calendar objects in range: %w", err)
	}
	defer rows.Close()

	var objects []CalendarObject
	for rows.Next() {
		var o CalendarObject
		if err := rows.Scan(&o.ID, &o.CalendarID, &o.UID, &o.ICALData, &o.ETag, &o.EntityType, &o.EntityID, &o.CreatedAt, &o.UpdatedAt, &o.ICALRaw, &o.RRule); err != nil {
			return nil, fmt.Errorf("db: scan calendar object: %w", err)
		}
		objects = append(objects, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: rows iteration: %w", err)
	}
	return objects, nil
}

// ----- Calendar change tracking (sync-collection) -----

// recordChangeQ inserts a single row into calendar_changes describing a
// create/update/delete of a calendar object. The revision is drawn from the
// calendar_changes_revision_seq sequence via the column DEFAULT, so each row
// gets a globally-monotonic revision without any per-calendar locking. The
// caller MUST invoke this inside the same transaction as the event mutation
// it describes — otherwise a crash between the mutation and the change row
// would leave sync-collection blind to the change (or, conversely, would
// record a change for a mutation that was rolled back).
//
// The querier is either *pgxpool.Pool or pgx.Tx; the only caller today is
// the *WithCTag wrapper, which passes its tx.
func recordChangeQ(ctx context.Context, q querier, calendarID string, eventUID string, action CalendarChangeAction) error {
	_, err := q.Exec(ctx, `
		INSERT INTO calendar_changes (calendar_id, event_uid, action)
		VALUES ($1, $2, $3)
	`, calendarID, eventUID, string(action))
	if err != nil {
		return fmt.Errorf("db: record calendar change: %w", err)
	}
	return nil
}

// ListChangesSince returns every calendar_changes row for the given calendar
// with revision strictly greater than sinceRevision, ordered by revision
// ascending. The household ownership guard is enforced via an INNER JOIN with
// calendars so a credential from another household cannot observe change
// history (and cannot learn whether a calendar even exists). Returns an empty
// (non-nil) slice when no changes match.
//
// This is the read side of sync-collection: the handler walks the returned
// rows, emits a D:response with the event data for create/update rows (the
// event_uid is looked up in calendar_objects), and a 404 tombstone for
// delete rows.
func (r *Repo) ListChangesSince(ctx context.Context, householdID string, calendarID string, sinceRevision int64) ([]CalendarChange, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT ch.id, ch.calendar_id, ch.event_uid, ch.action, ch.revision, ch.created_at
		FROM calendar_changes ch
		INNER JOIN calendars c ON c.id = ch.calendar_id
		WHERE ch.calendar_id = $1 AND c.household_id = $2 AND ch.revision > $3
		ORDER BY ch.revision ASC
	`, calendarID, householdID, sinceRevision)
	if err != nil {
		return nil, fmt.Errorf("db: list calendar changes: %w", err)
	}
	defer rows.Close()

	var changes []CalendarChange
	for rows.Next() {
		var ch CalendarChange
		if err := rows.Scan(&ch.ID, &ch.CalendarID, &ch.EventUID, &ch.Action, &ch.Revision, &ch.CreatedAt); err != nil {
			return nil, fmt.Errorf("db: scan calendar change: %w", err)
		}
		changes = append(changes, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: rows iteration: %w", err)
	}
	if changes == nil {
		changes = []CalendarChange{}
	}
	return changes, nil
}

// LatestChangeRevision returns the highest revision currently recorded for the
// given calendar, or 0 if the calendar has no recorded changes yet (e.g. a
// brand-new calendar, or one whose events all predate the change-tracking
// migration). The household ownership guard is enforced via an INNER JOIN so a
// credential from another household gets 0 (indistinguishable from "no
// changes") rather than learning the calendar exists.
//
// sync-collection uses this to compute the new sync-token it hands back to the
// client: the token encodes this revision, and the client's next
// sync-collection request returns only changes with revision > this value.
func (r *Repo) LatestChangeRevision(ctx context.Context, householdID string, calendarID string) (int64, error) {
	var rev int64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(ch.revision), 0)
		FROM calendar_changes ch
		INNER JOIN calendars c ON c.id = ch.calendar_id
		WHERE ch.calendar_id = $1 AND c.household_id = $2
	`, calendarID, householdID).Scan(&rev)
	if err != nil {
		return 0, fmt.Errorf("db: latest change revision: %w", err)
	}
	return rev, nil
}
