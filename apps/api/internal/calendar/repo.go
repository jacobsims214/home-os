package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Calendar represents a calendar collection.
type Calendar struct {
	ID          string    `json:"id"`
	HouseholdID string    `json:"household_id"`
	PropertyID  *string   `json:"property_id,omitempty"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Color       string    `json:"color"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Event is a calendar event stored as structured JSON inside ical_data.
type Event struct {
	ID          string    `json:"id"`
	CalendarID  string    `json:"calendar_id"`
	EventType   string    `json:"event_type"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	AllDay      bool      `json:"all_day"`
	Location    string    `json:"location"`
	Color       string    `json:"color"`
	EntityType  string    `json:"entity_type,omitempty"`
	EntityID    string    `json:"entity_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Repo handles calendar database operations.
type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// GetCalendarByIDForHousehold returns the calendar with the given ID if it
// exists AND belongs to the given household. Returns (nil, nil) when the
// calendar does not exist or is owned by a different household — callers MUST
// treat both cases as 404 Not Found so that an attacker cannot distinguish
// "no such calendar" from "calendar exists but isn't yours" (matching the
// CalDAV-side pattern in apps/calendar/internal/db/repo.go).
func (r *Repo) GetCalendarByIDForHousehold(ctx context.Context, calendarID, householdID uuid.UUID) (*Calendar, error) {
	var c Calendar
	err := r.pool.QueryRow(ctx,
		`SELECT id, household_id, property_id, name, type, COALESCE(color,'#6366f1'), created_at, updated_at
		 FROM calendars WHERE id = $1 AND household_id = $2`,
		calendarID, householdID,
	).Scan(&c.ID, &c.HouseholdID, &c.PropertyID, &c.Name, &c.Type, &c.Color, &c.CreatedAt, &c.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get calendar for household: %w", err)
	}
	return &c, nil
}

// GetEventCalendarID returns the calendar_id of the event with the given ID.
// Returns uuid.Nil if the event does not exist. Used by the DeleteEvent
// handler to resolve an event's owning calendar before verifying household
// ownership — the event ID alone is not enough to scope the check.
func (r *Repo) GetEventCalendarID(ctx context.Context, eventID uuid.UUID) (uuid.UUID, error) {
	var calendarID uuid.UUID
	err := r.pool.QueryRow(ctx,
		`SELECT calendar_id FROM calendar_objects WHERE id = $1`,
		eventID,
	).Scan(&calendarID)
	if err == pgx.ErrNoRows {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("get event calendar id: %w", err)
	}
	return calendarID, nil
}

// ListCalendars returns all calendars for a household.
func (r *Repo) ListCalendars(ctx context.Context, householdID uuid.UUID) ([]Calendar, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, property_id, name, type, COALESCE(color,'#6366f1'), created_at, updated_at
		 FROM calendars WHERE household_id = $1 ORDER BY name`,
		householdID)
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}
	defer rows.Close()

	var cals []Calendar
	for rows.Next() {
		var c Calendar
		if err := rows.Scan(&c.ID, &c.HouseholdID, &c.PropertyID, &c.Name, &c.Type, &c.Color, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("list calendars: scan: %w", err)
		}
		cals = append(cals, c)
	}
	if cals == nil {
		cals = []Calendar{}
	}
	return cals, nil
}

// CreateCalendar inserts a new calendar. propertyID can be nil for household-wide calendars.
func (r *Repo) CreateCalendar(ctx context.Context, householdID uuid.UUID, name, calType, color string, propertyID *uuid.UUID) (*Calendar, error) {
	uid := uuid.New().String()
	var c Calendar
	err := r.pool.QueryRow(ctx,
		`INSERT INTO calendars (household_id, property_id, name, type, color, caldav_uid)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, household_id, property_id, name, type, COALESCE(color,'#6366f1'), created_at, updated_at`,
		householdID, propertyID, name, calType, color, uid,
	).Scan(&c.ID, &c.HouseholdID, &c.PropertyID, &c.Name, &c.Type, &c.Color, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create calendar: %w", err)
	}
	return &c, nil
}

// ListEvents returns all events for a calendar, optionally filtered by date range.
func (r *Repo) ListEvents(ctx context.Context, calendarID uuid.UUID, start, end *time.Time) ([]Event, error) {
	// Events are stored as JSON in ical_data column
	rows, err := r.pool.Query(ctx,
		`SELECT id, calendar_id, uid, ical_data, event_type, entity_type, entity_id, created_at, updated_at
		 FROM calendar_objects WHERE calendar_id = $1 ORDER BY created_at`,
		calendarID)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var id, calID, uid, icalData, eventType string
		var entityType, entityID *string
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &calID, &uid, &icalData, &eventType, &entityType, &entityID, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("list events: scan: %w", err)
		}
		ev, err := parseEventJSON(id, calID, icalData, eventType, entityType, entityID, createdAt, updatedAt)
		if err != nil {
			log.Printf("calendar: list events: skip malformed event id=%s: %v", id, err)
			continue
		}
		// Apply date filter if provided
		if start != nil && ev.End.Before(*start) {
			continue
		}
		if end != nil && ev.Start.After(*end) {
			continue
		}
		events = append(events, *ev)
	}
	if events == nil {
		events = []Event{}
	}
	return events, nil
}

// ListAllEvents returns events across all calendars for a household in a date range.
// If propertyID is provided, filters to calendars scoped to that property.
func (r *Repo) ListAllEvents(ctx context.Context, householdID uuid.UUID, start, end *time.Time, propertyID *uuid.UUID) ([]Event, error) {
	var query string
	var args []any

	if propertyID != nil {
		query = `SELECT co.id, co.calendar_id, co.uid, co.ical_data, co.event_type, co.entity_type, co.entity_id, co.created_at, co.updated_at
		 FROM calendar_objects co
		 JOIN calendars c ON c.id = co.calendar_id
		 WHERE c.household_id = $1 AND c.property_id = $2
		 ORDER BY co.created_at`
		args = []any{householdID, propertyID}
	} else {
		query = `SELECT co.id, co.calendar_id, co.uid, co.ical_data, co.event_type, co.entity_type, co.entity_id, co.created_at, co.updated_at
		 FROM calendar_objects co
		 JOIN calendars c ON c.id = co.calendar_id
		 WHERE c.household_id = $1
		 ORDER BY co.created_at`
		args = []any{householdID}
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list all events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var id, calID, uid, icalData, eventType string
		var entityType, entityID *string
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&id, &calID, &uid, &icalData, &eventType, &entityType, &entityID, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("list all events: scan: %w", err)
		}
		ev, err := parseEventJSON(id, calID, icalData, eventType, entityType, entityID, createdAt, updatedAt)
		if err != nil {
			log.Printf("calendar: list all events: skip malformed event id=%s: %v", id, err)
			continue
		}
		if start != nil && ev.End.Before(*start) {
			continue
		}
		if end != nil && ev.Start.After(*end) {
			continue
		}
		events = append(events, *ev)
	}
	if events == nil {
		events = []Event{}
	}
	return events, nil
}

// CreateEvent inserts a new event into a calendar, records a calendar_changes
// row (action=create) for sync-collection, and bumps the parent calendar's
// CTag — all in a single transaction. If any statement fails the whole
// operation rolls back, so Apple Calendar sync is never left pointing at a
// stale CTag while the event row has already been written, and sync-collection
// is never left blind to a created event.
func (r *Repo) CreateEvent(ctx context.Context, calendarID uuid.UUID, ev *Event) (*Event, error) {
	ev.ID = uuid.New().String()
	ev.CalendarID = calendarID.String()
	ev.CreatedAt = time.Now()
	ev.UpdatedAt = time.Now()

	icalData, err := marshalEventJSON(ev)
	if err != nil {
		return nil, fmt.Errorf("create event: marshal: %w", err)
	}
	etag := uuid.New().String()

	var entityType, entityID *string
	if ev.EntityType != "" {
		entityType = &ev.EntityType
	}
	if ev.EntityID != "" {
		entityID = &ev.EntityID
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("create event: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		ev.ID, calendarID, ev.ID, icalData, etag, ev.EventType, entityType, entityID,
	)
	if err != nil {
		return nil, fmt.Errorf("create event: insert: %w", err)
	}

	// Record the change row inside the same tx so sync-collection clients see
	// the new event on their next incremental sync. The revision is assigned
	// by the calendar_changes_revision_seq DEFAULT.
	if _, err = tx.Exec(ctx,
		`INSERT INTO calendar_changes (calendar_id, event_uid, action) VALUES ($1, $2, 'create')`,
		calendarID, ev.ID,
	); err != nil {
		return nil, fmt.Errorf("create event: record change: %w", err)
	}

	if _, err = tx.Exec(ctx,
		`UPDATE calendars SET ctag = gen_random_uuid()::text, updated_at = NOW() WHERE id = $1`,
		calendarID,
	); err != nil {
		return nil, fmt.Errorf("create event: bump ctag: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("create event: commit: %w", err)
	}
	return ev, nil
}

// DeleteEvent removes an event, records a calendar_changes row
// (action=delete) for sync-collection, and bumps the parent calendar's CTag
// in a single transaction. The CTag bump is required so CalDAV clients see
// the deletion; without it the event would remain visible in Apple Calendar
// until the next full re-sync. The change row is required so sync-collection
// emits a 404 tombstone that tells incremental-sync clients to remove the
// event locally.
//
// The change row is only recorded when a row was actually deleted — a
// repeated DELETE for an already-removed event affects zero rows and must not
// produce a spurious tombstone (idempotent delete contract preserved).
func (r *Repo) DeleteEvent(ctx context.Context, eventID uuid.UUID) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("delete event: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var calendarID uuid.UUID
	var eventUID string
	err = tx.QueryRow(ctx,
		`DELETE FROM calendar_objects WHERE id = $1 RETURNING calendar_id, uid`,
		eventID,
	).Scan(&calendarID, &eventUID)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Nothing to delete — preserve the prior contract of returning nil
			// for a missing event (no change row or CTag bump needed). The
			// deferred Rollback is a no-op since we never wrote anything.
			return nil
		}
		return fmt.Errorf("delete event: delete: %w", err)
	}

	// Record the delete change row so sync-collection can emit a 404
	// tombstone for this event_uid on the next incremental sync.
	if _, err = tx.Exec(ctx,
		`INSERT INTO calendar_changes (calendar_id, event_uid, action) VALUES ($1, $2, 'delete')`,
		calendarID, eventUID,
	); err != nil {
		return fmt.Errorf("delete event: record change: %w", err)
	}

	if _, err = tx.Exec(ctx,
		`UPDATE calendars SET ctag = gen_random_uuid()::text, updated_at = NOW() WHERE id = $1`,
		calendarID,
	); err != nil {
		return fmt.Errorf("delete event: bump ctag: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("delete event: commit: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────
// Maintenance sync. A maintenance task with a due date is an ALL-DAY event on
// the family's Maintenance calendar (fallback: the family's first calendar;
// no calendar: no event), calendar_objects entity_type 'maintenance_task',
// entity_id = task id. Every create/update/delete records a calendar_changes
// row and bumps the calendar ctag so phones sync.
//
// This mirrors apps/mcp/internal/maintenance/task.go (syncEvent,
// recordChange, maintenance_delete) exactly; the two modules cannot share a
// package, so a change to one must be made to the other. Both have mirrored
// integration tests (maintenance_sync_test.go here, tools_test.go there).
//
// Deleting a maintenance event deletes its task through the 027 trigger
// (AFTER DELETE since 038); deleting a task deletes its event through the
// 038 rehome trigger.
// ─────────────────────────────────────────────

// maintenanceCivil returns t's calendar date as midnight UTC, the way pgx
// scans a DATE column.
func maintenanceCivil(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// UpsertMaintenanceEvent creates or updates the calendar event of a
// maintenance task with a due date, in one transaction. An existing event
// stays on its calendar and keeps keys this package doesn't model (the CalDAV
// service's `sequence`, which is bumped). A new event goes on the Maintenance
// calendar, or the first calendar; a family with no calendar gets no event
// and no error.
func (r *Repo) UpsertMaintenanceEvent(ctx context.Context, householdID, taskID uuid.UUID, title, description string, dueDate time.Time) error {
	start := maintenanceCivil(dueDate)
	fields := map[string]any{
		"title":       title,
		"description": description,
		"start":       start.Format(time.RFC3339),
		"end":         start.AddDate(0, 0, 1).Format(time.RFC3339),
		"all_day":     true,
		"event_type":  "maintenance",
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("upsert maintenance event: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var eventID, calendarID, uid, icalData string
	err = tx.QueryRow(ctx,
		`SELECT co.id::text, co.calendar_id::text, co.uid, co.ical_data
		   FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id
		  WHERE c.household_id = $1 AND co.entity_id = $2
		    AND co.entity_type IN ('maintenance', 'maintenance_task')
		  ORDER BY co.created_at LIMIT 1
		  FOR UPDATE OF co`, householdID, taskID,
	).Scan(&eventID, &calendarID, &uid, &icalData)
	switch {
	case err == nil:
		merged := map[string]any{}
		if jerr := json.Unmarshal([]byte(icalData), &merged); jerr != nil {
			merged = map[string]any{}
		}
		if seq, ok := merged["sequence"].(float64); ok {
			merged["sequence"] = seq + 1
		}
		for k, v := range fields {
			merged[k] = v
		}
		b, _ := json.Marshal(merged)
		if _, err := tx.Exec(ctx,
			`UPDATE calendar_objects SET ical_data = $1, etag = $2, event_type = 'maintenance',
			        entity_type = 'maintenance_task', updated_at = NOW()
			  WHERE id = $3`, string(b), uuid.NewString(), eventID); err != nil {
			return fmt.Errorf("upsert maintenance event: update: %w", err)
		}
		if err := recordMaintenanceChange(ctx, tx, calendarID, uid, "update"); err != nil {
			return fmt.Errorf("upsert maintenance event: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("upsert maintenance event: commit: %w", err)
		}
		return nil
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return fmt.Errorf("upsert maintenance event: find event: %w", err)
	}

	err = tx.QueryRow(ctx,
		`SELECT id::text FROM calendars WHERE household_id = $1
		  ORDER BY (type = 'maintenance') DESC, created_at, id LIMIT 1`, householdID,
	).Scan(&calendarID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("upsert maintenance event: find calendar: %w", err)
	}
	eventID = uuid.NewString()
	b, _ := json.Marshal(fields)
	if _, err := tx.Exec(ctx,
		`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
		 VALUES ($1, $2, $3, $4, $5, 'maintenance', 'maintenance_task', $6)`,
		eventID, calendarID, eventID, string(b), uuid.NewString(), taskID); err != nil {
		return fmt.Errorf("upsert maintenance event: insert: %w", err)
	}
	if err := recordMaintenanceChange(ctx, tx, calendarID, eventID, "create"); err != nil {
		return fmt.Errorf("upsert maintenance event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("upsert maintenance event: commit: %w", err)
	}
	return nil
}

// DeleteMaintenanceEvent deletes a maintenance task's calendar events (scoped
// to the household) and records a delete change for each, in one
// transaction. The 027 trigger deletes the task with its event, so callers
// use this to delete the task itself; a task with no event is left alone and
// the caller deletes it directly. A repeat call deletes nothing and records
// nothing.
func (r *Repo) DeleteMaintenanceEvent(ctx context.Context, householdID, taskID uuid.UUID) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("delete maintenance event: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		`DELETE FROM calendar_objects co USING calendars c
		  WHERE c.id = co.calendar_id AND c.household_id = $1
		    AND co.entity_id = $2 AND co.entity_type IN ('maintenance', 'maintenance_task')
		  RETURNING co.calendar_id::text, co.uid`, householdID, taskID)
	if err != nil {
		return fmt.Errorf("delete maintenance event: %w", err)
	}
	type deleted struct{ calendarID, uid string }
	var events []deleted
	for rows.Next() {
		var e deleted
		if err := rows.Scan(&e.calendarID, &e.uid); err != nil {
			rows.Close()
			return fmt.Errorf("delete maintenance event: scan: %w", err)
		}
		events = append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("delete maintenance event: %w", err)
	}
	for _, e := range events {
		if err := recordMaintenanceChange(ctx, tx, e.calendarID, e.uid, "delete"); err != nil {
			return fmt.Errorf("delete maintenance event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("delete maintenance event: commit: %w", err)
	}
	return nil
}

// recordMaintenanceChange adds a calendar_changes row and bumps the
// calendar's ctag (MCP: recordChange).
func recordMaintenanceChange(ctx context.Context, tx pgx.Tx, calendarID, uid, action string) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO calendar_changes (calendar_id, event_uid, action) VALUES ($1, $2, $3)`,
		calendarID, uid, action); err != nil {
		return fmt.Errorf("record calendar change: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE calendars SET ctag = gen_random_uuid()::text, updated_at = NOW() WHERE id = $1`,
		calendarID); err != nil {
		return fmt.Errorf("bump calendar ctag: %w", err)
	}
	return nil
}
