package pet

import (
	"home-os/mcp/internal/server"
)

// Register adds the pet tools (catalog v2: pet_find, pet_create, pet_update,
// pet_delete) to srv. cmd/mcp calls it once at startup, so changes to these
// tools stay inside this package. pet_find replaces list_pets and get_pet;
// the write tools are #4461.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewPetFindTool(deps.Pool))
	srv.RegisterTool(NewPetCreateTool(deps.Pool))
	srv.RegisterTool(NewPetUpdateTool(deps.Pool))
	srv.RegisterTool(NewPetDeleteTool(deps.Pool))
}
