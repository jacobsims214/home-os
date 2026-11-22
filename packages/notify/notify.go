// Package notify holds the vocabulary of the notifications table: what a
// notification IS (its Type) and how it reaches its person (its Channel).
//
// These are the Go side of migration 079's domains, and they exist so that a
// misspelled type is a COMPILE error instead of an insert that fails at run
// time — or, before 079, an insert that succeeded and produced a row no client
// could read.
//
// Before 079 the types existed only as string literals scattered across the
// writer that produced each one:
//
//	bill_due         packages/billdue/policy.go  NotificationTypeBill
//	bill_digest      packages/billdue/policy.go  NotificationTypeDigest
//	maintenance_due  apps/worker/cmd/worker/main.go:200 -- a bare SQL literal,
//	                 with no Go constant at all
//	calendar_alarm   apps/worker/internal/alarms  NotificationType
//	item_changed     SQL only, notify_entity_creator() (054/061/070)
//
// Every writer uses the constants here now, and TestNotificationVocabularySubset
// in apps/mcp/internal/testutil asserts that every constant is a member of the
// domain in the database. Adding a constant without extending 079 fails that
// test; extending 079 without adding the constant leaves a value the same test
// names as unwritten.
//
// This module has no dependencies on purpose: it is imported by apps/api,
// apps/mcp and apps/worker, and it must never drag a driver or a framework into
// a caller that did not ask for one.
package notify

// Type is what a notification IS. It is the domain notification_type from
// migration 079; a value that is not one of the constants below is refused by
// the database on every path (API, MCP, worker, psql, a migration).
type Type string

// Channel is how a notification reaches its person. It is the domain
// notification_channel from migration 079.
type Channel string

// The notification types. Each one has a writer; the comment names it, so a
// constant here cannot become an orphan nobody produces.
const (
	// TypeBillDue is one bill reminder from the worker's bill_due job
	// (packages/billdue -> apps/worker/cmd/worker/billreminders.go). It keeps
	// the name it has always had, so the existing client icons, email template
	// and read filters keep working (#5500 requirement 3: no parallel delivery
	// path).
	TypeBillDue Type = "bill_due"

	// TypeBillDigest is the one notification that stands in for a family's
	// too-many reminders in a single run (081's digest, same job).
	TypeBillDigest Type = "bill_digest"

	// TypeMaintenanceDue is a maintenance task due soon, from the worker's
	// maintenance job (apps/worker/cmd/worker/main.go).
	TypeMaintenanceDue Type = "maintenance_due"

	// TypeCalendarAlarm is an iCal VALARM reminder
	// (apps/worker/internal/alarms).
	TypeCalendarAlarm Type = "calendar_alarm"

	// TypeItemChanged is "someone else changed what you created", written by
	// notify_entity_creator() (054). No Go code writes it, but the MCP
	// notification tools filter on it and idx_notifications_item_changed is a
	// partial index keyed on it, so it is part of the vocabulary.
	TypeItemChanged Type = "item_changed"
)

// The channels. There is exactly one because there is exactly one writer: 015's
// DEFAULT and every SQL writer (054/061/070) set in_app, and no Go code sets a
// channel at all. The API's email is a side effect of an in_app bill_due /
// maintenance_due row (apps/api/internal/notification/service.go), not a
// channel of its own — which is why "email" is deliberately absent. Adding it
// here without a writer would be the same invention 079 exists to stop.
const (
	// ChannelInApp is the in-app notification list.
	ChannelInApp Channel = "in_app"
)

// Types is every type the database accepts, in the order migration 079 declares
// them. A caller that needs to iterate the vocabulary (a client icon table, a
// test, a filter picker) uses this rather than keeping its own copy.
var Types = []Type{
	TypeBillDue,
	TypeBillDigest,
	TypeMaintenanceDue,
	TypeCalendarAlarm,
	TypeItemChanged,
}

// Channels is every channel the database accepts.
var Channels = []Channel{ChannelInApp}

// Valid reports whether t is in the vocabulary. It exists for the one caller
// that must answer before the database does: the API's POST
// /api/v1/notifications, which turns an unknown type into a 400 naming the
// allowed values instead of a constraint violation it would have to translate.
func (t Type) Valid() bool {
	for _, v := range Types {
		if t == v {
			return true
		}
	}
	return false
}

// Valid reports whether c is in the vocabulary.
func (c Channel) Valid() bool {
	for _, v := range Channels {
		if c == v {
			return true
		}
	}
	return false
}

// String makes a Type usable wherever a plain string is wanted (a log field, a
// JSON response, a query argument).
func (t Type) String() string { return string(t) }

// String makes a Channel usable wherever a plain string is wanted.
func (c Channel) String() string { return string(c) }

// Names returns the types as plain strings, for a caller that has to hand the
// vocabulary to something that does not import this package (an error message,
// an API response body).
func Names() []string {
	out := make([]string, len(Types))
	for i, t := range Types {
		out[i] = string(t)
	}
	return out
}
