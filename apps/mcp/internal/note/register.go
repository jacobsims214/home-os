package note

import (
	"home-os/mcp/internal/server"
)

// Register adds the note tools to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	noteListName, noteListTool, noteListHandler := NewListNotesTool(deps.Pool)
	srv.RegisterTool(noteListName, noteListTool, noteListHandler)
	noteCreateName, noteCreateTool, noteCreateHandler := NewCreateNoteTool(deps.Pool)
	srv.RegisterTool(noteCreateName, noteCreateTool, noteCreateHandler)
	noteGetName, noteGetTool, noteGetHandler := NewNoteGetTool(deps.Pool)
	srv.RegisterTool(noteGetName, noteGetTool, noteGetHandler)
	noteUpdateName, noteUpdateTool, noteUpdateHandler := NewNoteUpdateTool(deps.Pool)
	srv.RegisterTool(noteUpdateName, noteUpdateTool, noteUpdateHandler)
	noteDeleteName, noteDeleteTool, noteDeleteHandler := NewNoteDeleteTool(deps.Pool)
	srv.RegisterTool(noteDeleteName, noteDeleteTool, noteDeleteHandler)
}
