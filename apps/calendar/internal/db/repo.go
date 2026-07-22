// Package db provides PostgreSQL database access for the calendar service.
// It reads from the calendars and calendar_objects tables shared with the
// core API. The same DATABASE_URL environment variable is used.
package db

import (
	"context"
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
}

// GetUserByEmailForCalDAV looks up a user by email for CalDAV Basic Auth.
// Returns nil, nil if the user is not found.
func (r *Repo) GetUserByEmailForCalDAV(ctx context.Context, email string) (*CalDAVUser, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, email, caldav_password_hash
		FROM users
		WHERE email = $1
	`, email)

	var u CalDAVUser
	err := row.Scan(&u.ID, &u.Email, &u.CalDAVPasswordHash)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("db: get user by email for caldav: %w", err)
	}
	return &u, nil
}

// GetHouseholdIDForUser returns the household_id for a user's first membership.
// Returns empty string if the user has no memberships.
func (r *Repo) GetHouseholdIDForUser(ctx context.Context, userID string) (string, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT household_id FROM memberships WHERE user_id = $1 LIMIT 1
	`, userID)

	var householdID string
	err := row.Scan(&householdID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("db: get household for user: %w", err)
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
	ID         int64               `json:"id"`
	CalendarID string              `json:"calendar_id"`
	EventUID   string              `json:"event_uid"`
	Action     CalendarChangeAction `json:"action"`
	Revision   int64               `json:"revision"`
	CreatedAt  time.Time           `json:"created_at"`
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
func (r *Repo) ListCalendars(ctx context.Context, householdID string) ([]Calendar, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, household_id, name, type, color, caldav_uid, ctag, created_at, updated_at
		FROM calendars
		WHERE household_id = $1
		ORDER BY created_at ASC
	`, householdID)
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
// existence of cross-tenant calendars.
func (r *Repo) GetCalendarByCalDAVUID(ctx context.Context, householdID string, uid string) (*Calendar, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, household_id, name, type, color, caldav_uid, ctag, created_at, updated_at
		FROM calendars
		WHERE caldav_uid = $1 AND household_id = $2
	`, uid, householdID)

	var c Calendar
	err := row.Scan(&c.ID, &c.HouseholdID, &c.Name, &c.Type, &c.Color, &c.CalDAVUID, &c.CTag, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("db: get calendar by uid: %w", err)
	}
	return &c, nil
}

// ListCalendarObjects returns all calendar objects for a given calendar ID,
// but only if that calendar belongs to the given household. The household
// check is enforced via an INNER JOIN with calendars — if the calendar is
// owned by a different household, the join yields no rows.
func (r *Repo) ListCalendarObjects(ctx context.Context, householdID string, calendarID string) ([]CalendarObject, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT o.id, o.calendar_id, o.uid, o.ical_data, o.etag, o.entity_type, o.entity_id, o.created_at, o.updated_at
		FROM calendar_objects o
		INNER JOIN calendars c ON c.id = o.calendar_id
		WHERE o.calendar_id = $1 AND c.household_id = $2
		ORDER BY o.created_at ASC
	`, calendarID, householdID)
	if err != nil {
		return nil, fmt.Errorf("db: list calendar objects: %w", err)
	}
	defer rows.Close()

	var objects []CalendarObject
	for rows.Next() {
		var o CalendarObject
		if err := rows.Scan(&o.ID, &o.CalendarID, &o.UID, &o.ICALData, &o.ETag, &o.EntityType, &o.EntityID, &o.CreatedAt, &o.UpdatedAt); err != nil {
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
// household.
func (r *Repo) GetCalendarObjectByUID(ctx context.Context, householdID string, calendarID string, uid string) (*CalendarObject, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT o.id, o.calendar_id, o.uid, o.ical_data, o.etag, o.entity_type, o.entity_id, o.created_at, o.updated_at
		FROM calendar_objects o
		INNER JOIN calendars c ON c.id = o.calendar_id
		WHERE o.calendar_id = $1 AND o.uid = $2 AND c.household_id = $3
	`, calendarID, uid, householdID)

	var o CalendarObject
	err := row.Scan(&o.ID, &o.CalendarID, &o.UID, &o.ICALData, &o.ETag, &o.EntityType, &o.EntityID, &o.CreatedAt, &o.UpdatedAt)
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
	return upsertCalendarObjectQ(ctx, r.pool, householdID, calendarID, uid, icalData, etag)
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
	return upsertCalendarObjectQ(ctx, tx, householdID, calendarID, uid, icalData, etag)
}

// upsertCalendarObjectQ is the single implementation of the upsert SQL shared
// by the pool and tx variants. The querier may be either *pgxpool.Pool or pgx.Tx.
// Returns (nil, nil) when the calendar is not owned by the household — callers
// must handle this (rollback the tx and return 404).
func upsertCalendarObjectQ(ctx context.Context, q querier, householdID string, calendarID string, uid string, icalData string, etag string) (*CalendarObject, error) {
	row := q.QueryRow(ctx, `
		WITH owned AS (
			SELECT id FROM calendars WHERE id = $1 AND household_id = $5
		)
		INSERT INTO calendar_objects (calendar_id, uid, ical_data, etag)
		SELECT $1, $2, $3, $4 FROM owned
		ON CONFLICT (calendar_id, uid)
		DO UPDATE SET ical_data = $3, etag = $4, updated_at = NOW()
		WHERE EXISTS (SELECT 1 FROM owned)
		RETURNING id, calendar_id, uid, ical_data, etag, entity_type, entity_id, created_at, updated_at
	`, calendarID, uid, icalData, etag, householdID)

	var o CalendarObject
	err := row.Scan(&o.ID, &o.CalendarID, &o.UID, &o.ICALData, &o.ETag, &o.EntityType, &o.EntityID, &o.CreatedAt, &o.UpdatedAt)
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
// and the error is returned. This is the operation CalDAV PUT and API
// CreateEvent should use: a stale CTag (event written but CTag not bumped)
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
func (r *Repo) UpsertCalendarObjectWithCTag(ctx context.Context, householdID string, calendarID string, uid string, icalData string, etag string) (*CalendarObject, error) {
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

	obj, err := upsertCalendarObjectQ(ctx, tx, householdID, calendarID, uid, icalData, etag)
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

// DeleteCalendarObjectWithCTag deletes the calendar object, records a
// calendar_changes row (action=delete) for the removed event_uid, and bumps
// the calendar CTag inside a single pgx transaction. If any operation fails
// the whole transaction is rolled back and the error is returned. This is the
// operation CalDAV DELETE and API DeleteEvent should use: a stale CTag (event
// deleted but CTag not bumped) would cause Apple Calendar to keep showing the
// deleted event forever, and a missing calendar_changes row would cause
// sync-collection to never emit the 404 tombstone that tells incremental-sync
// clients to remove the event locally.
//
// The change row is only recorded when a row was actually deleted. CalDAV
// DELETE is idempotent — a repeated DELETE for an already-removed event must
// not produce a spurious delete change row, otherwise sync-collection would
// tell clients to tombstone an event that is already gone (harmless per RFC
// 6578 but wasteful and confusing in logs).
//
// The household ownership guard lives inside the delete SQL: if the calendar is
// not owned by the household, no row is deleted (the call still succeeds with
// nil — DELETE is idempotent by CalDAV convention — but no change row and no
// CTag bump occur). Callers should have already returned 404 if the calendar
// wasn't owned (via a prior GetCalendarByCalDAVUID check), so reaching this
// method with an un-owned calendar_id indicates a bug upstream rather than a
// normal 404 path.
func (r *Repo) DeleteCalendarObjectWithCTag(ctx context.Context, householdID string, calendarID string, uid string) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("db: begin tx (delete+ctag): %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
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

	// Only record a delete change row if a row was actually removed. A
	// repeated DELETE for an already-deleted event affects zero rows and must
	// not produce a tombstone, otherwise every redundant client DELETE would
	// pollute the change stream.
	if tag.RowsAffected() > 0 {
		if err := recordChangeQ(ctx, tx, calendarID, uid, ChangeDelete); err != nil {
			return err
		}
		if err := incrementCalendarCTagQ(ctx, tx, calendarID); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit (delete+ctag): %w", err)
	}
	return nil
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
func (r *Repo) ListCalendarObjectsInRange(ctx context.Context, householdID string, calendarID string) ([]CalendarObject, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT o.id, o.calendar_id, o.uid, o.ical_data, o.etag, o.entity_type, o.entity_id, o.created_at, o.updated_at
		FROM calendar_objects o
		INNER JOIN calendars c ON c.id = o.calendar_id
		WHERE o.calendar_id = $1 AND c.household_id = $2
		ORDER BY o.created_at ASC
	`, calendarID, householdID)
	if err != nil {
		return nil, fmt.Errorf("db: list calendar objects in range: %w", err)
	}
	defer rows.Close()

	var objects []CalendarObject
	for rows.Next() {
		var o CalendarObject
		if err := rows.Scan(&o.ID, &o.CalendarID, &o.UID, &o.ICALData, &o.ETag, &o.EntityType, &o.EntityID, &o.CreatedAt, &o.UpdatedAt); err != nil {
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