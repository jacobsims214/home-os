package notification

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"home-os/mcp/internal/server"
)

// Role filter on read (#4794). Migration 061 (#4740) stops new notices going
// to a member whose role can't see the record, but notices written while the
// member had a broader role stay in the table: a member demoted from owner to
// viewer still has `Dana updated secret "Bank login"`. Every read here (find,
// counts, UnreadCount, mark read) therefore also checks the reader's CURRENT
// role against what each notification is about, with the same SQL function
// 061 uses (role_can_see_entity, the SQL copy of auth.CanSeeEntity), so the
// rule lives in one place:
//
//   - the notification's entity_type must be visible to the role. A NULL
//     entity_type is an unknown kind and is visible to family roles only
//     (fail closed): every writer sets one, and it is NULL only after the
//     record was deleted (rehome_entity_refs), when the title may still name
//     a credential or bill.
//   - for kinds that sit on another record (note, file, list, document_link:
//     their entity_type column), that parent's kind must be visible too, as
//     061 checks parent_type: a note on a secret is a credential record for a
//     viewer. A note on the household is a family note (role_can_see_entity
//     'household' = family_notes, so not for housesitters, as auth.CanSeeEntity
//     says; stricter than 061, which skips 'household' parents). For lists,
//     files and document links a 'household' parent means none (a family
//     grocery list stays visible to a housesitter). When the record is gone
//     the parent is unknown and the notice is hidden.
//   - a file attached to a credential or linked from one (document_links of
//     a secret) is hidden from roles without secrets read, the rule of
//     secretFileWhere in internal/file; a document link pointing at a secret
//     likewise.
//
// An unknown or empty role sees nothing (role_can_see_entity is false).
// apps/api/internal/notification/visibility.go holds the same condition;
// change both together.

// roleSeesSQL is the condition on notifications n that the role, an SQL
// expression such as "$3::text", may see what n is about.
func roleSeesSQL(role string) string {
	parent := `(p.entity_type::text = 'household' OR role_can_see_entity(ROLE, p.entity_type::text))`
	noteParent := `role_can_see_entity(ROLE, p.entity_type::text)`
	return strings.ReplaceAll(`(role_can_see_entity(ROLE, n.entity_type::text) AND CASE n.entity_type::text
		WHEN 'note' THEN EXISTS (SELECT 1 FROM notes p
			WHERE p.id = n.entity_id AND p.household_id = n.household_id AND `+noteParent+`)
		WHEN 'list' THEN EXISTS (SELECT 1 FROM lists p
			WHERE p.id = n.entity_id AND p.household_id = n.household_id AND `+parent+`)
		WHEN 'document_link' THEN EXISTS (SELECT 1 FROM document_links p
			WHERE p.id = n.entity_id AND p.household_id = n.household_id AND `+parent+`
			  AND (p.link_type::text <> 'secret' OR role_can_see_entity(ROLE, 'secret')))
		WHEN 'file' THEN EXISTS (SELECT 1 FROM files p
			WHERE p.id = n.entity_id AND p.household_id = n.household_id AND `+parent+`
			  AND (role_can_see_entity(ROLE, 'secret') OR NOT EXISTS (
				SELECT 1 FROM document_links sl
				 WHERE sl.household_id = p.household_id AND sl.link_type = 'file'
				   AND sl.link_id = p.id::text AND sl.entity_type::text = 'secret')))
		ELSE true
	END)`, "ROLE", role)
}

// visibleSQL is the WHERE fragment for the rows the caller may see: $1 is
// the household id, $2 the caller's user id (NULL when the token has no
// user UUID, which leaves only family-wide rows) and $3 the caller's current
// role (callerRole).
var visibleSQL = `n.household_id = $1 AND (n.user_id IS NULL OR n.user_id = $2) AND ` + roleSeesSQL("$3::text")

// callerRole is the caller's current role in the family, re-read from
// memberships so a demotion applies at once; "" (sees nothing) when they are
// no longer a member. Without a user UUID (local-dev claims) the role on the
// claims in ctx is used; an agent token always has a user UUID, so one
// without gets "".
func callerRole(ctx context.Context, q querier, householdID string, userID *uuid.UUID) (string, error) {
	if userID == nil {
		if c := server.ClaimsFromContext(ctx); c != nil && c.AgentTokenID == "" && c.HouseholdID == householdID {
			return c.Role, nil
		}
		return "", nil
	}
	var role string
	err := q.QueryRow(ctx, `SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
		householdID, *userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return role, err
}
