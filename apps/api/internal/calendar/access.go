package calendar

import "fmt"

// Role policy for calendar reads (#4807). KB decisions/2026-11-15-role-policy.md.
//
// CalDAV already hides these rows from these roles (#4718,
// apps/calendar/internal/db/access.go); the API must not be the side door
// around it. Since #4485/#4716 every bill has a mirror event on the Bills
// calendar whose description carries "Amount: $X", the autopay flag and the
// payment URL, and GET /api/v1/calendars[/{id}/events] is ResCalendar read =
// rolesSitter (auth/roles.go), so a housesitter was handed the family's bill
// names, amounts and due dates over the API while CalDAV refused them.
//
// The rule is not this package's own: every read query in repo.go asks the SQL
// function role_can_see_entity(role, kind) from migration 061, the SQL copy of
// auth.CanSeeEntity in apps/api/internal/auth/roles.go (kept in step by
// apps/api/internal/auth/roles_sql_test.go). Nothing here names a role.
//
//   - A calendar's kind comes from calendars.type: bills -> 'bill',
//     maintenance -> 'maintenance_task', vehicles -> 'vehicle', anything else
//     (family, properties, custom) -> 'calendar_event'.
//   - An event's kind is what it mirrors: a bill mirror (event_type 'bill' or
//     entity_type 'bill') is 'bill', a maintenance task event (event_type
//     'maintenance' or entity_type maintenance_task/maintenance) is
//     'maintenance_task', every other event is 'calendar_event'. An event
//     linked to a person, pet or vehicle stays a calendar event: the link does
//     not hide a family appointment from a housesitter.
//   - A calendar is visible when the role may see its kind; an event when the
//     role may see both its calendar's kind and its own kind.
//
// What that means in practice: a housesitter keeps every ordinary event (the
// Family, Properties and custom calendars and all their events, kind
// 'calendar_event') and loses the Bills calendar and its mirrors ('bill'),
// Maintenance ('maintenance_task': ResMaintenance read is rolesFabric) and
// Vehicles ('vehicle': ResVehicles read is rolesReaders). The record is
// dropped whole rather than field by field: a bill mirror's SUMMARY is the
// bill's name and its DESCRIPTION is the amount, category, autopay flag and
// payment URL, so no partial version of it is safe to show, and a sitter has
// no use for the calendar of bills they may not open.
//
// Hidden calendars and events are absent, like CalDAV: left out of the lists
// and answered as 404 (never 403) on direct access. An empty or unknown role
// sees nothing, so a read that reaches the repo without a role fails closed.
//
// The CASE expressions and the two predicate builders are copied from
// apps/calendar/internal/db/access.go (the api and the calendar app cannot
// share a package). Change both together, or a housesitter just switches
// client to read the bills.
//
// Vendor narrowing (memberships.vendor_id, #4799, which CalDAV applies to
// maintenance events) is deliberately not repeated here: ResCalendar read is
// rolesSitter, so a vendor is refused by RequirePolicy before any calendar
// query runs.

// calendarKindSQL maps a calendars row to a role_can_see_entity kind. The
// table alias is %[1]s; CalDAV uses "c", and this package's event reads do
// too, so the text matches apps/calendar/internal/db/access.go.
const calendarKindSQL = `(CASE %[1]s.type::text
		WHEN 'bills' THEN 'bill'
		WHEN 'maintenance' THEN 'maintenance_task'
		WHEN 'vehicles' THEN 'vehicle'
		ELSE 'calendar_event' END)`

// objectKindSQL maps a calendar_objects row to a role_can_see_entity kind.
// The table alias is %[1]s: CalDAV aliases the row "o" and this package
// aliases it "co", which is the only difference from that copy.
const objectKindSQL = `(CASE
		WHEN %[1]s.event_type = 'bill' OR %[1]s.entity_type::text = 'bill' THEN 'bill'
		WHEN %[1]s.event_type = 'maintenance' OR %[1]s.entity_type::text IN ('maintenance_task', 'maintenance') THEN 'maintenance_task'
		ELSE 'calendar_event' END)`

// calendarVisibleSQL is the calendar read rule for the role in SQL argument
// role (a placeholder such as "$2", which the caller passes the role for),
// for a calendars row aliased "c".
func calendarVisibleSQL(role string) string {
	return `role_can_see_entity(` + role + `, ` + fmt.Sprintf(calendarKindSQL, "c") + `)`
}

// objectVisibleSQL is the event read rule for the role in SQL argument role,
// for a calendar_objects row aliased "co". It assumes the event's calendar
// (aliased "c") is joined and already checked with calendarVisibleSQL.
func objectVisibleSQL(role string) string {
	return `role_can_see_entity(` + role + `, ` + fmt.Sprintf(objectKindSQL, "co") + `)`
}

// visibleToRole appends the calendar and event policy to an event-read query
// whose FROM already joins calendars c to calendar_objects o, so every event
// read goes through one place and a new read cannot skip the rule.
func visibleToRole(query, role string) string {
	return query + ` AND ` + calendarVisibleSQL(role) + ` AND ` + objectVisibleSQL(role)
}
