package file

import (
	"home-os/mcp/internal/server"
)

// Register adds the document tools (catalog v2: document_find, document_get,
// document_upload) to srv. cmd/mcp calls it once at startup, so changes to
// these tools stay inside this package. The package takes no search indexer:
// the worker indexes documents (#4444), so there is no typed-nil indexer to
// guard against here (the bug #4368 fixed in asset).
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewDocumentFindTool(deps.Pool))
	srv.RegisterTool(NewDocumentGetTool(deps.Pool))
	srv.RegisterTool(NewDocumentUploadTool(deps.Pool))
}
