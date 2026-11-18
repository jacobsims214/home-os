package household

import (
	"home-os/mcp/internal/server"
)

// Register adds the family and person tools (catalog v2: family_overview,
// family_update, person_find) to srv. cmd/mcp calls it once at startup, so
// changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewFamilyOverviewTool(deps.Pool))
	srv.RegisterTool(NewFamilyUpdateTool(deps.Pool))
	srv.RegisterTool(NewPersonFindTool(deps.Pool))
}
