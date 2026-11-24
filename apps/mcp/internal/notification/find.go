package notification

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// findAliases are parameter names small models reach for, accepted but not
// advertised. "kind" is the catalog's word for what a notification is; "type"
// is the column's. Both are accepted, so an agent that read either works.
var findAliases = map[string]string{"unread": "unread_only", "kind": "type"}

// findExample is a valid notification_find call for error examples.
var findExample = map[string]any{"unread_only": true, "limit": 10}

// findInput is the validated notification_find arguments.
type findInput struct {
	unreadOnly bool
	// typ limits the page to one notification type, compared against the
	// notifications.type column (a bill reminder, an upkeep reminder, a
	// calendar alarm, a change notice). Empty means every type; an agent reads
	// the real values off each row's own type field.
	typ string
	// since is the earliest created_at to include. A YYYY-MM-DD since is read
	// at midnight in the family timezone (sinceDate); an ISO 8601 one is the
	// instant it names (sinceAt).
	sinceDate *string
	sinceAt   *time.Time
	limit     int
	offset    int
}

// findResult is notification_find's data. unread_count is always present
// (catalog: "Always includes unread_count"), so the result is a single
// object rather than respond's list envelope, which has no room for it.
type findResult struct {
	UnreadCount int `json:"unread_count" jsonschema_description:"How many of your notifications are unread, whatever the filter."`
	// NeedsYou is how many of this page's notifications still need a person:
	// a bill_due about a bill that pays itself is false, everything else is
	// true. It is the count to answer "what needs my attention right now?"
	// with, so an agent never has to reason about bill state itself.
	NeedsYou      int                `json:"needs_you" jsonschema_description:"How many on this page still need a person (a reminder about a bill that pays itself does not)."`
	Total         int                `json:"total" jsonschema_description:"How many notifications match the filter in all."`
	Returned      int                `json:"returned" jsonschema_description:"How many are in notifications."`
	Truncated     bool               `json:"truncated" jsonschema_description:"true when more match; pass next_cursor as cursor for the next page."`
	NextCursor    string             `json:"next_cursor,omitempty" jsonschema_description:"Pass as cursor, with the same filters, for the next page. Absent on the last page."`
	Notifications []notificationData `json:"notifications" jsonschema_description:"Newest first. needs_you says whether each one still needs a person."`
}

// listSQL loads one page of the caller's visible notifications, newest
// first. $4 limits it to unread ones, $5 to one type (” = every type), $6 to
// those created at or after an instant (NULL = no lower bound).
var listSQL = `
	SELECT n.id, n.type, n.title, NULLIF(btrim(n.body), ''), n.user_id IS NULL, n.read_at, n.created_at,
	       NULLIF(btrim(n.entity_type), ''), n.entity_id
	FROM notifications n
	WHERE ` + visibleSQL + `
	  AND (NOT $4 OR n.read_at IS NULL)
	  AND ($5 = '' OR n.type = $5)
	  AND ($6::timestamptz IS NULL OR n.created_at >= $6)
	ORDER BY n.created_at DESC, n.id DESC
	LIMIT $7 OFFSET $8`

// countSQL counts the notifications a filter matches. $4 is unread_only, $5
// the type, $6 the since instant: the same conditions as listSQL, so total and
// the page agree. unread_count is separate and unfiltered (see counts).
var countSQL = `
	SELECT count(*)
	FROM notifications n
	WHERE ` + visibleSQL + `
	  AND (NOT $4 OR n.read_at IS NULL)
	  AND ($5 = '' OR n.type = $5)
	  AND ($6::timestamptz IS NULL OR n.created_at >= $6)`

// NewNotificationFindTool creates the notification_find MCP tool (catalog
// v2, class R), replacing list_notifications and get_unread_count.
//
// The description leads with the watching use case: an agent that polls
// unread_only=true and reads needs_you can tell its person what needs them
// without guessing about bills.
func NewNotificationFindTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("List your notifications (reminders, changes). Poll unread_only=true to watch for new ones; needs_you tells you which need your person. Always includes unread_count."),
		mcp.WithBoolean("unread_only", mcp.Description("true to list only unread ones; poll this to watch (default false)")),
		mcp.WithString("type", mcp.Description("Only one kind, as each row's type field shows (default all)")),
		mcp.WithString("since", mcp.Description("Only ones created at or after this: YYYY-MM-DD or ISO 8601 with offset")),
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
// so unread_count, total and the page agree. The bills a page is about are
// loaded in the same snapshot, so a bill's attention state cannot change under
// the page.
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
	loc, err := loadLocation(ctx, tx, householdID)
	if err != nil {
		return res, err
	}
	since, err := in.sinceInstant(loc)
	if err != nil {
		return res, err
	}
	// unread_count is the whole unread set, whatever the filter (the catalog
	// promises "always"); total is what the filter matches.
	unread, _, err := counts(ctx, tx, householdID, userID, role)
	if err != nil {
		return res, err
	}
	res.UnreadCount = unread
	if err := tx.QueryRow(ctx, countSQL, householdID, userID, role, in.unreadOnly, in.typ, since).Scan(&res.Total); err != nil {
		return res, err
	}

	rows, err := tx.Query(ctx, listSQL, householdID, userID, role, in.unreadOnly, in.typ, since, in.limit, in.offset)
	if err != nil {
		return res, err
	}
	page, err := pgx.CollectRows(rows, func(cr pgx.CollectableRow) (row, error) {
		var r row
		err := cr.Scan(&r.id, &r.typ, &r.title, &r.body, &r.familyWide, &r.readAt, &r.createdAt, &r.entityType, &r.entityID)
		return r, err
	})
	if err != nil {
		return res, err
	}
	if err := attachBills(ctx, tx, householdID, page, role); err != nil {
		return res, err
	}
	today := todayIn(loc)
	for _, r := range page {
		res.Notifications = append(res.Notifications, r.data(loc, today))
		if r.bill == nil || r.bill.needsYou(today) {
			res.NeedsYou++
		}
	}
	res.Returned = len(res.Notifications)
	if end := in.offset + res.Returned; end < res.Total && res.Returned > 0 {
		res.Truncated = true
		res.NextCursor = respond.EncodeCursor(end)
	}
	return res, tx.Commit(ctx)
}

// sinceInstant resolves the since filter: a date at midnight in the family
// timezone, an ISO 8601 instant as given. A nil since means no lower bound.
func (in findInput) sinceInstant(loc *time.Location) (*time.Time, error) {
	if in.sinceDate != nil {
		d, err := time.ParseInLocation(time.DateOnly, *in.sinceDate, loc)
		if err != nil {
			return nil, badArg("since", fmt.Sprintf("since %q is not a date.", *in.sinceDate),
				"Pass since as YYYY-MM-DD, or as ISO 8601 with an offset, e.g. 2026-11-01T00:00:00Z.")
		}
		return &d, nil
	}
	return in.sinceAt, nil
}

// todayIn is the family's date as midnight UTC, the form billdue's attention
// rule takes (it compares against DATE columns).
func todayIn(loc *time.Location) time.Time {
	y, m, d := time.Now().In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// findSummary is notification_find's one-line summary. The needs_you clause
// appears only when a row on the page is handled (a bill that pays itself), so
// the line says something an agent could not have counted from "returned".
func findSummary(in findInput, res findResult) string {
	unread := plural(res.UnreadCount, "unread notification")
	switch {
	case res.Total == 0 && in.unreadOnly:
		return "No unread notifications."
	case res.Total == 0 && in.typ != "":
		return fmt.Sprintf("No %s notifications.", in.typ)
	case res.Total == 0:
		return "No notifications yet."
	case res.Returned == 0:
		return fmt.Sprintf("Found %s; this page is past the end. You have %s.", plural(res.Total, matchNoun(in)), unread)
	case res.Truncated || in.offset > 0:
		return fmt.Sprintf("Showing %d-%d of %s%s; you have %s.", in.offset+1, in.offset+res.Returned,
			plural(res.Total, matchNoun(in)), handledNote(res), unread)
	case res.Returned == 1:
		n := res.Notifications[0]
		return fmt.Sprintf("1 %s: %q (%s %s)%s; you have %s.", matchNoun(in), n.Title, n.Weekday, n.LocalDate, handledNote(res), unread)
	default:
		return fmt.Sprintf("Found %s%s; you have %s.", plural(res.Total, matchNoun(in)), handledNote(res), unread)
	}
}

// handledNote is " (N already handled)" when the page holds a reminder that is
// already handled, and "" otherwise. It is how the summary itself tells an
// agent that a bill reminder is noise: 4 notifications, 1 already handled.
func handledNote(res findResult) string {
	if n := res.Returned - res.NeedsYou; n > 0 {
		return fmt.Sprintf(" (%d already handled)", n)
	}
	return ""
}

func matchNoun(in findInput) string {
	if in.typ != "" {
		return in.typ + " notification"
	}
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
	if t, err := optionalString(args, "type"); err != nil {
		return in, err
	} else if t != nil {
		in.typ = *t
	}
	if s, err := optionalString(args, "since"); err != nil {
		return in, err
	} else if s != nil {
		if _, err := time.Parse(time.DateOnly, *s); err == nil {
			in.sinceDate = s
		} else if t, err := time.Parse(time.RFC3339, *s); err == nil {
			in.sinceAt = &t
		} else if looksNaive(*s) {
			return in, badArg("since", fmt.Sprintf("since %q has no timezone offset.", *s),
				"Add the offset, e.g. 2026-11-01T00:00:00Z, or pass just the date, e.g. 2026-11-01.")
		} else {
			return in, badArg("since", fmt.Sprintf("since %q is not a date or time.", *s),
				"Pass since as YYYY-MM-DD, or as ISO 8601 with an offset.")
		}
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

// looksNaive reports whether s looks like an ISO 8601 time with no offset, so
// the error can say what is missing rather than "not a date".
func looksNaive(s string) bool {
	if len(s) < 16 {
		return false
	}
	_, err := time.Parse("2006-01-02T15:04:05", s)
	return err == nil
}
