package meals

import (
	"home-os/mcp/internal/server"
)

// Register adds the recipe tools (catalog v2: recipe_find, recipe_get,
// recipe_create, recipe_update, recipe_delete) to srv. cmd/mcp calls it once
// at startup, so changes to these tools stay inside this package. The meal_*
// tools (#4487, #4496, #4500) register here too when they land.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewRecipeFindTool(deps.Pool))
	srv.RegisterTool(NewRecipeGetTool(deps.Pool))
	srv.RegisterTool(NewRecipeCreateTool(deps.Pool))
	srv.RegisterTool(NewRecipeUpdateTool(deps.Pool))
	srv.RegisterTool(NewRecipeDeleteTool(deps.Pool))
}
