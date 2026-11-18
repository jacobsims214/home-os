// Package credential holds the household-credential MCP tools (catalog v2:
// credential_find, credential_reveal; #4446).
//
// Policy (KB decisions/2026-11-15-agent-credential-access.md, #4439):
//
//   - Every credential is zero-knowledge by default: encrypted in the browser
//     with the family's master password. No tool can read it. credential_find
//     returns names, types and where to see the value in the app, never a
//     value (encrypted_data is never selected).
//   - An owner or family_manager may share ONE credential with agents. The API
//     then stores a server-sealed copy in secrets.agent_ciphertext (see
//     agentkey.go). credential_reveal opens only that copy, only for a member
//     of the household (Dex session, or an agent token with the read scope),
//     and writes an activity_log row (action 'read': who, which agent token,
//     which credential; never the value) before it returns anything.
//   - viewer, housesitter and vendor members get no credential tools at all.
//   - Values are never logged, indexed, put in notifications or echoed into
//     summaries or next hints.
package credential

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/respond"
)

// deniedRoles may not see household credentials, not even their names
// (role policy, #4689: the API hides credentials from viewers too).
var deniedRoles = map[string]bool{"viewer": true, "housesitter": true, "vendor": true}

// Credential is one credential as the tools return it. Value is set only by
// credential_reveal, for a credential shared with agents.
type Credential struct {
	CredentialID     string         `json:"credential_id"`
	Name             string         `json:"name"`
	Type             string         `json:"type" jsonschema_description:"login, note, api_key or card"`
	AboutType        string         `json:"about_type" jsonschema_description:"What it belongs to: family, property, vehicle, pet, asset, contact, ..."`
	AboutID          *string        `json:"about_id,omitempty"`
	SharedWithAgents bool           `json:"shared_with_agents" jsonschema_description:"true when the family shared it with agents; then credential_reveal can read it"`
	Where            string         `json:"where" jsonschema_description:"Where the user sees the value in the Home OS app"`
	Value            map[string]any `json:"value,omitempty" jsonschema_description:"The credential's fields (e.g. username, password). Only from credential_reveal."`
}

// where is the app path to a credential, the answer to "where is it?" for
// every credential the agent may not read.
func where(name string) string {
	return "Home OS → Credentials → " + name
}

// aboutToDB maps catalog about_type names to the DB entity_kind where they
// differ (same table as the note package).
var aboutToDB = map[string]string{
	"family":      "household",
	"contact":     "vendor",
	"maintenance": "maintenance_task",
}

var aboutTypes = []string{"family", "person", "property", "room", "asset", "vehicle", "pet", "contact", "bill", "loan", "maintenance"}

func aboutFromDB(kind string, entityID, householdID uuid.UUID) (string, *string) {
	for about, k := range aboutToDB {
		if k == kind {
			kind = about
			break
		}
	}
	if kind == "family" && entityID == householdID {
		return kind, nil
	}
	s := entityID.String()
	return kind, &s
}

// access is who is calling, checked against the household on every call.
type access struct {
	householdID uuid.UUID
	userID      *uuid.UUID
	agentToken  *uuid.UUID
	// clientID is the OAuth client (azp) of an interactive Dex session;
	// nil for agent tokens, which agentToken identifies (#4491).
	clientID *string
	role     string
}

// checkAccess resolves the caller's current membership. Claims are taken
// from the auth middleware, but the role is re-read from memberships so a
// demoted or removed member loses access at once. A non-nil result is the
// isError to return.
func checkAccess(ctx context.Context, pool *pgxpool.Pool, c *auth.Claims) (*access, *mcp.CallToolResult) {
	if c == nil {
		return nil, notSignedIn()
	}
	hid, err := uuid.Parse(c.HouseholdID)
	if err != nil {
		return nil, notSignedIn()
	}
	a := &access{householdID: hid, role: c.Role}
	if uid, err := uuid.Parse(c.UserID); err == nil {
		a.userID = &uid
		var role string
		err := pool.QueryRow(ctx,
			`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`, hid, uid).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notSignedIn()
		}
		if err != nil {
			return nil, respond.Failed("check your access", err)
		}
		a.role = role
	} else if c.AgentTokenID != "" {
		// An agent token always resolves to a users.id; anything else is
		// not a member identity.
		return nil, notSignedIn()
	}
	if c.AgentTokenID != "" {
		tid, err := uuid.Parse(c.AgentTokenID)
		if err != nil {
			return nil, notSignedIn()
		}
		a.agentToken = &tid
	} else if c.ClientID != "" {
		cid := c.ClientID
		if len(cid) > 200 { // activity_log.actor_client_id is VARCHAR(200)
			cid = cid[:200]
		}
		a.clientID = &cid
	}
	if deniedRoles[a.role] {
		return nil, respond.Fail(respond.Error{
			Message: fmt.Sprintf("Members with the %s role can't see the family's credentials.", a.role),
			Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
		})
	}
	return a, nil
}

// hasReadScope: Dex sessions act with the member's full rights; an agent
// token needs the read scope to reveal a value.
func hasReadScope(c *auth.Claims) bool {
	if c.AgentTokenID == "" {
		return true
	}
	for _, s := range c.Scopes {
		if s == auth.ScopeRead {
			return true
		}
	}
	return false
}

func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}

func str(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

// hints sets all four catalog annotations for a class R tool.
func readOnly(title string) []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithTitleAnnotation(title),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	}
}
