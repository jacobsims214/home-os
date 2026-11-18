package billcal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is what the mirror needs: a pgx.Tx in practice, the transaction
// that writes the bill, so the bill and its event change together.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Change actions Sync reports.
const (
	ActionNone   = ""
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionDelete = "delete"
)

// Change is what Sync did. EventID is the bill's mirror event after the
// sync ("" when it has none).
type Change struct {
	EventID string
	Action  string
}

// storedEvent is a bill's mirror event row.
type storedEvent struct {
	id, calendarID, uid, icalData string
	rrule                         *string
	hasRaw                        bool
}

// findEvent locks and returns the bill's mirror event; ok is false when it
// has none.
func findEvent(ctx context.Context, q Querier, householdID, billID string) (storedEvent, bool, error) {
	var e storedEvent
	err := q.QueryRow(ctx,
		`SELECT co.id::text, co.calendar_id::text, co.uid, co.ical_data, co.rrule, co.ical_raw IS NOT NULL
		   FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id
		  WHERE c.household_id = $1 AND co.entity_type = $2 AND co.entity_id = $3
		    AND co.event_type = $4
		  ORDER BY co.created_at, co.id LIMIT 1
		  FOR UPDATE OF co`, householdID, EntityType, billID, EventType,
	).Scan(&e.id, &e.calendarID, &e.uid, &e.icalData, &e.rrule, &e.hasRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, false, nil
	}
	if err != nil {
		return e, false, fmt.Errorf("find bill calendar event: %w", err)
	}
	return e, true, nil
}

// Sync creates, updates or (when the bill no longer has a usable schedule)
// deletes the bill's mirror event inside q. With no event yet it creates one
// on the household's first Bills calendar, or nothing when there is none. An
// unchanged event is left alone, so phones don't re-download it when an
// unrelated field (notes) changes.
//
// The caller holds the bill row's lock (it just inserted or updated it, or
// LoadBill locked it), so two syncs of one bill can't both create an event.
func Sync(ctx context.Context, q Querier, householdID string, b Bill) (Change, error) {
	stored, found, err := findEvent(ctx, q, householdID, b.ID)
	if err != nil {
		return Change{}, err
	}

	if !found {
		m, ok := Master(b, "", 0)
		if !ok {
			return Change{}, nil
		}
		var calendarID string
		err := q.QueryRow(ctx,
			`SELECT id::text FROM calendars WHERE household_id = $1 AND type = 'bills'
			  ORDER BY created_at, id LIMIT 1`, householdID,
		).Scan(&calendarID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, nil
		}
		if err != nil {
			return Change{}, fmt.Errorf("find bills calendar: %w", err)
		}
		id := uuid.NewString()
		m.UID = id
		data, raw, rule, err := Encode(m)
		if err != nil {
			return Change{}, err
		}
		if _, err := q.Exec(ctx,
			`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, ical_raw, rrule, etag, event_type, entity_type, entity_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			id, calendarID, id, data, raw, rule, uuid.NewString(), EventType, EntityType, b.ID); err != nil {
			return Change{}, fmt.Errorf("create bill calendar event: %w", err)
		}
		if err := recordChange(ctx, q, calendarID, id, ActionCreate); err != nil {
			return Change{}, err
		}
		return Change{EventID: id, Action: ActionCreate}, nil
	}

	var prev map[string]any
	if err := json.Unmarshal([]byte(stored.icalData), &prev); err != nil {
		prev = map[string]any{}
	}
	seq := 0
	if s, ok := prev["sequence"].(float64); ok {
		seq = int(s)
	}
	m, ok := Master(b, stored.uid, seq)
	if !ok {
		if _, err := q.Exec(ctx, `DELETE FROM calendar_objects WHERE id = $1`, stored.id); err != nil {
			return Change{}, fmt.Errorf("delete bill calendar event: %w", err)
		}
		if err := recordChange(ctx, q, stored.calendarID, stored.uid, ActionDelete); err != nil {
			return Change{}, err
		}
		return Change{Action: ActionDelete}, nil
	}
	if stored.hasRaw && sameEvent(prev, stored.rrule, m) {
		return Change{EventID: stored.id}, nil
	}
	m.Sequence = seq + 1
	data, raw, rule, err := Encode(m)
	if err != nil {
		return Change{}, err
	}
	if _, err := q.Exec(ctx,
		`UPDATE calendar_objects SET ical_data = $1, ical_raw = $2, rrule = $3, etag = $4,
		        event_type = $5, updated_at = NOW()
		  WHERE id = $6`,
		data, raw, rule, uuid.NewString(), EventType, stored.id); err != nil {
		return Change{}, fmt.Errorf("update bill calendar event: %w", err)
	}
	if err := recordChange(ctx, q, stored.calendarID, stored.uid, ActionUpdate); err != nil {
		return Change{}, err
	}
	return Change{EventID: stored.id, Action: ActionUpdate}, nil
}

// LoadBill reads and locks (FOR UPDATE) the bill's mirror fields; ok is
// false when the household has no such bill.
func LoadBill(ctx context.Context, q Querier, householdID, billID string) (Bill, bool, error) {
	var b Bill
	err := q.QueryRow(ctx,
		`SELECT id::text, name, amount::float8, category, COALESCE(is_autopay, false), payment_url,
		        created_at, COALESCE(frequency, ''), due_day, due_date
		   FROM bills WHERE id = $1 AND household_id = $2
		    FOR UPDATE`, billID, householdID,
	).Scan(&b.ID, &b.Name, &b.Amount, &b.Category, &b.AutoPay, &b.PaymentURL,
		&b.CreatedAt, &b.Frequency, &b.DueDay, &b.DueDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, false, nil
	}
	if err != nil {
		return b, false, fmt.Errorf("load bill for calendar: %w", err)
	}
	return b, true, nil
}

// SyncByID loads (and locks) the bill and syncs its mirror event. A bill
// that doesn't exist in the household changes nothing. Writers that don't
// hold the bill's mirror fields in memory (the API, legacyfinance, the
// backfill) use this after their INSERT or UPDATE, in the same transaction.
func SyncByID(ctx context.Context, q Querier, householdID, billID string) (Change, error) {
	b, ok, err := LoadBill(ctx, q, householdID, billID)
	if err != nil || !ok {
		return Change{}, err
	}
	return Sync(ctx, q, householdID, b)
}

// Delete deletes the bill's mirror events inside q, recording a
// calendar_changes row and bumping the ctag of each calendar touched. Call
// it BEFORE deleting the bill row: the 038 rehome trigger only unlinks
// events from a deleted bill, so the event would otherwise stay on phones.
// It returns how many events it deleted.
func Delete(ctx context.Context, q Querier, householdID, billID string) (int, error) {
	var n int
	err := q.QueryRow(ctx,
		`WITH gone AS (
		     DELETE FROM calendar_objects co USING calendars c
		      WHERE c.id = co.calendar_id AND c.household_id = $1
		        AND co.entity_type = $2 AND co.entity_id = $3 AND co.event_type = $4
		  RETURNING co.calendar_id, co.uid
		 ), changes AS (
		     INSERT INTO calendar_changes (calendar_id, event_uid, action)
		     SELECT calendar_id, uid, 'delete' FROM gone
		 ), bumped AS (
		     UPDATE calendars SET ctag = gen_random_uuid()::text, updated_at = NOW()
		      WHERE id IN (SELECT calendar_id FROM gone)
		 )
		 SELECT count(*) FROM gone`,
		householdID, EntityType, billID, EventType).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("delete bill calendar event: %w", err)
	}
	return n, nil
}

// recordChange adds a calendar_changes row and bumps the calendar's ctag so
// CalDAV clients sync.
func recordChange(ctx context.Context, q Querier, calendarID, uid, action string) error {
	if _, err := q.Exec(ctx,
		`INSERT INTO calendar_changes (calendar_id, event_uid, action) VALUES ($1, $2, $3)`,
		calendarID, uid, action); err != nil {
		return fmt.Errorf("record calendar change: %w", err)
	}
	if _, err := q.Exec(ctx,
		`UPDATE calendars SET ctag = gen_random_uuid()::text, updated_at = NOW() WHERE id = $1`,
		calendarID); err != nil {
		return fmt.Errorf("bump calendar ctag: %w", err)
	}
	return nil
}
