package main

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/mcp/internal/activity"
	"home-os/mcp/internal/asset"
	"home-os/mcp/internal/bill"
	"home-os/mcp/internal/budget"
	"home-os/mcp/internal/calendar"
	"home-os/mcp/internal/config"
	"home-os/mcp/internal/credential"
	"home-os/mcp/internal/file"
	"home-os/mcp/internal/finance"
	"home-os/mcp/internal/guides"
	"home-os/mcp/internal/household"
	"home-os/mcp/internal/kb"
	"home-os/mcp/internal/lists"
	"home-os/mcp/internal/loan"
	"home-os/mcp/internal/maintenance"
	"home-os/mcp/internal/meals"
	"home-os/mcp/internal/note"
	"home-os/mcp/internal/notification"
	"home-os/mcp/internal/people"
	"home-os/mcp/internal/pet"
	"home-os/mcp/internal/prompts"
	"home-os/mcp/internal/property"
	"home-os/mcp/internal/resources"
	"home-os/mcp/internal/search"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/vehicle"
	"home-os/mcp/internal/vendors"
)

// registrars lists one Register function per package. Each package owns its
// tools, prompts or resource templates in its own register.go, so changing a
// tool never touches this file. Add a package here once, when it is created.
var registrars = []func(*server.Server, server.Deps){
	file.Register,
	note.Register,
	lists.Register,
	search.Register,
	bill.Register,
	maintenance.Register,
	asset.Register,
	property.Register,
	vehicle.Register,
	pet.Register,
	guides.Register,
	kb.Register,
	meals.Register,
	vendors.Register,
	calendar.Register,
	notification.Register,
	household.Register,
	people.Register,
	loan.Register,
	finance.Register,
	budget.Register,
	credential.Register,
	activity.Register,
	prompts.Register,
	resources.Register,
}

// registerAll registers every tool, prompt and resource template on srv.
// main and the protocol-compatibility test share it, so the test sees exactly
// the tool set production serves.
func registerAll(srv *server.Server, cfg *config.Config, pool *pgxpool.Pool) {
	deps := server.Deps{Pool: pool, Config: cfg}
	for _, register := range registrars {
		register(srv, deps)
	}
}
