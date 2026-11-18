package credential

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// NewCredentialRevealTool creates credential_reveal (catalog v2, class R):
// read a credential the family shared with agents.
//
// Order of checks, each a refusal with no value:
//  1. a current member of the household, not viewer/housesitter/vendor;
//  2. an agent token needs the read scope (Dex sessions have full rights);
//  3. the credential exists in this household (another household's id is
//     plain "not found");
//  4. it is shared with agents; otherwise the answer says where the user can
//     see it ("Home OS → Credentials → Netflix");
//  5. the server has its agent key, and the blob opens for this household
//     and credential id.
//
// Then one activity_log row (action 'read', entity 'secret', actor user, the
// client: agent token id, or the OAuth client id (azp) for an interactive
// Dex session; the credential's name; never the value) is written, and only
// if that insert succeeds is the value returned. The feed reads it as
// `Dana revealed secret "Netflix" via agent "Claude"`.
func NewCredentialRevealTool(pool *pgxpool.Pool, key *AgentKey) (string, mcp.Tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Read a credential the family shared with agents (shared_with_agents true in credential_find). Every read is logged in activity."),
		mcp.WithString("credential_id",
			mcp.Description("The credential's id (UUID) from credential_find, or its exact name"),
			mcp.Required(),
		),
		respond.Schema[Credential](),
	}
	tool := mcp.NewTool("credential_reveal", append(opts, readOnly("Reveal shared credential")...)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		a, deny := checkAccess(ctx, pool, claims)
		if deny != nil {
			return deny, nil
		}
		if !hasReadScope(claims) {
			return respond.Fail(respond.Error{
				Message: "This agent token has no read scope, so it can't read credentials.",
				Fix:     "Tell the user to create an agent token with the read scope; retrying will not help.",
			}), nil
		}

		raw := str(req.GetArguments(), "credential_id")
		example := map[string]any{"credential_id": "3f6c1b9e-6a51-4c1e-9a43-5d2a7c8b9e10"}
		if raw == "" {
			return respond.Invalid("credential_id", "credential_id is required.",
				"Pass the credential's id from credential_find.", example), nil
		}

		var (
			sid, entityID uuid.UUID
			name, typ     string
			kind          string
			blob          []byte
		)
		const cols = `SELECT id, name, secret_type, entity_type::text, entity_id, agent_ciphertext FROM secrets`
		var err error
		if id, perr := uuid.Parse(raw); perr == nil {
			err = pool.QueryRow(ctx, cols+` WHERE id = $1 AND household_id = $2`, id, a.householdID).
				Scan(&sid, &name, &typ, &kind, &entityID, &blob)
		} else {
			rows, qerr := pool.Query(ctx, cols+` WHERE household_id = $1 AND lower(name) = lower($2) LIMIT 2`, a.householdID, raw)
			if qerr != nil {
				return respond.Failed("read the credential", qerr), nil
			}
			n := 0
			for rows.Next() {
				n++
				err = rows.Scan(&sid, &name, &typ, &kind, &entityID, &blob)
			}
			rows.Close()
			if rerr := rows.Err(); rerr != nil {
				return respond.Failed("read the credential", rerr), nil
			}
			if n > 1 {
				return respond.Invalid("credential_id", fmt.Sprintf("More than one credential is named %q.", raw),
					"Call credential_find with query and pass the credential_id you mean.", example), nil
			}
			if n == 0 && err == nil {
				err = pgx.ErrNoRows
			}
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("credential", raw, "Call credential_find to find the credential_id."), nil
		}
		if err != nil {
			return respond.Failed("read the credential", err), nil
		}

		c := Credential{CredentialID: sid.String(), Name: name, Type: typ, SharedWithAgents: blob != nil, Where: where(name)}
		c.AboutType, c.AboutID = aboutFromDB(kind, entityID, a.householdID)

		if blob == nil {
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("%q is not shared with agents, so its value stays private.", name),
				Fix: fmt.Sprintf("Tell the user to open %s to see it. An owner or family manager can share it with agents there.",
					c.Where),
			}), nil
		}
		if key == nil {
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("%q is shared with agents, but this server can't read shared credentials right now.", name),
				Fix:     fmt.Sprintf("Tell the user to open %s to see it.", c.Where),
			}), nil
		}

		plain, err := key.Open(a.householdID, sid, blob)
		if err != nil {
			// Wrong or rotated master key, or a tampered row. Never log bytes.
			slog.Warn("credential_reveal: shared copy did not open",
				"household_id", a.householdID, "secret_id", sid)
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("The shared copy of %q could not be read.", name),
				Fix:     fmt.Sprintf("Tell the user to open %s and share it with agents again.", c.Where),
			}), nil
		}
		var value map[string]any
		jerr := json.Unmarshal(plain, &value)
		clear(plain)
		if jerr != nil {
			slog.Warn("credential_reveal: shared copy is not a JSON object",
				"household_id", a.householdID, "secret_id", sid)
			return respond.Failed("read the credential", errors.New("stored value is malformed")), nil
		}

		if err := audit(ctx, pool, a, sid, name, kind, entityID); err != nil {
			// No audit row, no value.
			slog.Error("credential_reveal: audit insert failed; value withheld",
				"household_id", a.householdID, "secret_id", sid, "error", err)
			return respond.Failed("log this credential read", errors.New("the activity log is unavailable")), nil
		}

		c.Value = value
		return respond.OK(
			fmt.Sprintf("Read %q, which the family shared with agents. This read is logged in the family's activity.", name),
			c,
			"Use the value only for what the user asked. Don't save it in notes, memory or messages.",
		), nil
	}
	return "credential_reveal", tool, handler
}

var spaces = regexp.MustCompile(`\s+`)

// audit writes the activity_log row for one agent read: same label rules as
// 050's log_activity (the name, whitespace collapsed, 80 characters) and the
// same parent rule (a credential attached to a record sits inside it).
func audit(ctx context.Context, pool *pgxpool.Pool, a *access, sid uuid.UUID, name, kind string, entityID uuid.UUID) error {
	label := strings.TrimSpace(spaces.ReplaceAllString(name, " "))
	if r := []rune(label); len(r) > 80 {
		label = string(r[:80])
	}
	summary := "revealed secret"
	if label != "" {
		summary += ` "` + label + `"`
	}
	var parentType *string
	var parentID *uuid.UUID
	if kind != "household" {
		parentType, parentID = &kind, &entityID
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO activity_log (household_id, actor_user_id, actor_agent_token_id, actor_client_id,
		                           entity_type, entity_id, parent_type, parent_id, action, summary)
		 VALUES ($1, $2, $3, $4, 'secret', $5, $6, $7, 'read', $8)`,
		a.householdID, a.userID, a.agentToken, a.clientID, sid, parentType, parentID, summary)
	return err
}
