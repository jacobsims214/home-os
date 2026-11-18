package lists

import (
	"home-os/mcp/internal/server"
)

// Register adds the list tools (catalog v2: list_find, list_get,
// list_create, list_add_items, list_check_items, list_update_item,
// list_remove_items, list_update) to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewListFindTool(deps.Pool))
	srv.RegisterTool(NewListGetTool(deps.Pool))
	srv.RegisterTool(NewListCreateTool(deps.Pool))
	srv.RegisterTool(NewListAddItemsTool(deps.Pool))
	srv.RegisterTool(NewListCheckItemsTool(deps.Pool))
	srv.RegisterTool(NewListUpdateItemTool(deps.Pool))
	srv.RegisterTool(NewListRemoveItemsTool(deps.Pool))
	srv.RegisterTool(NewListUpdateTool(deps.Pool))
}
