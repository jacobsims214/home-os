package calendar

import (
	"home-os/mcp/internal/server"
)

// Register adds the calendar tools to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	listCalTool, listCalHandler := NewListCalendarsTool(deps.Pool)
	srv.RegisterTool("list_calendars", listCalTool, listCalHandler)

	listEvtTool, listEvtHandler := NewListEventsTool(deps.Pool)
	srv.RegisterTool("list_events", listEvtTool, listEvtHandler)

	createEvtTool, createEvtHandler := NewCreateEventTool(deps.Pool)
	srv.RegisterTool("create_event", createEvtTool, createEvtHandler)

	updateEvtTool, updateEvtHandler := NewUpdateEventTool(deps.Pool)
	srv.RegisterTool("update_event", updateEvtTool, updateEvtHandler)

	deleteEvtTool, deleteEvtHandler := NewDeleteEventTool(deps.Pool)
	srv.RegisterTool("delete_event", deleteEvtTool, deleteEvtHandler)

	slotsTool, slotsHandler := NewFindAvailableSlotsTool(deps.Pool)
	srv.RegisterTool("find_available_slots", slotsTool, slotsHandler)

	briefTool, briefHandler := NewGetDailyBriefingTool(deps.Pool)
	srv.RegisterTool("get_daily_briefing", briefTool, briefHandler)

	conflictTool, conflictHandler := NewCheckConflictsTool(deps.Pool)
	srv.RegisterTool("check_conflicts", conflictTool, conflictHandler)
}
