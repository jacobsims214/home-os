package vehicle

import (
	"home-os/mcp/internal/server"
)

// Register adds the vehicle tools to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool("list_vehicles", NewListTool(), HandleList(deps.Pool))
	srv.RegisterTool("get_vehicle", NewGetTool(), HandleGet(deps.Pool))
}
