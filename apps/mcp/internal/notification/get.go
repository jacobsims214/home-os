package notification

// notification_get: one notification, in full. The catalog's find tools return
// enough to reason about; get is for the one an agent decides to act on, and
// the two things find cannot carry for a page: the whole body, and the exact
// call that opens the record it is about.
//
// It is the same row, the same visibility and the same bill attention as find,
// so a notification an agent cannot see in find cannot be fetched by id, and a
// bill_due about a bill that pays itself reads needs_you=false here too.

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// getAliases are parameter names small models reach for, accepted but not
// advertised. "id" is the catalog's find-tool name for the row's id.
var getAliases = map[string]string{"id": "notification_id", "notification": "notification_id"}

// getExample is a valid notification_get call for error examples.
var getExample = map[string]any{"notification_id": "5f1a2c3d-4b6e-4f80-9a1b-2c3d4e5f6a7b"}

// getSQL reads one notification the caller may see. visibleSQL applies the
// same role scoping as find (and as the UI), so a notification about a bill is
// invisible to a role that cannot see bills. visibleSQL's placeholders are $1
// (household), $2 (user) and $3 (role); the id is $4, so the two conditions
// cannot shadow each other.
var getSQL = `
	SELECT n.id, n.type, n.title, NULLIF(btrim(n.body), ''), n.user_id IS NULL, n.read_at, n.created_at,
	       NULLIF(btrim(n.entity_type), ''), n.entity_id
	FROM notifications n
	WHERE n.id = $4 AND ` + visibleSQL

// NewNotificationGetTool creates the notification_get MCP tool (catalog v2,
// class R).
func NewNotificationGetTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Get one notification in full: its whole body and the call that opens what it is about. needs_you says whether it still needs a person."),
		mcp.WithString("notification_id", mcp.Description("The id from notification_find"), mcp.Required()),
		respond.Schema[notificationData](),
	}
	tool := mcp.NewTool("notification_get", append(opts, annotations("Get a notification", true, false, true)...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}
		id, err := parseGetInput(req.GetArguments())
		if err != nil {
			return invalid(err, getExample), nil
		}
		res, err := getNotification(ctx, pool, claims.HouseholdID, callerUser(claims.UserID), id)
		if errors.Is(err, errNotFound) {
			return respond.Fail(respond.Error{
				Message: "No notification with that id is in your household, or you cannot see it.",
				Fix:     "Call notification_find and pass an id from its result; another person's notification is not yours to read.",
			}), nil
		}
		if err != nil {
			return respond.Failed("get notification", err), nil
		}
		return respond.OK(getSummary(res), res, getNext(res)...), nil
	}
	return "notification_get", tool, handler
}

// errNotFound is getNotification's "no such row, or not yours".
var errNotFound = errors.New("notification not found")

// getNotification reads one notification in a read-only snapshot, with its
// bill's attention state when it is about a bill.
func getNotification(ctx context.Context, pool *pgxpool.Pool, householdID string, userID *uuid.UUID, id uuid.UUID) (notificationData, error) {
	var res notificationData
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
	var r row
	err = tx.QueryRow(ctx, getSQL, householdID, userID, role, id).Scan(
		&r.id, &r.typ, &r.title, &r.body, &r.familyWide, &r.readAt, &r.createdAt, &r.entityType, &r.entityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, errNotFound
	}
	if err != nil {
		return res, err
	}
	page := []row{r}
	if err := attachBills(ctx, tx, householdID, page, role); err != nil {
		return res, err
	}
	res = page[0].data(loc, todayIn(loc))
	return res, tx.Commit(ctx)
}

// getSummary is notification_get's one-line summary. It says what needs a
// person when the notification is about a bill, because that is the whole
// question an agent asks after reading it.
func getSummary(res notificationData) string {
	switch {
	case !res.NeedsYou:
		return fmt.Sprintf("%q — %s does not need a person: %s.", res.Title, res.Type, attentionNote(res.Attention))
	case res.Attention != nil:
		return fmt.Sprintf("%q — needs a person (%s).", res.Title, attentionNote(res.Attention))
	case res.Read:
		return fmt.Sprintf("%q, already read.", res.Title)
	default:
		return fmt.Sprintf("%q, unread.", res.Title)
	}
}

// attentionNote is the words for a bill's attention state, for a summary.
func attentionNote(attention *string) string {
	if attention == nil {
		return "no bill state"
	}
	switch *attention {
	case "automatic":
		return "the bill pays itself (autopay)"
	case "needs_setup":
		return "nothing is set to pay it"
	case "due_soon":
		return "the bill is due soon"
	case "overdue":
		return "the bill is overdue"
	}
	return "state " + *attention
}

// getNext is notification_get's next-step hints.
func getNext(res notificationData) []string {
	var next []string
	if res.Open != nil {
		next = append(next, "Open what it is about with "+*res.Open+".")
	}
	if !res.Read {
		next = append(next, fmt.Sprintf("After telling the user, mark it read with notification_mark_read notification_ids=[%q].", res.ID))
	}
	return next
}

// parseGetInput validates notification_get's arguments. A malformed id is a
// bad argument (the agent can fix the call); a well-formed id that is not there
// is a not-found error.
func parseGetInput(args map[string]any) (uuid.UUID, error) {
	args = withAliases(args, getAliases)
	id, err := optionalString(args, "notification_id")
	if err != nil {
		return uuid.Nil, err
	}
	if id == nil {
		return uuid.Nil, badArg("notification_id", "notification_id is required.",
			"Pass the id from notification_find, e.g. "+getExample["notification_id"].(string)+".")
	}
	parsed, err := uuid.Parse(*id)
	if err != nil {
		return uuid.Nil, badArg("notification_id", fmt.Sprintf("%q is not a notification id.", *id),
			"Pass the id from notification_find, which is a UUID.")
	}
	return parsed, nil
}
