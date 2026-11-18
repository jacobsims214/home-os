package activity

import (
	"home-os/mcp/internal/server"
)

// Register adds the activity tools (catalog v2 "activity": activity_find) to
// srv. cmd/mcp calls it once at startup, so changes to this tool stay inside
// this package.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewActivityFindTool(deps.Pool))
}
