// Package billcal keeps a bill's mirror event on its household's Bills
// calendar (calendars.type = 'bills'), so phones see bill due dates over
// CalDAV (#4485, #4716). It is the one implementation of the mirror: the MCP
// bill tools, the API's bill endpoints (and legacyfinance, which writes bills
// for the property and vehicle forms) and the worker's backfill all call it.
//
// Every bill with a usable schedule is one all-day event: entity_type 'bill',
// entity_id = bill id and event_type 'bill' (EventType). It repeats like the
// bill (Rule) and has one VALARM ReminderDays before each due date at
// ReminderHour. Like the calendar writers (#4458, #4465) the event is built as
// an icalx master and ical_data (JSON), ical_raw (VCALENDAR) and rrule are
// written together from it, so CalDAV's staleness rule holds and phones get
// the raw text with the RRULE and VALARM. Writes add a calendar_changes row and
// bump the calendar ctag. No attendees are written (never mailto).
//
// event_type must stay 'bill': the calendar tools leave such events out of
// agendas and conflicts, and the worker's alarm job skips them (#4724), since
// in-app bill reminders come from the bill_due job.
//
// Only rows with entity_type 'bill' AND event_type 'bill' are ever read,
// changed or deleted here, so an event a person made and linked to a bill is
// never touched.
package billcal

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"home-os/icalx"
)

const (
	// EventType is calendar_objects.event_type (and the JSON event_type) of
	// a bill's mirror event.
	EventType = "bill"
	// EntityType is calendar_objects.entity_type of a mirror event.
	EntityType = "bill"
	// ReminderDays is how many days before a due date phones alert, matching
	// the worker's bill_due window (due within 3 days).
	ReminderDays = 3
	// ReminderHour is the local hour the reminder fires on that day.
	ReminderHour = 9
)

// Bill frequencies (bills.frequency, migration 044). "" is read as monthly
// (rows that predate 044).
const (
	FrequencyMonthly    = "monthly"
	FrequencyQuarterly  = "quarterly"
	FrequencySemiannual = "semiannual"
	FrequencyAnnual     = "annual"
	FrequencyOnce       = "once"
)

// Bill is what the mirror event is built from: the bills columns it shows.
type Bill struct {
	ID         string
	Name       string
	Amount     *float64
	Category   *string
	AutoPay    bool
	PaymentURL *string
	// CreatedAt anchors a monthly bill's series (see Rule).
	CreatedAt time.Time
	Frequency string
	DueDay    *int
	DueDate   *time.Time
}

// stepMonths is the number of months between due dates (1, 3, 6, 12, or 0
// for a once bill); ok is false for an unknown frequency.
func stepMonths(frequency string) (int, bool) {
	switch frequency {
	case FrequencyMonthly, "":
		return 1, true
	case FrequencyQuarterly:
		return 3, true
	case FrequencySemiannual:
		return 6, true
	case FrequencyAnnual:
		return 12, true
	case FrequencyOnce:
		return 0, true
	}
	return 0, false
}

// Rule returns the first instance (DTSTART, a civil date at midnight UTC)
// and the RRULE ("" for a once bill) that give exactly the bill's due dates
// under the bill due rule (#4468): a monthly bill on dueDay of every month,
// other bills every 3, 6 or 12 months from dueDate on its day of the month,
// days 29-31 clamped to short months; a once bill only on dueDate. created is
// the bill's created_at: a monthly bill has no anchor, so its series starts
// with the due date in the month it was created (stable across updates). ok
// is false when the schedule is unusable (unknown frequency, a monthly bill
// without due_day 1-31, another bill without due_date).
//
// The MCP test TestBillRuleMatchesDueRule expands this RRULE with icalx and
// compares it to the MCP due rule (bill.BillDueFor's cadence) over six years
// for every frequency.
func Rule(frequency string, dueDay *int, dueDate *time.Time, created time.Time) (start time.Time, rule string, ok bool) {
	step, ok := stepMonths(frequency)
	if !ok {
		return time.Time{}, "", false
	}
	if step == 1 {
		if dueDay == nil || *dueDay < 1 || *dueDay > 31 {
			return time.Time{}, "", false
		}
		y, m, _ := created.UTC().Date()
		return dueDateInMonth(y, m, *dueDay), "FREQ=MONTHLY;" + monthDayRule(*dueDay), true
	}
	if dueDate == nil {
		return time.Time{}, "", false
	}
	y, m, d := dueDate.Date()
	anchor := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	switch step {
	case 0:
		return anchor, "", true
	case 12:
		if d <= 28 {
			return anchor, "FREQ=YEARLY", true
		}
		return anchor, fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;%s", int(m), monthDayRule(d)), true
	default:
		return anchor, fmt.Sprintf("FREQ=MONTHLY;INTERVAL=%d;%s", step, monthDayRule(d)), true
	}
}

// dueDateInMonth is day of the month, clamped to the month's length.
func dueDateInMonth(year int, month time.Month, day int) time.Time {
	if last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day(); day > last {
		day = last
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// monthDayRule is the BYMONTHDAY (and BYSETPOS) part for a due day clamped
// to the month's length: 31 is the last day (BYMONTHDAY=-1), 29 and 30 the
// last of 28..day (BYSETPOS=-1).
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

// Title is the event's title.
func Title(b Bill) string { return b.Name + " due" }

// Description describes the bill on the event.
func Description(b Bill) string {
	var lines []string
	if b.Amount != nil {
		lines = append(lines, fmt.Sprintf("Amount: $%.2f", *b.Amount))
	}
	if b.Category != nil && *b.Category != "" {
		lines = append(lines, "Category: "+*b.Category)
	}
	if b.AutoPay {
		lines = append(lines, "Paid automatically (autopay).")
	}
	if b.PaymentURL != nil && *b.PaymentURL != "" {
		lines = append(lines, "Pay at: "+*b.PaymentURL)
	}
	lines = append(lines, "Kept in sync with the bill in Home OS; change the bill, not this event.")
	return strings.Join(lines, "\n")
}

// Master builds the mirror event's master VEVENT. ok is false when the bill
// has no usable schedule (no event).
func Master(b Bill, uid string, sequence int) (*icalx.Event, bool) {
	start, rule, ok := Rule(b.Frequency, b.DueDay, b.DueDate, b.CreatedAt)
	if !ok {
		return nil, false
	}
	return &icalx.Event{
		UID:         uid,
		Summary:     Title(b),
		Description: Description(b),
		Status:      icalx.StatusConfirmed,
		Sequence:    sequence,
		Start:       start,
		End:         start.AddDate(0, 0, 1),
		AllDay:      true,
		RRule:       rule,
		Alarms: []icalx.Alarm{{
			Action:      icalx.ActionDisplay,
			Description: fmt.Sprintf("%s is due in %d days", b.Name, ReminderDays),
			// Relative to the all-day start (local midnight of the due
			// day): ReminderHour o'clock ReminderDays days earlier.
			Trigger: -(time.Duration(ReminderDays)*24*time.Hour - ReminderHour*time.Hour),
			Related: icalx.RelatedStart,
		}},
	}, true
}

// EventJSON is the ical_data JSON for master m, in the shape the calendar
// tools, the API and CalDAV read (start/end as UTC RFC 3339, all-day
// midnights), with sequence carried in the JSON as CalDAV keeps it.
func EventJSON(m *icalx.Event) (string, error) {
	doc := map[string]any{
		"title":       m.Summary,
		"description": m.Description,
		"start":       m.Start.UTC().Format(time.RFC3339),
		"end":         m.End.UTC().Format(time.RFC3339),
		"all_day":     true,
		"location":    "",
		"event_type":  EventType,
	}
	if m.Sequence > 0 {
		doc["sequence"] = m.Sequence
	}
	b, err := json.Marshal(doc)
	return string(b), err
}

// Encode renders master m as the three stored views: ical_data JSON,
// ical_raw VCALENDAR and the rrule column (nil for a once bill).
func Encode(m *icalx.Event) (data, raw string, rule *string, err error) {
	if data, err = EventJSON(m); err != nil {
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

// sameEvent reports whether the stored JSON and rrule already describe
// master m (sequence aside).
func sameEvent(prev map[string]any, storedRule *string, m *icalx.Event) bool {
	want, err := EventJSON(m)
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
