package resources

import (
	"home-os/mcp/internal/server"
)

// Register adds the resource templates to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterResourceTemplate(NewHouseholdResourceTemplate(), HandleHouseholdResource(deps.Pool))
	srv.RegisterResourceTemplate(NewCalendarEventsResourceTemplate(), HandleCalendarEventsResource(deps.Pool))
	srv.RegisterResourceTemplate(NewAssetResourceTemplate(), HandleAssetResource(deps.Pool))
}
