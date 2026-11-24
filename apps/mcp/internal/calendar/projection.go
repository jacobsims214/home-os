package calendar

// Projection ownership (#5301): an event that a record keeps is not an event
// anyone made here, and the tools must say so.
//
// War game R2 (F-AGENT-2) caught the assistant telling a parent that the
// bill-sync entries on the Bills calendar were "duplicates I added" and
// offering to delete them. It had added nothing: every one of those entries is
// a bill's projection (#4943), written by packages/billcal when the bill was
// created and rewritten whenever the bill changes, and it predated every
// session of the agent that claimed it. A parent saying "yes, delete them"
// would have destroyed the family's calendar bill view — and only until the
// next bill edit or the worker's hourly backfill, which would put the entries
// straight back.
//
// The root cause is that nothing in an event said who owns it. Given a title
// like "Electric due" on the Bills calendar, a model has only a title/date
// heuristic to decide "is this mine?", and it guessed yes. So the ownership
// test lives here, and it is the database's answer, never a guess:
//
//   - An event is OWNED BY A RECORD when calendar_objects.entity_type names one
//     of the kinds below AND entity_id points at it. That is a real FK link
//     written by the code that creates and maintains the entry — billcal.Sync
//     for a bill, maintenance.syncEvent for an upkeep task — so it is a
//     reliable record of who keeps the event, not an inference from its text.
//   - Everything else is an ordinary event. That includes an entry linked to a
//     person, a pet or a vehicle: that link says what the event is ABOUT, not
//     who keeps it, and the family may still edit or delete it here. It also
//     includes a legacy "bill due" entry with no entity link (#4943/#5140):
//     nothing keeps it, so nothing owns it, and it stays deletable.
//
// An owned event reports managed_by/managed_id in its payload, event_update and
// event_delete refuse it with the record's own tools as the fix, and eventHints
// never offers to remove one. There is deliberately NO "an agent created this"
// signal: the entity link is the only ownership claim the tools make, so an
// assistant with no such link has no basis to call an event its own. "I added
// this" is a claim only a tool result in the same conversation can support
// (the #4951 rule).

import (
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/respond"
)

// projectionOwner is one record kind that keeps a calendar entry.
type projectionOwner struct {
	// kind is calendar_objects.entity_type, and the event's managed_by.
	kind string
	// record names the record in words a model can repeat.
	record string
	// eventType, when set, is the event_type the record's writer stores on the
	// entry, and an entry only counts as this record's when it carries it too.
	// It is the writer's own claim predicate, no wider: billcal.findEvent
	// requires entity_type='bill' AND event_type='bill', so an event a person
	// made and merely linked to a bill (event_type='event') stays theirs to
	// edit or delete. Empty means the writer claims on the entity link alone
	// (maintenance.syncEvent finds its mirror by entity_type/entity_id only).
	eventType string
	// idArg is the argument the record's tools take the id in.
	idArg string
	// updateTool, deleteTool and findTool are the tools that change it.
	updateTool, deleteTool, findTool string
}

// billcal.EntityType ('bill') and the maintenance writer's entity type. They
// are spelled out rather than imported so this file stays the single list of
// what the calendar tools treat as an owned entry: packages/billcal is the bill
// writer, and apps/mcp/internal/maintenance/task.go writes 'maintenance_task'
// (which the API's calendar repo writes too, apps/api/internal/calendar/repo.go).
const (
	EntityTypeBill            = "bill"
	EntityTypeMaintenanceTask = "maintenance_task"
)

// projectionOwners is every entity_type whose entry the record keeps. A kind
// that is not here (person, pet, vehicle, household, ...) is an ordinary event
// that merely references a record.
var projectionOwners = map[string]projectionOwner{
	EntityTypeBill: {
		kind: EntityTypeBill, record: "bill", eventType: EntityTypeBill, idArg: "bill_id",
		updateTool: "bill_update", deleteTool: "bill_delete", findTool: "bill_find",
	},
	EntityTypeMaintenanceTask: {
		kind: EntityTypeMaintenanceTask, record: "upkeep task", idArg: "task_id",
		updateTool: "maintenance_update", deleteTool: "maintenance_delete", findTool: "maintenance_find",
	},
}

// projectionOf reports whether (entityType, entityID, eventType) is an entry a
// record keeps, and returns that record's owner. The id is required: an
// entity_type with no entity_id owns nothing (migration 013 allows the type
// alone, and the 038 rehome trigger clears both together, but a type on its
// own must not be allowed to lock an event).
func projectionOf(entityType, entityID, eventType string) (projectionOwner, bool) {
	if entityType == "" || entityID == "" {
		return projectionOwner{}, false
	}
	p, ok := projectionOwners[entityType]
	if !ok || (p.eventType != "" && p.eventType != eventType) {
		return projectionOwner{}, false
	}
	return p, true
}

// managedBy reads the pair off an event row (any may be nil).
func managedBy(entityType, entityID *string, eventType string) (projectionOwner, bool) {
	if entityType == nil || entityID == nil {
		return projectionOwner{}, false
	}
	return projectionOf(*entityType, *entityID, eventType)
}

// ownerOfKind is the owner for a managed_by already computed by managedBy (the
// eventType test has been applied, and the pair is known non-empty).
func ownerOfKind(kind, id string) (projectionOwner, bool) {
	if kind == "" || id == "" {
		return projectionOwner{}, false
	}
	p, ok := projectionOwners[kind]
	return p, ok
}

// deleteRefusal is event_delete's answer for an owned entry: nothing was
// deleted, and the record's own tools are the fix. It is a refusal, not a
// warning, because deleting the entry does not even last — the next bill edit
// or the worker's backfill recreates it — so reporting a deletion would be a
// false claim about the family's data as well as a destructive one.
func (p projectionOwner) deleteRefusal(title, id string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("%q is the calendar entry kept by the %s %s, not an event anyone made here. Nothing was deleted.", title, p.record, id),
		Fix: fmt.Sprintf("Change the %s with %s %s=%s, or remove it with %s %s=%s; %s lists them. The entry is recreated from the record, so event_delete would not stick.",
			p.record, p.updateTool, p.idArg, id, p.deleteTool, p.idArg, id, p.findTool),
	})
}

// updateRefusal is event_update's answer for an owned entry: nothing was
// changed. An edit here is overwritten the next time the record is saved, so
// accepting it would report a change that does not survive.
func (p projectionOwner) updateRefusal(title, id string) *mcp.CallToolResult {
	return respond.Fail(respond.Error{
		Message: fmt.Sprintf("%q is the calendar entry kept by the %s %s, not an event anyone made here: an edit here is overwritten the next time the %s changes. Nothing was changed.",
			title, p.record, id, p.record),
		Fix: fmt.Sprintf("Change the %s with %s %s=%s; %s lists them.", p.record, p.updateTool, p.idArg, id, p.findTool),
	})
}

// hints are the next steps for an owned entry, in place of the ordinary
// "Remove it with event_delete" pair: the offer #5301 exists to remove.
func (p projectionOwner) hints(title, id string) []string {
	return []string{
		fmt.Sprintf("%q is kept by the %s %s, so it is not an event anyone made here: change that %s with %s %s=%s (or remove it with %s), and never report this entry as something an assistant added.",
			title, p.record, id, p.record, p.updateTool, p.idArg, id, p.deleteTool),
	}
}
