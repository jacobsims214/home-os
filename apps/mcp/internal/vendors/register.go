package vendors

import (
	"home-os/mcp/internal/server"
)

// Register adds the contact tools (catalog v2: contact_find) to srv.
// cmd/mcp calls it once at startup, so changes to these tools stay inside
// this package. contact_find replaces list_vendors and get_vendor. The
// contact write tools (contact_create, contact_update, contact_delete) are
// Stage 2 (#4466). Nothing here indexes into Typesense, so no indexer is
// passed (and no typed-nil indexer can slip into an interface, see #4368).
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewContactFindTool(deps.Pool))
}
