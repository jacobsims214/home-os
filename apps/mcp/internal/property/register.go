package property

import (
	"home-os/mcp/internal/server"
)

// Register adds the property tools (catalog v2: property_find) to srv.
// cmd/mcp calls it once at startup, so changes to these tools stay inside
// this package. property_find replaces list_properties and get_property.
// The property/room write tools (property_create, property_update,
// property_delete, room_delete) are Stage 2 (#4462).
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewPropertyFindTool(deps.Pool))
}
