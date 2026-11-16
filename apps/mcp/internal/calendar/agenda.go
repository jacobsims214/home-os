package calendar

// agenda_get (#4810): the family's week in one small answer.
//
// A default call used to return ~35 KB for a week (~146 KB for a month) on the
// bench household: every occurrence of every repeating series with every
// field, and every bill due. A small local model cannot use that, and the
// 2026-11-15 war game showed the cost (agents looping on repeated briefings,
// turns dying at the 600s limit). The pattern is #4809's (KB
// mcp/small-model-payloads.md), applied here: the default is a summary in two
// dimensions at once (less per item, and caps with totals), and every result
// carries a coverage block first saying what it covers and what it left out,
// with how_to_get_detail naming the exact arguments that return the rest.
// Nothing is silently truncated: a cap always carries its total, and coverage
// says how many items are not listed.
//
// The event-kind rule is apps/api/internal/calendar/access.go's (#4807),
// applied through auth.CanSeeEntity so the policy table stays the only source:
// a housesitter keeps ordinary events and loses upkeep and vehicle ones, and a
// role that may not see money gets no bills section at all.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/bill"
	"home-os/mcp/internal/meals"
	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

const (
	// maxAgendaDays is the longest range one call may ask for.
	maxAgendaDays = 31
	// defaultAgendaDays is the default range: today plus six days. A week is
	// what "what's this week look like?" means, and it is the size a small
	// model can hold.
	defaultAgendaDays = 7
	// defaultAgendaPerDay caps one day's one-off events at summary detail. A
	// typical family day holds two or three; a day with more still reports the
	// full count in coverage.events_occurrences, and max_per_day raises the cap.
	// Repeating series are one entry with their dates, so they never need it.
	defaultAgendaPerDay = 6
	// maxAgendaPerDay is the largest max_per_day a caller may ask for.
	maxAgendaPerDay = 50
	// agendaSummaryBills is how many bills a summary lists: the soonest ones,
	// by due date. A busy family's week holds dozens; coverage still counts and
	// sums every one, so the total is never a sample.
	agendaSummaryBills = 8
	// agendaSummaryListItems is how many due list items a summary lists; the
	// full answer and list_get have the rest.
	agendaSummaryListItems = 8
	// agendaSummaryOpenLists is how many open lists a summary names. A family
	// with more sees open_total and gets the rest on request: list_find.
	agendaSummaryOpenLists = 5
)

// agendaAreas are the areas agenda_get accepts. The meals area is the meal
// plan (meals.AgendaMeals, #4487); the catalog has no separate meal-read tool.
var (
	agendaAreas      = []string{"events", "bills", "maintenance", "meals", "lists"}
	agendaAreasOrder = []string{"events", "bills", "maintenance", "meals", "lists"}
	// agendaDefaultAreas is what a call with no areas returns: the day-to-day
	// picture. Upkeep is the one area with its own read tool
	// (maintenance_find), so it is left out of the default and named in
	// coverage.not_included.
	agendaDefaultAreas = []string{"events", "bills", "meals", "lists"}
)

// agendaDetailLevels are the detail levels, smallest first.
const (
	agendaDetailSummary = "summary"
	agendaDetailFull    = "full"
)

var agendaDetailLevels = []string{agendaDetailSummary, agendaDetailFull}

// The lists area is kept small (the agenda is already large for a month):
// at most agendaMaxListItems due items, and when the range includes today
// at most agendaMaxOpenLists open lists with agendaListPreview item texts
// each. list_get has the rest.
const (
	agendaMaxListItems = 20
	agendaMaxOpenLists = 10
	agendaListPreview  = 3
)

// AgendaBill is a bill due in the agenda's range.
type AgendaBill struct {
	BillID   string  `json:"bill_id"`
	Name     string  `json:"name"`
	Amount   float64 `json:"amount"`
	Category string  `json:"category,omitempty"`
	DueDate  string  `json:"local_date" jsonschema_description:"Due date (YYYY-MM-DD)."`
	Weekday  string  `json:"weekday"`
}

// AgendaTask is a maintenance task due in the agenda's range.
type AgendaTask struct {
	TaskID      string  `json:"task_id"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Status      string  `json:"status"`
	DueDate     string  `json:"local_date" jsonschema_description:"Due date (YYYY-MM-DD)."`
	Weekday     string  `json:"weekday"`
	PropertyID  *string `json:"property_id,omitempty"`
}

// AgendaListItem is an open (unchecked) list item due in the agenda's range.
type AgendaListItem struct {
	ListID   string  `json:"list_id"`
	ListName string  `json:"list_name"`
	ItemID   string  `json:"item_id" jsonschema_description:"Pass it in item_ids to list_check_items."`
	Text     string  `json:"text"`
	DueDate  string  `json:"local_date" jsonschema_description:"Due date (YYYY-MM-DD)."`
	Weekday  string  `json:"weekday"`
	Assignee *string `json:"assignee_name,omitempty" jsonschema_description:"Who does it, if assigned."`
}

// AgendaOpenList is an active list with items still open.
type AgendaOpenList struct {
	ListID    string   `json:"list_id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind" jsonschema_description:"grocery, packing, shopping, todo, chores or other."`
	OpenItems int      `json:"open_items" jsonschema_description:"Unchecked items on the list."`
	Preview   []string `json:"preview" jsonschema_description:"The first few open items, in list order; list_get has them all."`
}

// AgendaLists is the agenda's lists section.
type AgendaLists struct {
	Due       []AgendaListItem `json:"due" jsonschema_description:"Open items due in the range, by date then list; at most 20."`
	DueTotal  int              `json:"due_total" jsonschema_description:"All open items due in the range (more than due holds when capped)."`
	Open      []AgendaOpenList `json:"open,omitempty" jsonschema_description:"Only when the range includes today: active lists with open items, by name; at most 10."`
	OpenTotal int              `json:"open_total,omitempty" jsonschema_description:"Active lists with open items (more than open holds when capped)."`
}

// AgendaEvent is one entry of the agenda's events section.
//
// At detail=summary an entry says what is on, which day and what time, and
// nothing a small model does not need: a repeating series is ONE entry with
// every occurrence day in the range (dates) and how it repeats, and a one-off
// event is one entry. Both use a local day plus a clock time (at) rather than
// two RFC 3339 instants, since the coverage block already names the timezone;
// there is no description, color, event type or calendar id. At detail=full
// every entry is one occurrence with every field, exactly as event_find
// returns it: start and end are RFC 3339 instants, and last_date is absent.
type AgendaEvent struct {
	ID    string `json:"id" jsonschema_description:"Event id; pass it as event_id to event_update or event_delete."`
	Title string `json:"title"`

	// Summary shape: the day and the clock time, in the coverage timezone.
	LocalDate string `json:"local_date,omitempty" jsonschema_description:"Day in the family timezone (YYYY-MM-DD); absent on a series entry, where dates[0] is its first day."`
	Weekday   string `json:"weekday,omitempty" jsonschema_description:"Weekday of local_date, e.g. Wednesday; absent on a series entry."`
	At        string `json:"at,omitempty" jsonschema_description:"Start time of day, HH:MM 24-hour in the family timezone; absent for an all-day event."`
	Until     string `json:"until,omitempty" jsonschema_description:"End time of day, HH:MM, when it is worth knowing: the event runs 3 hours or more, or past midnight."`
	AllDay    bool   `json:"all_day,omitempty" jsonschema_description:"true for a whole-day event; absent for a timed one."`
	// LastDate is the last day of a multi-day event (a field trip, a holiday);
	// without it a three-day event would read as one day.
	LastDate string `json:"last_date,omitempty" jsonschema_description:"Last day of a multi-day event (YYYY-MM-DD); absent when it lasts one day."`
	Location string `json:"location,omitempty" jsonschema_description:"Where, when the event has one."`

	// Series shape, summary detail only.
	Repeat string `json:"repeat,omitempty" jsonschema_description:"How it repeats, in words, e.g. every week on Mon, Wed; absent for a one-off."`
	// Dates is every occurrence day, so one series is one entry rather than one
	// entry a day: a weekly series in a week is a single date, a daily one lists
	// all seven.
	Dates       []string `json:"dates,omitempty" jsonschema_description:"Every occurrence day in the range (YYYY-MM-DD). The series is one entry, not one entry a day."`
	Occurrences int      `json:"occurrences,omitempty" jsonschema_description:"How many occurrences the series has in the range; dates lists every one."`

	// Full shape, detail=full only: the instants and the rest of the fields.
	Start        string  `json:"start,omitempty" jsonschema_description:"detail=full only: start, RFC 3339 in the family timezone."`
	End          string  `json:"end,omitempty" jsonschema_description:"detail=full only: exclusive end, RFC 3339."`
	Description  *string `json:"description,omitempty" jsonschema_description:"detail=full only: the event's notes."`
	CalendarID   string  `json:"calendar_id,omitempty" jsonschema_description:"detail=full only."`
	CalendarName string  `json:"calendar_name,omitempty" jsonschema_description:"detail=full only."`
	Color        string  `json:"color,omitempty" jsonschema_description:"detail=full only."`
	EventType    string  `json:"event_type,omitempty" jsonschema_description:"detail=full only."`
	RecurrenceID string  `json:"recurrence_id,omitempty" jsonschema_description:"detail=full only: the occurrence's original start, for occurrence_date."`
}

// AgendaMeal is one planned meal. detail=summary keeps the day, the slot and
// what is planned (with any allergy warning, which is never dropped);
// detail=full is meals.PlannedMeal exactly, with ids, notes and ratings.
type AgendaMeal struct {
	LocalDate string `json:"local_date" jsonschema_description:"YYYY-MM-DD in the family timezone."`
	Weekday   string `json:"weekday" jsonschema_description:"e.g. Tuesday."`
	Slot      string `json:"slot" jsonschema_description:"breakfast, lunch, dinner or snack."`
	Title     string `json:"title" jsonschema_description:"What is eaten: the recipe name or free text."`
	// AllergyWarnings is the one field never trimmed: it is the reason the
	// meal plan reaches an agent at all.
	AllergyWarnings []string `json:"allergy_warnings,omitempty" jsonschema_description:"e.g. Nora (peanuts): peanut butter. Always tell the user."`

	// detail=full only.
	MealID   string             `json:"meal_id,omitempty" jsonschema_description:"detail=full only."`
	RecipeID *string            `json:"recipe_id,omitempty" jsonschema_description:"detail=full only: the recipe, for recipe_get."`
	Notes    *string            `json:"notes,omitempty" jsonschema_description:"detail=full only."`
	Ratings  []meals.MealRating `json:"ratings,omitempty" jsonschema_description:"detail=full only: how each person liked it."`
}

// agendaCoverage says what an agenda_get result covers and what it left out.
//
// It is the guard against the war game's worst failure: an agent answering
// "nothing is due" from an answer that was never the whole picture (#4809's
// rule, applied to the agenda). It is the first field of the payload, so a
// model reading top-down meets the scope before any event.
type agendaCoverage struct {
	Detail    string   `json:"detail" jsonschema_description:"summary (one entry per repeating series, fewer fields, one-off events capped per day) or full (every occurrence with every field)"`
	From      string   `json:"from" jsonschema_description:"First day covered, YYYY-MM-DD"`
	To        string   `json:"to" jsonschema_description:"Last day covered, inclusive, YYYY-MM-DD"`
	Days      int      `json:"days" jsonschema_description:"How many days the range covers"`
	Timezone  string   `json:"timezone" jsonschema_description:"The family timezone every date and time in this result is in"`
	Calendars []string `json:"calendars,omitempty" jsonschema_description:"The calendars searched for events. An event on a calendar not listed was not looked at"`
	Scope     string   `json:"scope" jsonschema_description:"What this result covers, in words. Nothing outside this scope is counted or implied to be missing"`
	Areas     []string `json:"areas" jsonschema_description:"The areas this result carries"`
	// NotIncluded names the areas that exist but are not here, so a model
	// knows their numbers are not merely zero.
	NotIncluded []string `json:"not_included,omitempty" jsonschema_description:"Areas that exist but are not in this result. Ask for one with areas to get it"`
	// Hidden names the areas this role may not see at all, so "no bills" is
	// never mistaken for "no bills are due".
	Hidden []string `json:"hidden,omitempty" jsonschema_description:"Areas this role may not see. Do not say they are empty; say you cannot see them"`

	// The counters are pointers so they only appear for an area that is
	// present; the not-listed ones only when something was left out.
	EventsListed      *int   `json:"events_listed,omitempty" jsonschema_description:"Event entries listed. At summary detail one entry can stand for a whole repeating series"`
	EventsOccurrences *int   `json:"events_occurrences,omitempty" jsonschema_description:"Event occurrences in the range, across every entry"`
	EventsNotListed   *int   `json:"events_not_listed,omitempty" jsonschema_description:"One-off events a max_per_day cap left out. Raise max_per_day or narrow from/to to see them"`
	BillsListed       *int   `json:"bills_listed,omitempty"`
	BillsCount        *int   `json:"bills_count,omitempty" jsonschema_description:"Every bill due in the range, listed or not"`
	BillsNotListed    *int   `json:"bills_not_listed,omitempty" jsonschema_description:"Bills due in the range that are not listed here; their money is still in bills_amount_total"`
	BillsAmountTotal  string `json:"bills_amount_total,omitempty" jsonschema_description:"What every bill due in the range comes to, as a decimal string, listed or not"`
	MaintenanceListed *int   `json:"maintenance_listed,omitempty" jsonschema_description:"Upkeep jobs due in the range"`
	MealsListed       *int   `json:"meals_listed,omitempty" jsonschema_description:"Planned meals in the range"`
	ListsDueTotal     *int   `json:"lists_due_total,omitempty" jsonschema_description:"Open list items due in the range, listed or not"`
	ListsDueNotListed *int   `json:"lists_due_not_listed,omitempty"`

	HowToGetDetail string `json:"how_to_get_detail" jsonschema_description:"Exactly how to ask for the detail this result left out"`
}

// agendaResult is agenda_get's data. Coverage is first, on purpose (#4809).
type agendaResult struct {
	Coverage    agendaCoverage `json:"coverage" jsonschema_description:"What this result covers and left out: range, timezone, calendars, areas, and how many items are not listed. Read it before concluding anything is missing."`
	Events      []AgendaEvent  `json:"events"`
	Bills       []AgendaBill   `json:"bills,omitempty" jsonschema_description:"Bills due in the range, by date then name; present when the bills area is included"`
	Maintenance []AgendaTask   `json:"maintenance,omitempty" jsonschema_description:"Upkeep due in the range, by date then name; present when the maintenance area is included"`
	Meals       []AgendaMeal   `json:"meals,omitempty" jsonschema_description:"Planned meals by date then slot, with allergy warnings; change one with meal_set date and slot."`
	MealChecks  []string       `json:"meal_checks,omitempty" jsonschema_description:"Saved allergies that could not be checked; tell the user."`
	Lists       *AgendaLists   `json:"lists,omitempty" jsonschema_description:"Present when the lists area is included."`
	Warnings    []string       `json:"warnings,omitempty" jsonschema_description:"Sections that failed to load; don't assume they are empty."`
}

// agendaInput is the validated agenda_get arguments.
type agendaInput struct {
	first, last time.Time
	// dayCount is how many days the range covers.
	dayCount int
	areas    []string
	// explicitAreas is true when the caller named areas, which changes what a
	// hidden area says (a role-hidden area is only worth a warning when it was
	// asked for).
	explicitAreas bool
	detail        string
	// perDay caps one day's event occurrences at summary detail.
	perDay int
}

func (in agendaInput) wants(area string) bool { return slices.Contains(in.areas, area) }

// NewAgendaGetTool creates agenda_get (replaces get_daily_briefing).
func NewAgendaGetTool(pool *pgxpool.Pool) (mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("agenda_get", toolOptions(classR, "Get agenda",
		mcp.WithDescription("What's on for a date or range: events, bills due, meals and list items, with weekdays. "+
			"A summarized week by default; coverage in the result says what it left out."),
		mcp.WithString("from", mcp.Description(fmt.Sprintf("First day, YYYY-MM-DD (default today); to defaults to %d days later", defaultAgendaDays-1))),
		mcp.WithString("to", mcp.Description(fmt.Sprintf("Last day, inclusive (default from + %d days; at most %d)", defaultAgendaDays-1, maxAgendaDays))),
		mcp.WithArray("areas",
			mcp.Description("Areas to include (default events, bills, meals, lists), e.g. [\"maintenance\"]"),
			mcp.WithStringEnumItems(agendaAreas)),
		mcp.WithString("detail", mcp.Description("summary (default: one entry per series) or full (every occurrence, all fields)"),
			mcp.Enum(agendaDetailLevels...)),
		mcp.WithNumber("max_per_day", mcp.Description(fmt.Sprintf("One-off events per day, 1-%d (default %d)", maxAgendaPerDay, defaultAgendaPerDay)),
			mcp.Min(1), mcp.Max(maxAgendaPerDay)),
		respond.Schema[agendaResult](),
	)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return authRequired(), nil
		}
		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)
		in, bad := parseAgendaArgs(req.GetArguments(), loc)
		if bad != nil {
			return bad, nil
		}

		// The household role policy (#4717): agenda_get answers every role, so
		// each section is trimmed to what the caller may see. Role and vendor
		// link are re-read from memberships on every call, so a demotion
		// applies at once.
		view, bad := agendaViewFor(ctx, pool, claims)
		if bad != nil {
			return bad, nil
		}

		b := &agendaBuilder{
			in:   in,
			loc:  loc,
			view: view,
			days: daysInRange(in.first, in.last, loc),
			want: map[string]bool{},
			res: agendaResult{
				Coverage: agendaCoverage{
					Detail:   in.detail,
					From:     in.first.Format(dateLayout),
					To:       in.last.Format(dateLayout),
					Days:     in.dayCount,
					Timezone: loc.String(),
					Areas:    []string{},
				},
				Events: []AgendaEvent{},
			},
		}
		b.resolveAreas()
		if b.want["events"] {
			b.loadEvents(ctx, pool, claims.HouseholdID)
		}
		if b.want["bills"] {
			b.loadBills(ctx, pool, claims.HouseholdID)
		}
		if b.want["maintenance"] {
			b.loadMaintenance(ctx, pool, claims.HouseholdID)
		}
		if b.want["meals"] {
			b.loadMeals(ctx, pool, claims.HouseholdID)
		}
		if b.want["lists"] {
			b.loadLists(ctx, pool, claims.HouseholdID)
		}
		b.fillCoverage()

		return respond.OK(b.summary(), b.res, agendaHints(b.res)...), nil
	}
	return tool, handler
}

// parseAgendaArgs validates every argument and returns a corrective error
// result rather than guessing at intent. detail is matched in any case with -
// and space folded to _, the convention #4809 set.
func parseAgendaArgs(args map[string]any, loc *time.Location) (agendaInput, *mcp.CallToolResult) {
	in := agendaInput{
		first:  todayIn(loc),
		detail: agendaDetailSummary,
		perDay: defaultAgendaPerDay,
		areas:  agendaDefaultAreas,
	}
	var bad *mcp.CallToolResult
	if s := strings.TrimSpace(getStringArg(args, "from")); s != "" {
		if in.first, bad = parseDay("from", s, loc); bad != nil {
			return in, bad
		}
	}
	in.last = in.first.AddDate(0, 0, defaultAgendaDays-1)
	if s := strings.TrimSpace(getStringArg(args, "to")); s != "" {
		if in.last, bad = parseDay("to", s, loc); bad != nil {
			return in, bad
		}
	}
	days := daysInRange(in.first, in.last, loc)
	switch {
	case len(days) == 0:
		return in, respond.Invalid("to", "to must not be before from.", "Pass to on or after from (YYYY-MM-DD).",
			map[string]any{"from": in.first.Format(dateLayout), "to": in.first.AddDate(0, 0, defaultAgendaDays-1).Format(dateLayout)})
	case len(days) > maxAgendaDays:
		return in, respond.Invalid("to", fmt.Sprintf("The range is longer than %d days.", maxAgendaDays), "Ask for a week or a month at a time.",
			map[string]any{"from": in.first.Format(dateLayout), "to": in.first.AddDate(0, 0, defaultAgendaDays-1).Format(dateLayout)})
	}
	in.dayCount = len(days)

	if raw, ok := args["detail"]; ok && raw != nil {
		s, ok := raw.(string)
		if !ok {
			return in, respond.Invalid("detail", "detail must be a name, not a number or a list.",
				`Pass detail as "summary" or "full", or leave it out for summary.`, map[string]any{"detail": agendaDetailSummary})
		}
		s = strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(s)))
		if s != "" {
			if !slices.Contains(agendaDetailLevels, s) {
				return in, respond.Invalid("detail", fmt.Sprintf("detail %q is not an agenda detail level.", s),
					fmt.Sprintf("Pass detail as %s, or leave it out for %s.", quoteJoin(agendaDetailLevels), agendaDetailSummary),
					map[string]any{"detail": agendaDetailFull})
			}
			in.detail = s
		}
	}
	if raw, ok := args["max_per_day"]; ok && raw != nil {
		n, ok := wholeNumber(raw)
		if !ok || n < 1 || n > maxAgendaPerDay {
			return in, respond.Invalid("max_per_day",
				fmt.Sprintf("max_per_day %v must be a whole number from 1 to %d.", raw, maxAgendaPerDay),
				fmt.Sprintf("Omit max_per_day for %d, or pass a number up to %d.", defaultAgendaPerDay, maxAgendaPerDay),
				map[string]any{"max_per_day": maxAgendaPerDay})
		}
		in.perDay = n
	}

	requested, ok := stringListArg(args, "areas")
	if !ok {
		return in, respond.Invalid("areas", "areas must be a list of area names.",
			"Pass areas as a list, e.g. [\"events\",\"bills\"].", map[string]any{"areas": []string{"events", "bills"}})
	}
	if len(requested) > 0 {
		in.explicitAreas = true
		in.areas = nil
		for _, a := range requested {
			a = strings.ToLower(strings.TrimSpace(a))
			if a == "" {
				continue
			}
			if !slices.Contains(agendaAreas, a) {
				return in, respond.Invalid("areas", fmt.Sprintf("%q is not an agenda area.", a),
					"Use any of: "+strings.Join(agendaAreas, ", ")+".", map[string]any{"areas": []string{"events", "bills"}})
			}
			if !slices.Contains(in.areas, a) {
				in.areas = append(in.areas, a)
			}
		}
		if len(in.areas) == 0 {
			return in, respond.Invalid("areas", "areas was given but names no area.",
				"Use any of: "+strings.Join(agendaAreas, ", ")+".", map[string]any{"areas": []string{"events", "bills"}})
		}
	}
	return in, nil
}

// stringListArg reads a list argument. mcp-go decodes a JSON array as []any, but
// an in-process caller (a test, or another tool) passes []string, and a lone
// value may arrive as a bare string: all three are accepted, so a caller's
// areas are never silently ignored. ok is false for a value that is not a list
// of strings at all.
func stringListArg(args map[string]any, key string) (out []string, ok bool) {
	raw, present := args[key]
	if !present || raw == nil {
		return nil, true
	}
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, true
		}
		return []string{v}, true
	case []string:
		return v, true
	case []any:
		out = make([]string, 0, len(v))
		for _, item := range v {
			s, isString := item.(string)
			if !isString {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	}
	return nil, false
}

// wholeNumber reads a JSON number as a whole number. mcp-go decodes a JSON
// number as float64 over the wire but an in-process caller may pass an int, so
// both are accepted and a fractional value is refused rather than rounded, the
// rule #4809 set for due_within_days.
func wholeNumber(raw any) (int, bool) {
	switch v := raw.(type) {
	case float64:
		if v != math.Trunc(v) {
			return 0, false
		}
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	}
	return 0, false
}

// quoteJoin renders names as "a", "b" and "c".
func quoteJoin(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	return joinWords(q)
}

// agendaBuilder accumulates one agenda_get result, so the handler reads as the
// order sections load and every section can record what it left out in the
// coverage block without the handler threading counts around.
type agendaBuilder struct {
	in   agendaInput
	loc  *time.Location
	view agendaView
	days []time.Time
	res  agendaResult
	want map[string]bool

	// Counters the coverage block reads.
	eventEntries, eventOccurrences, eventsSkipped int
	billsCount                                    int
	billsAmount                                   float64
	billsListed                                   int
}

// resolveAreas intersects what was asked for with what the role may see. An
// area the role may not see is recorded as hidden, whether or not it was asked
// for, so "no bills" can never be read as "no bills are due".
func (b *agendaBuilder) resolveAreas() {
	for _, a := range b.in.areas {
		switch a {
		case "events":
			if !b.view.events {
				b.hide("events", "The calendar is not shared with your role.")
				continue
			}
		case "bills":
			if !b.view.bills {
				b.hide("bills", "Bills are not shared with your role.")
				continue
			}
		case "maintenance":
			if !b.view.maintenance {
				b.hide("maintenance", "Upkeep is not shared with your role.")
				continue
			}
		case "lists":
			if !b.view.lists {
				b.hide("lists", "Lists are not shared with your role.")
				continue
			}
		}
		b.want[a] = true
	}
	for _, a := range agendaAreasOrder {
		if b.want[a] {
			b.res.Coverage.Areas = append(b.res.Coverage.Areas, a)
		}
	}
	// NotIncluded is every area that exists and is not in the result: the
	// model's list of what it may still ask for. A hidden area is reported
	// separately, so it is never offered as something to ask for.
	for _, a := range agendaAreasOrder {
		if !b.want[a] && !slices.Contains(b.res.Coverage.Hidden, a) {
			b.res.Coverage.NotIncluded = append(b.res.Coverage.NotIncluded, a)
		}
	}
}

// hide removes an area the role may not see and records it. An explicit request
// also gets a warning in words, since the caller asked for it.
func (b *agendaBuilder) hide(area, warning string) {
	delete(b.want, area)
	if !slices.Contains(b.res.Coverage.Hidden, area) {
		b.res.Coverage.Hidden = append(b.res.Coverage.Hidden, area)
		sortAreas(b.res.Coverage.Hidden)
	}
	if b.in.explicitAreas && b.in.wants(area) {
		b.res.Warnings = append(b.res.Warnings, warning)
	}
}

// sortAreas keeps a name list in the fixed area order, so a result does not
// depend on the order the caller passed areas in.
func sortAreas(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		return slices.Index(agendaAreasOrder, names[i]) < slices.Index(agendaAreasOrder, names[j])
	})
}

// loadEvents fills the events section: the role's visible kinds (#4807) and the
// detail level's shape.
func (b *agendaBuilder) loadEvents(ctx context.Context, pool *pgxpool.Pool, householdID string) {
	dayStart := b.days[0]
	dayEnd := time.Date(b.in.last.Year(), b.in.last.Month(), b.in.last.Day()+1, 0, 0, 0, 0, b.loc) // DST-safe
	visible, cals, err := visibleCalendars(ctx, pool, householdID, b.view.role)
	if err != nil {
		slog.Error("agenda_get: list calendars error", "error", err)
		b.res.Warnings = append(b.res.Warnings, "Could not load the calendars; do not assume the days are free.")
		return
	}
	b.res.Coverage.Calendars = cals
	events, truncated, err := queryOccurrences(ctx, pool, householdID, b.loc, nil, dayStart, dayEnd)
	if err != nil {
		slog.Error("agenda_get: query events error", "error", err)
		b.res.Warnings = append(b.res.Warnings, "Could not load events; do not assume the days are free.")
		return
	}
	if len(truncated) > 0 {
		b.res.Warnings = append(b.res.Warnings, truncatedNote(truncated))
	}
	kept := make([]Event, 0, len(events))
	for _, ev := range events {
		// A bill's mirror belongs to the bills section (#4485); an event whose
		// kind or calendar the role may not see is dropped whole (#4807), so an
		// upkeep job's mirror is never an appointment a housesitter can read.
		if isBillEvent(ev) || !b.eventVisible(ev, visible) || !eventOverlapsRange(ev, dayStart, dayEnd) {
			continue
		}
		kept = append(kept, ev)
	}
	slices.SortStableFunc(kept, func(x, y Event) int {
		switch {
		case startsBefore(x.Start, y.Start):
			return -1
		case startsBefore(y.Start, x.Start):
			return 1
		}
		return 0
	})
	b.eventOccurrences = len(kept)
	b.res.Events, b.eventsSkipped = shapeAgendaEvents(kept, b.in.detail, b.in.perDay, b.loc)
	b.eventEntries = len(b.res.Events)
}

// eventVisible reports whether the role may see this event: its calendar's kind
// and its own kind both have to be visible, the rule
// apps/api/internal/calendar/access.go applies (#4807). A maintenance task's
// mirror is not an appointment, and a vehicle's event needs the vehicle read
// grant.
func (b *agendaBuilder) eventVisible(ev Event, calendars map[string]bool) bool {
	if !calendars[ev.CalendarID] {
		return false
	}
	kind := agendaEventKind(ev)
	return kind == "calendar_event" || auth.CanSeeEntity(b.view.role, kind)
}

// agendaEventKind is what an event mirrors: a bill mirror is a bill, a
// maintenance task's event is a maintenance task, everything else is a calendar
// event. It is the event half of #4807's rule; the calendar's own kind is
// calendarKind.
func agendaEventKind(ev Event) string {
	switch ev.EventType {
	case "bill":
		return "bill"
	case "maintenance":
		return "maintenance_task"
	}
	return "calendar_event"
}

// shapeAgendaEvents turns the range's occurrences into the events section.
//
// At detail=full every occurrence comes back whole. At detail=summary a
// repeating series collapses to ONE entry carrying every occurrence date in the
// range — the ticket's "one line plus its occurrence dates" — so a daily series
// costs one line for a week, not seven. One-off events keep only the fields a
// model needs, and at most perDay of them appear on any one day: a day with
// thirty birthdays in it does not push the week out of a small model's window.
//
// skipped is how many one-off occurrences the per-day cap left out, which the
// coverage block reports. A listed series' dates are always complete, so nothing
// here can make a series look like it skips a day.
func shapeAgendaEvents(events []Event, detail string, perDay int, loc *time.Location) (out []AgendaEvent, skipped int) {
	if detail == agendaDetailFull {
		out = make([]AgendaEvent, 0, len(events))
		for _, ev := range events {
			out = append(out, fullAgendaEvent(ev))
		}
		return out, 0
	}
	out = make([]AgendaEvent, 0, len(events))
	// Every occurrence of a series shares its row's id (expandSeries keeps it),
	// so the id is the series key.
	series := map[string]int{}
	oneOffs := map[string]int{}
	for _, ev := range events {
		if ev.Recurring {
			if i, ok := series[ev.ID]; ok {
				out[i].Dates = append(out[i].Dates, ev.LocalDate)
				out[i].Occurrences++
				continue
			}
			e := seriesAgendaEvent(ev, loc)
			series[ev.ID] = len(out)
			out = append(out, e)
			continue
		}
		if oneOffs[ev.LocalDate] >= perDay {
			skipped++
			continue
		}
		oneOffs[ev.LocalDate]++
		out = append(out, summaryAgendaEvent(ev, loc))
	}
	return out, skipped
}

// seriesAgendaEvent is one repeating series at summary detail: what it is, how
// it repeats, the days it falls on, and where and when (from its first
// occurrence in the range). Its id is the series' event id, so event_update
// with scope=all, or event_find, act on it.
func seriesAgendaEvent(ev Event, loc *time.Location) AgendaEvent {
	e := summaryAgendaEvent(ev, loc)
	e.LocalDate, e.Weekday = "", ""
	e.Repeat = describeRepeat(ev.Repeat, ev.RRule)
	e.Dates = []string{ev.LocalDate}
	e.Occurrences = 1
	return e
}

// fullAgendaEvent is one occurrence at detail=full, every field event_find
// returns.
func fullAgendaEvent(ev Event) AgendaEvent {
	return AgendaEvent{
		ID: ev.ID, Title: ev.Title, LocalDate: ev.LocalDate, Weekday: ev.Weekday,
		AllDay: ev.AllDay, Location: ev.Location,
		Start: ev.Start, End: ev.End,
		Description: &ev.Description, CalendarID: ev.CalendarID, CalendarName: ev.CalendarName,
		Color: ev.Color, EventType: ev.EventType, RecurrenceID: ev.RecurrenceID,
	}
}

// summaryAgendaEvent is what a model needs to say what is on, when and where,
// and nothing it does not: the day, the start time of day and, only when it is
// worth knowing, the end time.
func summaryAgendaEvent(ev Event, loc *time.Location) AgendaEvent {
	e := AgendaEvent{
		ID: ev.ID, Title: ev.Title, LocalDate: ev.LocalDate, Weekday: ev.Weekday,
		AllDay: ev.AllDay, Location: ev.Location,
	}
	if ev.AllDay {
		// The stored end is the exclusive instant after the last day
		// (localizeAllDay put it at household-local midnight), so the last day
		// is the end's local date minus one day.
		if _, end, err := parseEventTime(ev); err == nil {
			if last := end.In(loc).AddDate(0, 0, -1).Format(dateLayout); last != ev.LocalDate {
				e.LastDate = last
			}
		}
		return e
	}
	start, end, err := parseEventTime(ev)
	if err != nil {
		return e
	}
	local := func(t time.Time) time.Time { return t.In(loc) }
	e.At = local(start).Format("15:04")
	if end.Sub(start) >= 3*time.Hour || local(end).Format(dateLayout) != local(start).Format(dateLayout) {
		e.Until = local(end).Format("15:04")
	}
	return e
}

// visibleCalendars returns the calendar ids the role may see, with their names
// for the coverage block. It asks the policy table the same question #4807's API
// rule does: a calendar's kind comes from its type, so a housesitter loses
// Bills, Maintenance and Vehicles.
func visibleCalendars(ctx context.Context, q querier, householdID, role string) (map[string]bool, []string, error) {
	rows, err := q.Query(ctx, `SELECT id::text, name, type::text FROM calendars WHERE household_id = $1 ORDER BY name`, householdID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	visible := map[string]bool{}
	var names []string
	for rows.Next() {
		var id, name, typ string
		if err := rows.Scan(&id, &name, &typ); err != nil {
			return nil, nil, err
		}
		if !auth.CanSeeEntity(role, calendarKind(typ)) {
			continue
		}
		visible[id] = true
		names = append(names, name)
	}
	return visible, names, rows.Err()
}

// calendarKind maps a calendars.type to the policy's entity kind, exactly as
// apps/api/internal/calendar/access.go does (#4807).
func calendarKind(typ string) string {
	switch typ {
	case "bills":
		return "bill"
	case "maintenance":
		return "maintenance_task"
	case "vehicles":
		return "vehicle"
	}
	return "calendar_event"
}

// shapeAgendaMeals trims the meal plan to what a summary needs: the day, the
// slot and what is planned, plus every allergy warning. The meal's own id, the
// recipe id, notes and past ratings are one detail level away, and meal_set and
// meal_rate address a meal by date and slot anyway.
func shapeAgendaMeals(planned []meals.PlannedMeal, detail string) []AgendaMeal {
	out := make([]AgendaMeal, 0, len(planned))
	for _, m := range planned {
		e := AgendaMeal{
			LocalDate: m.LocalDate, Weekday: m.Weekday, Slot: m.Slot, Title: m.Title,
			AllergyWarnings: m.AllergyWarnings,
		}
		if detail == agendaDetailFull {
			e.MealID, e.RecipeID, e.Notes, e.Ratings = m.MealID, m.RecipeID, m.Notes, m.Ratings
		}
		out = append(out, e)
	}
	return out
}

// agendaMealLine is one meal for the summary sentence: "Tue dinner Chicken
// Tacos", the shape meals.AgendaMealLine gives.
func agendaMealLine(m AgendaMeal) string {
	wd := m.Weekday
	if len(wd) > 3 {
		wd = wd[:3]
	}
	return wd + " " + m.Slot + " " + m.Title
}

// loadBills fills the bills section, capped per day at summary detail. Every
// bill is counted and summed whether or not it is listed, so the totals are
// never a sample (#4809).
func (b *agendaBuilder) loadBills(ctx context.Context, pool *pgxpool.Pool, householdID string) {
	bills, err := billsDueBetween(ctx, pool, householdID, b.days)
	if err != nil {
		slog.Error("agenda_get: query bills error", "error", err)
		b.res.Warnings = append(b.res.Warnings, "Could not load bills; do not assume none are due.")
		return
	}
	b.billsCount = len(bills)
	for _, bill := range bills {
		b.billsAmount += bill.Amount
	}
	list := bills
	if b.in.detail == agendaDetailSummary && len(list) > agendaSummaryBills {
		list = append([]AgendaBill(nil), list[:agendaSummaryBills]...)
	}
	b.billsListed = len(list)
	b.res.Bills = list
}

func (b *agendaBuilder) loadMaintenance(ctx context.Context, pool *pgxpool.Pool, householdID string) {
	tasks, err := maintenanceDueBetween(ctx, pool, householdID, b.in.first, b.in.last, b.view.ownJobs)
	if err != nil {
		slog.Error("agenda_get: query maintenance error", "error", err)
		b.res.Warnings = append(b.res.Warnings, "Could not load maintenance tasks; do not assume none are due.")
		return
	}
	b.res.Maintenance = tasks
}

func (b *agendaBuilder) loadMeals(ctx context.Context, pool *pgxpool.Pool, householdID string) {
	planned, mealChecks, allowed, err := meals.AgendaMeals(ctx, pool, b.in.first, b.in.last)
	switch {
	case err != nil:
		slog.Error("agenda_get: query meals error", "error", err)
		b.res.Warnings = append(b.res.Warnings, "Could not load the meal plan; do not assume nothing is planned.")
	case !allowed:
		// Vendors don't see the meal plan.
		b.hide("meals", "The meal plan is not shared with your role.")
	default:
		b.res.Meals = shapeAgendaMeals(planned, b.in.detail)
		// An allergy saved as text nobody can read matches nothing; say so
		// rather than let the warning quietly disappear (#4802).
		b.res.MealChecks = mealChecks
	}
}

func (b *agendaBuilder) loadLists(ctx context.Context, pool *pgxpool.Pool, householdID string) {
	today := todayIn(b.loc)
	withOpen := !today.Before(b.in.first) && !today.After(b.in.last)
	lists, err := listsDueBetween(ctx, pool, householdID, b.in.first, b.in.last, withOpen)
	if err != nil {
		slog.Error("agenda_get: query lists error", "error", err)
		b.res.Warnings = append(b.res.Warnings, "Could not load lists; do not assume nothing is due.")
		return
	}
	if b.in.detail == agendaDetailSummary {
		// due_total and open_total already cover everything; the rest are one
		// detail away, and list_get lists a whole list.
		capped := &AgendaLists{DueTotal: lists.DueTotal, OpenTotal: lists.OpenTotal}
		capped.Due = lists.Due[:min(len(lists.Due), agendaSummaryListItems)]
		capped.Open = lists.Open[:min(len(lists.Open), agendaSummaryOpenLists)]
		lists = capped
	}
	b.res.Lists = lists
}

// fillCoverage completes the coverage block once every section has loaded.
func (b *agendaBuilder) fillCoverage() {
	c := &b.res.Coverage
	c.Scope = b.scope()
	if b.want["events"] {
		entries, occ, skipped := b.eventEntries, b.eventOccurrences, b.eventsSkipped
		c.EventsListed, c.EventsOccurrences = &entries, &occ
		if skipped > 0 {
			c.EventsNotListed = &skipped
		}
	}
	if b.want["bills"] {
		listed, count, notListed := b.billsListed, b.billsCount, b.billsCount-b.billsListed
		c.BillsListed, c.BillsCount = &listed, &count
		if notListed > 0 {
			c.BillsNotListed = &notListed
		}
		c.BillsAmountTotal = decimal(b.billsAmount)
	}
	if b.want["maintenance"] {
		n := len(b.res.Maintenance)
		c.MaintenanceListed = &n
	}
	if b.want["meals"] {
		n := len(b.res.Meals)
		c.MealsListed = &n
	}
	if lists := b.res.Lists; lists != nil {
		total, notListed := lists.DueTotal, lists.DueTotal-len(lists.Due)
		c.ListsDueTotal, c.ListsDueNotListed = &total, &notListed
	}
	c.HowToGetDetail = b.howToGetDetail()
}

// scope states what the result covers in words: the range, the timezone, the
// calendars searched, and what each area means. A model must be able to tell an
// empty section from an absent one, and an appointment's absence from "the days
// are free".
func (b *agendaBuilder) scope() string {
	var parts []string
	if b.in.dayCount == 1 {
		parts = append(parts, fmt.Sprintf("one day, %s, %s", b.in.first.Format("Mon Jan 2, 2006"), b.loc.String()))
	} else {
		parts = append(parts, fmt.Sprintf("%d days, %s to %s, %s",
			b.in.dayCount, b.in.first.Format("Mon Jan 2"), b.in.last.Format("Mon Jan 2, 2006"), b.loc.String()))
	}
	if b.want["events"] {
		if len(b.res.Coverage.Calendars) == 0 {
			parts = append(parts, "no calendar is visible to your role")
		} else {
			parts = append(parts, "events on the "+joinWords(b.res.Coverage.Calendars)+" "+plural(len(b.res.Coverage.Calendars), "calendar", "calendars"))
		}
	}
	if b.want["bills"] {
		parts = append(parts, "bills due and unpaid for that due date")
	}
	if b.want["maintenance"] {
		parts = append(parts, "upkeep jobs due")
	}
	if b.want["meals"] {
		parts = append(parts, "the meal plan")
	}
	if b.want["lists"] {
		parts = append(parts, "open list items due")
	}
	s := strings.Join(parts, "; ") + "."
	s += " Only what Home OS holds: no email, no messages, no other families' records."
	return s
}

// howToGetDetail is the one sentence naming the exact arguments that return what
// this result left out. It only claims an omission that exists, so a complete
// answer never sends a model chasing detail it already has (#4809).
func (b *agendaBuilder) howToGetDetail() string {
	var asks []string
	if b.in.detail == agendaDetailSummary {
		var small []string
		if b.eventsSkipped > 0 {
			small = append(small, fmt.Sprintf("%d one-off events (a day held more than max_per_day: pass max_per_day=%d, or narrow from/to)", b.eventsSkipped, maxAgendaPerDay))
		}
		if n := b.billsCount - b.billsListed; n > 0 {
			small = append(small, fmt.Sprintf("%d bills (their count and amount are still in bills_count and bills_amount_total)", n))
		}
		if len(small) > 0 {
			asks = append(asks, `detail="full" to list `+joinWords(small))
		} else {
			asks = append(asks, `detail="full" for one entry per occurrence with description, calendar, color, event type and end time`)
		}
	}
	if len(b.res.Coverage.NotIncluded) > 0 {
		asks = append(asks, "areas="+areaList(b.res.Coverage.NotIncluded)+" to add the "+joinWords(b.res.Coverage.NotIncluded)+" area")
	}
	if b.in.dayCount < maxAgendaDays {
		asks = append(asks, fmt.Sprintf("a later to (at most %d days) to look further ahead", maxAgendaDays))
	}
	if len(asks) == 0 {
		return "Nothing is omitted: this result carries every event and every due item in the range."
	}
	return strings.Join(asks, "; ") + "."
}

// areaList renders area names as a JSON list for a hint, e.g. ["events","meals"].
func areaList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

// joinWords joins names as "a, b and c".
func joinWords(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// decimal formats an amount as a decimal string, the shape budget_summary uses.
func decimal(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// summary is the envelope's one line: what the range holds, by area, and the two
// things that must not be lost — that this is a summary, and that a role may not
// see a section at all.
func (b *agendaBuilder) summary() string {
	var when string
	if b.in.dayCount == 1 {
		when = b.days[0].Format("Monday, January 2, 2006")
	} else {
		when = b.days[0].Format("Mon Jan 2") + " – " + b.days[len(b.days)-1].Format("Mon Jan 2, 2006")
	}
	count := func(n int, one, many string) string {
		if n == 0 {
			return "no " + many
		}
		return fmt.Sprintf("%d %s", n, plural(n, one, many))
	}
	var parts []string
	if b.want["events"] {
		p := count(b.eventOccurrences, "event", "events")
		if b.eventEntries != b.eventOccurrences {
			p += fmt.Sprintf(" in %d %s", b.eventEntries, plural(b.eventEntries, "entry", "entries"))
		}
		parts = append(parts, p)
	}
	if b.want["bills"] {
		p := count(b.billsCount, "bill", "bills") + " due"
		if b.billsCount > 0 {
			p += " ($" + decimal(b.billsAmount) + ")"
		}
		if b.billsCount > b.billsListed {
			p += fmt.Sprintf(", %d listed", b.billsListed)
		}
		parts = append(parts, p)
	}
	if b.want["maintenance"] {
		parts = append(parts, count(len(b.res.Maintenance), "maintenance task", "maintenance tasks")+" due")
	}
	if b.want["meals"] {
		p := count(len(b.res.Meals), "planned meal", "planned meals")
		if n := len(b.res.Meals); n > 0 && n <= 7 {
			lines := make([]string, n)
			for i, m := range b.res.Meals {
				lines[i] = agendaMealLine(m)
			}
			p += " (" + strings.Join(lines, "; ") + ")"
		}
		for _, m := range b.res.Meals {
			if len(m.AllergyWarnings) > 0 {
				p += " with allergy warnings"
				break
			}
		}
		if len(b.res.MealChecks) > 0 {
			p += "; some allergies could not be checked"
		}
		parts = append(parts, p)
	}
	if lists := b.res.Lists; lists != nil {
		p := count(lists.DueTotal, "list item", "list items") + " due"
		if lists.OpenTotal > 0 {
			p += fmt.Sprintf(" (%d open %s)", lists.OpenTotal, plural(lists.OpenTotal, "list", "lists"))
		}
		parts = append(parts, p)
	}
	s := when + ": " + strings.Join(parts, ", ") + "."
	if len(parts) == 0 {
		s = when + ": nothing requested that Home OS can show you."
	}
	// These two sentences are what stop a summary reading as the whole picture;
	// the coverage block carries the detail.
	if len(b.res.Coverage.NotIncluded) > 0 {
		s += " Not included: " + joinWords(b.res.Coverage.NotIncluded) + "."
	}
	if len(b.res.Coverage.Hidden) > 0 {
		s += " Not visible to your role: " + joinWords(b.res.Coverage.Hidden) + "."
	}
	for _, c := range b.res.MealChecks {
		s += " " + c
	}
	if len(b.res.Warnings) > 0 {
		s += " Some sections could not be loaded; see warnings."
	}
	return s
}

func agendaHints(res agendaResult) []string {
	var out []string
	if len(res.Events) > 0 {
		out = append(out, fmt.Sprintf("Change an event with event_update event_id=%s.", res.Events[0].ID))
	}
	if len(res.Bills) > 0 {
		out = append(out, fmt.Sprintf("Mark a bill paid with bill_pay bill_id=%s.", res.Bills[0].BillID))
	}
	if len(res.Meals) > 0 {
		m := res.Meals[0]
		out = append(out, fmt.Sprintf(`Change a meal with meal_set meals=[{"date":"%s","slot":"%s","text":"…"}].`, m.LocalDate, m.Slot))
	}
	if l := res.Lists; l != nil {
		if len(l.Due) > 0 {
			first := l.Due[0].ListID
			ids := []string{}
			for _, it := range l.Due {
				if it.ListID == first && len(ids) < 5 {
					ids = append(ids, strconv.Quote(it.ItemID))
				}
			}
			out = append(out, fmt.Sprintf("Check items off with list_check_items list_id=%s item_ids=[%s].", first, strings.Join(ids, ",")))
		}
		if len(l.Open) > 0 {
			out = append(out, fmt.Sprintf("See a whole list with list_get list_id=%s.", l.Open[0].ListID))
		} else if len(l.Due) > 0 {
			out = append(out, fmt.Sprintf("See the whole list with list_get list_id=%s.", l.Due[0].ListID))
		}
	}
	if len(out) == 0 {
		out = append(out, "Add something with event_create, or look further ahead with a later to.")
	}
	return out[:min(len(out), 2)]
}

// ─────────────────────────────────────────────
// Section loaders
// ─────────────────────────────────────────────

// billsDueBetween returns, for each local calendar day in days, the household's
// bills due that day and not yet paid for that due date, ordered by date then
// name.
//
// It applies bill.BillDueFor, the rule bill_find uses, so the agenda agrees with
// it for every frequency (monthly, quarterly, semiannual, annual and once,
// #4468): due days are clamped to the month (31 is due Nov 30) and the last
// payment (paid_for_due_date, else inferred from paid_date) only hides the bill
// when it covers this due date (a payment for last month's bill does not).
func billsDueBetween(ctx context.Context, pool querier, householdID string, days []time.Time) ([]AgendaBill, error) {
	rows, err := pool.Query(ctx,
		`SELECT id::text, name, COALESCE(amount, 0)::float8, COALESCE(category, ''),
		        frequency, due_day, due_date, paid_date, paid_for_due_date
		 FROM bills
		 WHERE household_id = $1 AND (due_day IS NOT NULL OR due_date IS NOT NULL)
		 ORDER BY name`,
		householdID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type billRow struct {
		b        AgendaBill
		schedule bill.Schedule
		paidDate *time.Time
		paidFor  *time.Time
	}
	var all []billRow
	for rows.Next() {
		var r billRow
		if err := rows.Scan(&r.b.BillID, &r.b.Name, &r.b.Amount, &r.b.Category,
			&r.schedule.Frequency, &r.schedule.DueDay, &r.schedule.DueDate, &r.paidDate, &r.paidFor); err != nil {
			return nil, fmt.Errorf("scan bill: %w", err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]AgendaBill, 0)
	for _, d := range days {
		y, m, dd := d.Date()
		day := time.Date(y, m, dd, 0, 0, 0, 0, time.UTC) // bill dates are midnight UTC
		for _, r := range all {
			if due, ok := bill.BillDueFor(day, r.schedule, r.paidDate, r.paidFor); ok && due.Next.Equal(day) {
				b := r.b
				b.DueDate, b.Weekday = day.Format(dateLayout), day.Weekday().String()
				out = append(out, b)
			}
		}
	}
	return out, nil
}

// maintenanceDueBetween returns the agenda's maintenance section: the
// household's maintenance tasks due on a local day from first to last
// inclusive, by date then name. ownJobs limits it to one vendor's own jobs
// (#4799), the rule maintenance_find, search_all, the API and CalDAV apply.
func maintenanceDueBetween(ctx context.Context, pool querier, householdID string, first, last time.Time,
	ownJobs *uuid.UUID) ([]AgendaTask, error) {
	civil := func(t time.Time) time.Time {
		y, m, d := t.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	rows, err := pool.Query(ctx,
		`SELECT id::text, name, COALESCE(description, ''), status::text, due_date, property_id::text
		 FROM maintenance_tasks
		 WHERE household_id = $1 AND due_date BETWEEN $2 AND $3
		   AND ($4::uuid IS NULL OR vendor_id = $4::uuid)
		 ORDER BY due_date, name`,
		householdID, civil(first), civil(last), ownJobs,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AgendaTask, 0)
	for rows.Next() {
		var t AgendaTask
		var due time.Time
		if err := rows.Scan(&t.TaskID, &t.Name, &t.Description, &t.Status, &due, &t.PropertyID); err != nil {
			return nil, fmt.Errorf("scan maintenance task: %w", err)
		}
		t.DueDate, t.Weekday = due.Format(dateLayout), due.Weekday().String()
		out = append(out, t)
	}
	return out, rows.Err()
}

// listsDueBetween returns the agenda's lists section: the household's open items
// due on a day from first to last inclusive, from active non-template lists, by
// due date, list name and position (capped, with the total); and, when withOpen,
// the active non-template lists that still have open items, with their open
// count and the first few open item texts (capped, with the total). Both queries
// filter lists and items by household.
func listsDueBetween(ctx context.Context, pool querier, householdID string, first, last time.Time, withOpen bool) (*AgendaLists, error) {
	civil := func(t time.Time) time.Time {
		y, m, d := t.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	out := &AgendaLists{Due: []AgendaListItem{}}
	rows, err := pool.Query(ctx,
		`SELECT l.id::text, l.name, i.id::text, i.text, i.due_date, pe.display_name, COUNT(*) OVER ()
		 FROM list_items i
		 JOIN lists l ON l.id = i.list_id AND l.household_id = i.household_id
		 LEFT JOIN people pe ON pe.id = i.assignee_person_id AND pe.household_id = i.household_id
		 WHERE i.household_id = $1 AND l.household_id = $1
		   AND NOT i.checked AND i.due_date BETWEEN $2 AND $3
		   AND NOT l.archived AND NOT l.is_template
		 ORDER BY i.due_date, lower(l.name), l.id, i.position
		 LIMIT $4`,
		householdID, civil(first), civil(last), agendaMaxListItems,
	)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var it AgendaListItem
		var due time.Time
		if err := rows.Scan(&it.ListID, &it.ListName, &it.ItemID, &it.Text, &due, &it.Assignee, &out.DueTotal); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan list item: %w", err)
		}
		it.DueDate, it.Weekday = due.Format(dateLayout), due.Weekday().String()
		out.Due = append(out.Due, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !withOpen {
		return out, nil
	}

	rows, err = pool.Query(ctx,
		`SELECT l.id::text, l.name, l.kind, COUNT(*)::int,
		        (array_agg(i.text ORDER BY i.position, i.id))[1:$2],
		        COUNT(*) OVER ()
		 FROM lists l
		 JOIN list_items i ON i.list_id = l.id AND i.household_id = l.household_id AND NOT i.checked
		 WHERE l.household_id = $1 AND NOT l.archived AND NOT l.is_template
		 GROUP BY l.id, l.name, l.kind
		 ORDER BY lower(l.name), l.id
		 LIMIT $3`,
		householdID, agendaListPreview, agendaMaxOpenLists,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l AgendaOpenList
		if err := rows.Scan(&l.ListID, &l.Name, &l.Kind, &l.OpenItems, &l.Preview, &out.OpenTotal); err != nil {
			return nil, fmt.Errorf("scan open list: %w", err)
		}
		if l.Preview == nil {
			l.Preview = []string{}
		}
		out.Open = append(out.Open, l)
	}
	return out, rows.Err()
}
