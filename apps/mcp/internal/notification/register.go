package notification

import (
	"home-os/mcp/internal/server"
)

// Register adds the notification tools to srv. cmd/mcp calls it once at startup,
// so changes to these tools stay inside this package.
func Register(srv *server.Server, deps server.Deps) {
	srv.RegisterTool("list_notifications", NewListNotificationsTool(), HandleListNotifications(deps.Pool))
	srv.RegisterTool("get_unread_count", NewGetUnreadCountTool(), HandleGetUnreadCount(deps.Pool))
}
