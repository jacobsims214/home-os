package household

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/bill"
	"home-os/mcp/internal/calendar"
	"home-os/mcp/internal/notification"
	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// familyHeader is family_overview.family: who the family is and what day it
// is for them.
type familyHeader struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Timezone string `json:"timezone" jsonschema_description:"IANA timezone, e.g. America/New_York"`
	Today    string `json:"today" jsonschema_description:"Today's date in the family timezone, YYYY-MM-DD"`
	Weekday  string `json:"weekday" jsonschema_description:"Today's weekday in the family timezone, e.g. Thu"`
}

// calendarRow is one entry of family_overview.calendars.
type calendarRow struct {
	ID    string  `json:"id" jsonschema_description:"calendar_id; a calendar name also works wherever calendar_id is asked for"`
	Name  string  `json:"name"`
	Type  string  `json:"type"`
	Color *string `json:"color"`
}

// overview is the data of a family_overview result. Sections follow the
// catalog order (KB architecture/mcp-tool-catalog.md).
type overview struct {
	Family              familyHeader   `json:"family"`
	People              []personRow    `json:"people"`
	Calendars           []calendarRow  `json:"calendars"`
	Counts              areaCounts     `json:"counts" jsonschema_description:"How many of each the family has; list them with the area's find tool"`
	NextDays            nextDays       `json:"next_days"`
	UnreadNotifications int            `json:"unread_notifications" jsonschema_description:"Unread notifications for you, the same number notification_find reports"`
	RecentChanges       []recentChange `json:"recent_changes" jsonschema_description:"The latest changes anyone made, newest first (at most 5)"`
	HowTo               []string       `json:"how_to" jsonschema_description:"Short hints on using the Home OS tools"`
	Warnings            []string       `json:"warnings,omitempty" jsonschema_description:"Sections that failed to load; don't assume they are empty"`
}

// areaCounts is family_overview.counts.
type areaCounts struct {
	Bills     int `json:"bills"`
	Loans     int `json:"loans"`
	Assets    int `json:"assets"`
	Vehicles  int `json:"vehicles"`
	Pets      int `json:"pets"`
	Contacts  int `json:"contacts"`
	Notes     int `json:"notes"`
	Lists     int `json:"lists" jsonschema_description:"Active lists (not archived, not templates)"`
	Recipes   int `json:"recipes"`
	Documents int `json:"documents"`
	Guides    int `json:"guides"`
}

// nextDays is family_overview.next_days: today and the days-1 days after it,
// in brief. Each section lists at most maxPreview items; total is the full
// count and more names the find tool that lists the rest.
type nextDays struct {
	From           string             `json:"from" jsonschema_description:"First day (today), YYYY-MM-DD"`
	To             string             `json:"to" jsonschema_description:"Last day, inclusive, YYYY-MM-DD"`
	Days           int                `json:"days"`
	Events         eventsSection      `json:"events"`
	BillsDue       billsSection       `json:"bills_due"`
	MaintenanceDue maintenanceSection `json:"maintenance_due" jsonschema_description:"Open upkeep tasks due by to, overdue ones included"`
	// BillLikeEntries is the guard against the war game's worst briefing
	// failure (#4943): calendar entries that READ as bill statements but are
	// not a bill's record. They are kept out of `events` on purpose — an entry
	// like "Electric bill due — $180" is a claim about money that nothing
	// keeps up to date — and listed here with the reason instead. Never report
	// one as a bill due or unpaid; bill_find is the record.
	BillLikeEntries []billLikeBrief `json:"bill_like_entries,omitempty" jsonschema_description:"Calendar entries that read as a bill but are NOT bill state. Never report one as a bill due or unpaid; use bill_find."`
}

// billLikeBrief is one calendar entry that looks like a bill but is not one.
type billLikeBrief struct {
	EventID   string  `json:"event_id"`
	Title     string  `json:"title"`
	LocalDate string  `json:"local_date,omitempty"`
	Calendar  string  `json:"calendar"`
	Reason    string  `json:"reason" jsonschema_description:"Why it is a calendar entry and not bill state. Tell the user this."`
	BillID    *string `json:"bill_id,omitempty" jsonschema_description:"The bill it appears to be about, when one matches by name."`
}

type eventsSection struct {
	Items []eventBrief `json:"items"`
	Total int          `json:"total"`
	More  string       `json:"more,omitempty" jsonschema_description:"How to list the rest when total > len(items)"`
}

type billsSection struct {
	Items []billBrief `json:"items"`
	Total int         `json:"total"`
	More  string      `json:"more,omitempty" jsonschema_description:"How to list the rest when total > len(items)"`
}

type maintenanceSection struct {
	Items []taskBrief `json:"items"`
	Total int         `json:"total"`
	More  string      `json:"more,omitempty" jsonschema_description:"How to list the rest when total > len(items)"`
}

// eventBrief is one event in next_days.events.
type eventBrief struct {
	ID        string `json:"id" jsonschema_description:"event_id for event_update or event_delete"`
	Title     string `json:"title"`
	Start     string `json:"start" jsonschema_description:"RFC 3339 in the family timezone"`
	AllDay    bool   `json:"all_day"`
	LocalDate string `json:"local_date" jsonschema_description:"Start day in the family timezone (YYYY-MM-DD)"`
	Weekday   string `json:"weekday"`
	Calendar  string `json:"calendar"`
}

// billBrief is one bill in next_days.bills_due.
type billBrief struct {
	ID        string   `json:"id" jsonschema_description:"bill_id for bill_pay"`
	Name      string   `json:"name"`
	Amount    *float64 `json:"amount" jsonschema_description:"null when the amount varies or is not set"`
	LocalDate string   `json:"local_date" jsonschema_description:"Next unpaid due date (YYYY-MM-DD)"`
	Weekday   string   `json:"weekday"`
	DaysUntil int      `json:"days_until"`
}

// taskBrief is one maintenance task in next_days.maintenance_due.
type taskBrief struct {
	ID        string `json:"id" jsonschema_description:"task_id for maintenance_complete"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	LocalDate string `json:"local_date" jsonschema_description:"Due date (YYYY-MM-DD)"`
	Weekday   string `json:"weekday"`
	Overdue   bool   `json:"overdue"`
}

// recentChange is one entry of family_overview.recent_changes.
type recentChange struct {
	EntityType string `json:"entity_type" jsonschema_description:"What changed, e.g. bill or event"`
	EntityID   string `json:"entity_id"`
	Action     string `json:"action" jsonschema_description:"create, update, delete, or read (an agent read a shared credential)"`
	Summary    string `json:"summary"`
	At         string `json:"at" jsonschema_description:"When, RFC 3339 in the family timezone"`
}

const (
	defaultOverviewDays = 7
	// maxPreview caps each next_days section so the result stays small.
	maxPreview = 10
	// maxRecentChanges caps recent_changes.
	maxRecentChanges = 5
)

// howTo is family_overview.how_to: conventions every tool shares. Only
// registered tool names may appear here (#4671).
var howTo = []string{
	"Dates are YYYY-MM-DD in the family timezone; times are ISO 8601 with an offset, e.g. 2026-11-19T17:30:00-05:00.",
	"Every date in a result also carries local_date and weekday; trust those over your own weekday math.",
	"find tools list and search in one call; pass id for exactly one full record.",
	"When a list is truncated, pass its next_cursor back as cursor (same filters) to get the next page.",
	"person_id comes from people[].id here or person_find; calendar_id accepts a name, e.g. Family.",
	"Not sure which area something is in? search_all looks everywhere at once.",
	"A bill is not a document. For a paper bill, document_upload its text verbatim, then bill_update source_document_id; never call a bill filed without one.",
	"The family owns everything (rename it or change its timezone with family_update): properties have rooms, and an asset sits in a room, a property or the family. Bills, loans and upkeep may be for one property, vehicle or asset; none needs a property.",
}

// NewFamilyOverviewTool creates family_overview (catalog v2; replaces
// get_household and is what protocol tests call instead of ping): the
// family, people, calendars, counts per area, the next days in brief,
// unread notifications, recent changes and how to use the tools. A section
// that fails to load becomes a warning instead of failing the call.
func NewFamilyOverviewTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("family_overview",
		mcp.WithDescription("Start here. One call returns the family's name, timezone, today's date and weekday, people, calendars, counts per area, the next 7 days in brief, and how to use these tools."),
		mcp.WithNumber("days",
			mcp.Description("Days ahead to preview (1-31, default 7)"),
			mcp.Min(1), mcp.Max(31),
		),
		mcp.WithTitleAnnotation("Family overview"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.Schema[overview](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}
		// The household role policy (#4717): family_overview answers every
		// role, so each section is trimmed to what the caller may see. The
		// role is re-read from memberships, and a linked vendor's vendors id
		// with it (#4799), so a demotion or a new link applies at once.
		view, bad := overviewViewFor(ctx, pool, claims)
		if bad != nil {
			return bad, nil
		}
		days, given, ok := intArg(req.GetArguments(), "days")
		if !ok || (given && (days < 1 || days > 31)) {
			return respond.Invalid("days", "days must be a whole number from 1 to 31.",
				"Pass days as a number, or leave it out for 7.", map[string]any{"days": defaultOverviewDays}), nil
		}
		if !given {
			days = defaultOverviewDays
		}
		hh := claims.HouseholdID

		var ov overview
		err := pool.QueryRow(ctx,
			`SELECT id::text, name, timezone FROM households WHERE id = $1`, hh,
		).Scan(&ov.Family.ID, &ov.Family.Name, &ov.Family.Timezone)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("household", hh,
				"Tell the user to reconnect Home OS; retrying will not help."), nil
		}
		if err != nil {
			return respond.Failed("load the family", err), nil
		}
		// One clock reading for every section, so today, next_days and the
		// bill due dates never disagree across midnight.
		now := time.Now()
		loc := familyLocation(ov.Family.Timezone)
		ov.Family.Today, ov.Family.Weekday = familyClock(now, ov.Family.Timezone)

		// Every section after the household row is independent: load them
		// concurrently (each on its own pool connection) so the call costs
		// about one round trip plus the slowest query, not their sum.
		var (
			mu                 sync.Mutex
			peopleErr, calsErr error
		)
		warn := func(section string, err error) {
			slog.Error("family_overview: load section", "section", section, "error", err)
			mu.Lock()
			defer mu.Unlock()
			ov.Warnings = append(ov.Warnings, fmt.Sprintf("Could not load %s; do not assume it is empty.", section))
		}
		// A section the role may not see is never queried: the values do not
		// enter the process, and a housesitter's overview cannot be slowed or
		// broken by the family's finance rows (the same rule the API's
		// property repo follows, #4806).
		parallel(
			func() {
				if !view.people {
					ov.People = []personRow{}
					return
				}
				rows, err := pool.Query(ctx, peopleSelect+peopleOrder, hh)
				if err == nil {
					ov.People, _, err = scanPeople(rows)
				}
				peopleErr = err
			},
			func() {
				if !view.calendars {
					ov.Calendars = []calendarRow{}
					return
				}
				ov.Calendars, calsErr = loadCalendars(ctx, pool, hh)
			},
			func() {
				var err error
				if ov.Counts, err = loadCounts(ctx, pool, hh); err != nil {
					warn("counts", err)
				}
			},
			func() { ov.NextDays = loadNextDays(ctx, pool, hh, loc, now, days, view, warn) },
			func() {
				var err error
				if ov.UnreadNotifications, err = notification.UnreadCount(ctx, pool, hh, claims.UserID); err != nil {
					warn("unread_notifications", err)
				}
			},
			func() {
				if !view.recentChanges {
					ov.RecentChanges = []recentChange{}
					return
				}
				var err error
				if ov.RecentChanges, err = loadRecentChanges(ctx, pool, hh, loc); err != nil {
					warn("recent_changes", err)
				}
			},
		)
		if peopleErr != nil {
			return respond.Failed("load the family's people", peopleErr), nil
		}
		if calsErr != nil {
			return respond.Failed("load the family's calendars", calsErr), nil
		}
		// Trim in one place, after loading: a section added to the overview
		// without a view decision is caught by the trim, not by each loader.
		view.apply(&ov)
		sort.Strings(ov.Warnings)
		ov.HowTo = howTo

		return respond.OK(overviewSummary(ov, view), ov, overviewNext(ov, view)...), nil
	}
	return tool.Name, tool, handler
}

// familyLocation is the family timezone, UTC when empty or unknown (the
// calendar tools' fallback).
func familyLocation(tz string) *time.Location {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		return time.UTC
	}
	return loc
}

func loadCalendars(ctx context.Context, pool *pgxpool.Pool, hh string) ([]calendarRow, error) {
	rows, err := pool.Query(ctx,
		`SELECT id::text, name, type::text, color FROM calendars WHERE household_id = $1 ORDER BY name, id`, hh)
	if err != nil {
		return nil, err
	}
	cals, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (calendarRow, error) {
		var c calendarRow
		err := r.Scan(&c.ID, &c.Name, &c.Type, &c.Color)
		return c, err
	})
	if cals == nil {
		cals = []calendarRow{}
	}
	return cals, err
}

// loadCounts counts every area in one round trip; each subquery uses the
// table's household index.
func loadCounts(ctx context.Context, pool *pgxpool.Pool, hh string) (areaCounts, error) {
	var c areaCounts
	err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM bills     WHERE household_id = $1),
		(SELECT count(*) FROM loans     WHERE household_id = $1),
		(SELECT count(*) FROM assets    WHERE household_id = $1),
		(SELECT count(*) FROM vehicles  WHERE household_id = $1),
		(SELECT count(*) FROM pets      WHERE household_id = $1),
		(SELECT count(*) FROM vendors   WHERE household_id = $1),
		(SELECT count(*) FROM notes     WHERE household_id = $1),
		(SELECT count(*) FROM lists     WHERE household_id = $1 AND NOT archived AND NOT is_template),
		(SELECT count(*) FROM recipes   WHERE household_id = $1),
		(SELECT count(*) FROM files     WHERE household_id = $1),
		(SELECT count(*) FROM guides    WHERE household_id = $1)`, hh,
	).Scan(&c.Bills, &c.Loans, &c.Assets, &c.Vehicles, &c.Pets, &c.Contacts,
		&c.Notes, &c.Lists, &c.Recipes, &c.Documents, &c.Guides)
	return c, err
}

// loadNextDays builds next_days for today (in loc, as of now) and the
// days-1 days after it. Events follow agenda_get (calendar.EventsBetween),
// bills follow bill_find (bill.BillDueFor: the next unpaid due date, any
// frequency), and
// maintenance follows maintenance_find due_within_days (open tasks, overdue
// included). A failed section is reported through warn and left empty.
func loadNextDays(ctx context.Context, pool *pgxpool.Pool, hh string, loc *time.Location, now time.Time,
	days int, view overviewView, warn func(string, error)) nextDays {
	y, m, d := now.In(loc).Date()
	first := time.Date(y, m, d, 0, 0, 0, 0, loc)
	end := time.Date(y, m, d+days, 0, 0, 0, 0, loc) // DST-safe
	last := time.Date(y, m, d+days-1, 0, 0, 0, 0, loc)
	nd := nextDays{
		From: first.Format(time.DateOnly), To: last.Format(time.DateOnly), Days: days,
		BillLikeEntries: []billLikeBrief{},
	}
	within := days - 1                                // due_within_days for the same window
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC) // bill dates are midnight UTC

	parallel(
		func() {
			if !view.calendars {
				nd.Events = eventsSection{Items: []eventBrief{}}
				return
			}
			// The caller's role goes through to the event read: a bill mirror
			// whose title is the bill's name is not shown to a role that may not
			// see money (#4808, same rule as event_find).
			nd.Events = eventsPreview(ctx, pool, view.role, hh, loc, first, end, nd.From, nd.To, warn)
		},
		func() {
			if !view.billsDue {
				nd.BillsDue = billsSection{Items: []billBrief{}}
				return
			}
			nd.BillsDue = billsPreview(ctx, pool, hh, today, within, warn)
		},
		func() {
			if !view.maintenance {
				nd.MaintenanceDue = maintenanceSection{Items: []taskBrief{}}
				return
			}
			nd.MaintenanceDue = maintenancePreview(ctx, pool, hh, today, within, view.ownJobs, warn)
		},
		func() {
			// The guard (#4943): a calendar entry that reads as a bill is
			// reported, never trusted as bill state. A role that may not see
			// money gets none of them (BillLikeEntriesBetween drops them), so a
			// housesitter's briefing cannot leak a bill's name or amount.
			if !view.billsDue {
				nd.BillLikeEntries = []billLikeBrief{}
				return
			}
			entries, err := calendar.BillLikeEntriesBetween(ctx, pool, view.role, hh, loc, first, end)
			if err != nil {
				warn("next_days.bill_like_entries", err)
				nd.BillLikeEntries = []billLikeBrief{}
				return
			}
			nd.BillLikeEntries = make([]billLikeBrief, 0, len(entries))
			for _, e := range entries {
				nd.BillLikeEntries = append(nd.BillLikeEntries, billLikeBrief{
					EventID: e.EventID, Title: e.Title, LocalDate: e.LocalDate,
					Calendar: e.Calendar, Reason: e.Reason, BillID: e.BillID,
				})
			}
		})
	return nd
}

// parallel runs fns concurrently and waits for all of them.
func parallel(fns ...func()) {
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}
	wg.Wait()
}

func eventsPreview(ctx context.Context, pool *pgxpool.Pool, role, hh string, loc *time.Location, first, end time.Time,
	from, to string, warn func(string, error)) eventsSection {
	sec := eventsSection{Items: []eventBrief{}}
	if events, err := calendar.EventsBetween(ctx, pool, role, hh, loc, first, end); err != nil {
		warn("next_days.events", err)
	} else {
		sec.Total = len(events)
		for _, ev := range events[:min(len(events), maxPreview)] {
			sec.Items = append(sec.Items, eventBrief{
				ID: ev.ID, Title: ev.Title, Start: ev.Start, AllDay: ev.AllDay,
				LocalDate: ev.LocalDate, Weekday: ev.Weekday, Calendar: ev.CalendarName,
			})
		}
		if sec.Total > len(sec.Items) {
			sec.More = fmt.Sprintf("%d more: agenda_get from=%s to=%s, or event_find from=%s to=%s.",
				sec.Total-len(sec.Items), from, to, from, to)
		}
	}
	return sec
}

func billsPreview(ctx context.Context, pool *pgxpool.Pool, hh string, today time.Time, within int, warn func(string, error)) billsSection {
	sec := billsSection{Items: []billBrief{}}
	bills, err := billsDue(ctx, pool, hh, today, within)
	if err != nil {
		warn("next_days.bills_due", err)
		return sec
	}
	sec.Total = len(bills)
	sec.Items = bills[:min(len(bills), maxPreview)]
	if sec.Total > len(sec.Items) {
		sec.More = fmt.Sprintf("%d more: bill_find due_within_days=%d.", sec.Total-len(sec.Items), within)
	}
	return sec
}

func maintenancePreview(ctx context.Context, pool *pgxpool.Pool, hh string, today time.Time, within int,
	ownJobs *uuid.UUID, warn func(string, error)) maintenanceSection {
	sec := maintenanceSection{Items: []taskBrief{}}
	tasks, total, err := maintenanceDue(ctx, pool, hh, today, today.AddDate(0, 0, within), ownJobs)
	if err != nil {
		warn("next_days.maintenance_due", err)
		return sec
	}
	sec.Items, sec.Total = tasks, total
	if total > len(tasks) {
		sec.More = fmt.Sprintf("%d more: maintenance_find due_within_days=%d.", total-len(tasks), within)
	}
	return sec
}

// billsDue returns every bill whose next unpaid due date (bill.BillDueFor, the
// rule bill_find uses) is within `within` days of today, by date then name.
func billsDue(ctx context.Context, pool *pgxpool.Pool, hh string, today time.Time, within int) ([]billBrief, error) {
	rows, err := pool.Query(ctx,
		`SELECT id::text, name, amount::float8, frequency, due_day, due_date, paid_date, paid_for_due_date
		   FROM bills WHERE household_id = $1 AND (due_day IS NOT NULL OR due_date IS NOT NULL)
		  ORDER BY name, id`, hh)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []billBrief{}
	for rows.Next() {
		var b billBrief
		var s bill.Schedule
		var paidDate, paidFor *time.Time
		if err := rows.Scan(&b.ID, &b.Name, &b.Amount, &s.Frequency, &s.DueDay, &s.DueDate, &paidDate, &paidFor); err != nil {
			return nil, fmt.Errorf("scan bill: %w", err)
		}
		due, ok := bill.BillDueFor(today, s, paidDate, paidFor)
		if !ok || due.Next.IsZero() || due.DaysUntil < 0 || due.DaysUntil > within {
			continue
		}
		b.LocalDate, b.Weekday, b.DaysUntil = due.Next.Format(time.DateOnly), due.Next.Weekday().String(), due.DaysUntil
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LocalDate < out[j].LocalDate })
	return out, nil
}

// maintenanceDue returns up to maxPreview open tasks due on or before last
// (overdue first), plus how many there are in all.
// maintenanceDue returns up to maxPreview open tasks due on or before last
// (overdue first), plus how many there are in all. ownJobs limits it to one
// vendor's own jobs (#4799), the same rule maintenance_find, search_all and
// the API apply.
func maintenanceDue(ctx context.Context, pool *pgxpool.Pool, hh string, today, last time.Time, ownJobs *uuid.UUID) ([]taskBrief, int, error) {
	rows, err := pool.Query(ctx,
		`SELECT id::text, name, status::text, due_date, count(*) OVER ()
		   FROM maintenance_tasks
		  WHERE household_id = $1 AND status IN ('pending', 'in_progress') AND due_date <= $2
		    AND ($4::uuid IS NULL OR vendor_id = $4::uuid)
		  ORDER BY due_date, name, id
		  LIMIT $3`, hh, last, maxPreview, ownJobs)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []taskBrief{}
	total := 0
	for rows.Next() {
		var t taskBrief
		var due time.Time
		if err := rows.Scan(&t.ID, &t.Name, &t.Status, &due, &total); err != nil {
			return nil, 0, fmt.Errorf("scan maintenance task: %w", err)
		}
		t.LocalDate, t.Weekday, t.Overdue = due.Format(time.DateOnly), due.Weekday().String(), due.Before(today)
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// loadRecentChanges returns the family's latest activity_log entries
// (idx_activity_log_household_created), newest first.
func loadRecentChanges(ctx context.Context, pool *pgxpool.Pool, hh string, loc *time.Location) ([]recentChange, error) {
	rows, err := pool.Query(ctx,
		`SELECT entity_type, entity_id::text, action, summary, created_at
		   FROM activity_log WHERE household_id = $1
		  ORDER BY created_at DESC, id DESC LIMIT $2`, hh, maxRecentChanges)
	if err != nil {
		return []recentChange{}, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (recentChange, error) {
		var c recentChange
		var at time.Time
		err := r.Scan(&c.EntityType, &c.EntityID, &c.Action, &c.Summary, &at)
		c.At = at.In(loc).Format(time.RFC3339)
		return c, err
	})
	if out == nil {
		out = []recentChange{}
	}
	return out, err
}

// overviewSummary describes the overview the caller actually got: a section
// the role may not see is left out of the sentence rather than reported as
// empty, so the model does not tell the user the family has no bills.
func overviewSummary(ov overview, view overviewView) string {
	nd := ov.NextDays
	var parts []string
	if view.visible("people") {
		parts = append(parts, fmt.Sprintf("%d %s", len(ov.People), plural(len(ov.People), "person", "people")))
	}
	if view.visible("calendars") {
		parts = append(parts, fmt.Sprintf("%d %s", len(ov.Calendars), plural(len(ov.Calendars), "calendar", "calendars")))
	}
	s := ov.Family.Name
	if len(parts) > 0 {
		s += ": " + strings.Join(parts, ", ")
	}
	s += fmt.Sprintf(". Today is %s %s (%s).", ov.Family.Weekday, ov.Family.Today, ov.Family.Timezone)

	var due []string
	if view.visible("bills_due") {
		due = append(due, fmt.Sprintf("%d %s due", nd.BillsDue.Total, plural(nd.BillsDue.Total, "bill", "bills")))
	}
	if view.visible("maintenance_due") {
		due = append(due, fmt.Sprintf("%d maintenance %s due", nd.MaintenanceDue.Total, plural(nd.MaintenanceDue.Total, "task", "tasks")))
	}
	head := fmt.Sprintf(" Next %d %s: ", nd.Days, plural(nd.Days, "day", "days"))
	if view.visible("calendars") {
		head += fmt.Sprintf("%d %s", nd.Events.Total, plural(nd.Events.Total, "event", "events"))
		if len(due) > 0 {
			head += ", "
		}
	}
	s += head + strings.Join(due, ", ") + "."
	if len(parts) == 0 && len(due) == 0 && !view.visible("calendars") {
		// Nothing about the family's own records is shared with this role.
		s += " Your role sees only the family name, its timezone and your own notifications."
	}
	s += fmt.Sprintf(" %d unread %s.", ov.UnreadNotifications, plural(ov.UnreadNotifications, "notification", "notifications"))
	if len(ov.Warnings) > 0 {
		s += " Some sections failed to load; see warnings."
	}
	return s
}

// overviewNext suggests next steps from what the overview found. Only
// registered tool names (#4671), and only tools and records the caller's role
// may use: a hint naming a tool the role policy refuses is a call the model
// cannot make (#4771).
func overviewNext(ov overview, view overviewView) []string {
	nd := ov.NextDays
	next := []string{fmt.Sprintf("See the days in full with agenda_get from=%s to=%s.", nd.From, nd.To)}
	if len(nd.BillsDue.Items) > 0 {
		next = append(next, fmt.Sprintf("Mark a bill paid with bill_pay bill_id=%s.", nd.BillsDue.Items[0].ID))
	}
	if len(nd.MaintenanceDue.Items) > 0 {
		next = append(next, fmt.Sprintf("Mark upkeep done with maintenance_complete task_id=%s.", nd.MaintenanceDue.Items[0].ID))
	}
	if ov.UnreadNotifications > 0 {
		next = append(next, "Read notifications with notification_find unread_only=true.")
	}
	if view.visible("calendars") {
		next = append(next, "Add an event with event_create.")
	}
	if view.visible("people") {
		next = append(next, "Look someone up with person_find.")
	}
	return next
}
