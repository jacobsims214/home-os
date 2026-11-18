package search

import (
	"home-os/mcp/internal/server"
)

// Register adds the search tool to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	searchName, searchTool, searchHandler := NewSearchTool(deps.Pool)
	srv.RegisterTool(searchName, searchTool, searchHandler)
}
