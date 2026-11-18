package server

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/config"
)

// Deps carries what tool packages need to build their tools. Each tool
// package exposes Register(srv, deps); cmd/mcp builds one Deps and passes it
// to every package. Add a field here when a package needs a new dependency
// instead of widening every Register signature.
type Deps struct {
	Pool   *pgxpool.Pool
	Config *config.Config
}
