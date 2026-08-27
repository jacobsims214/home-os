// Package prompts provides MCP prompt templates for common Home OS agent
// interactions. Each prompt is a template that guides the LLM through a
// specific task using available tools.
package prompts

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// NewDailyBriefingPrompt creates the "daily-briefing" prompt template.
// It accepts an optional "date" argument and instructs the agent to
// summarize today's events, tasks due, bills due, and unread notifications.
func NewDailyBriefingPrompt() mcp.Prompt {
	return mcp.NewPrompt("daily-briefing",
		mcp.WithPromptDescription("Summarize today's events, tasks due, bills due, and unread notifications"),
		mcp.WithArgument("date",
			mcp.ArgumentDescription("The date for the briefing (YYYY-MM-DD). Defaults to today if not provided."),
		),
	)
}

// HandleDailyBriefing handles the daily-briefing prompt request.
func HandleDailyBriefing(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	date := req.Params.Arguments["date"]
	if date == "" {
		date = "today"
	}

	messages := []mcp.PromptMessage{
		mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(fmt.Sprintf(`You are a home assistant. Provide a daily briefing for %s.

Please use the available tools to gather and summarize the following:

1. **Today's Events** — Call list_calendars and list_events to find any events scheduled for today.
2. **Tasks Due** — Call list_tasks or similar to find tasks due today or overdue.
3. **Bills Due** — Call get_upcoming_bills to find bills due today or soon.
4. **Unread Notifications** — Call get_unread_count and list_notifications to check for unread notifications.

Present the information in a clear, friendly summary. If any data is unavailable, note that gracefully.`, date))),
	}

	return &mcp.GetPromptResult{
		Description: "Daily briefing prompt",
		Messages:    messages,
	}, nil
}

// NewScheduleEventPrompt creates the "schedule-event" prompt template.
// It guides the agent through creating a calendar event with proper
// validation of title, date, time, duration, and calendar selection.
func NewScheduleEventPrompt() mcp.Prompt {
	return mcp.NewPrompt("schedule-event",
		mcp.WithPromptDescription("Guide the user through scheduling a new calendar event"),
		mcp.WithArgument("title",
			mcp.ArgumentDescription("The event title"),
			mcp.RequiredArgument(),
		),
		mcp.WithArgument("date",
			mcp.ArgumentDescription("The event date (YYYY-MM-DD)"),
			mcp.RequiredArgument(),
		),
		mcp.WithArgument("time",
			mcp.ArgumentDescription("The event start time (HH:MM, 24-hour format)"),
			mcp.RequiredArgument(),
		),
		mcp.WithArgument("duration",
			mcp.ArgumentDescription("The event duration in minutes (default 60)"),
		),
		mcp.WithArgument("calendar",
			mcp.ArgumentDescription("The calendar name or ID to add the event to"),
		),
	)
}

// HandleScheduleEvent handles the schedule-event prompt request.
func HandleScheduleEvent(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	title := req.Params.Arguments["title"]
	date := req.Params.Arguments["date"]
	time := req.Params.Arguments["time"]
	duration := req.Params.Arguments["duration"]
	calendar := req.Params.Arguments["calendar"]

	if duration == "" {
		duration = "60"
	}

	messages := []mcp.PromptMessage{
		mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(fmt.Sprintf(`You are a home assistant helping to schedule a calendar event.

Please follow these steps:

1. **Validate the inputs:**
   - Title: "%s"
   - Date: "%s"
   - Time: "%s"
   - Duration: %s minutes
   - Calendar: %s

2. **If the calendar is not specified**, call list_calendars to find available calendars and ask the user which one to use.

3. **Call create_event** with the validated parameters to create the event.

4. **Confirm the result** to the user, including the event details and which calendar it was added to.

If any parameter is invalid (e.g., malformed date or time), explain the issue to the user and ask for corrected input.`, title, date, time, duration, calendar))),
	}

	return &mcp.GetPromptResult{
		Description: "Schedule event prompt",
		Messages:    messages,
	}, nil
}

// NewCheckBillsPrompt creates the "check-bills" prompt template.
// It instructs the agent to call get_upcoming_bills and summarize
// what's due, including amounts and due dates.
func NewCheckBillsPrompt() mcp.Prompt {
	return mcp.NewPrompt("check-bills",
		mcp.WithPromptDescription("Check upcoming bills and summarize what's due"),
	)
}

// HandleCheckBills handles the check-bills prompt request.
func HandleCheckBills(_ context.Context, _ mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	messages := []mcp.PromptMessage{
		mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(`You are a home assistant helping to review upcoming bills.

Please follow these steps:

1. **Call get_upcoming_bills** to retrieve all bills that are due soon.

2. **Summarize the results** for the user:
   - Total amount due
   - List each bill with its amount and due date
   - Highlight any bills that are overdue or due within the next 7 days
   - Note any bills that have already been paid

3. If there are no upcoming bills, let the user know everything is clear.

Present the information in a clear, organized format.`)),
	}

	return &mcp.GetPromptResult{
		Description: "Check bills prompt",
		Messages:    messages,
	}, nil
}
