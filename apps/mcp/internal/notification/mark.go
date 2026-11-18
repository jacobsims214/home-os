package notification

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// maxMarkIDs caps notification_ids per call.
const maxMarkIDs = 100

// markAliases are parameter names small models reach for, accepted but not
// advertised.
var markAliases = map[string]string{"notification_id": "notification_ids", "id": "notification_ids", "ids": "notification_ids"}

// markExample is a valid notification_mark_read call for error examples.
var markExample = map[string]any{"notification_ids": []string{"3f2b8c1e-5d4a-4e7b-9c1f-2a6d8e0b7c45"}}

// retiredAllParams are the parameter names that used to ask for a blanket
// mark-read (all:true), plus the shorthands small models invent for it. They
// are refused by name with a fix (#4947): one boolean marked every unread
// notification read with no evidence the agent had seen any of them, which is
// how a war-game agent "tidying up" consumed a whole household's unread state.
var retiredAllParams = []string{"all", "mark_all", "all_unread"}

// markInput is the validated notification_mark_read arguments.
type markInput struct {
	ids []uuid.UUID
}

// markResult is notification_mark_read's data.
type markResult struct {
	Marked          int      `json:"marked" jsonschema_description:"How many notifications this call marked read."`
	AlreadyRead     int      `json:"already_read" jsonschema_description:"How many named notifications were already read (left unchanged)."`
	FamilyWide      int      `json:"family_wide" jsonschema_description:"How many marked ones are family-wide, so now read for everyone."`
	NotificationIDs []string `json:"notification_ids" jsonschema_description:"The ids this call marked read."`
	UnreadCount     int      `json:"unread_count" jsonschema_description:"How many of your notifications are still unread."`
}

// lockSQL locks the named visible notifications and reports which are read.
var lockSQL = `
	SELECT n.id, n.read_at IS NOT NULL
	FROM notifications n
	WHERE ` + visibleSQL + ` AND n.id = ANY($4)
	FOR UPDATE`

// markIDsSQL marks the named unread visible notifications read.
var markIDsSQL = `
	UPDATE notifications n SET read_at = NOW()
	WHERE ` + visibleSQL + ` AND n.id = ANY($4) AND n.read_at IS NULL
	RETURNING n.id, n.user_id IS NULL`

// NewNotificationMarkReadTool creates the notification_mark_read MCP tool
// (catalog v2, class U: a repeat changes nothing more, because read_at is
// only set on unread rows).
func NewNotificationMarkReadTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Mark the notifications you just told the user about read, by id. There is no mark-all."),
		mcp.WithArray("notification_ids",
			mcp.Description("Ids from notification_find to mark read, e.g. [\"3f2b8c1e-...\"]"),
			mcp.WithStringItems(),
			mcp.Required(),
		),
		respond.Schema[markResult](),
	}
	tool := mcp.NewTool("notification_mark_read", append(opts, annotations("Mark notifications read", false, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}
		in, err := parseMarkInput(req.GetArguments())
		if err != nil {
			return invalid(err, markExample), nil
		}
		res, missing, err := markRead(ctx, pool, claims.HouseholdID, callerUser(claims.UserID), in)
		if err != nil {
			return respond.Failed("mark notifications read", err), nil
		}
		if len(missing) > 0 {
			return notFound(missing), nil
		}
		return respond.OK(markSummary(res), res, markNext(res)...), nil
	}
	return "notification_mark_read", tool, handler
}

// markRead marks the notifications in one transaction. With ids, every id
// must be visible to the caller or nothing is marked and missing lists the
// unknown ones (another member's personal notification counts as unknown).
func markRead(ctx context.Context, pool *pgxpool.Pool, householdID string, userID *uuid.UUID, in markInput) (res markResult, missing []string, err error) {
	res.NotificationIDs = []string{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return res, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The role is re-read in this transaction: a notification the caller's
	// current role can't see is not found and is never marked.
	role, err := callerRole(ctx, tx, householdID, userID)
	if err != nil {
		return res, nil, err
	}
	found := make(map[uuid.UUID]bool, len(in.ids))
	lock, err := tx.Query(ctx, lockSQL, householdID, userID, role, in.ids)
	if err != nil {
		return res, nil, err
	}
	var (
		lockedID uuid.UUID
		wasRead  bool
	)
	if _, err = pgx.ForEachRow(lock, []any{&lockedID, &wasRead}, func() error {
		found[lockedID] = true
		if wasRead {
			res.AlreadyRead++
		}
		return nil
	}); err != nil {
		return res, nil, err
	}
	for _, id := range in.ids {
		if !found[id] {
			missing = append(missing, id.String())
		}
	}
	if len(missing) > 0 {
		return res, missing, nil
	}
	rows, err := tx.Query(ctx, markIDsSQL, householdID, userID, role, in.ids)
	if err != nil {
		return res, nil, err
	}
	var (
		id         uuid.UUID
		familyWide bool
	)
	if _, err = pgx.ForEachRow(rows, []any{&id, &familyWide}, func() error {
		res.NotificationIDs = append(res.NotificationIDs, id.String())
		if familyWide {
			res.FamilyWide++
		}
		return nil
	}); err != nil {
		return res, nil, err
	}
	res.Marked = len(res.NotificationIDs)
	if res.UnreadCount, _, err = counts(ctx, tx, householdID, userID, role); err != nil {
		return res, nil, err
	}
	return res, nil, tx.Commit(ctx)
}

// notFound is the isError result for ids the caller cannot see. Nothing
// was marked, so the call can be repeated with the right ids.
func notFound(missing []string) *mcp.CallToolResult {
	msg := fmt.Sprintf("No notification with id %s was found; nothing was marked.", missing[0])
	if len(missing) > 1 {
		msg = fmt.Sprintf("No notifications with ids %s were found; nothing was marked.", strings.Join(missing, ", "))
	}
	return respond.Fail(respond.Error{
		Message: msg,
		Fix:     "Call notification_find to get your notification ids, then call again with only those.",
		Field:   "notification_ids",
		Example: markExample,
	})
}

// markSummary is notification_mark_read's one-line summary.
func markSummary(res markResult) string {
	left := plural(res.UnreadCount, "unread notification") + " left"
	var s string
	switch {
	case res.Marked == 0:
		s = fmt.Sprintf("%s already read; nothing changed. %s.", alreadyNoun(res.AlreadyRead), capitalize(left))
	default:
		s = fmt.Sprintf("Marked %s read; %s.", plural(res.Marked, "notification"), left)
		if res.AlreadyRead > 0 {
			s += fmt.Sprintf(" %d already read.", res.AlreadyRead)
		}
		if res.FamilyWide > 0 {
			s += " Family-wide ones are now read for everyone in the family."
		}
	}
	return s
}

func alreadyNoun(n int) string {
	if n == 1 {
		return "That notification was"
	}
	return "Those notifications were"
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// markNext is notification_mark_read's next-step hints.
func markNext(res markResult) []string {
	if res.UnreadCount > 0 {
		return []string{"See what is still unread with notification_find unread_only=true."}
	}
	return []string{"All caught up; see what's coming with agenda_get."}
}

// parseMarkInput validates notification_mark_read arguments. notification_ids
// (an array, or a comma-separated string) is required: mark-read names what it
// changes. The retired blanket form is refused by name rather than ignored, so
// an agent working from an old habit gets a fix instead of a silent no-op.
func parseMarkInput(raw map[string]any) (markInput, error) {
	var in markInput
	args := withAliases(raw, markAliases)
	if err := refuseMarkAll(args); err != nil {
		return in, err
	}
	ids, err := idList(args["notification_ids"])
	if err != nil {
		return in, err
	}
	if len(ids) == 0 {
		return in, badArg("notification_ids", "Say which notifications to mark read.",
			"Pass the ids notification_find returned for the notifications you told the user about; nothing is marked in bulk.")
	}
	in.ids = ids
	return in, nil
}

// refuseMarkAll rejects a blanket mark-read (#4947). Read state is the family's
// record of what each adult has seen, so a bulk write is not a convenience an
// agent may take on its own initiative: it has to name each notification it is
// about to consume.
func refuseMarkAll(args map[string]any) error {
	for _, key := range retiredAllParams {
		v, present := args[key]
		if !present || v == nil {
			continue
		}
		if b, ok := v.(bool); ok && !b {
			continue
		}
		return badArg(key, "Marking every notification read is not allowed; there is no mark-all.",
			"Pass notification_ids=[...] with the ids notification_find returned for the notifications you told the user about. A notification about another member's record stays unread until they see it.")
	}
	return nil
}

// idList reads notification_ids: an array of UUID strings, or one string
// holding UUIDs separated by commas or spaces (small models send both).
// Duplicates are dropped.
func idList(v any) ([]uuid.UUID, error) {
	var raw []string
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		raw = strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
	case []string:
		raw = x
	case []any:
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, badArg("notification_ids", "notification_ids must be a list of id strings.",
					"Pass notification_ids as [\"<id>\", ...] with ids from notification_find.")
			}
			raw = append(raw, s)
		}
	default:
		return nil, badArg("notification_ids", "notification_ids must be a list of id strings.",
			"Pass notification_ids as [\"<id>\", ...] with ids from notification_find.")
	}
	seen := make(map[uuid.UUID]bool, len(raw))
	var ids []uuid.UUID
	for _, s := range raw {
		s = strings.Trim(strings.TrimSpace(s), `"'[]`)
		if s == "" {
			continue
		}
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, badArg("notification_ids", fmt.Sprintf("%q is not a notification id.", s),
				"Pass ids (UUIDs) exactly as notification_find returned them.")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) > maxMarkIDs {
		return nil, badArg("notification_ids", fmt.Sprintf("At most %d ids per call; got %d.", maxMarkIDs, len(ids)),
			"Split the ids over several calls, or narrow with notification_find unread_only=true.")
	}
	return ids, nil
}
