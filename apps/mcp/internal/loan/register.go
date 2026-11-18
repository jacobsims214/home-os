package loan

import (
	"home-os/mcp/internal/server"
)

// Register adds the loan tools (catalog v2: loan_find, loan_create,
// loan_update, loan_delete) to srv. cmd/mcp calls it once at startup, so
// changes to these tools stay inside this package. They replace list_loans,
// get_loan, create_loan, update_loan and delete_loan.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewLoanFindTool(deps.Pool))
	srv.RegisterTool(NewLoanCreateTool(deps.Pool))
	srv.RegisterTool(NewLoanUpdateTool(deps.Pool))
	srv.RegisterTool(NewLoanDeleteTool(deps.Pool))
}
