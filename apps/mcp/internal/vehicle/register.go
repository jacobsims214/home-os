package vehicle

import (
	"home-os/mcp/internal/server"
)

// Register adds the vehicle tools (catalog v2: vehicle_find) to srv. cmd/mcp
// calls it once at startup, so changes to these tools stay inside this
// package. vehicle_find replaces list_vehicles and get_vehicle. The write
// tools (vehicle_create/update/delete) arrive with #4460.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewVehicleFindTool(deps.Pool))
}
