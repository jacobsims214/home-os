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

// The household role policy (KB decisions/2026-11-15-role-policy.md, #4689)
// applied to search_all (#4717). Search keeps its own copy of the policy for
// one release as internal/search/role.go did, but the copy is gone: every
// decision below asks internal/auth, the MCP's mirror of
// apps/api/internal/auth/roles.go, so a search area and the find tool for the
// same data cannot disagree.

// areaResource maps each search area to the entity kind (and so the resource)
// that guards it, so an area is searchable exactly when the role may see
// records of that kind. "event" is the calendar; "credential" is a secret;
// "document" is a file.
var areaResource = map[string]string{
	"person":      "person",
	"event":       "calendar_event",
	"bill":        "bill",
	"loan":        "loan",
	"maintenance": "maintenance_task",
	"contact":     "vendor",
	"note":        "note",
	"list":        "list",
	"recipe":      "recipe",
	"document":    "file",
	"asset":       "asset",
	"vehicle":     "vehicle",
	"pet":         "pet",
	"property":    "property",
	"room":        "room",
	"credential":  "secret",
}

// canSearch reports whether role may see results of the named area: it may
// see records of the area's kind (auth.CanSeeKind, which lets a housesitter
// find people by name and relationship). An unknown area or role is false, so
// a new area (or a new role) fails closed until it is mapped.
func canSearch(role, areaName string) bool {
	kind, ok := areaResource[areaName]
	if !ok {
		return false
	}
	return auth.CanSeeKind(role, kind)
}

// forRole adapts an area's SQL to the caller: records attached to a secret
// are dropped unless the role may see credentials, a housesitter searches
// only the part of a record they may see, and a linked vendor finds only
// their own maintenance tasks (#4799, the same rule as maintenance_find, the
// API and CalDAV).
func forRole(a area, who caller) area {
	if auth.Can(who.Role, auth.ResSecrets, auth.ActRead) {
		a.SecretWhere = ""
	}
	if who.Role == auth.RoleHousesitter && a.SitterDoc != "" {
		a.Doc = a.SitterDoc
	}
	if who.Role == auth.RoleVendor && who.VendorID != nil && a.Name == "maintenance" {
		// A uuid.UUID prints only hex digits and dashes, so it is safe as a
		// literal; searchArea's parameters are positional and shared.
		a.ScopeWhere = fmt.Sprintf("t.vendor_id = '%s'::uuid", who.VendorID.String())
	}
	return a
}

// caller is the search_all caller's current access: the role and, for a
// vendor linked to a vendors row (memberships.vendor_id, #4799), that
// vendor's id. A linked vendor's maintenance hits are only their own jobs;
// an unlinked vendor (nil) still finds every maintenance task.
type caller struct {
	Role     string
	VendorID *uuid.UUID
}

// callerRole resolves the caller's current role from memberships for the
// claims' user and household, so a demoted member loses access at once and a
// removed one gets nothing. The vendor link is read in the same query. A
// non-nil result is the isError to return.
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

// roleLabel names the role in a message.
func roleLabel(role string) string { return auth.RoleLabel(role) }
