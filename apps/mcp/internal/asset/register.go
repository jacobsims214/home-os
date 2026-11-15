package asset

import (
	"home-os/mcp/internal/search"
	"home-os/mcp/internal/server"
)

// Register adds the asset tools (catalog v2: asset_find, asset_create,
// asset_update, asset_delete) to srv. cmd/mcp calls it once at startup, so
// changes to these tools stay inside this package. They replace list_assets,
// get_asset, create_asset, update_asset and delete_asset.
func Register(srv *server.Server, deps server.Deps) {
	var idx searchIndexer
	if i := search.NewIndexer(deps.Config); i != nil {
		idx = i
	}
	srv.RegisterTool(NewAssetFindTool(deps.Pool))
	srv.RegisterTool(NewAssetCreateTool(deps.Pool, idx))
	srv.RegisterTool(NewAssetUpdateTool(deps.Pool, idx))
	srv.RegisterTool(NewAssetDeleteTool(deps.Pool))
}
