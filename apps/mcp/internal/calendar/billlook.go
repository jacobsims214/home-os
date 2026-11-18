package calendar

// Bill-like calendar entries (#4943): a calendar entry that reads as a bill
// statement but is not the bills module's record of that bill.
//
// War game R1 left two sources of truth for the same money: the bills module
// (bill_find, bill_pay) and round-1 calendar entries ("Electric bill due —
// $180" on the Bills calendar) that nothing kept up to date. Paying the bill
// never touched the entry, so a briefing that read the calendar told Jacob a
// bill he had paid that morning was "still unpaid" (discoveries/
// war-game-r1-jacob.md G2 + G5, war-game-r1-amber.md G3).
//
// Two things stop that from reaching a briefing again:
//
//  1. Migration 073 supersedes an entry it can match unambiguously to a bill
//     (calendar_absorb_bill_events, the SQL twin of billName/billAmounts/
//     titleReadsAsBill below). A superseded entry is skipped by every live
//     read here, so the Bills calendar shows the bill's projection once, and a
//     new bill absorbs its own stale entries on insert.
//  2. Whatever 073 could not match — a second bill of the same name, an amount
//     that disagrees, no bill at all — stays live and is REPORTED, not
//     trusted. billLikeEntries() finds those, and agenda_get lists them in the
//     bills section as calendar entries (bill_like_entries) with the reason, so
//     a model reading the agenda meets "this calendar entry claims a bill, and
//     Home OS has no bill it is the record for" instead of an amount it might
//     repeat as bill state.
//
// The rule is deliberately narrow: an entry only counts when it states a money
// amount or uses a word only a bill would use ("bill", "payment", "invoice",
// "autopay"), or when it sits on a Bills calendar. "Library books due" and
// "Dentist appointment" are not bill-like.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/auth"
)

// billLikeEntry is one live calendar entry that reads as a bill statement and
// is not a bill in the bills module.
type billLikeEntry struct {
	EventID string `json:"event_id" jsonschema_description:"The calendar entry. Change it with event_update, or retire it with event_delete."`
	Title   string `json:"title"`
	// LocalDate is the entry's day, when it has a readable one.
	LocalDate string `json:"local_date,omitempty" jsonschema_description:"The entry's day (YYYY-MM-DD), when it has one."`
	Calendar  string `json:"calendar" jsonschema_description:"The calendar it is on."`
	// Reason says why Home OS did not treat it as a bill, in words a model can
	// repeat: it names the bill it looks like, or says there is none.
	Reason string `json:"reason" jsonschema_description:"Why this is a calendar entry and not bill state. Tell the user this, not the entry's amount as a bill."`
	// BillID is the bill it appears to be about, when exactly one bill matches
	// by name. Never a claim that the entry is current: bill_find has the real
	// amount and paid state.
	BillID *string `json:"bill_id,omitempty" jsonschema_description:"The bill it appears to be about, when one bill matches by name. bill_find has its real amount and paid state."`
}

// The words that make a title read as a bill statement rather than an
// appointment. Narrower than billNameWords: "due" alone is not enough, so
// "Library books due" stays an ordinary event.
//
// Go's regexp has \b, not Postgres's \y, for a word boundary. Postgres also
// counts "_" as a word character and Go does not; no bill name is expected to
// contain an underscore, and the test comparing the two rules (billlook_test.go)
// uses titles, so the difference cannot hide a mismatch.
var (
	billTitleAmount = regexp.MustCompile(`\$[0-9]`)
	billTitleWords  = regexp.MustCompile(`\b(bill|bills|payment|payments|invoice|invoices|autopay)\b`)
	// billNameAmount and billAmountUSD are what billName strips: a money amount
	// is not part of a bill's name.
	billNameAmount = regexp.MustCompile(`\$[0-9][0-9,]*(\.[0-9]+)?`)
	billAmountUSD  = regexp.MustCompile(`\b[0-9][0-9,]*(\.[0-9]{2})?\b\s*(usd|dollars?)\b`)
	// billNameWords are the words that say what kind of record a title is, never
	// which bill it is.
	billNameWords = regexp.MustCompile(`\b(bill|bills|payment|payments|due|autopay|invoice|invoices|reminder|notice|monthly)\b`)
	billNameSep   = regexp.MustCompile(`[^a-z0-9]+`)
	billNameSpace = regexp.MustCompile(`\s+`)
	// billAmountPattern is the same money pattern calendar_bill_amounts()
	// matches (migration 073), comma grouping included.
	billAmountPattern = regexp.MustCompile(`\$([0-9][0-9,]*(\.[0-9]+)?)`)
)

// titleReadsAsBill reports whether a title reads as a bill statement: it states
// a money amount, or uses a word only a bill would use. The SQL twin is
// calendar_title_reads_as_bill() (migration 073).
func titleReadsAsBill(title string) bool {
	t := strings.ToLower(title)
	return billTitleAmount.MatchString(t) || billTitleWords.MatchString(t)
}

// claimsMoney reports whether an entry makes a claim about money: it states an
// amount, or it sits on a Bills calendar. That is the narrower test that keeps
// an entry out of the events list (#4943): a person's own note titled
// "Electric bill" states nothing to be wrong about, while "Electric bill due —
// $180" and anything on the Bills calendar do.
func claimsMoney(title, calendarType string) bool {
	return billTitleAmount.MatchString(strings.ToLower(title)) || calendarType == "bills"
}

// billName is the name a bill statement is about, with the amount and the words
// that only say "this is a bill due" taken out: "Electric bill due — $180",
// "Electric bill" and "ELECTRIC — $180.00" all give "electric". The SQL twin is
// calendar_bill_name() (migration 073).
func billName(title string) string {
	t := strings.ToLower(title)
	t = billNameAmount.ReplaceAllString(t, " ")
	t = billAmountUSD.ReplaceAllString(t, " ")
	t = billNameWords.ReplaceAllString(t, " ")
	t = billNameSep.ReplaceAllString(t, " ")
	return strings.TrimSpace(billNameSpace.ReplaceAllString(t, " "))
}

// billAmounts is every money amount a title states, in order: "$1,234.56" ->
// [1234.56]. Empty when it states none. The SQL twin is
// calendar_bill_amounts() (migration 073).
func billAmounts(title string) []float64 {
	out := []float64{}
	for _, m := range billAmountPattern.FindAllStringSubmatch(strings.ToLower(title), -1) {
		v, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
		if err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

// sameMoney compares two amounts to the cent, the rule migration 073 uses.
func sameMoney(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.005
}

// moneyText formats an amount the way bill_find's summaries do.
func moneyText(v float64) string { return fmt.Sprintf("$%.2f", v) }

// billLikeRowsSQL selects a household's live entries that may read as a bill
// statement and are not a bill's own mirror: the predicate migrations 073 and
// 074's reconcile use, so what it could not supersede is exactly what this
// reports. The ical_data test is a cheap superset — the title decides
// (titleReadsAsBill, then claimsMoney).
//
// A bill's mirror is told apart by entity_type = 'bill' with entity_id set
// (packages/billcal), never by event_type: the round-1 legacy entries this
// guard exists for were typed event_type 'bill' with entity_type NULL, and a
// test on event_type excluded exactly those from being superseded (#5140).
// The SQL function that replaces rows for this predicate is
// calendar_absorb_bill_events(), corrected in migration 074 — keep the two in
// step (testutil/legacy_bill_events_test.go and this package's billlook_test.go
// compare them).
const billLikeRowsSQL = `
	SELECT co.id::text, c.name, c.type::text, co.ical_data
	  FROM calendar_objects co
	  JOIN calendars c ON c.id = co.calendar_id
	 WHERE c.household_id = $1
	   AND co.superseded_at IS NULL
	   AND co.entity_type IS NULL
	 ORDER BY co.starts_at NULLS LAST, co.id
`

// billLikeEntries returns the household's live bill-like calendar entries, each
// with why it is not bill state. bills is the family's bill list (the same rows
// the bills section reads), so an entry is compared against the records rather
// than trusted on its own.
//
// This is the guard #4943 asks for: a bill-like entry that is not a bill is
// surfaced, never trusted as bill state.
func billLikeEntries(ctx context.Context, q querier, householdID string, bills []AgendaBill, loc *time.Location) ([]billLikeEntry, error) {
	rows, err := q.Query(ctx, billLikeRowsSQL, householdID)
	if err != nil {
		return nil, fmt.Errorf("query bill-like calendar entries: %w", err)
	}
	defer rows.Close()

	// The family's bills by normalised name, so an entry can be told apart from
	// the record it claims to be.
	byName := map[string][]AgendaBill{}
	for _, b := range bills {
		if n := billName(b.Name); n != "" {
			byName[n] = append(byName[n], b)
		}
	}

	out := []billLikeEntry{}
	for rows.Next() {
		var id, calendarName, calendarType, data string
		if err := rows.Scan(&id, &calendarName, &calendarType, &data); err != nil {
			return nil, fmt.Errorf("scan bill-like calendar entry: %w", err)
		}
		ej, err := decodeEventJSON(data)
		if err != nil {
			// An unreadable row cannot be judged, and it is not bill state
			// either way; every other calendar read skips it too.
			continue
		}
		// The guard is wider than the events-list rule on purpose: an entry
		// that merely names a bill is worth surfacing too (it says a bill
		// exists, and bill_find is the record), while only one that states an
		// amount or sits on the Bills calendar is kept out of the events list.
		if !titleReadsAsBill(ej.Title) && calendarType != "bills" {
			continue
		}
		e := billLikeEntry{EventID: id, Title: ej.Title, Calendar: calendarName}
		e.LocalDate, e.BillID, e.Reason = billLikeDay(ej, loc), nil, ""
		e.BillID, e.Reason = billLikeReason(ej.Title, byName)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LocalDate != out[j].LocalDate {
			return out[i].LocalDate < out[j].LocalDate
		}
		return out[i].Title < out[j].Title
	})
	return out, nil
}

// billLikeReason says why an entry is a calendar entry rather than bill state,
// naming the bill it appears to be about when exactly one matches by name.
func billLikeReason(title string, byName map[string][]AgendaBill) (*string, string) {
	name := billName(title)
	matches := byName[name]
	switch {
	case name == "":
		return nil, "It reads as a bill but does not name one, so it cannot be matched to a bill. Check bill_find for what is actually due; do not treat this entry as bill state."
	case len(matches) > 1:
		return nil, fmt.Sprintf("More than one bill is called %q, so this entry cannot be matched to one of them. Use bill_find; do not treat this entry as bill state.", matches[0].Name)
	case len(matches) == 1:
		b := matches[0]
		if stated := billAmounts(title); len(stated) > 0 && b.Amount > 0 && !sameMoney(stated[0], b.Amount) {
			id := b.BillID
			return &id, fmt.Sprintf("It names the bill %q but states %s where the bill is %s. bill_find has the amount to trust; do not repeat this entry's amount as the bill's.",
				b.Name, moneyText(stated[0]), moneyText(b.Amount))
		}
		id := b.BillID
		return &id, fmt.Sprintf("It reads like the bill %q, which bill_find already tracks; this calendar entry is not the bill's state, so never report it as due or unpaid.", b.Name)
	default:
		return nil, "Home OS has no bill by this name, so this calendar entry is the only record of it. Ask the user whether to add it with bill_create (that retires the entry), and check bill_find before saying anything is due."
	}
}

// decodeEventJSON decodes a calendar_objects.ical_data row's JSON shape.
func decodeEventJSON(data string) (eventJSON, error) {
	var ej eventJSON
	if err := json.Unmarshal([]byte(data), &ej); err != nil {
		return ej, fmt.Errorf("parse ical_data: %w", err)
	}
	return ej, nil
}

// billLikeDay is an entry's day in the household's zone, or "" when its start
// is unreadable or absent.
func billLikeDay(ej eventJSON, loc *time.Location) string {
	start := strings.TrimSpace(ej.Start)
	if start == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, start); err == nil {
		if loc != nil {
			return t.In(loc).Format(dateLayout)
		}
		return t.UTC().Format(dateLayout)
	}
	if len(start) >= 10 {
		return start[:10]
	}
	return ""
}

// BillLikeEntriesBetween is billLikeEntries for family_overview's next_days:
// the same guard, over the same range, for a caller whose role may see bills.
// A role that may not see money gets nothing — the entries name amounts and
// bill names, which is exactly what #4808 keeps from a housesitter.
func BillLikeEntriesBetween(ctx context.Context, pool *pgxpool.Pool, role, householdID string, loc *time.Location, start, end time.Time) ([]billLikeEntry, error) {
	if !auth.CanSeeMoney(role) {
		return []billLikeEntry{}, nil
	}
	bills, err := allBills(ctx, pool, householdID)
	if err != nil {
		return nil, err
	}
	entries, err := billLikeEntries(ctx, pool, householdID, bills, loc)
	if err != nil {
		return nil, err
	}
	// The family's bills, not the due window: a stale entry is exactly the one
	// whose bill is NOT due, so filtering by the range would hide it.
	out := make([]billLikeEntry, 0, len(entries))
	for _, e := range entries {
		if e.LocalDate == "" || inDayRange(e.LocalDate, start, end, loc) {
			out = append(out, e)
		}
	}
	return out, nil
}

// inDayRange reports whether a YYYY-MM-DD day falls in [start, end), read as
// local days in loc the way agenda_get reads its range.
func inDayRange(day string, start, end time.Time, loc *time.Location) bool {
	d, err := time.Parse(dateLayout, day)
	if err != nil {
		return true // an entry with a broken day is still worth reporting
	}
	first := time.Date(start.In(loc).Year(), start.In(loc).Month(), start.In(loc).Day(), 0, 0, 0, 0, loc)
	last := time.Date(end.In(loc).Year(), end.In(loc).Month(), end.In(loc).Day(), 0, 0, 0, 0, loc)
	t := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	return !t.Before(first) && t.Before(last)
}
