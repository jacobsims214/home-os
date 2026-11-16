package budget

import (
	"home-os/mcp/internal/server"
)

// Register adds the budget write tool (catalog v2: budget_set_category) to
// srv. Reading budgets is budget_summary's "budget" and "due_soon" sections
// (finance), which call Load and LoadDueSoon here.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewSetCategoryTool(deps.Pool))
}
