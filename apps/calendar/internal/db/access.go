package db

import (
	"context"
	"fmt"
)

// Role policy for CalDAV (#4718). KB decisions/2026-11-15-role-policy.md.
//
// Reads follow the SQL function role_can_see_entity(role, kind) from
// migration 061, which is the SQL copy of auth.CanSeeEntity in
// apps/api/internal/auth/roles.go. CalDAV does not keep its own role table:
// every read query below asks that function, so the rule lives in one place
// (roles.go plus its SQL mirror, kept in step by roles_sql_test.go).
//
//   - A calendar's kind comes from calendars.type: bills -> 'bill',
//     maintenance -> 'maintenance_task', vehicles -> 'vehicle', anything else
//     (family, properties, custom) -> 'calendar_event'.
//   - An event's kind is what the event mirrors: a bill mirror (event_type
//     'bill' or entity_type 'bill') is 'bill', a maintenance task event
//     (event_type 'maintenance' or entity_type maintenance_task/maintenance) is
//     'maintenance_task', every other event is 'calendar_event'. Events linked
//     to a person, pet or vehicle stay calendar events: the link does not hide
//     a family appointment from a housesitter.
//   - A calendar is visible when the role may see its kind; an event when the
//     role may see both its calendar's kind and its own kind.
//   - A vendor whose membership names their vendors row (memberships.vendor_id,
//     migration 064) sees only maintenance task events whose task has that
//     vendor_id. A vendor without a link sees every maintenance task event.
//
// Hidden calendars and events are absent: left out of PROPFIND and REPORT
// results and 404 on direct access, never 403.
//
// Writes (PUT, DELETE, PROPPATCH, MKCALENDAR) follow policy[calendar][write],
// which is the family roles; everyone else gets 403 (see Access.CanWrite).

// calendarKindSQL maps calendars c.type to a role_can_see_entity kind.
const calendarKindSQL = `(CASE c.type::text
		WHEN 'bills' THEN 'bill'
		WHEN 'maintenance' THEN 'maintenance_task'
		WHEN 'vehicles' THEN 'vehicle'
		ELSE 'calendar_event' END)`

// objectKindSQL maps a calendar_objects row o to a role_can_see_entity kind.
const objectKindSQL = `(CASE
		WHEN o.event_type = 'bill' OR o.entity_type::text = 'bill' THEN 'bill'
		WHEN o.event_type = 'maintenance' OR o.entity_type::text IN ('maintenance_task', 'maintenance') THEN 'maintenance_task'
		ELSE 'calendar_event' END)`

// calendarVisibleSQL is the calendar read rule for role $role.
func calendarVisibleSQL(role string) string {
	return `role_can_see_entity(` + role + `, ` + calendarKindSQL + `)`
}

// objectVisibleSQL is the event read rule for role $role and vendor link
// $vendor (NULL when the member has none). It assumes calendar c is already
// checked with calendarVisibleSQL.
func objectVisibleSQL(role, vendor string) string {
	return `role_can_see_entity(` + role + `, ` + objectKindSQL + `)
		AND (` + role + ` <> 'vendor' OR ` + vendor + `::uuid IS NULL
		     OR (o.entity_type::text = 'maintenance_task' AND EXISTS (
		         SELECT 1 FROM maintenance_tasks t
		          WHERE t.id = o.entity_id AND t.household_id = c.household_id
		            AND t.vendor_id = ` + vendor + `::uuid)))`
}

// Membership is one household the authenticated user belongs to, with the
// role and (for vendors) the vendors row their login is linked to. It is read
// from memberships on every request, so a demotion applies immediately.
type Membership struct {
	HouseholdID string
	Role        string
	// VendorID is memberships.vendor_id, "" when unset.
	VendorID string
}

// Access is what the caller may do in one household. The zero value (no
// role) sees nothing and writes nothing, so a request that somehow reaches a
// handler without a membership fails closed.
type Access struct {
	Role     string
	VendorID string
}

// Access returns the membership's access.
func (m Membership) Access() Access {
	return Access{Role: m.Role, VendorID: m.VendorID}
}

// CanWrite reports whether the role may change calendars and events
// (policy[calendar][write] in apps/api/internal/auth/roles.go: rolesFamily).
func (a Access) CanWrite() bool {
	switch a.Role {
	case "owner", "family_manager", "family_member":
		return true
	}
	return false
}

// vendorArg is the vendor link as a query argument: nil when unset.
func (a Access) vendorArg() any {
	if a.VendorID == "" {
		return nil
	}
	return a.VendorID
}

// ListMembershipsForUser returns every household the user belongs to with
// their role there, oldest membership first (ties broken by household_id),
// like ListHouseholdIDsForUser.
func (r *Repo) ListMembershipsForUser(ctx context.Context, userID string) ([]Membership, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT household_id::text, role::text, COALESCE(vendor_id::text, '')
		FROM memberships
		WHERE user_id = $1
		ORDER BY created_at ASC, household_id ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("db: list memberships for user: %w", err)
	}
	defer rows.Close()

	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.HouseholdID, &m.Role, &m.VendorID); err != nil {
			return nil, fmt.Errorf("db: scan membership: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: rows iteration: %w", err)
	}
	return out, nil
}
