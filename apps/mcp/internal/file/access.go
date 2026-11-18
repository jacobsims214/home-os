package file

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// The household role policy for documents (KB decisions/2026-11-15-role-policy.md,
// #4689; the API's table is apps/api/internal/auth/roles.go, files: read =
// readers, write = family). The API file handler also hides files attached
// to a credential from roles without secrets read (#4478), and so does this
// package:
//
//   - owner, family_manager, family_member: read and write every document.
//   - viewer: reads documents, except those attached to a credential (or
//     linked from one); never writes.
//   - housesitter, vendor and any unknown role: no documents at all, so a new
//     role fails closed.
//
// The role is re-read from memberships on every call (callerOf), so a
// demoted member loses access at once and a removed one gets nothing.

// member is who is calling, with the role re-read from memberships.
type member struct {
	householdID uuid.UUID
	role        string
}

// familyRoles may read and write every document, credential-attached ones
// included.
var familyRoles = map[string]bool{"owner": true, "family_manager": true, "family_member": true}

// canRead reports whether role may see documents at all.
func canRead(role string) bool { return familyRoles[role] || role == "viewer" }

// seesSecretFiles reports whether role may see documents attached to a
// credential (the roles with secrets read).
func (c member) seesSecretFiles() bool { return familyRoles[c.role] }

// canWrite reports whether role may upload, change and delete documents.
func canWrite(role string) bool { return familyRoles[role] }

// callerOf resolves the caller like the vehicle, pet and people tools do. A
// non-nil result is the isError to return.
func callerOf(ctx context.Context, q querier) (member, *mcp.CallToolResult) {
	c := server.ClaimsFromContext(ctx)
	if c == nil {
		return member{}, notSignedIn()
	}
	hid, err := uuid.Parse(c.HouseholdID)
	if err != nil {
		return member{}, notSignedIn()
	}
	out := member{householdID: hid, role: c.Role}
	uid, err := uuid.Parse(c.UserID)
	if err != nil {
		if c.AgentTokenID != "" { // an agent token always resolves to a users.id
			return member{}, notSignedIn()
		}
		// Local-dev claims without a user: keep the claims' role.
		var ok bool
		err = q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM households WHERE id = $1)`, hid).Scan(&ok)
		if err == nil && !ok {
			return member{}, notSignedIn()
		}
	} else {
		err = q.QueryRow(ctx, `SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
			hid, uid).Scan(&out.role)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return member{}, notSignedIn()
	}
	if err != nil {
		return member{}, respond.Failed("check your access", err)
	}
	return out, nil
}

// readerOf is callerOf plus the read check every document tool makes.
func readerOf(ctx context.Context, q querier, tool string) (member, *mcp.CallToolResult) {
	c, bad := callerOf(ctx, q)
	if bad != nil {
		return c, bad
	}
	if !canRead(c.role) {
		return c, respond.Fail(respond.Error{
			Message: fmt.Sprintf("The %s role can't see the family's documents (tool %s).", roleLabel(c.role), tool),
			Fix:     "Documents are private to the family; tell the user to ask an owner or family manager. Retrying will not help.",
		})
	}
	return c, nil
}

// writerOf is callerOf plus the write check. The server middleware refuses
// read-only roles first; this is the second check.
func writerOf(ctx context.Context, q querier, tool string) (member, *mcp.CallToolResult) {
	c, bad := callerOf(ctx, q)
	if bad != nil {
		return c, bad
	}
	if !canWrite(c.role) {
		return c, respond.Fail(respond.Error{
			Message: fmt.Sprintf("The %s role can't change documents (tool %s): only an owner, family manager or family member can.", roleLabel(c.role), tool),
			Fix:     "Tell the user to ask an owner or family manager; retrying will not help.",
		})
	}
	return c, nil
}

func roleLabel(role string) string {
	if role == "" {
		return "current"
	}
	return role
}

// secretFileWhere is the SQL condition that hides documents attached to a
// credential (entity_type 'secret') or linked from one (a document_links row
// of a secret pointing at the file). $n is a boolean: true lets them through.
func secretFileWhere(n int) string {
	return fmt.Sprintf(`($%[1]d::boolean OR (f.entity_type::text <> 'secret' AND NOT EXISTS (
		SELECT 1 FROM document_links sl
		 WHERE sl.household_id = f.household_id AND sl.link_type = 'file'
		   AND sl.link_id = f.id::text AND sl.entity_type::text = 'secret')))`, n)
}

// SecretFileSQL is the same rule for other packages' queries (search_all,
// #4798), like the API's file.SecretFileSQL: a condition that is true when
// the file whose id is the SQL expression fileID is attached to or linked
// from a credential, so roles without secrets read must not see it or
// anything about it (its notes). It takes no parameters. A file that no
// longer exists is not hidden by it.
func SecretFileSQL(fileID string) string {
	return `EXISTS (SELECT 1 FROM files f WHERE f.id = ` + fileID + ` AND NOT ` +
		strings.Replace(secretFileWhere(0), "$0::boolean", "false", 1) + `)`
}
