package search

import (
	"context"
	"errors"
	"fmt"

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

// forRole adapts an area's SQL to the caller: records attached to a secret
// are dropped unless the role may see credentials, a housesitter searches
// only the part of a record they may see, and a linked vendor finds only
// their own maintenance tasks (#4799, the same rule as maintenance_find,
// the API and CalDAV).
func forRole(a area, who caller) area {
	if credentialRoles[who.Role] {
		a.SecretWhere = ""
	}
	if who.Role == "housesitter" && a.SitterDoc != "" {
		a.Doc = a.SitterDoc
	}
	if who.Role == "vendor" && who.VendorID != nil && a.Name == "maintenance" {
		// A uuid.UUID prints only hex digits and dashes, so it is safe as a
		// literal; searchArea's parameters are positional and shared.
		a.ScopeWhere = fmt.Sprintf("t.vendor_id = '%s'::uuid", who.VendorID.String())
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

// caller is the search_all caller's current access: the role and, for a
// vendor linked to a vendors row (memberships.vendor_id, #4799), that
// vendor's id. A linked vendor's maintenance hits are only their own jobs;
// an unlinked vendor (nil) still finds every maintenance task.
type caller struct {
	Role     string
	VendorID *uuid.UUID
}

// callerRole resolves the caller's current role the way credential.checkAccess
// does: the role is re-read from memberships for the claims' user and
// household, so a demoted member loses access at once and a removed one gets
// nothing. The vendor link is read in the same query. A non-nil result is
// the isError to return.
func callerRole(ctx context.Context, pool *pgxpool.Pool, c *auth.Claims) (caller, *mcp.CallToolResult) {
	hid, err := uuid.Parse(c.HouseholdID)
	if err != nil {
		return caller{}, notSignedIn()
	}
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.AgentTokenID != "" {
			// An agent token always resolves to a users.id.
			return caller{}, notSignedIn()
		}
		return caller{Role: c.Role}, nil
	}
	var who caller
	err = pool.QueryRow(ctx,
		`SELECT role::text, CASE WHEN role = 'vendor' THEN vendor_id END
		 FROM memberships WHERE household_id = $1 AND user_id = $2`, hid, uid).Scan(&who.Role, &who.VendorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return caller{}, notSignedIn()
	}
	if err != nil {
		return caller{}, respond.Failed("check your access", err)
	}
	return who, nil
}

func notSignedIn() *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: "You are not signed in to a household.",
		Fix:     "Tell the user to reconnect Home OS; retrying will not help.",
	})
}
