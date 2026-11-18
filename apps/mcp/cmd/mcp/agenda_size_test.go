package main

// agenda_get payload size (#4810).
//
// agenda_get is the first tool a family agent calls ("what's this week look
// like?"), and its default answer used to be every occurrence of every
// repeating series with every field, plus every bill due: 35,247 bytes of
// content[0].text for a week on the benchmark household, and 146,260 for a
// month. On a small local model that one result ate a large share of the
// context window, and the 2026-11-15 war game showed the cost: agents looping
// on repeated briefings and turns dying at the 600s limit.
//
// The default is now a summarized week: repeating series collapsed to one entry
// with their occurrence dates, one-off events capped per day, bills capped with
// their count and amount still totalled, upkeep on request, and a coverage block
// that says what it covers and what it left out. This test holds the default
// under its budget on the benchmark household and proves the detail stays
// reachable, so a future field cannot quietly grow the default back.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/calendar"
	"home-os/mcp/internal/testutil"
)

// maxDefaultAgendaBytes is the budget for agenda_get's default answer on the
// benchmark household: 10 KB, as the ticket requires. The measured default is
// about 8.3 KB, so there is headroom for a field or two without a test failure
// being the first anyone hears about a size problem.
const maxDefaultAgendaBytes = 10 * 1024

// TestAgendaGetDefaultPayloadSize seeds the benchmark household (a year of a
// busy family's data) and checks that agenda_get with no arguments answers a
// summarized week that still covers everything it claims.
func TestAgendaGetDefaultPayloadSize(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	seedBusyFamily(t, pool, hh.HouseholdID, hh.UserID, 5)
	if _, err := pool.Exec(context.Background(), `ANALYZE`); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	_, h := calendar.NewAgendaGetTool(pool)

	call := func(args map[string]any) (string, agendaSizeEnvelope) {
		t.Helper()
		var req mcp.CallToolRequest
		req.Params.Name = "agenda_get"
		req.Params.Arguments = args
		res, err := h(ctx, req)
		if err != nil {
			t.Fatalf("agenda_get(%v): %v", args, err)
		}
		text := res.Content[0].(mcp.TextContent).Text
		if res.IsError {
			t.Fatalf("agenda_get(%v) failed: %s", args, text)
		}
		var env agendaSizeEnvelope
		if err := json.Unmarshal([]byte(text), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return text, env
	}

	// The default answer: a week, small, and honest about its scope.
	text, env := call(nil)
	if n := len(text); n > maxDefaultAgendaBytes {
		t.Errorf("default agenda_get is %d bytes, budget is %d", n, maxDefaultAgendaBytes)
	} else {
		t.Logf("default agenda_get: %d bytes (~%d tokens)", n, n/4)
	}
	d := env.Data
	if d == nil {
		t.Fatalf("no data: %s", text)
	}
	c := d.Coverage
	if c.Detail != "summary" {
		t.Errorf("coverage.detail = %q, want summary", c.Detail)
	}
	if c.From == "" || c.To == "" || c.Days != 7 {
		t.Errorf("coverage does not state a 7-day range: %+v", c)
	}
	if c.Timezone == "" {
		t.Error("coverage does not state the timezone")
	}
	if len(c.Calendars) == 0 {
		t.Error("coverage does not name the calendars searched")
	}
	if c.Scope == "" || c.HowToGetDetail == "" {
		t.Errorf("coverage does not state its scope or how to get detail: %+v", c)
	}
	// Upkeep is the one area the default leaves out, and it says so.
	if len(c.NotIncluded) != 1 || c.NotIncluded[0] != "maintenance" {
		t.Errorf("coverage.not_included = %v, want [maintenance]", c.NotIncluded)
	}
	if len(c.Hidden) != 0 {
		t.Errorf("an owner is hidden areas: %v", c.Hidden)
	}
	for _, a := range []string{"events", "bills", "meals", "lists"} {
		if !hasArea(c.Areas, a) {
			t.Errorf("default areas = %v, missing %s", c.Areas, a)
		}
	}
	if hasArea(c.Areas, "maintenance") || len(d.Maintenance) != 0 {
		t.Errorf("default includes upkeep: %v", c.Areas)
	}

	// A summary must still be complete in what it reports: every bill due in
	// the range is counted and summed even though few are listed.
	if c.BillsCount == nil || *c.BillsCount < 20 {
		t.Errorf("coverage.bills_count = %v, want the whole week's ~60 bills", c.BillsCount)
	}
	if c.BillsListed == nil || *c.BillsListed != len(d.Bills) {
		t.Errorf("coverage.bills_listed = %v, bills listed = %d", c.BillsListed, len(d.Bills))
	}
	if c.BillsNotListed == nil || *c.BillsNotListed == 0 {
		t.Errorf("coverage does not say how many bills it left out: %+v", c)
	}
	if c.BillsAmount == "" || c.BillsAmount == "0.00" {
		t.Errorf("coverage.bills_amount_total = %q, want the week's total", c.BillsAmount)
	}
	// Events: series are one entry each, and every occurrence is counted.
	if c.EventsOccurrences == nil || *c.EventsOccurrences < 20 {
		t.Errorf("coverage.events_occurrences = %v, want the whole week's events", c.EventsOccurrences)
	}
	if *c.EventsOccurrences <= *c.EventsListed {
		t.Errorf("a week of the bench household should collapse series into fewer entries than occurrences: %d listed, %d occurrences",
			*c.EventsListed, *c.EventsOccurrences)
	}
	for _, e := range d.Events {
		if e.Title == "" || e.ID == "" {
			t.Errorf("event entry without a title or id: %+v", e)
		}
		if e.Repeat != "" && len(e.Dates) == 0 {
			t.Errorf("series %q has no dates", e.Title)
		}
		if e.Description != nil || e.CalendarID != "" {
			t.Errorf("summary entry %q carries full-detail fields: %+v", e.Title, e)
		}
	}

	// detail=full is reachable and bigger: every occurrence with every field.
	fullText, fullEnv := call(map[string]any{"detail": "full"})
	if len(fullText) <= len(text) {
		t.Errorf("detail=full is %d bytes, not bigger than the default's %d", len(fullText), len(text))
	}
	if got := fullEnv.Data.Coverage.Detail; got != "full" {
		t.Errorf("coverage.detail = %q, want full", got)
	}
	if *fullEnv.Data.Coverage.EventsListed != *fullEnv.Data.Coverage.EventsOccurrences {
		t.Errorf("detail=full lists %d of %d occurrences, want every one",
			*fullEnv.Data.Coverage.EventsListed, *fullEnv.Data.Coverage.EventsOccurrences)
	}
	if len(fullEnv.Data.Events) <= len(d.Events) {
		t.Errorf("detail=full has %d entries, not more than the summary's %d", len(fullEnv.Data.Events), len(d.Events))
	}
	for _, e := range fullEnv.Data.Events {
		if e.Start == "" || e.End == "" || e.CalendarID == "" || e.Description == nil {
			t.Errorf("detail=full entry is missing fields: %+v", e)
		}
	}

	// areas=[maintenance] still answers what is due, and only that.
	_, maint := call(map[string]any{"areas": []string{"maintenance"}})
	if len(maint.Data.Maintenance) == 0 {
		t.Error("areas=[maintenance] returned nothing on a household with 400 upkeep tasks")
	}
	if len(maint.Data.Maintenance) > 40 {
		t.Errorf("areas=[maintenance] listed %d tasks; a week of 400 tasks over a year should be a handful", len(maint.Data.Maintenance))
	}
	if maint.Data.Coverage.MaintenanceListed == nil || *maint.Data.Coverage.MaintenanceListed != len(maint.Data.Maintenance) {
		t.Errorf("coverage.maintenance_listed = %v, listed %d", maint.Data.Coverage.MaintenanceListed, len(maint.Data.Maintenance))
	}
	if len(maint.Data.Events) != 0 || len(maint.Data.Bills) != 0 {
		t.Errorf("areas=[maintenance] carried other areas")
	}

	// max_per_day raises the one-off cap, and the result says it was raised.
	_, raised := call(map[string]any{"max_per_day": 20, "areas": []string{"events"}})
	if *raised.Data.Coverage.EventsListed < *c.EventsListed {
		t.Errorf("max_per_day=20 listed %d entries, not at least the default's %d",
			*raised.Data.Coverage.EventsListed, *c.EventsListed)
	}

	// A month is still answerable, and still small enough to be worth asking.
	month, monthEnv := call(map[string]any{"from": c.From, "to": dayAfter(t, c.From, 30)})
	if n := len(month); n > 4*maxDefaultAgendaBytes {
		t.Errorf("a 30-day range is %d bytes, more than 4x the week's budget", n)
	} else {
		t.Logf("30-day range: %d bytes", n)
	}
	if monthEnv.Data.Coverage.Days != 31 {
		t.Errorf("30 days later is %d days, want 31", monthEnv.Data.Coverage.Days)
	}
}

func hasArea(areas []string, want string) bool {
	for _, a := range areas {
		if a == want {
			return true
		}
	}
	return false
}

func dayAfter(t *testing.T, day string, n int) string {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatalf("dayAfter(%q): %v", day, err)
	}
	return parsed.AddDate(0, 0, n).Format("2006-01-02")
}

// agendaSizeEnvelope is the part of an agenda_get result this test reads.
type agendaSizeEnvelope struct {
	OK   bool `json:"ok"`
	Data *struct {
		Coverage struct {
			Detail            string   `json:"detail"`
			From              string   `json:"from"`
			To                string   `json:"to"`
			Days              int      `json:"days"`
			Timezone          string   `json:"timezone"`
			Calendars         []string `json:"calendars"`
			Scope             string   `json:"scope"`
			Areas             []string `json:"areas"`
			NotIncluded       []string `json:"not_included"`
			Hidden            []string `json:"hidden"`
			EventsListed      *int     `json:"events_listed"`
			EventsOccurrences *int     `json:"events_occurrences"`
			EventsNotListed   *int     `json:"events_not_listed"`
			BillsListed       *int     `json:"bills_listed"`
			BillsCount        *int     `json:"bills_count"`
			BillsNotListed    *int     `json:"bills_not_listed"`
			BillsAmount       string   `json:"bills_amount_total"`
			MaintenanceListed *int     `json:"maintenance_listed"`
			MealsListed       *int     `json:"meals_listed"`
			HowToGetDetail    string   `json:"how_to_get_detail"`
		} `json:"coverage"`
		Events []struct {
			ID          string   `json:"id"`
			Title       string   `json:"title"`
			LocalDate   string   `json:"local_date"`
			Weekday     string   `json:"weekday"`
			At          string   `json:"at"`
			Repeat      string   `json:"repeat"`
			Dates       []string `json:"dates"`
			Start       string   `json:"start"`
			End         string   `json:"end"`
			CalendarID  string   `json:"calendar_id"`
			Description *string  `json:"description"`
		} `json:"events"`
		Bills []struct {
			BillID string `json:"bill_id"`
			Name   string `json:"name"`
		} `json:"bills"`
		Maintenance []struct {
			TaskID string `json:"task_id"`
		} `json:"maintenance"`
	} `json:"data"`
}
