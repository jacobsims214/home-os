package credential

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

const (
	defaultFindLimit = 25
	maxFindLimit     = 100
)

// NewCredentialFindTool creates credential_find (catalog v2, class R): find
// household logins by name. It returns name, type, what it's for, whether it
// is shared with agents and where to see it; never a value. Neither
// encrypted_data nor agent_ciphertext is selected.
func NewCredentialFindTool(pool *pgxpool.Pool) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Find household logins (Netflix, Wi-Fi) by name. Returns name, type, what it's for and where to see it; never the secret value."),
		mcp.WithString("id", mcp.Description("A credential_id (UUID) to get exactly one credential")),
		mcp.WithString("query", mcp.Description("Words in the name or type, e.g. \"netflix\"")),
		mcp.WithString("about_type", mcp.Description("Only credentials for this kind, e.g. property"), mcp.Enum(aboutTypes...)),
		mcp.WithString("about_id", mcp.Description("Only credentials for this record (UUID); needs about_type")),
		mcp.WithNumber("limit", mcp.Description("Max results, 1-100 (default 25)")),
		mcp.WithString("cursor", mcp.Description("next_cursor from the previous page")),
		respond.ListSchema[Credential](),
	}
	tool := mcp.NewTool("credential_find", append(opts, readOnly("Find credentials")...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		a, deny := checkAccess(ctx, pool, server.ClaimsFromContext(ctx))
		if deny != nil {
			return deny, nil
		}
		args := req.GetArguments()
		example := map[string]any{"query": "netflix"}

		conds := []string{"household_id = $1"}
		params := []any{a.householdID}
		add := func(cond string, v any) {
			params = append(params, v)
			conds = append(conds, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(params))))
		}

		if raw := str(args, "id"); raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				return respond.Invalid("id", fmt.Sprintf("id %q is not a UUID.", raw),
					"Pass a credential_id from credential_find, or use query with the name.", example), nil
			}
			add("id = ?", id)
		}
		if q := str(args, "query"); q != "" {
			esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
			add("(name ILIKE '%' || ? || '%' OR secret_type ILIKE '%' || ? || '%')", esc)
		}
		aboutType := strings.ToLower(str(args, "about_type"))
		aboutRaw := str(args, "about_id")
		if aboutRaw != "" && aboutType == "" {
			return respond.Invalid("about_type", "about_id was given without about_type.",
				"Pass about_type too, e.g. property.", map[string]any{"about_type": "property", "about_id": "<uuid>"}), nil
		}
		if aboutType != "" {
			valid := false
			for _, t := range aboutTypes {
				valid = valid || t == aboutType
			}
			if !valid {
				return respond.Invalid("about_type", fmt.Sprintf("about_type %q is not one of the known kinds.", aboutType),
					"Use one of: "+strings.Join(aboutTypes, ", ")+".", example), nil
			}
			kind := aboutType
			if k, ok := aboutToDB[aboutType]; ok {
				kind = k
			}
			add("entity_type::text = ?", kind)
			if aboutType == "family" {
				add("entity_id = ?", a.householdID)
			} else if aboutRaw != "" {
				id, err := uuid.Parse(aboutRaw)
				if err != nil {
					return respond.Invalid("about_id", fmt.Sprintf("about_id %q is not a UUID.", aboutRaw),
						"Pass the record's id (UUID).", example), nil
				}
				add("entity_id = ?", id)
			}
		}

		limit := defaultFindLimit
		if v, ok := args["limit"]; ok && v != nil {
			n, ok := toInt(v)
			if !ok || n < 1 || n > maxFindLimit {
				return respond.Invalid("limit", "limit must be a whole number from 1 to 100.",
					"Pass limit between 1 and 100, or leave it out.", map[string]any{"limit": 25}), nil
			}
			limit = n
		}
		offset := 0
		if c := str(args, "cursor"); c != "" {
			o, ok := respond.DecodeCursor(c)
			if !ok {
				return respond.Invalid("cursor", "cursor is not a next_cursor from credential_find.",
					"Pass next_cursor exactly as returned, with the same filters.", example), nil
			}
			offset = o
		}

		cond := strings.Join(conds, " AND ")
		var total int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM secrets WHERE `+cond, params...).Scan(&total); err != nil {
			return respond.Failed("find credentials", err), nil
		}
		rows, err := pool.Query(ctx,
			`SELECT id, name, secret_type, entity_type::text, entity_id, agent_ciphertext IS NOT NULL
			   FROM secrets WHERE `+cond+`
			  ORDER BY lower(name), id
			  LIMIT `+strconv.Itoa(limit)+` OFFSET `+strconv.Itoa(offset), params...)
		if err != nil {
			return respond.Failed("find credentials", err), nil
		}
		defer rows.Close()
		items := []Credential{}
		shared := ""
		for rows.Next() {
			var (
				id, entityID uuid.UUID
				c            Credential
				kind         string
			)
			if err := rows.Scan(&id, &c.Name, &c.Type, &kind, &entityID, &c.SharedWithAgents); err != nil {
				return respond.Failed("find credentials", err), nil
			}
			c.CredentialID = id.String()
			c.AboutType, c.AboutID = aboutFromDB(kind, entityID, a.householdID)
			c.Where = where(c.Name)
			if c.SharedWithAgents && shared == "" {
				shared = c.CredentialID
			}
			items = append(items, c)
		}
		if err := rows.Err(); err != nil {
			return respond.Failed("find credentials", err), nil
		}

		page := respond.Page{Total: total, NextCursor: respond.CursorAfter(offset+len(items), total),
			Narrow: "Pass query with part of the name to narrow the results."}
		summary := fmt.Sprintf("Found %d of %d credentials. Values are not shown here.", len(items), total)
		if total == 0 {
			summary = "No credentials matched."
		}
		var next []string
		if shared != "" {
			next = append(next, fmt.Sprintf("Credentials with shared_with_agents true can be read with credential_reveal credential_id=%s (every read is logged).", shared))
		}
		if len(items) > 0 {
			next = append(next, "For the others, tell the user where to see the value (the where field); agents can't read them.")
		}
		return respond.List(summary, items, page, next...), nil
	}
	return "credential_find", tool, handler
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n != float64(int(n)) {
			return 0, false
		}
		return int(n), true
	case int:
		return n, true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		return i, err == nil
	}
	return 0, false
}
