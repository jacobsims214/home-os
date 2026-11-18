// #4944: event_find with a bare text query searched only the next 7 days, so
// "is the Christmas thing on the calendar?" answered "no" for a December 5
// event that existed — a confident wrong absence, the worst answer a read
// tool can give (war game R1, gap G4).
//
// A query is now a lookup over every date. Any window the tool still applies
// by itself is reported twice: loudly in the summary, and as searched_range
// in the structured result, so a caller that reads only JSON can tell "it
// does not exist" from "it is outside what I searched" too.

package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/testutil"
)

// foundRange is the structured half of the promise: what a search covered,
// readable without parsing the summary.
type foundRange struct {
	OK       bool           `json:"ok"`
	Summary  string         `json:"summary"`
	Items    []Event        `json:"items"`
	Total    int            `json:"total"`
	Next     []string       `json:"next"`
	Searched *respond.Range `json:"searched_range"`
}

func decodeFound(t *testing.T, text string) foundRange {
	t.Helper()
	var r foundRange
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return r
}

// ids returns the item ids as one comma-joined string, for failure messages.
func (r foundRange) ids() string {
	out := make([]string, len(r.Items))
	for i, ev := range r.Items {
		out[i] = ev.Title
	}
	return strings.Join(out, ", ")
}

// windowEnv is a household in New York holding the war game's reported event
// plus events at a spread of distances from today, so "inside the week the
// tool chose" and "3 weeks out" can be told apart.
type windowEnv struct {
	ctx                                 context.Context
	find                                handlerFunc
	local                               func(days int) string // a YYYY-MM-DD, days from today, family time
	christmas, near, lastYear, nextYear string
	absentText                          string // a word no event carries
}

func newWindowEnv(t *testing.T) windowEnv {
	t.Helper()
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	family := seedNamedCalendar(t, pool, hh.HouseholdID, "Family", "family")

	env := windowEnv{
		ctx:        ctx,
		find:       bind(t, NewEventFindTool, pool),
		absentText: "Kwanzaa",
		local: func(days int) string {
			return time.Now().AddDate(0, 0, days).Format(dateLayout)
		},
	}
	// The Christmas tree is pinned: it is the event the war game could not
	// find. The others move with today so the test keeps its meaning.
	env.christmas = seedEvent(t, pool, family, "christmas@example.com",
		`{"title":"Christmas tree with the kids","description":"cut our own at the farm","location":"Hilltop Farm","start":"2026-12-05T15:00:00Z","end":"2026-12-05T18:00:00Z"}`)
	env.near = seedEvent(t, pool, family, "near@example.com",
		`{"title":"Dentist","start":"`+env.local(2)+`T19:00:00Z","end":"`+env.local(2)+`T20:00:00Z"}`)
	env.lastYear = seedEvent(t, pool, family, "last-year@example.com",
		`{"title":"Christmas tree with the kids (last year)","start":"2025-12-06T15:00:00Z","end":"2025-12-06T18:00:00Z"}`)
	env.nextYear = seedEvent(t, pool, family, "next-year@example.com",
		`{"title":"Dentist checkup (next year)","start":"2027-12-20T19:00:00Z","end":"2027-12-20T20:00:00Z"}`)
	return env
}

func (e windowEnv) call(t *testing.T, args map[string]any) foundRange {
	t.Helper()
	text, isErr := callTool(t, e.find, e.ctx, args)
	if isErr {
		t.Fatalf("event_find %v errored: %s", args, text)
	}
	return decodeFound(t, text)
}

// TestEventFindBareQueryFindsTheChristmasTree is the reported case: the query
// that answered "No events from Mon Nov 16 to Sun Nov 22 matching
// "Christmas"" for both parents, while the December 5 event existed.
func TestEventFindBareQueryFindsTheChristmasTree(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	family := seedNamedCalendar(t, pool, hh.HouseholdID, "Family", "family")
	find := bind(t, NewEventFindTool, pool)
	christmas := seedEvent(t, pool, family, "christmas@example.com",
		`{"title":"Christmas tree with the kids","description":"cut our own at the farm","location":"Hilltop Farm","start":"2026-12-05T15:00:00Z","end":"2026-12-05T18:00:00Z"}`)

	text, isErr := callTool(t, find, ctx, map[string]any{"query": "Christmas"})
	if isErr {
		t.Fatalf("bare query errored: %s", text)
	}
	got := decodeFound(t, text)
	if len(got.Items) != 1 || got.Items[0].ID != christmas || got.Items[0].LocalDate != "2026-12-05" {
		t.Fatalf("bare query %q found [%s]; want the 2026-12-05 Christmas tree\ntext: %s", "Christmas", got.ids(), text)
	}
	// A complete search must not hedge either: a caller told "may have more"
	// every time learns to ignore it.
	if got.Searched == nil || !got.Searched.AllDates || got.Searched.MayHaveMore {
		t.Errorf("searched_range = %+v, want all_dates with no may_have_more", got.Searched)
	}
	if strings.Contains(got.Summary, "SEARCHED THAT RANGE ONLY") || strings.Contains(got.Summary, "only") {
		t.Errorf("summary of a complete search warns about a window: %q", got.Summary)
	}
	if !strings.Contains(got.Summary, `matching "Christmas"`) || !strings.Contains(got.Summary, "any date") {
		t.Errorf("summary = %q, want it to say the query covered any date", got.Summary)
	}
}

// TestEventFindBareQuerySearchesEveryDate proves the search is a lookup and
// not a range scan: an event 3 weeks out, one 13 months out and one from last
// year all come back, each by the same bare query an agent asks with.
func TestEventFindBareQuerySearchesEveryDate(t *testing.T) {
	env := newWindowEnv(t)

	for _, tc := range []struct{ query, wantID, wantDate string }{
		{"Christmas tree with the kids", env.christmas, "2026-12-05"}, // 3 weeks out
		{"Dentist checkup", env.nextYear, "2027-12-20"},               // 13 months out
		{"(last year)", env.lastYear, "2025-12-06"},                   // 11 months back
	} {
		t.Run(tc.query, func(t *testing.T) {
			got := env.call(t, map[string]any{"query": tc.query})
			var found bool
			for _, ev := range got.Items {
				if ev.ID == tc.wantID {
					found = true
					if ev.LocalDate != tc.wantDate {
						t.Errorf("%s local_date = %q, want %q", tc.wantID, ev.LocalDate, tc.wantDate)
					}
				}
			}
			if !found {
				t.Errorf("bare query %q found [%s], missing %s", tc.query, got.ids(), tc.wantID)
			}
			if got.Searched == nil || !got.Searched.AllDates || got.Searched.MayHaveMore {
				t.Errorf("searched_range = %+v, want all_dates and no may_have_more", got.Searched)
			}
		})
	}

	// A title that is inside any 7-day window still comes back, so the fix
	// does not trade one blind spot for another.
	t.Run("two days out", func(t *testing.T) {
		got := env.call(t, map[string]any{"query": "Dentist"})
		var sawNear bool
		for _, ev := range got.Items {
			if ev.ID == env.near {
				sawNear = true
			}
		}
		if !sawNear {
			t.Errorf("bare query \"Dentist\" found [%s], missing %s", got.ids(), env.near)
		}
	})
}

// TestEventFindBareQueryReportsAbsenceClearly is the other half: when the
// event genuinely does not exist, the answer must say so and must not leave
// room for "I only looked at this week". A caller may never read a false
// absence out of the response.
func TestEventFindBareQueryReportsAbsenceClearly(t *testing.T) {
	env := newWindowEnv(t)

	got := env.call(t, map[string]any{"query": env.absentText})
	if len(got.Items) != 0 || got.Total != 0 {
		t.Fatalf("a genuinely absent event matched: [%s]", got.ids())
	}
	if got.Searched == nil {
		t.Fatal("no searched_range, so an agent cannot tell what was searched")
	}
	if !got.Searched.AllDates || got.Searched.MayHaveMore {
		t.Errorf("searched_range = %+v, want all_dates and no may_have_more", got.Searched)
	}
	if !strings.Contains(got.Summary, "every date was searched") || !strings.Contains(got.Summary, "no event matches") {
		t.Errorf("summary = %q, want it to state plainly that every date was searched", got.Summary)
	}
	// The hedging words that made G4 a trap must be absent from a complete
	// search: no "only", no "widen", no "may exist outside".
	lower := strings.ToLower(got.Summary)
	for _, bad := range []string{"only", "widen", "may exist outside", "may match"} {
		if strings.Contains(lower, bad) {
			t.Errorf("summary of a complete search hedges with %q: %q", bad, got.Summary)
		}
	}
	for _, hint := range got.Next {
		if strings.Contains(strings.ToLower(hint), "may exist outside") {
			t.Errorf("hint of a complete search claims more may exist outside: %q", hint)
		}
	}
}

// TestEventFindDefaultRangeIsLoud: with no query and no from/to the tool still
// picks today + 7 days for a plain listing, but it may not do so silently. An
// empty result has to say the window was the tool's choice and that other
// dates were not read.
func TestEventFindDefaultRangeIsLoud(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	family := seedNamedCalendar(t, pool, hh.HouseholdID, "Family", "family")
	find := bind(t, NewEventFindTool, pool)
	// Something real, but outside the default week (in any month).
	seedEvent(t, pool, family, "far@example.com",
		`{"title":"Christmas tree with the kids","start":"2026-12-05T15:00:00Z","end":"2026-12-05T18:00:00Z"}`)

	text, isErr := callTool(t, find, ctx, map[string]any{})
	if isErr {
		t.Fatalf("no-arg call errored: %s", text)
	}
	got := decodeFound(t, text)
	if got.Searched == nil {
		t.Fatalf("no searched_range on a defaulted range: %s", text)
	}
	if got.Searched.AllDates || got.Searched.From == "" || got.Searched.To == "" || !got.Searched.MayHaveMore {
		t.Errorf("searched_range = %+v, want a from/to window with may_have_more", got.Searched)
	}
	if !strings.Contains(got.Summary, "SEARCHED THAT RANGE ONLY") {
		t.Errorf("summary = %q, want it to say the range was searched only", got.Summary)
	}
	if !strings.Contains(got.Summary, "leave them off") {
		t.Errorf("summary = %q, want the way to search every date", got.Summary)
	}
	if !strings.Contains(strings.Join(got.Next, " "), "without from/to") {
		t.Errorf("next = %v, want a hint to search every date", got.Next)
	}
}

// TestEventFindExplicitRangeIsNotHedged: a window the caller spelled out in
// full is theirs, and an empty result in it is a real answer. The response
// must not warn about it, or the warning becomes noise nobody reads.
func TestEventFindExplicitRangeIsNotHedged(t *testing.T) {
	env := newWindowEnv(t)

	got := env.call(t, map[string]any{"from": "2026-11-16", "to": "2026-11-22", "query": "Christmas"})
	if len(got.Items) != 0 {
		t.Fatalf("the week of 11/16 should hold no Christmas event: [%s]", got.ids())
	}
	if got.Searched == nil || got.Searched.AllDates || got.Searched.MayHaveMore {
		t.Errorf("searched_range = %+v, want the caller's own window and no may_have_more", got.Searched)
	}
	if got.Searched.From != "2026-11-16" || got.Searched.To != "2026-11-22" {
		t.Errorf("searched_range = %s..%s, want 2026-11-16..2026-11-22", got.Searched.From, got.Searched.To)
	}
	if strings.Contains(got.Summary, "SEARCHED THAT RANGE ONLY") {
		t.Errorf("summary of a caller-chosen range shouts: %q", got.Summary)
	}
}

// TestEventFindOneSidedRangeIsLoud: a query narrowed by only one end gets the
// other end from the tool, which is the tool choosing a window again.
func TestEventFindOneSidedRangeIsLoud(t *testing.T) {
	env := newWindowEnv(t)

	got := env.call(t, map[string]any{"from": "2026-11-16", "query": "Christmas"})
	if got.Searched == nil || got.Searched.AllDates || !got.Searched.MayHaveMore {
		t.Errorf("searched_range = %+v, want a window with may_have_more", got.Searched)
	}
	if got.Searched.From != "2026-11-16" || got.Searched.To != "2026-11-22" {
		t.Errorf("searched_range = %s..%s, want from 2026-11-16 to 11-22", got.Searched.From, got.Searched.To)
	}
	if !strings.Contains(got.Summary, "SEARCHED THAT RANGE ONLY") {
		t.Errorf("summary = %q, want it to say the range was searched only", got.Summary)
	}
}

// TestEventFindBareQueryFindsRecurringSeries: a lookup reaches a repeating
// series too, and answers with the series — one item, its rule — instead of
// nothing, or 400 copies of it.
func TestEventFindBareQueryFindsRecurringSeries(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	seedNamedCalendar(t, pool, hh.HouseholdID, "Family", "family")
	create := bind(t, NewEventCreateTool, pool)
	find := bind(t, NewEventFindTool, pool)

	created, isErr := callTool(t, create, ctx, map[string]any{
		"title": "Ava swim practice", "start": "2027-01-05T17:30:00-05:00", "end": "2027-01-05T18:30:00-05:00",
		"repeat": map[string]any{"freq": "weekly", "until": "2027-12-31"},
	})
	if isErr {
		t.Fatalf("create errored: %s", created)
	}
	id := writtenEvent(t, created).Event.ID

	text, isErr := callTool(t, find, ctx, map[string]any{"query": "swim"})
	if isErr {
		t.Fatalf("bare query errored: %s", text)
	}
	got := decodeFound(t, text)
	if len(got.Items) != 1 || got.Items[0].ID != id {
		t.Fatalf("bare query on a series = [%s], want the one series %s", got.ids(), id)
	}
	if !got.Items[0].Recurring || got.Items[0].RRule == "" {
		t.Errorf("series came back without its rule: %+v", got.Items[0])
	}
}

// TestEventFindSearchIsFasterThanItsCap guards the unbounded read: a household
// with many entries still answers a bare query quickly, because the search is
// one indexed read of the household's rows rather than a scan of a window.
func TestEventFindSearchStaysFast(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	setHouseholdTimezone(t, pool, hh.HouseholdID, "America/New_York")
	family := seedNamedCalendar(t, pool, hh.HouseholdID, "Family", "family")
	find := bind(t, NewEventFindTool, pool)
	for i := range 200 {
		seedEvent(t, pool, family, fmt.Sprintf("bulk-%d@example.com", i),
			fmt.Sprintf(`{"title":"Event %d","start":"2026-06-01T15:00:00Z","end":"2026-06-01T16:00:00Z"}`, i))
	}
	seedEvent(t, pool, family, "needle@example.com",
		`{"title":"Christmas tree with the kids","start":"2026-12-05T15:00:00Z","end":"2026-12-05T18:00:00Z"}`)

	start := time.Now()
	text, isErr := callTool(t, find, ctx, map[string]any{"query": "Christmas"})
	if isErr {
		t.Fatalf("bare query errored: %s", text)
	}
	got := decodeFound(t, text)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("bare query over 201 events took %v", d)
	}
	if len(got.Items) != 1 || got.Items[0].LocalDate != "2026-12-05" {
		t.Fatalf("bare query found [%s], want only the Christmas tree", got.ids())
	}
}

// TestFoundSummaryNeverClaimsAbsence is the rule behind #4944 stated directly:
// for every way a search can come back empty, the sentence a caller reads
// says whether the event is really absent or merely outside what was read.
func TestFoundSummaryNeverClaimsAbsence(t *testing.T) {
	const where = `on any date matching "Christmas"`
	all := respond.AllDatesRange("Searched every date, so an empty result means no event matches.")
	capped := respond.DaysRange("", "", true, "only the first 5000 of more entries were read")
	defaulted := respond.DaysRange("2026-11-17", "2026-11-23", true, "searched this week only")
	callerWindow := respond.DaysRange("2026-11-16", "2026-11-22", false, "Searched exactly this range.")

	tests := []struct {
		name      string
		total     int
		searched  *respond.Range
		def       bool
		want      []string // every one must appear
		notWanted []string // none may appear
	}{
		{
			name: "exhaustive search, nothing there", total: 0, searched: all,
			want:      []string{"every date was searched", "no event matches"},
			notWanted: []string{"ONLY", "stopped short", "not proof"},
		},
		{
			name: "tool defaulted the window", total: 0, searched: defaulted, def: true,
			want:      []string{"SEARCHED THAT RANGE ONLY", "leave them off"},
			notWanted: []string{"every date was searched", "no event matches"},
		},
		{
			name: "cut short by the size cap", total: 0, searched: capped,
			want:      []string{"stopped short", "not proof the event does not exist"},
			notWanted: []string{"every date was searched", "no event matches"},
		},
		{
			name: "caller's own window", total: 0, searched: callerWindow,
			want:      []string{"No events"},
			notWanted: []string{"ONLY", "every date was searched"},
		},
		{
			name: "hits found", total: 1, searched: all,
			want:      []string{"Found 1 event"},
			notWanted: []string{"ONLY", "stopped short"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := foundSummary(where, tc.total, 0, tc.total, tc.def, tc.searched)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("summary %q is missing %q", got, want)
				}
			}
			for _, bad := range tc.notWanted {
				if strings.Contains(got, bad) {
					t.Errorf("summary %q must not say %q", got, bad)
				}
			}
		})
	}
}
