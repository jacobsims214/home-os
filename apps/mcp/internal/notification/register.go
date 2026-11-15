package notification

import (
	"home-os/mcp/internal/server"
)

// Register adds the notification tools (catalog v2: notification_find and
// notification_mark_read) to srv. cmd/mcp calls it once at startup, so
// changes to these tools stay inside this package. notification_find
// replaces list_notifications and get_unread_count.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool(NewNotificationFindTool(deps.Pool))
	srv.RegisterTool(NewNotificationMarkReadTool(deps.Pool))
}
