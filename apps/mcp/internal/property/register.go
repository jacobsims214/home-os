package property

import (
	"home-os/mcp/internal/server"
)

// Register adds the property tools to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	propertyTools := NewTools(deps.Pool)
	srv.RegisterTool("list_properties", propertyTools.ListPropertiesTool(), propertyTools.HandleListProperties)
	srv.RegisterTool("get_property", propertyTools.GetPropertyTool(), propertyTools.HandleGetProperty)
}
