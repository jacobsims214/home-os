package file

import (
	"home-os/mcp/internal/server"
)

// Register adds the document tools (catalog v2: document_find, document_get,
// document_upload, document_update, document_delete) to srv. cmd/mcp calls it
// once at startup, so changes to these tools stay inside this package. The
// Typesense client is a *Typesense that is nil when search is not
// configured; its methods are nil-safe, so there is no typed-nil interface
// to guard against (the bug #4368 fixed in asset).
func Register(srv *server.Server, deps server.Deps) {
	ts := NewTypesense(deps.Config)
	srv.RegisterTool(NewDocumentFindTool(deps.Pool, ts))
	srv.RegisterTool(NewDocumentGetTool(deps.Pool))
	srv.RegisterTool(NewDocumentUploadTool(deps.Pool, ts))
	srv.RegisterTool(NewDocumentUpdateTool(deps.Pool, ts))
	srv.RegisterTool(NewDocumentDeleteTool(deps.Pool, ts))
}
