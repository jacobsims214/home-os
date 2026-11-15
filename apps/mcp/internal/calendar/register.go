package calendar

import (
	"home-os/mcp/internal/server"
)

// Register adds the calendar tools to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package. Names follow the v2
// catalog (KB architecture/mcp-tool-catalog.md); check_conflicts is merged
// into event_find (from/to = the proposed time).
func Register(srv *server.Server, deps server.Deps) {
	calTool, calHandler := NewCalendarFindTool(deps.Pool)
	srv.RegisterTool(calTool.Name, calTool, calHandler)

	findTool, findHandler := NewEventFindTool(deps.Pool)
	srv.RegisterTool(findTool.Name, findTool, findHandler)

	createTool, createHandler := NewEventCreateTool(deps.Pool)
	srv.RegisterTool(createTool.Name, createTool, createHandler)

	updateTool, updateHandler := NewEventUpdateTool(deps.Pool)
	srv.RegisterTool(updateTool.Name, updateTool, updateHandler)

	deleteTool, deleteHandler := NewEventDeleteTool(deps.Pool)
	srv.RegisterTool(deleteTool.Name, deleteTool, deleteHandler)

	freeTool, freeHandler := NewCalendarFindFreeTimeTool(deps.Pool)
	srv.RegisterTool(freeTool.Name, freeTool, freeHandler)

	agendaTool, agendaHandler := NewAgendaGetTool(deps.Pool)
	srv.RegisterTool(agendaTool.Name, agendaTool, agendaHandler)
}
