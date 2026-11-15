package file

import (
	"home-os/mcp/internal/server"
)

// Register adds the file tools to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	fileListName, fileListTool, fileListHandler := NewListFilesTool(deps.Pool)
	srv.RegisterTool(fileListName, fileListTool, fileListHandler)
	fileUploadName, fileUploadTool, fileUploadHandler := NewUploadFileTool(deps.Pool)
	srv.RegisterTool(fileUploadName, fileUploadTool, fileUploadHandler)
	fileGetName, fileGetTool, fileGetHandler := NewGetFileTool(deps.Pool)
	srv.RegisterTool(fileGetName, fileGetTool, fileGetHandler)
	fileSearchName, fileSearchTool, fileSearchHandler := NewSearchFilesTool(deps.Pool)
	srv.RegisterTool(fileSearchName, fileSearchTool, fileSearchHandler)
}
