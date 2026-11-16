package notification

import "strings"

// Role filter on read (#4794). Migration 061 (#4740) stops new notices going
// to a member whose role can't see the record, but notices written while the
// member had a broader role stay in the table: a member demoted from owner to
// viewer still has `Dana updated secret "Bank login"`. Every read here (list
// and mark read) therefore also checks the reader's CURRENT role (claims.Role,
// which RequireAuth re-reads from memberships on every request) against what
// each notification is about, with the same SQL function 061 uses
// (role_can_see_entity, the SQL copy of auth.CanSeeEntity), so the rule lives
// in one place:
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
// apps/mcp/internal/notification/visibility.go holds the same roleSeesSQL;
// TestRoleSeesMatchesMCP fails when the two differ.

// roleSeesSQL is the condition on notifications n that the role, an SQL
// expression such as "$3::text", may see what n is about.
func roleSeesSQL(role string) string {
	// ownJob limits a maintenance_task notice to the caller's own jobs when
	// they are a vendor linked to a vendors row (memberships.vendor_id,
	// #4799): maintenance_due is family-wide, so without it every linked
	// vendor would read the name and description of every job in the family.
	// It hides a notice only when the task exists and belongs to another
	// vendor, so a notice about a task that has since been deleted stays as
	// visible as it was. The condition is self-contained ($2 is the caller's
	// user id, NULL for a caller without one) so the API and the MCP keep
	// identical SQL.
	ownJob := `NOT EXISTS (SELECT 1 FROM maintenance_tasks p
			JOIN memberships m ON m.household_id = n.household_id AND m.user_id = $2
			  AND m.role = 'vendor' AND m.vendor_id IS NOT NULL
			WHERE p.id = n.entity_id AND p.household_id = n.household_id
			  AND p.vendor_id IS DISTINCT FROM m.vendor_id)`
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
		WHEN 'maintenance_task' THEN `+ownJob+`
		ELSE true
	END)`, "ROLE", role)
}

// visibleSQL is the WHERE fragment for the rows the caller may see: $1 is
// the household id, $2 the caller's user id (NULL without a user UUID, which
// leaves only family-wide rows) and $3 the caller's role. Like the MCP, a
// member sees family-wide notifications (user_id NULL) and their own
// personal ones, never another member's.
var visibleSQL = `n.household_id = $1 AND (n.user_id IS NULL OR n.user_id = $2) AND ` + roleSeesSQL("$3::text")
