// Package prompts provides MCP prompt templates for common family workflows.
// Each prompt is a short, numbered recipe for a small local model: it names
// the exact v2 tools (architecture/mcp-tool-catalog.md) and the params to pass.
// cmd/mcp's TestPromptsNameOnlyRegisteredTools fails if a prompt names a tool
// that isn't registered, so rename prompts here when a tool is renamed.
package prompts

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// arg returns the named prompt argument, or def when it is empty.
func arg(req mcp.GetPromptRequest, name, def string) string {
	if v := req.Params.Arguments[name]; v != "" {
		return v
	}
	return def
}

// userPrompt wraps one user message in a GetPromptResult.
func userPrompt(description, text string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{
		Description: description,
		Messages: []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(text)),
		},
	}
}

// NewDailyBriefingPrompt creates the "daily-briefing" prompt: one day's
// events, bills due and upkeep due, from agenda_get.
func NewDailyBriefingPrompt() mcp.Prompt {
	return mcp.NewPrompt("daily-briefing",
		mcp.WithPromptDescription("Brief the family on one day: events, bills due and upkeep due"),
		mcp.WithArgument("date",
			mcp.ArgumentDescription("Day to brief, YYYY-MM-DD (default today)"),
		),
	)
}

// HandleDailyBriefing handles the daily-briefing prompt request.
func HandleDailyBriefing(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	from := "from omitted (today)"
	if d := arg(req, "date", ""); d != "" {
		from = fmt.Sprintf("from=%q", d)
	}
	return userPrompt("Daily briefing", fmt.Sprintf(`Give the family a short briefing for one day.

1. Call agenda_get with %s. It returns events, bills due and maintenance due, with weekdays.
2. Call notification_find with unread_only=true for anything unread.
3. Reply in a few short lines: events by time, then bills due (name, amount), then upkeep due. Say "nothing" for an empty section.
Use the weekday and local_date the tools return; don't work them out yourself.`, from)), nil
}

// NewScheduleEventPrompt creates the "schedule-event" prompt: check free
// time with calendar_find_free_time, then add the event with event_create.
func NewScheduleEventPrompt() mcp.Prompt {
	return mcp.NewPrompt("schedule-event",
		mcp.WithPromptDescription("Add an event to the family calendar after checking it's free"),
		mcp.WithArgument("title",
			mcp.ArgumentDescription("Event title, e.g. Dentist"),
			mcp.RequiredArgument(),
		),
		mcp.WithArgument("date",
			mcp.ArgumentDescription("Event date, YYYY-MM-DD"),
			mcp.RequiredArgument(),
		),
		mcp.WithArgument("time",
			mcp.ArgumentDescription("Start time, HH:MM 24-hour, e.g. 17:30"),
			mcp.RequiredArgument(),
		),
		mcp.WithArgument("duration",
			mcp.ArgumentDescription("Length in minutes (default 60)"),
		),
		mcp.WithArgument("calendar",
			mcp.ArgumentDescription("Calendar name or id (default Family)"),
		),
	)
}

// HandleScheduleEvent handles the schedule-event prompt request.
func HandleScheduleEvent(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	title := arg(req, "title", "")
	date := arg(req, "date", "")
	start := arg(req, "time", "")
	duration := arg(req, "duration", "60")
	calendar := arg(req, "calendar", "Family")

	return userPrompt("Schedule event", fmt.Sprintf(`Add "%s" on %s at %s for %s minutes to the %s calendar.

1. Call calendar_find_free_time with from=%q, duration_minutes=%s. If %s isn't inside a free slot, tell the user what clashes and offer the nearest free slot; wait for their answer.
2. Call event_create with title=%q, calendar_id=%q, start and end as ISO 8601 with the family's offset (e.g. %sT%s:00-05:00; family_overview gives the timezone).
3. Confirm the title, weekday, date and time from the result. If it lists conflicts, mention them.`,
		title, date, start, duration, calendar,
		date, duration, start,
		title, calendar, date, start)), nil
}

// NewCheckBillsPrompt creates the "check-bills" prompt: the month's picture
// from budget_summary, then what's due soon from bill_find.
func NewCheckBillsPrompt() mcp.Prompt {
	return mcp.NewPrompt("check-bills",
		mcp.WithPromptDescription("Summarize what the family pays each month and which bills are due soon"),
		mcp.WithArgument("days",
			mcp.ArgumentDescription("How many days ahead count as due soon (default 7)"),
		),
	)
}

// HandleCheckBills handles the check-bills prompt request.
func HandleCheckBills(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	days := arg(req, "days", "7")
	return userPrompt("Check bills", fmt.Sprintf(`Review the family's bills.

1. Call budget_summary with sections=["obligations"] for the monthly total by category.
2. Call bill_find with due_within_days=%s for bills due soon.
3. Reply with the monthly total, then each bill due soon: name, amount, weekday and date, and whether it's autopay.
4. If the user says one is paid, call bill_pay with its bill_id.
If nothing is due, say so.`, days)), nil
}

// NewPlanWeekPrompt creates the "plan-week" prompt: the week ahead from
// agenda_get, gaps from calendar_find_free_time, new plans via event_create.
func NewPlanWeekPrompt() mcp.Prompt {
	return mcp.NewPrompt("plan-week",
		mcp.WithPromptDescription("Plan the family's week: what's on, what's due, and when there's free time"),
		mcp.WithArgument("from",
			mcp.ArgumentDescription("First day of the week, YYYY-MM-DD (default today)"),
		),
	)
}

// HandlePlanWeek handles the plan-week prompt request.
func HandlePlanWeek(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	from := arg(req, "from", "")
	when := "from today"
	fromParam := `from omitted (today), to = 6 days later`
	if from != "" {
		when = "from " + from
		fromParam = fmt.Sprintf(`from=%q, to = 6 days later`, from)
	}
	return userPrompt("Plan the week", fmt.Sprintf(`Help the family plan the 7 days %s.

1. Call agenda_get with %s (YYYY-MM-DD). It lists events, bills due and maintenance due per day.
2. Reply day by day (weekday + date), one line per item. Point out busy days and anything due.
3. If the user wants to fit something in, call calendar_find_free_time with the same from/to and duration_minutes, then add it with event_create (start/end ISO 8601 with offset).
Don't add anything without the user saying yes.`, when, fromParam)), nil
}

// NewHomeUpkeepPrompt creates the "home-upkeep" prompt: upkeep due soon from
// maintenance_find, marked done with maintenance_complete.
func NewHomeUpkeepPrompt() mcp.Prompt {
	return mcp.NewPrompt("home-upkeep",
		mcp.WithPromptDescription("Check upkeep due for the house, things and cars, and mark jobs done"),
		mcp.WithArgument("days",
			mcp.ArgumentDescription("How many days ahead to look (default 30)"),
		),
	)
}

// HandleHomeUpkeep handles the home-upkeep prompt request.
func HandleHomeUpkeep(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	days := arg(req, "days", "30")
	return userPrompt("Home upkeep", fmt.Sprintf(`Review upkeep for the house, appliances and cars.

1. Call maintenance_find with due_within_days=%s (overdue jobs are included).
2. Reply with overdue jobs first, then the rest by due date: name, what it's for, due weekday and date.
3. When the user says a job is done, call maintenance_complete with its task_id (add cost or notes if they give them).
4. To find a pro for a job, call contact_find with query set to the trade, e.g. plumber.`, days)), nil
}

// NewFindDocumentPrompt creates the "find-document" prompt: search with
// document_find, read with document_get.
func NewFindDocumentPrompt() mcp.Prompt {
	return mcp.NewPrompt("find-document",
		mcp.WithPromptDescription("Find a family document (insurance, receipt, manual) and answer from its text"),
		mcp.WithArgument("question",
			mcp.ArgumentDescription("What the user wants to know, e.g. When does the car insurance renew?"),
			mcp.RequiredArgument(),
		),
	)
}

// HandleFindDocument handles the find-document prompt request.
func HandleFindDocument(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	question := arg(req, "question", "")
	return userPrompt("Find document", fmt.Sprintf(`Answer from the family's documents: %q

1. Call document_find with query set to 1-3 key words from the question (e.g. "car insurance").
2. Pick the best match and call document_get with its document_id. If the answer isn't in the text, call document_get again with text_offset = the next page.
3. Answer in one or two sentences and name the document you used. If nothing matches, say so and suggest note_find.`, question)), nil
}

// NewTripPackingPrompt creates the "trip-packing" prompt: start a packing
// list from a template with list_create, add extras with list_add_items,
// and tick things off with list_check_items.
func NewTripPackingPrompt() mcp.Prompt {
	return mcp.NewPrompt("trip-packing",
		mcp.WithPromptDescription("Start a packing list for a trip from a template, then tick things off as they're packed"),
		mcp.WithArgument("trip",
			mcp.ArgumentDescription("Name for the trip's list, e.g. Beach trip"),
			mcp.RequiredArgument(),
		),
		mcp.WithArgument("template",
			mcp.ArgumentDescription("Template to copy, e.g. Beach trip packing (default: ask)"),
		),
	)
}

// HandleTripPacking handles the trip-packing prompt request.
func HandleTripPacking(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	trip := arg(req, "trip", "")
	pick := `Call list_find with kind="packing" and templates_only=true, show the user the template names, and ask which one fits (or none).`
	if t := arg(req, "template", ""); t != "" {
		pick = fmt.Sprintf(`Use the template %q (call list_find with kind="packing" and templates_only=true if you need to check its name).`, t)
	}
	return userPrompt("Trip packing", fmt.Sprintf(`Make a packing list for %q.

1. %s
2. Call list_create with name=%q, kind="packing" and from_template_id set to the template's name or id. Leave from_template_id out if there is none.
3. Ask what else to bring, then call list_add_items once with list_id=%q and all the extras as items.
4. As things are packed, call list_check_items with list_id=%q and texts=[...] (the item words, e.g. ["sunscreen"]). Read the "left" line back to the user.
5. After the trip, offer to archive it with list_update archived=true.`, trip, pick, trip, trip, trip)), nil
}
