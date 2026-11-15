package maintenance

import (
	"home-os/mcp/internal/server"
)

// Register adds the upkeep tools (catalog v2: maintenance_find,
// maintenance_create, maintenance_update, maintenance_complete,
// maintenance_delete) to srv. They replace list_maintenance_tasks,
// get_upcoming_maintenance and create_maintenance_task. cmd/mcp calls it
// once at startup, so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewMaintenanceFindTool(deps.Pool))
	srv.RegisterTool(NewMaintenanceCreateTool(deps.Pool))
	srv.RegisterTool(NewMaintenanceUpdateTool(deps.Pool))
	srv.RegisterTool(NewMaintenanceCompleteTool(deps.Pool))
	srv.RegisterTool(NewMaintenanceDeleteTool(deps.Pool))
}
