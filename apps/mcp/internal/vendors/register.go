package vendors

import (
	"home-os/mcp/internal/server"
)

// Register adds the vendor tools to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool("list_vendors", NewListTool(), HandleList(deps.Pool))
	srv.RegisterTool("get_vendor", NewGetTool(), HandleGet(deps.Pool))
}
