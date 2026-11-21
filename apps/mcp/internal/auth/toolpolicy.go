package auth

import "fmt"

// Per-tool policy for the MCP (#4717). Every registered tool has exactly one
// entry in ToolPolicy, saying what the household role policy (policy.go)
// requires of the caller. apps/mcp/cmd/mcp's TestToolPolicyCoversEveryTool
// fails when a registered tool has no entry, so a new tool cannot ship
// without a decision: the gate (server.requireRolePolicy and the AuthMiddleware
// pre-check) refuses a tool with no entry, which is how this bug class (a tool
// that quietly ignored the policy) stays closed.
//
// Three kinds of entry:
//
//   - Any: the caller's role must hold at least one of the grants. A tool that
//     returns money lists the money resource, so a housesitter or vendor is
//     refused the tool outright (bill_find, loan_find, budget_summary,
//     vehicle_find, credential_*).
//   - All: the role must hold every grant (a tool that reads one resource and
//     writes another, e.g. meal_build_grocery_list).
//   - Trim: the tool is readable by a role that may see only part of what it
//     returns. The handler trims the rest (family_overview, agenda_get,
//     search_all, the notes on records the role may not see, a linked
//     vendor's own maintenance jobs, money fields at the serialization
//     boundary). Trim never refuses; Any/All decide who may call it.
//
// An empty Any and All means every signed-in role may call the tool
// (agenda_get and family_overview are entry points for everyone; they trim).

// Grant is one (resource, action) pair the role policy grants.
type Grant struct {
	Resource Resource
	Action   Action
}

// String names a grant in a message, e.g. "bills.read".
func (g Grant) String() string { return string(g.Resource) + "." + string(g.Action) }

// Requirement is what a tool needs from the role policy.
type Requirement struct {
	// Any is satisfied when the role holds at least one grant. Empty means
	// every role.
	Any []Grant
	// All must all be held. Empty means none.
	All []Grant
	// Trim is true when a role that passes Any/All still sees a trimmed
	// result: the handler removes sections and fields the role may not see.
	Trim bool
	// Note is the one-line policy decision, printed by the inventory test
	// and kept for review.
	Note string
}

// grant is shorthand for Requirement.Any.
func grant(res Resource, act Action) Grant { return Grant{Resource: res, Action: act} }

// Allows reports whether role may call a tool with requirement r.
func (r Requirement) Allows(role string) bool {
	if len(r.All) > 0 {
		for _, g := range r.All {
			if !Can(role, g.Resource, g.Action) {
				return false
			}
		}
	}
	if len(r.Any) == 0 {
		return true
	}
	for _, g := range r.Any {
		if Can(role, g.Resource, g.Action) {
			return true
		}
	}
	return false
}

// Denial is what a role is missing for r: the grant to name in a message.
func (r Requirement) Denial() string {
	if len(r.Any) > 0 {
		return r.Any[0].String()
	}
	if len(r.All) > 0 {
		return r.All[0].String()
	}
	return ""
}

// ToolPolicy maps every registered tool to its policy requirement. Keep it in
// step with apps/mcp/cmd/mcp/register.go (one entry per registered tool) and
// with the catalog (architecture/mcp-tool-catalog.md).
var ToolPolicy = map[string]Requirement{
	// ---------------------------------------------------------------- reads
	"activity_find": {Any: []Grant{grant(ResActivity, ActRead)},
		Note: "the activity feed is readers-only (API ResActivity read)"},
	"agenda_get": {Trim: true,
		Note: "everyone; the bills section is money, maintenance is the vendor's own jobs, meals/lists need their resources"},
	"asset_find": {Any: []Grant{grant(ResAssets, ActRead)}, Trim: true,
		Note: "everyone (API ResAssets read). KNOWN LEAK, deferred to #4808: purchase_price/current_value still reach a housesitter and a vendor through MCP (the API has the same leak); this ticket did not add the CanSeeMoney trim here"},
	"bill_find": {Any: []Grant{grant(ResBills, ActRead)},
		Note: "money: refused to housesitter and vendor (API ResBills read = rolesReaders)"},
	"budget_summary": {Any: []Grant{grant(ResBudget, ActRead)},
		Note: "money: refused to housesitter and vendor"},
	"calendar_find": {Any: []Grant{grant(ResCalendar, ActRead)},
		Note: "calendars are readable by everyone but vendor (API ResCalendar read)"},
	"calendar_find_free_time": {Any: []Grant{grant(ResCalendar, ActRead)},
		Note: "free time comes from the calendar, so it needs the same read grant"},
	"contact_find": {Any: []Grant{grant(ResVendors, ActRead)},
		Note: "contacts (vendors) are readable by everyone but vendor (API ResVendors read)"},
	"credential_find": {Any: []Grant{grant(ResSecrets, ActRead)},
		Note: "credentials are family-only, not even names for a viewer"},
	"credential_reveal": {Any: []Grant{grant(ResSecrets, ActRead)},
		Note: "credentials are family-only"},
	"document_find": {Any: []Grant{grant(ResFiles, ActRead)},
		Note: "documents are readers-only (API ResFiles read)"},
	"document_get": {Any: []Grant{grant(ResFiles, ActRead)},
		Note: "documents are readers-only"},
	"event_find": {Any: []Grant{grant(ResCalendar, ActRead)}, Trim: true,
		Note: "calendar is readable by everyone but vendor; bill mirror events are money and are left to #4808"},
	"family_overview": {Trim: true,
		Note: "the entry point for every role; each section is trimmed to what the role may see"},
	"guide_find": {Any: []Grant{grant(ResGuides, ActRead)},
		Note: "guides are readable by everyone but vendor (API ResGuides read)"},
	"guide_get": {Any: []Grant{grant(ResGuides, ActRead)},
		Note: "guides are readable by everyone but vendor"},
	// The family knowledge base (#5502): every role reads it, a housesitter and
	// a vendor included, because the point of the fixture is that the
	// babysitter can find the router doc. Writes are the family roles.
	"kb_list": {Any: []Grant{grant(ResKB, ActRead)},
		Note: "the KB is readable by every household role (API ResKB read = rolesEveryone)"},
	"kb_search": {Any: []Grant{grant(ResKB, ActRead)},
		Note: "the KB is readable by every household role"},
	"list_find": {Any: []Grant{grant(ResLists, ActRead)},
		Note: "lists are readable by everyone but vendor (API ResLists read)"},
	"list_get": {Any: []Grant{grant(ResLists, ActRead)},
		Note: "lists are readable by everyone but vendor"},
	"loan_find": {Any: []Grant{grant(ResLoans, ActRead)},
		Note: "money: refused to housesitter and vendor"},
	"maintenance_find": {Any: []Grant{grant(ResMaintenance, ActRead)}, Trim: true,
		Note: "maintenance is readable by everyone but housesitter; a linked vendor sees only their own jobs"},
	"meal_suggest": {Any: []Grant{grant(ResMeals, ActRead)},
		Note: "meals are readable by everyone but vendor (API ResMeals read)"},
	"note_find": {Any: []Grant{grant(ResNotes, ActRead)}, Trim: true,
		Note: "notes are readable by everyone but vendor; the trim is wired (#4881, note.visibleNotesSQL): a note on a record the role may not see, on a secret, or on a file attached to or linked from one is omitted"},
	"note_get": {Any: []Grant{grant(ResNotes, ActRead)}, Trim: true,
		Note: "notes are readable by everyone but vendor; the trim is wired (#4881): a note on a hidden record is not found, exactly as if it did not exist"},
	"notification_find": {Trim: true,
		Note: "every role reads its own notifications; maintenance_due is limited to a linked vendor's own jobs"},
	"person_find": {Any: []Grant{grant(ResPeople, ActReadBasic)}, Trim: true,
		Note: "names/relationship/allergies for a housesitter; no people at all for a vendor"},
	"person_get": {Any: []Grant{grant(ResPeople, ActReadBasic)}, Trim: true,
		Note: "names/relationship/allergies for a housesitter; no people at all for a vendor"},
	"pet_find": {Any: []Grant{grant(ResPets, ActRead)},
		Note: "pets are readable by everyone but vendor (API ResPets read)"},
	"property_find": {Any: []Grant{grant(ResProperties, ActRead)}, Trim: true,
		Note: "everyone (API ResProperties read). KNOWN LEAK, deferred to #4808: mortgage/tax/insurance/purchase_value still reach a housesitter and a vendor through MCP (the API redacts them since #4806); the trim here is not yet wired"},
	"recipe_find": {Any: []Grant{grant(ResMeals, ActRead)},
		Note: "recipes are readable by everyone but vendor"},
	"recipe_get": {Any: []Grant{grant(ResMeals, ActRead)},
		Note: "recipes are readable by everyone but vendor"},
	"search_all": {Trim: true,
		Note: "every role may search, but only the areas it may read (auth.CanSeeEntity per area and hit)"},
	"vehicle_find": {Any: []Grant{grant(ResVehicles, ActRead)},
		Note: "vehicles are readers-only (API ResVehicles read): refused to housesitter and vendor, which also closes the VIN and value leak"},

	// --------------------------------------------------------------- writes
	"asset_create": {Any: []Grant{grant(ResAssets, ActWrite)},
		Note: "assets are family-only to write"},
	"asset_delete": {Any: []Grant{grant(ResAssets, ActWrite)},
		Note: "assets are family-only to write"},
	"asset_update": {Any: []Grant{grant(ResAssets, ActWrite)},
		Note: "assets are family-only to write"},
	"bill_create": {Any: []Grant{grant(ResBills, ActWrite)},
		Note: "money is family-only to write"},
	"bill_delete": {Any: []Grant{grant(ResBills, ActWrite)},
		Note: "money is family-only to write"},
	"bill_pay": {Any: []Grant{grant(ResBills, ActWrite)},
		Note: "paying a bill is a money write"},
	"bill_update": {Any: []Grant{grant(ResBills, ActWrite)},
		Note: "money is family-only to write"},
	"budget_set_category": {Any: []Grant{grant(ResBudget, ActWrite)},
		Note: "budgets are family-only to write"},
	"calendar_create": {Any: []Grant{grant(ResCalendar, ActWrite)},
		Note: "calendars are family-only to write"},
	"contact_create": {Any: []Grant{grant(ResVendors, ActWrite)},
		Note: "contacts are family-only to write"},
	"contact_delete": {Any: []Grant{grant(ResVendors, ActWrite)},
		Note: "contacts are family-only to write"},
	"contact_update": {Any: []Grant{grant(ResVendors, ActWrite)},
		Note: "contacts are family-only to write"},
	"document_delete": {Any: []Grant{grant(ResFiles, ActWrite)},
		Note: "documents are family-only to write"},
	"document_update": {Any: []Grant{grant(ResFiles, ActWrite)},
		Note: "documents are family-only to write"},
	"document_upload": {Any: []Grant{grant(ResFiles, ActWrite)},
		Note: "documents are family-only to write"},
	"event_create": {Any: []Grant{grant(ResCalendar, ActWrite)},
		Note: "events are family-only to write"},
	"event_delete": {Any: []Grant{grant(ResCalendar, ActWrite)},
		Note: "events are family-only to write"},
	"event_update": {Any: []Grant{grant(ResCalendar, ActWrite)},
		Note: "events are family-only to write"},
	"family_update": {Any: []Grant{grant(ResHousehold, ActWrite)},
		Note: "renaming the family or changing its timezone is family-only"},
	"guide_create": {Any: []Grant{grant(ResGuides, ActWrite)},
		Note: "guides are family-only to write"},
	"kb_get": {Any: []Grant{grant(ResKB, ActRead)},
		Note: "the KB is readable by every household role"},
	"kb_write": {Any: []Grant{grant(ResKB, ActWrite)},
		Note: "the KB is family-only to write; a housesitter reads it and cannot change it"},
	"guide_delete": {Any: []Grant{grant(ResGuides, ActWrite)},
		Note: "guides are family-only to write"},
	"guide_update": {Any: []Grant{grant(ResGuides, ActWrite)},
		Note: "guides are family-only to write"},
	"list_add_items": {Any: []Grant{grant(ResLists, ActAddItems)},
		Note: "a housesitter may add items, but not start a list (the handler refuses to create one)"},
	"list_check_items": {Any: []Grant{grant(ResLists, ActCheckItems)},
		Note: "a housesitter may check items off, the narrow write the role policy allows"},
	"list_create": {Any: []Grant{grant(ResLists, ActManage)},
		Note: "starting, renaming or archiving a list is family-only"},
	"list_remove_items": {Any: []Grant{grant(ResLists, ActEditItems)},
		Note: "removing items is family-only"},
	"list_update": {Any: []Grant{grant(ResLists, ActManage)},
		Note: "starting, renaming or archiving a list is family-only"},
	"list_update_item": {Any: []Grant{grant(ResLists, ActEditItems)},
		Note: "editing an item's text or assignee is family-only (a housesitter only checks it off)"},
	"loan_create": {Any: []Grant{grant(ResLoans, ActWrite)},
		Note: "money is family-only to write"},
	"loan_delete": {Any: []Grant{grant(ResLoans, ActWrite)},
		Note: "money is family-only to write"},
	"loan_update": {Any: []Grant{grant(ResLoans, ActWrite)},
		Note: "money is family-only to write"},
	"maintenance_complete": {Any: []Grant{grant(ResMaintenance, ActUpdateStatus)},
		Note: "a vendor may mark a job done (status/notes only, own jobs); a housesitter may not"},
	"maintenance_create": {Any: []Grant{grant(ResMaintenance, ActWrite)},
		Note: "creating a job is family-only"},
	"maintenance_delete": {Any: []Grant{grant(ResMaintenance, ActWrite)},
		Note: "deleting a job is family-only"},
	"maintenance_update": {Any: []Grant{grant(ResMaintenance, ActWrite), grant(ResMaintenance, ActUpdateStatus)}, Trim: true,
		Note: "a vendor may change status/completion/notes of their own jobs and nothing else (the handler enforces it)"},
	"meal_build_grocery_list": {All: []Grant{grant(ResMeals, ActRead), grant(ResLists, ActAddItems)},
		Note: "reads the meal plan and adds list items, so it needs both grants; a housesitter's call will not start a new list"},
	"meal_rate": {Any: []Grant{grant(ResMeals, ActWrite)},
		Note: "rating a meal is a meals write; rating for others is managers-only in the handler"},
	"meal_set": {Any: []Grant{grant(ResMeals, ActWrite)},
		Note: "planning a meal is a meals write"},
	"note_create": {Any: []Grant{grant(ResNotes, ActAnnotate)}, Trim: true,
		Note: "a housesitter may add a note on a pet only (the handler checks the entity kind)"},
	"note_delete": {Any: []Grant{grant(ResNotes, ActWrite)},
		Note: "deleting a note is family-only"},
	"note_update": {Any: []Grant{grant(ResNotes, ActWrite)},
		Note: "editing a note is family-only"},
	"notification_mark_read": {Any: []Grant{grant(ResNotifications, ActMarkRead)},
		Note: "every role may mark its own notifications read"},
	"person_create": {Any: []Grant{grant(ResPeople, ActWrite)},
		Note: "people are family-only to write"},
	"person_delete": {Any: []Grant{grant(ResPeople, ActWrite)},
		Note: "people are family-only to write"},
	"person_update": {Any: []Grant{grant(ResPeople, ActWrite)},
		Note: "people are family-only to write (a family_member edits only their own record)"},
	"pet_create": {Any: []Grant{grant(ResPets, ActWrite)},
		Note: "pets are family-only to write"},
	"pet_delete": {Any: []Grant{grant(ResPets, ActWrite)},
		Note: "pets are family-only to write"},
	"pet_update": {Any: []Grant{grant(ResPets, ActWrite)},
		Note: "pets are family-only to write"},
	"property_create": {Any: []Grant{grant(ResProperties, ActWrite)},
		Note: "properties are family-only to write"},
	"property_delete": {Any: []Grant{grant(ResProperties, ActWrite)},
		Note: "properties are family-only to write"},
	"property_update": {Any: []Grant{grant(ResProperties, ActWrite)},
		Note: "properties are family-only to write"},
	"recipe_create": {Any: []Grant{grant(ResMeals, ActWrite)},
		Note: "recipes are family-only to write"},
	"recipe_delete": {Any: []Grant{grant(ResMeals, ActWrite)},
		Note: "recipes are family-only to write"},
	"recipe_update": {Any: []Grant{grant(ResMeals, ActWrite)},
		Note: "recipes are family-only to write"},
	"room_delete": {Any: []Grant{grant(ResProperties, ActWrite)},
		Note: "rooms are family-only to write"},
	"vehicle_create": {Any: []Grant{grant(ResVehicles, ActWrite)},
		Note: "vehicles are family-only to write"},
	"vehicle_delete": {Any: []Grant{grant(ResVehicles, ActWrite)},
		Note: "vehicles are family-only to write"},
	"vehicle_update": {Any: []Grant{grant(ResVehicles, ActWrite)},
		Note: "vehicles are family-only to write"},
}

// RequirementFor returns the policy requirement for a tool. ok is false for a
// tool with no entry, which callers must treat as refused (fail closed).
func RequirementFor(tool string) (Requirement, bool) {
	r, ok := ToolPolicy[tool]
	return r, ok
}

// RoleCannotMessage is the refusal for a tool the caller's role may not use.
// It names the role and what it cannot see, so the model can tell the user
// rather than retry.
func RoleCannotMessage(role, tool string) string {
	r, ok := RequirementFor(tool)
	if !ok {
		return fmt.Sprintf("the %s role cannot use %s: it has no policy entry, so it is refused", RoleLabel(role), tool)
	}
	return fmt.Sprintf("the %s role cannot use %s (%s)", RoleLabel(role), tool, r.Denial())
}
