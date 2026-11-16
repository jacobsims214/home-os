package search

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/respond"
)

// The household role policy (KB decisions/2026-11-15-role-policy.md, #4689;
// the API's table is apps/api/internal/auth/roles.go) applied to search_all.
// The MCP has no shared per-resource read policy yet, so search keeps its own
// copy here; keep it in step with the policy and with credential.deniedRoles.

// credentialRoles may see household credentials, names included. Everyone
// else (viewer, housesitter, vendor, any unknown role) gets no credential
// hits and no notes or documents attached to a secret.
var credentialRoles = map[string]bool{"owner": true, "family_manager": true, "family_member": true}

// fullRoles may search every area other than credentials.
var fullRoles = map[string]bool{"owner": true, "family_manager": true, "family_member": true, "viewer": true}

// limitedAreas is what the partial roles may read, by search area.
//   - housesitter: pets, properties/rooms/assets, contacts (vendors),
//     calendar, lists and meals (recipes); people by name and relationship
//     only (area.SitterDoc). No money, vehicles, documents, notes or
//     credentials.
//   - vendor: properties/rooms/assets and maintenance tasks, nothing else.
var limitedAreas = map[string]map[string]bool{
	"housesitter": {"person": true, "event": true, "contact": true, "list": true, "recipe": true,
		"asset": true, "pet": true, "property": true, "room": true},
	"vendor": {"maintenance": true, "asset": true, "property": true, "room": true},
}

// canSearch reports whether role may see results of the named area. Unknown
// roles see nothing, so a new role fails closed.
func canSearch(role, areaName string) bool {
	if areaName == "credential" {
		return credentialRoles[role]
	}
	if fullRoles[role] {
		return true
	}
	return limitedAreas[role][areaName]
}

// forRole adapts an area's SQL to the role: records attached to a secret are
// dropped unless the role may see credentials, and a housesitter searches
// only the part of a record they may see.
func forRole(a area, role string) area {
	if credentialRoles[role] {
		a.SecretWhere = ""
	}
	if role == "housesitter" && a.SitterDoc != "" {
		a.Doc = a.SitterDoc
	}
	return a
}

// roleLabel names the role in messages.
func roleLabel(role string) string {
	if role == "" {
		return "current"
	}
	return role
}

// callerRole resolves the caller's current role the way credential.checkAccess
// does: the role is re-read from memberships for the claims' user and
// household, so a demoted member loses access at once and a removed one gets
// nothing. A non-nil result is the isError to return.
func callerRole(ctx context.Context, pool *pgxpool.Pool, c *auth.Claims) (string, *mcp.CallToolResult) {
	hid, err := uuid.Parse(c.HouseholdID)
	if err != nil {
		return "", notSignedIn()
	}
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.AgentTokenID != "" {
			// An agent token always resolves to a users.id.
			return "", notSignedIn()
		}
		return c.Role, nil
	}
	var role string
	err = pool.QueryRow(ctx,
		`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`, hid, uid).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", notSignedIn()
	}
	if err != nil {
		return "", respond.Failed("check your access", err)
	}
	return role, nil
}

func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}
