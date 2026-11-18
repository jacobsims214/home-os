package guides

import (
	"home-os/mcp/internal/server"
)

// Register adds the guide tools (catalog v2: guide_find, guide_get,
// guide_create, guide_update, guide_delete) to srv. cmd/mcp calls it once at
// startup, so changes to these tools stay inside this package.
//
// When Typesense is configured, saves are indexed into household_search and
// guide_find searches it by meaning; without it guide_find matches words in
// SQL.
func Register(srv *server.Server, deps server.Deps) {
	var idx searchIndex
	if deps.Config != nil {
		if ts := newTypesense(deps.Config.TypesenseHost, deps.Config.TypesensePort, deps.Config.TypesenseAPIKey); ts != nil {
			idx = ts
		}
	}
	srv.RegisterTool(NewGuideFindTool(deps.Pool, idx))
	srv.RegisterTool(NewGuideGetTool(deps.Pool))
	srv.RegisterTool(NewGuideCreateTool(deps.Pool, idx))
	srv.RegisterTool(NewGuideUpdateTool(deps.Pool, idx))
	srv.RegisterTool(NewGuideDeleteTool(deps.Pool, idx))
}
