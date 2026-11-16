package vendors

import (
	"home-os/mcp/internal/server"
)

// Register adds the contact tools (catalog v2: contact_find, contact_create,
// contact_update, contact_delete) to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package. contact_find replaces
// list_vendors and get_vendor. Nothing here indexes into Typesense (the
// pet and vehicle write tools don't either; search_all reads SQL), so no
// indexer is passed (and no typed-nil indexer can slip into an interface,
// see #4368).
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewContactFindTool(deps.Pool))
	srv.RegisterTool(NewContactCreateTool(deps.Pool))
	srv.RegisterTool(NewContactUpdateTool(deps.Pool))
	srv.RegisterTool(NewContactDeleteTool(deps.Pool))
}
