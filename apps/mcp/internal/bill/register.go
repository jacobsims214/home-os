package bill

import (
	"home-os/mcp/internal/server"
)

// Register adds the bill tools (catalog v2: bill_find, bill_create,
// bill_update, bill_delete, bill_pay) to srv. cmd/mcp calls it once at
// startup, so changes to these tools stay inside this package. The old
// get_bill_summary is folded into budget_summary (finance, #4372), which can
// use SummarizeByCategory for its obligations section.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewBillFindTool(deps.Pool))
	srv.RegisterTool(NewBillCreateTool(deps.Pool))
	srv.RegisterTool(NewBillUpdateTool(deps.Pool))
	srv.RegisterTool(NewBillDeleteTool(deps.Pool))
	srv.RegisterTool(NewBillPayTool(deps.Pool))
}
