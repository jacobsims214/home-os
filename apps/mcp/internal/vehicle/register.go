package vehicle

import (
	"home-os/mcp/internal/server"
)

// Register adds the vehicle tools (catalog v2: vehicle_find, vehicle_create,
// vehicle_update, vehicle_delete) to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package. vehicle_find replaces
// list_vehicles and get_vehicle.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewVehicleFindTool(deps.Pool))
	srv.RegisterTool(NewVehicleCreateTool(deps.Pool))
	srv.RegisterTool(NewVehicleUpdateTool(deps.Pool))
	srv.RegisterTool(NewVehicleDeleteTool(deps.Pool))
}
