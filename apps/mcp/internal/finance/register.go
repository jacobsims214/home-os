package finance

import (
	"home-os/mcp/internal/server"
)

// Register adds the finance tool (catalog v2: budget_summary) to srv. cmd/mcp
// calls it once at startup, so changes to these tools stay inside this
// package. budget_summary replaces get_financial_summary and the bill
// package's old get_bill_summary.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewBudgetSummaryTool(deps.Pool))
}
