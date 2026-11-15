package server

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
)

// RegisterPing adds the "ping" tool, which returns {"pong":true} so clients
// can check the server is up. It needs no dependencies.
func RegisterPing(srv *Server, _ Deps) {
	pingTool := mcp.NewTool("ping",
		mcp.WithDescription("A simple test tool that returns pong to verify the server is running"),
	)
	srv.RegisterTool("ping", pingTool, handlePing)
}

// handlePing is the handler for the "ping" tool.
func handlePing(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText(`{"pong":true}`), nil
}
