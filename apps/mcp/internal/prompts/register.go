package prompts

import (
	"home-os/mcp/internal/server"
)

// Register adds the prompts to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterPrompt("daily-briefing", NewDailyBriefingPrompt(), HandleDailyBriefing)
	srv.RegisterPrompt("schedule-event", NewScheduleEventPrompt(), HandleScheduleEvent)
	srv.RegisterPrompt("check-bills", NewCheckBillsPrompt(), HandleCheckBills)
}
