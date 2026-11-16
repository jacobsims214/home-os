package people

import (
	"home-os/mcp/internal/server"
)

// Register adds the person tools of catalog v2 that read and change one
// person (person_get, person_create, person_update, person_delete) to srv.
// person_find stays in internal/household, next to family_overview, which
// shares its query. cmd/mcp calls Register once at startup, so changes to
// these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewPersonGetTool(deps.Pool))
	srv.RegisterTool(NewPersonCreateTool(deps.Pool))
	srv.RegisterTool(NewPersonUpdateTool(deps.Pool))
	srv.RegisterTool(NewPersonDeleteTool(deps.Pool))
}
