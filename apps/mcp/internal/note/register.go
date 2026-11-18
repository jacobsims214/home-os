package note

import (
	"home-os/mcp/internal/server"
)

// Register adds the note tools (catalog v2: note_find, note_get,
// note_create, note_update, note_delete) to srv. cmd/mcp calls it once at
// startup, so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewNoteFindTool(deps.Pool))
	srv.RegisterTool(NewNoteGetTool(deps.Pool))
	srv.RegisterTool(NewNoteCreateTool(deps.Pool))
	srv.RegisterTool(NewNoteUpdateTool(deps.Pool))
	srv.RegisterTool(NewNoteDeleteTool(deps.Pool))
}
