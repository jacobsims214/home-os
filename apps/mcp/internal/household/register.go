package household

import (
	"home-os/mcp/internal/server"
)

// Register adds the household tools, including family_update to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool("get_household", NewGetHouseholdTool(), HandleGetHousehold(deps.Pool))
	srv.RegisterTool("list_members", NewListMembersTool(), HandleListMembers(deps.Pool))
	familyUpdateName, familyUpdateTool, familyUpdateHandler := NewFamilyUpdateTool(deps.Pool)
	srv.RegisterTool(familyUpdateName, familyUpdateTool, familyUpdateHandler)
}
