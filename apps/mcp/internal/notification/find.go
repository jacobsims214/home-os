package notification

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// findAliases are parameter names small models reach for, accepted but not
// advertised.
var findAliases = map[string]string{"unread": "unread_only"}

// findExample is a valid notification_find call for error examples.
var findExample = map[string]any{"unread_only": true, "limit": 10}

// findInput is the validated notification_find arguments.
type findInput struct {
	unreadOnly bool
	limit      int
	offset     int
}

// findResult is notification_find's data. unread_count is always present
// (catalog: "Always includes unread_count"), so the result is a single
// object rather than respond's list envelope, which has no room for it.
type findResult struct {
	UnreadCount   int                `json:"unread_count" jsonschema_description:"How many of your notifications are unread, whatever the filter."`
	Total         int                `json:"total" jsonschema_description:"How many notifications match the filter in all."`
	Returned      int                `json:"returned" jsonschema_description:"How many are in notifications."`
	Truncated     bool               `json:"truncated" jsonschema_description:"true when more match; pass next_cursor as cursor for the next page."`
	NextCursor    string             `json:"next_cursor,omitempty" jsonschema_description:"Pass as cursor, with the same filters, for the next page. Absent on the last page."`
	Notifications []notificationData `json:"notifications" jsonschema_description:"Newest first."`
}

// listSQL loads one page of the caller's visible notifications, newest
// first. $4 limits it to unread ones.
var listSQL = `
	SELECT n.id, n.type, n.title, NULLIF(btrim(n.body), ''), n.user_id IS NULL, n.read_at, n.created_at,
	       NULLIF(btrim(n.entity_type), ''), n.entity_id
	FROM notifications n
	WHERE ` + visibleSQL + `
	  AND (NOT $4 OR n.read_at IS NULL)
	ORDER BY n.created_at DESC, n.id DESC
	LIMIT $5 OFFSET $6`

// NewNotificationFindTool creates the notification_find MCP tool (catalog
// v2, class R), replacing list_notifications and get_unread_count.
func NewNotificationFindTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("List your notifications (reminders, changes others made). Always includes unread_count."),
		mcp.WithBoolean("unread_only", mcp.Description("true to list only unread notifications (default false)")),
		mcp.WithNumber("limit", mcp.Description("Max notifications to return, 1-100 (default 25)"), mcp.Min(1), mcp.Max(maxLimit)),
		mcp.WithString("cursor", mcp.Description("next_cursor from the previous result, for the next page")),
		respond.Schema[findResult](),
	}
	tool := mcp.NewTool("notification_find", append(opts, annotations("Find notifications", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}
		in, err := parseFindInput(req.GetArguments())
		if err != nil {
			return invalid(err, findExample), nil
		}
		res, err := findNotifications(ctx, pool, claims.HouseholdID, callerUser(claims.UserID), in)
		if err != nil {
			return respond.Failed("find notifications", err), nil
		}
		return respond.OK(findSummary(in, res), res, findNext(in, res)...), nil
	}
	return "notification_find", tool, handler
}

// findNotifications reads the counts and one page in one read-only snapshot,
// so unread_count, total and the page agree.
func findNotifications(ctx context.Context, pool *pgxpool.Pool, householdID string, userID *uuid.UUID, in findInput) (findResult, error) {
	res := findResult{Notifications: []notificationData{}}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	role, err := callerRole(ctx, tx, householdID, userID)
	if err != nil {
		return res, err
	}
	unread, all, err := counts(ctx, tx, householdID, userID, role)
	if err != nil {
		return res, err
	}
	res.UnreadCount, res.Total = unread, all
	if in.unreadOnly {
		res.Total = unread
	}
	loc, err := loadLocation(ctx, tx, householdID)
	if err != nil {
		return res, err
	}
	rows, err := tx.Query(ctx, listSQL, householdID, userID, role, in.unreadOnly, in.limit, in.offset)
	if err != nil {
		return res, err
	}
	page, err := pgx.CollectRows(rows, func(cr pgx.CollectableRow) (notificationData, error) {
		var r row
		err := cr.Scan(&r.id, &r.typ, &r.title, &r.body, &r.familyWide, &r.readAt, &r.createdAt, &r.entityType, &r.entityID)
		return r.data(loc), err
	})
	if err != nil {
		return res, err
	}
	if page != nil {
		res.Notifications = page
	}
	res.Returned = len(res.Notifications)
	if end := in.offset + res.Returned; end < res.Total && res.Returned > 0 {
		res.Truncated = true
		res.NextCursor = respond.EncodeCursor(end)
	}
	return res, tx.Commit(ctx)
}

// findSummary is notification_find's one-line summary.
func findSummary(in findInput, res findResult) string {
	unread := plural(res.UnreadCount, "unread notification")
	switch {
	case res.Total == 0 && in.unreadOnly:
		return "No unread notifications."
	case res.Total == 0:
		return "No notifications yet."
	case res.Returned == 0:
		return fmt.Sprintf("Found %s; this page is past the end. You have %s.", plural(res.Total, "notification"), unread)
	case res.Truncated || in.offset > 0:
		return fmt.Sprintf("Showing %d-%d of %s; you have %s.", in.offset+1, in.offset+res.Returned,
			plural(res.Total, matchNoun(in)), unread)
	case res.Returned == 1:
		n := res.Notifications[0]
		return fmt.Sprintf("1 %s: %q (%s %s). You have %s.", matchNoun(in), n.Title, n.Weekday, n.LocalDate, unread)
	default:
		return fmt.Sprintf("Found %s; you have %s.", plural(res.Total, matchNoun(in)), unread)
	}
}

func matchNoun(in findInput) string {
	if in.unreadOnly {
		return "unread notification"
	}
	return "notification"
}

// findNext is notification_find's next-step hints (at most three).
func findNext(in findInput, res findResult) []string {
	var next []string
	if res.NextCursor != "" {
		next = append(next, "Pass next_cursor as cursor for the next page.")
	}
	for _, n := range res.Notifications {
		if n.Open != nil && !n.Read {
			next = append(next, fmt.Sprintf("See what %q is about with %s.", n.Title, *n.Open))
			break
		}
	}
	switch {
	case res.UnreadCount > 0:
		var firstUnread string
		for _, n := range res.Notifications {
			if !n.Read {
				firstUnread = n.ID
				break
			}
		}
		if firstUnread != "" {
			next = append(next, fmt.Sprintf("After telling the user, mark the ones they saw read with notification_mark_read notification_ids=[%q].", firstUnread))
		} else {
			next = append(next, "After telling the user, mark the ones they saw read with notification_mark_read notification_ids=[…].")
		}
	case res.Total == 0 && !in.unreadOnly:
		next = append(next, "Bill and upkeep reminders appear here when they come due; see what's coming with agenda_get.")
	case res.Total == 0 && in.unreadOnly:
		next = append(next, "Everything is read; call notification_find without unread_only to see past ones.")
	}
	if len(next) > 3 {
		next = next[:3]
	}
	return next
}

// parseFindInput validates notification_find arguments.
func parseFindInput(raw map[string]any) (findInput, error) {
	in := findInput{limit: defaultLimit}
	args := withAliases(raw, findAliases)
	var err error
	if in.unreadOnly, err = optionalBool(args, "unread_only", false); err != nil {
		return in, err
	}
	if n, ok, err := wholeNumber(args, "limit", 1, maxLimit); err != nil {
		return in, err
	} else if ok {
		in.limit = n
	}
	if c, err := optionalString(args, "cursor"); err != nil {
		return in, err
	} else if c != nil {
		off, ok := respond.DecodeCursor(*c)
		if !ok {
			return in, badArg("cursor", fmt.Sprintf("cursor %q is not a cursor from notification_find.", *c),
				"Pass next_cursor from the previous result, or leave cursor out for the first page.")
		}
		in.offset = off
	}
	return in, nil
}
