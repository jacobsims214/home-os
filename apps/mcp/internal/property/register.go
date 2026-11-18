package property

import (
	"home-os/mcp/internal/server"
)

// Register adds the property tools (catalog v2: property_find,
// property_create, property_update, property_delete, room_delete) to srv.
// cmd/mcp calls it once at startup, so changes to these tools stay inside
// this package. property_find replaces list_properties and get_property;
// the write tools are #4462. Rooms are created and renamed through
// property_create rooms[] and property_update add_rooms / rename_rooms.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewPropertyFindTool(deps.Pool))
	srv.RegisterTool(NewPropertyCreateTool(deps.Pool))
	srv.RegisterTool(NewPropertyUpdateTool(deps.Pool))
	srv.RegisterTool(NewPropertyDeleteTool(deps.Pool))
	srv.RegisterTool(NewRoomDeleteTool(deps.Pool))
}
