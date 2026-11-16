package household

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/sqlutil"
)

// toolHandler is the handler signature server.RegisterTool takes.
type toolHandler = func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)

const (
	defaultFindLimit = 25
	maxFindLimit     = 100
)

// personRow is one person in person_find and family_overview.people.
//
// ID is the person id (people.id, migration 041): the person_id every other
// catalog tool takes. It stays the same when #4464 widens person_find to
// people without a login. UserID is the login behind the person.
type personRow struct {
	ID           string  `json:"id" jsonschema_description:"person_id to pass to other tools"`
	Name         string  `json:"name"`
	Relationship *string `json:"relationship" jsonschema_description:"How they belong to the family, e.g. parent or child; null if not set"`
	Age          *int    `json:"age" jsonschema_description:"Age in years in the family timezone; null without a birthdate"`
	HasAccount   bool    `json:"has_account" jsonschema_description:"true when they log in to Home OS"`
	UserID       *string `json:"user_id" jsonschema_description:"Their login's user id; null without an account"`
	Email        *string `json:"email,omitempty"`
	Role         *string `json:"role,omitempty" jsonschema_description:"Membership role, e.g. owner, family_manager, family_member"`
}

// peopleSelect lists the people of household $1 who are members (log in).
// People without a login (kids, grandparents) arrive with #4464. Age is
// computed from the birthdate in the family timezone.
const peopleSelect = `
SELECT p.id::text, p.display_name, p.relationship,
       CASE WHEN p.birthdate IS NULL THEN NULL
            ELSE date_part('year', age((NOW() AT TIME ZONE h.timezone)::date, p.birthdate))::int END,
       u.id::text, u.email, m.role::text,
       COUNT(*) OVER ()
  FROM people p
  JOIN households h ON h.id = p.household_id
  JOIN memberships m ON m.household_id = p.household_id AND m.user_id = p.user_id
  JOIN users u ON u.id = p.user_id
 WHERE p.household_id = $1`

// scanPeople reads rows from a peopleSelect query and returns them with the
// total match count (from the window column).
func scanPeople(rows pgx.Rows) ([]personRow, int, error) {
	defer rows.Close()
	out := []personRow{}
	total := 0
	for rows.Next() {
		var p personRow
		var userID, email, role string
		if err := rows.Scan(&p.ID, &p.Name, &p.Relationship, &p.Age, &userID, &email, &role, &total); err != nil {
			return nil, 0, err
		}
		p.HasAccount = true
		p.UserID, p.Email, p.Role = &userID, &email, &role
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// NewPersonFindTool creates person_find (catalog v2; replaces list_members).
// For now it lists the family members who log in; #4464 adds people without
// a login from the same people table, so ids do not change.
func NewPersonFindTool(pool *pgxpool.Pool) (string, mcp.Tool, toolHandler) {
	tool := mcp.NewTool("person_find",
		mcp.WithDescription("List or search family members, including kids and others without a login. Returns name, relationship, age and whether they have an account."),
		mcp.WithString("id",
			mcp.Description("A person_id (or their user id) to get exactly that person"),
		),
		mcp.WithString("query",
			mcp.Description("Text in the name or email, e.g. \"Nora\""),
		),
		mcp.WithString("relationship",
			mcp.Description("Only this relationship, e.g. parent or child"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Max people to return (1-100, default 25)"),
			mcp.Min(1), mcp.Max(maxFindLimit),
		),
		mcp.WithString("cursor",
			mcp.Description("Paging cursor from the previous person_find result"),
		),
		mcp.WithTitleAnnotation("Find people"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		respond.ListSchema[personRow](),
	)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return notSignedIn(), nil
		}
		// Household role policy (#4689, same as the API's people routes): a
		// vendor sees no people; a housesitter sees names and relationships
		// but not ages (birthdates), emails or roles.
		if claims.Role == "vendor" {
			return respond.Fail(respond.Error{
				Message: "Members with the vendor role can't see the family's people.",
				Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
			}), nil
		}
		basicOnly := claims.Role == "housesitter"
		args := req.GetArguments()
		id := strings.TrimSpace(str(args, "id"))
		query := strings.TrimSpace(str(args, "query"))
		relationship := strings.TrimSpace(str(args, "relationship"))

		limit, given, ok := intArg(args, "limit")
		if !ok || (given && (limit < 1 || limit > maxFindLimit)) {
			return respond.Invalid("limit", "limit must be a whole number from 1 to 100.",
				"Pass limit as a number, or leave it out for 25.", map[string]any{"limit": 25}), nil
		}
		if !given {
			limit = defaultFindLimit
		}
		offset := 0
		if c := strings.TrimSpace(str(args, "cursor")); c != "" {
			if offset, ok = respond.DecodeCursor(c); !ok {
				return respond.Invalid("cursor", "cursor is not one person_find returned.",
					"Pass the cursor exactly as the previous person_find result gave it, or leave it out to start over.",
					map[string]any{}), nil
			}
		}
		if id != "" {
			if _, err := uuid.Parse(id); err != nil {
				return respond.Invalid("id", fmt.Sprintf("id %q is not a person id.", id),
					"Leave id out and pass query with the person's name, e.g. query=\"Nora\".",
					map[string]any{"query": "Nora"}), nil
			}
		}

		sql := peopleSelect
		params := []any{claims.HouseholdID}
		add := func(cond string, v any) {
			params = append(params, v)
			sql += fmt.Sprintf(" AND "+cond, len(params))
		}
		if id != "" {
			add("(p.id::text = $%[1]d OR p.user_id::text = $%[1]d)", strings.ToLower(id))
		}
		if query != "" {
			// Escaped so "%" and "_" match literally, not as wildcards.
			if basicOnly { // no matching on emails it may not see
				add(`p.display_name ILIKE $%d ESCAPE '\'`, sqlutil.ContainsPattern(query))
			} else {
				add(`(p.display_name ILIKE $%[1]d ESCAPE '\' OR u.email ILIKE $%[1]d ESCAPE '\')`, sqlutil.ContainsPattern(query))
			}
		}
		if relationship != "" {
			add("lower(p.relationship) = lower($%d)", relationship)
		}
		params = append(params, limit, offset)
		sql += fmt.Sprintf(" ORDER BY m.created_at, p.id LIMIT $%d OFFSET $%d", len(params)-1, len(params))

		rows, err := pool.Query(ctx, sql, params...)
		if err != nil {
			return respond.Failed("find people", err), nil
		}
		items, total, err := scanPeople(rows)
		if err != nil {
			return respond.Failed("find people", err), nil
		}
		if basicOnly {
			for i := range items {
				items[i].Age, items[i].UserID, items[i].Email, items[i].Role = nil, nil, nil, nil
			}
		}

		if id != "" {
			if len(items) == 0 {
				return respond.NotFound("person", id, "Call person_find without id to see everyone's person_id."), nil
			}
			p := items[0]
			return respond.List(fmt.Sprintf("Found %s.", p.Name), items, respond.Page{},
				fmt.Sprintf("Use person_id=%s with other tools, e.g. note_create about_type=person about_id=%s.", p.ID, p.ID)), nil
		}
		if len(items) == 0 {
			if offset > 0 {
				return respond.List("No more people past this cursor.", items, respond.Page{},
					"Leave cursor out to start from the first person."), nil
			}
			return respond.List("No family members matched.", items, respond.Page{},
				"Call person_find with no filters to list everyone."), nil
		}

		names := make([]string, len(items))
		for i, p := range items {
			names[i] = p.Name
		}
		summary := fmt.Sprintf("%d %s: %s.", total, plural(total, "person", "people"), strings.Join(names, ", "))
		next := []string{"Pass id=<person id> for one person; pass person_id to other tools."}
		page := respond.Page{}
		if remaining := total - offset - len(items); remaining > 0 {
			page = respond.Page{
				NextCursor: respond.EncodeCursor(offset + len(items)),
				Total:      len(items) + remaining,
				Narrow:     fmt.Sprintf("%d more. Pass next_cursor as cursor with the same filters for the next page, or add query to narrow.", remaining),
			}
			summary = fmt.Sprintf("%d of %d people: %s.", len(items), total, strings.Join(names, ", "))
		}
		return respond.List(summary, items, page, next...), nil
	}
	return tool.Name, tool, handler
}

// notSignedIn is the isError result for a call without household claims.
func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}

func str(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// intArg reads a numeric argument sent as a JSON number or a numeric string.
// It returns the value, whether it was given, and whether it was a valid
// whole number.
func intArg(args map[string]any, key string) (int, bool, bool) {
	switch v := args[key].(type) {
	case nil:
		return 0, false, true
	case float64:
		return int(v), true, v == float64(int(v))
	case int:
		return v, true, true
	case string:
		if strings.TrimSpace(v) == "" {
			return 0, false, true
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, true, err == nil
	default:
		return 0, true, false
	}
}

// familyClock returns today's date (YYYY-MM-DD) and weekday (Mon) in tz,
// falling back to UTC when tz does not load.
func familyClock(now time.Time, tz string) (string, string) {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	t := now.In(loc)
	return t.Format("2006-01-02"), t.Format("Mon")
}
