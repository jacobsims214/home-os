package bill

import (
	"home-os/mcp/internal/server"
)

// Register adds the bill tools to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	billListName, billListTool, billListHandler := NewListBillsTool(deps.Pool)
	srv.RegisterTool(billListName, billListTool, billListHandler)
	billUpcomingName, billUpcomingTool, billUpcomingHandler := NewGetUpcomingBillsTool(deps.Pool)
	srv.RegisterTool(billUpcomingName, billUpcomingTool, billUpcomingHandler)
	billSummaryName, billSummaryTool, billSummaryHandler := NewGetBillSummaryTool(deps.Pool)
	srv.RegisterTool(billSummaryName, billSummaryTool, billSummaryHandler)
	billCreateName, billCreateTool, billCreateHandler := NewCreateBillTool(deps.Pool)
	srv.RegisterTool(billCreateName, billCreateTool, billCreateHandler)
	billUpdateName, billUpdateTool, billUpdateHandler := NewBillUpdateTool(deps.Pool)
	srv.RegisterTool(billUpdateName, billUpdateTool, billUpdateHandler)
	billDeleteName, billDeleteTool, billDeleteHandler := NewBillDeleteTool(deps.Pool)
	srv.RegisterTool(billDeleteName, billDeleteTool, billDeleteHandler)
	billPayName, billPayTool, billPayHandler := NewBillPayTool(deps.Pool)
	srv.RegisterTool(billPayName, billPayTool, billPayHandler)
}
