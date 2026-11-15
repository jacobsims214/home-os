package asset

import (
	"home-os/mcp/internal/search"
	"home-os/mcp/internal/server"
)

// Register adds the asset tools to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	assetTools := NewTools(deps.Pool, search.NewIndexer(deps.Config))
	srv.RegisterTool("list_assets", assetTools.ListAssetsTool(), assetTools.HandleListAssets)
	srv.RegisterTool("get_asset", assetTools.GetAssetTool(), assetTools.HandleGetAsset)
	srv.RegisterTool("create_asset", assetTools.CreateAssetTool(), assetTools.HandleCreateAsset)
	srv.RegisterTool("update_asset", assetTools.UpdateAssetTool(), assetTools.HandleUpdateAsset)
	srv.RegisterTool("delete_asset", assetTools.DeleteAssetTool(), assetTools.HandleDeleteAsset)
}
