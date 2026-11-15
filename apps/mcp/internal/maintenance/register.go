package maintenance

import (
	"home-os/mcp/internal/server"
)

// Register adds the maintenance tools to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	maintListName, maintListTool, maintListHandler := NewListMaintenanceTasksTool(deps.Pool)
	srv.RegisterTool(maintListName, maintListTool, maintListHandler)
	maintUpcomingName, maintUpcomingTool, maintUpcomingHandler := NewGetUpcomingMaintenanceTool(deps.Pool)
	srv.RegisterTool(maintUpcomingName, maintUpcomingTool, maintUpcomingHandler)
	maintCreateName, maintCreateTool, maintCreateHandler := NewCreateMaintenanceTaskTool(deps.Pool)
	srv.RegisterTool(maintCreateName, maintCreateTool, maintCreateHandler)
}
