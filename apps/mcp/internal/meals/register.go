package meals

import (
	"home-os/mcp/internal/server"
)

// Register adds the recipe tools (catalog v2: recipe_find, recipe_get,
// recipe_create, recipe_update, recipe_delete), the meal plan tools
// (meal_set, meal_rate; #4487) and meal_suggest (#4496) to srv. cmd/mcp
// calls it once at startup, so changes to these tools stay inside this
// package. The grocery-list tool (#4500) registers here too when it lands.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewRecipeFindTool(deps.Pool))
	srv.RegisterTool(NewRecipeGetTool(deps.Pool))
	srv.RegisterTool(NewRecipeCreateTool(deps.Pool))
	srv.RegisterTool(NewRecipeUpdateTool(deps.Pool))
	srv.RegisterTool(NewRecipeDeleteTool(deps.Pool))
	srv.RegisterTool(NewMealSetTool(deps.Pool))
	srv.RegisterTool(NewMealRateTool(deps.Pool))
	srv.RegisterTool(NewMealSuggestTool(deps.Pool))
}
