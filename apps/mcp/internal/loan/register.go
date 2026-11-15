package loan

import (
	"home-os/mcp/internal/server"
)

// Register adds the loan tools to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	loanTools := NewTools(deps.Pool)
	srv.RegisterTool("list_loans", loanTools.ListLoansTool(), loanTools.HandleListLoans)
	srv.RegisterTool("get_loan", loanTools.GetLoanTool(), loanTools.HandleGetLoan)
	srv.RegisterTool("create_loan", loanTools.CreateLoanTool(), loanTools.HandleCreateLoan)
	srv.RegisterTool("update_loan", loanTools.UpdateLoanTool(), loanTools.HandleUpdateLoan)
	srv.RegisterTool("delete_loan", loanTools.DeleteLoanTool(), loanTools.HandleDeleteLoan)
}
