package finance

import (
	"home-os/mcp/internal/server"
)

// Register adds the finance tool to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool("get_financial_summary", NewGetFinancialSummaryTool(), HandleGetFinancialSummary(deps.Pool))
}
