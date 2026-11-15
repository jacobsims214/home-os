package pet

import (
	"home-os/mcp/internal/server"
)

// Register adds the pet tools to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool("list_pets", NewListTool(), HandleList(deps.Pool))
	srv.RegisterTool("get_pet", NewGetTool(), HandleGet(deps.Pool))
}
