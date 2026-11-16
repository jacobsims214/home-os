package bill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"home-os/icalx"
)

// ─────────────────────────────────────────────
// Calendar mirror (#4485). Every bill with a usable schedule is one all-day
// event on the household's Bills calendar (calendars.type = 'bills'), so
// phones see due dates over CalDAV. The event row has entity_type 'bill',
// entity_id = bill id and event_type 'bill'. It repeats like the bill
// (RRULE from billRule) and has a VALARM reminder BillReminderDays before
// each due date at reminderHour.
//
// bill_create, bill_update and bill_delete call syncBillEvent /
// deleteBillEvent in their own transaction. Like the MCP calendar writers
// (#4465) the event is built as an icalx.Object, and ical_data (JSON),
// ical_raw (VCALENDAR) and rrule are written together from the same master,
// so CalDAV's staleness rule holds and phones get the raw text with the
// RRULE and VALARM. Writes record calendar_changes and bump the calendar
// ctag. No attendees are written (never mailto).
//
// The 038 rehome trigger only unlinks calendar events when a bill row is
// deleted, so deleteBillEvent removes the event before the bill goes.
// Paying a bill does not touch the event: it shows due dates, and the
// agenda / family_overview bills sections show what is still unpaid.
// The API's bill writes and the worker do not mirror yet.
// ─────────────────────────────────────────────

const (
	// CalendarEventType is calendar_objects.event_type (and the JSON
	// event_type) of a bill's mirror event. The calendar tools leave these
	// out of agenda/overview events and conflicts: the bills sections
	// already list bills, with their paid state.
	CalendarEventType = "bill"
	// calendarEntityType is calendar_objects.entity_type of a mirror event.
	calendarEntityType = "bill"
	// BillReminderDays is how many days before a due date phones alert,
	// matching the worker's bill_due window (due within 3 days).
	BillReminderDays = 3
	// reminderHour is the local hour the reminder fires on that day.
	reminderHour = 9
)

// billRule returns the first instance (DTSTART, a civil date at midnight
// UTC) and the RRULE ("" for a once bill) that give exactly the due dates of
// s (see cadence). created is the bill's created_at: a monthly bill has no
// anchor, so its series starts with the due date in the month it was
// created (a stable choice that doesn't move on every update). ok is false
// when s has no usable schedule.
//
// Days 29-31 are clamped to short months as the due rule does: 31 is the
// last day (BYMONTHDAY=-1), 29 and 30 the last of 28..day (BYSETPOS=-1).
func billRule(s Schedule, created time.Time) (start time.Time, rule string, ok bool) {
	c, ok := s.cadence()
	if !ok {
		return time.Time{}, "", false
	}
	switch c.step {
	case 0:
		return c.anchor, "", true
	case 1:
		y, m, _ := created.UTC().Date()
		return dueDateInMonth(y, m, c.day), "FREQ=MONTHLY;" + monthDayRule(c.day), true
	case 12:
		if c.day <= 28 {
			return c.anchor, "FREQ=YEARLY", true
		}
		return c.anchor, fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;%s", int(c.anchor.Month()), monthDayRule(c.day)), true
	default:
		return c.anchor, fmt.Sprintf("FREQ=MONTHLY;INTERVAL=%d;%s", c.step, monthDayRule(c.day)), true
	}
}

// monthDayRule is the BYMONTHDAY (and BYSETPOS) part for a due day clamped
// to the month's length.
func monthDayRule(day int) string {
	switch {
	case day <= 28:
		return fmt.Sprintf("BYMONTHDAY=%d", day)
	case day == 31:
		return "BYMONTHDAY=-1"
	default:
		days := make([]string, 0, 3)
		for d := 28; d <= day; d++ {
			days = append(days, fmt.Sprint(d))
		}
		return "BYMONTHDAY=" + strings.Join(days, ",") + ";BYSETPOS=-1"
	}
}

// billEventTitle is the event's title.
func billEventTitle(r billRow) string { return r.Name + " due" }

// billEventDescription describes the bill on the event.
func billEventDescription(r billRow) string {
	var lines []string
	if r.Amount != nil {
		lines = append(lines, "Amount: "+money(*r.Amount))
	}
	if r.Category != nil && *r.Category != "" {
		lines = append(lines, "Category: "+*r.Category)
	}
	if r.AutoPay {
		lines = append(lines, "Paid automatically (autopay).")
	}
	if r.PaymentURL != nil && *r.PaymentURL != "" {
		lines = append(lines, "Pay at: "+*r.PaymentURL)
	}
	lines = append(lines, "Kept in sync with the bill in Home OS; change the bill, not this event.")
	return strings.Join(lines, "\n")
}

// billMaster builds the mirror event's master VEVENT. ok is false when the
// bill has no usable schedule (no event).
func billMaster(r billRow, uid string, sequence int) (*icalx.Event, bool) {
	start, rule, ok := billRule(r.schedule(), r.CreatedAt)
	if !ok {
		return nil, false
	}
	return &icalx.Event{
		UID:         uid,
		Summary:     billEventTitle(r),
		Description: billEventDescription(r),
		Status:      icalx.StatusConfirmed,
		Sequence:    sequence,
		Start:       start,
		End:         start.AddDate(0, 0, 1),
		AllDay:      true,
		RRule:       rule,
		Alarms: []icalx.Alarm{{
			Action:      icalx.ActionDisplay,
			Description: fmt.Sprintf("%s is due in %d days", r.Name, BillReminderDays),
			// Relative to the all-day start (local midnight of the due
			// day): reminderHour o'clock BillReminderDays days earlier.
			Trigger: -(time.Duration(BillReminderDays)*24*time.Hour - reminderHour*time.Hour),
			Related: icalx.RelatedStart,
		}},
	}, true
}

// billEventJSON is the ical_data JSON for master m, in the shape the
// calendar tools and CalDAV read (start/end as UTC RFC 3339, all-day
// midnights), with sequence carried in the JSON as CalDAV keeps it.
func billEventJSON(m *icalx.Event) (string, error) {
	doc := map[string]any{
		"title":       m.Summary,
		"description": m.Description,
		"start":       m.Start.UTC().Format(time.RFC3339),
		"end":         m.End.UTC().Format(time.RFC3339),
		"all_day":     true,
		"location":    "",
		"event_type":  CalendarEventType,
	}
	if m.Sequence > 0 {
		doc["sequence"] = m.Sequence
	}
	b, err := json.Marshal(doc)
	return string(b), err
}

// storedBillEvent is a bill's mirror event row.
type storedBillEvent struct {
	id, calendarID, uid, icalData string
	rrule                         *string
	hasRaw                        bool
}

// findBillEvent locks and returns the bill's mirror event; ok is false when
// it has none.
func findBillEvent(ctx context.Context, tx pgx.Tx, householdID string, billID uuid.UUID) (storedBillEvent, bool, error) {
	var e storedBillEvent
	err := tx.QueryRow(ctx,
		`SELECT co.id::text, co.calendar_id::text, co.uid, co.ical_data, co.rrule, co.ical_raw IS NOT NULL
		   FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id
		  WHERE c.household_id = $1 AND co.entity_type = $2 AND co.entity_id = $3
		  ORDER BY co.created_at, co.id LIMIT 1
		  FOR UPDATE OF co`, householdID, calendarEntityType, billID,
	).Scan(&e.id, &e.calendarID, &e.uid, &e.icalData, &e.rrule, &e.hasRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, false, nil
	}
	if err != nil {
		return e, false, fmt.Errorf("find bill calendar event: %w", err)
	}
	return e, true, nil
}

// syncBillEvent creates, updates or (when the bill no longer has a usable
// schedule) deletes the bill's mirror event inside tx. It returns the event
// id, "" when there is none (also when the household has no Bills
// calendar). An unchanged event is left alone so phones don't re-download
// it on every bill_update.
func syncBillEvent(ctx context.Context, tx pgx.Tx, householdID string, r billRow) (string, error) {
	stored, found, err := findBillEvent(ctx, tx, householdID, r.ID)
	if err != nil {
		return "", err
	}

	if !found {
		m, ok := billMaster(r, "", 0)
		if !ok {
			return "", nil
		}
		var calendarID string
		err := tx.QueryRow(ctx,
			`SELECT id::text FROM calendars WHERE household_id = $1 AND type = 'bills'
			  ORDER BY created_at, id LIMIT 1`, householdID,
		).Scan(&calendarID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("find bills calendar: %w", err)
		}
		id := uuid.NewString()
		m.UID = id
		data, raw, rule, err := encodeBillEvent(m)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, ical_raw, rrule, etag, event_type, entity_type, entity_id)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			id, calendarID, id, data, raw, rule, uuid.NewString(), CalendarEventType, calendarEntityType, r.ID); err != nil {
			return "", fmt.Errorf("create bill calendar event: %w", err)
		}
		return id, recordCalendarChange(ctx, tx, calendarID, id, "create")
	}

	var prev map[string]any
	if err := json.Unmarshal([]byte(stored.icalData), &prev); err != nil {
		prev = map[string]any{}
	}
	seq := 0
	if s, ok := prev["sequence"].(float64); ok {
		seq = int(s)
	}
	m, ok := billMaster(r, stored.uid, seq)
	if !ok {
		return "", deleteEventRow(ctx, tx, stored)
	}
	if stored.hasRaw && sameBillEvent(prev, stored.rrule, m) {
		return stored.id, nil
	}
	m.Sequence = seq + 1
	data, raw, rule, err := encodeBillEvent(m)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE calendar_objects SET ical_data = $1, ical_raw = $2, rrule = $3, etag = $4,
		        event_type = $5, updated_at = NOW()
		  WHERE id = $6`,
		data, raw, rule, uuid.NewString(), CalendarEventType, stored.id); err != nil {
		return "", fmt.Errorf("update bill calendar event: %w", err)
	}
	return stored.id, recordCalendarChange(ctx, tx, stored.calendarID, stored.uid, "update")
}

// sameBillEvent reports whether the stored JSON and rrule already describe
// master m (sequence aside).
func sameBillEvent(prev map[string]any, storedRule *string, m *icalx.Event) bool {
	want, err := billEventJSON(m)
	if err != nil {
		return false
	}
	var next map[string]any
	if json.Unmarshal([]byte(want), &next) != nil {
		return false
	}
	delete(next, "sequence")
	for k, v := range next {
		if prev[k] != v {
			return false
		}
	}
	rule := ""
	if storedRule != nil {
		rule = *storedRule
	}
	return rule == m.RRule
}

// encodeBillEvent renders master m as the three stored views: ical_data
// JSON, ical_raw VCALENDAR and the rrule column (nil for a once bill).
func encodeBillEvent(m *icalx.Event) (data, raw string, rule *string, err error) {
	if data, err = billEventJSON(m); err != nil {
		return "", "", nil, fmt.Errorf("encode bill event json: %w", err)
	}
	obj := &icalx.Object{UID: m.UID, Master: m}
	b, err := obj.Encode()
	if err != nil {
		return "", "", nil, fmt.Errorf("encode bill event: %w", err)
	}
	if m.RRule != "" {
		r := m.RRule
		rule = &r
	}
	return data, string(b), rule, nil
}

// deleteBillEvent deletes every mirror event of the bill inside tx (call it
// before deleting the bill row).
func deleteBillEvent(ctx context.Context, tx pgx.Tx, householdID string, billID uuid.UUID) error {
	rows, err := tx.Query(ctx,
		`DELETE FROM calendar_objects co USING calendars c
		  WHERE c.id = co.calendar_id AND c.household_id = $1
		    AND co.entity_type = $2 AND co.entity_id = $3
		  RETURNING co.calendar_id::text, co.uid`, householdID, calendarEntityType, billID)
	if err != nil {
		return fmt.Errorf("delete bill calendar event: %w", err)
	}
	var gone []storedBillEvent
	for rows.Next() {
		var e storedBillEvent
		if err := rows.Scan(&e.calendarID, &e.uid); err != nil {
			rows.Close()
			return fmt.Errorf("delete bill calendar event: %w", err)
		}
		gone = append(gone, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("delete bill calendar event: %w", err)
	}
	for _, e := range gone {
		if err := recordCalendarChange(ctx, tx, e.calendarID, e.uid, "delete"); err != nil {
			return err
		}
	}
	return nil
}

// deleteEventRow deletes one mirror event row and records the change.
func deleteEventRow(ctx context.Context, tx pgx.Tx, e storedBillEvent) error {
	if _, err := tx.Exec(ctx, `DELETE FROM calendar_objects WHERE id = $1`, e.id); err != nil {
		return fmt.Errorf("delete bill calendar event: %w", err)
	}
	return recordCalendarChange(ctx, tx, e.calendarID, e.uid, "delete")
}

// recordCalendarChange adds a calendar_changes row and bumps the calendar's
// ctag so CalDAV clients sync (as maintenance.recordChange does; bill can't
// import calendar, which imports bill).
func recordCalendarChange(ctx context.Context, tx pgx.Tx, calendarID, uid, action string) error {
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
