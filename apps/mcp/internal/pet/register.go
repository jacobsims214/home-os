package pet

import (
	"home-os/mcp/internal/server"
)

// Register adds the pet tools (catalog v2: pet_find) to srv. cmd/mcp calls
// it once at startup, so changes to these tools stay inside this package.
// pet_find replaces list_pets and get_pet. The pet write tools (pet_create,
// pet_update, pet_delete) are Stage 2 (#4461).
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewPetFindTool(deps.Pool))
}
