package auth

// Household role rules. They decide who may change a household's membership
// (invite, change roles, remove members) and which roles they may hand out.
//
//   - Only owner and family_manager manage members. family_member, viewer,
//     housesitter and vendor may not invite, list invitations, change roles
//     or remove anyone.
//   - An owner may grant any role.
//   - A family_manager may grant any role except owner, and may not change or
//     remove an owner.
//
// Callers must pass the actor's role in the household the request is for
// (the X-Household-ID-resolved one), read from memberships.

// validRoles mirrors the membership_role enum (migration 001).
var validRoles = map[string]bool{
	RoleOwner: true, RoleFamilyManager: true, RoleFamilyMember: true,
	RoleViewer: true, RoleHousesitter: true, RoleVendor: true,
}

// ValidRole reports whether role is a membership_role enum value.
func ValidRole(role string) bool { return validRoles[role] }

// CanManageMembers reports whether a member with role may invite people,
// list invitations, change roles and remove members.
func CanManageMembers(role string) bool {
	return role == RoleOwner || role == RoleFamilyManager
}

// CanGrantRole reports whether a member with actorRole may give someone
// target (by invitation or a role change). target must also be valid.
func CanGrantRole(actorRole, target string) bool {
	if !ValidRole(target) || !CanManageMembers(actorRole) {
		return false
	}
	if target == RoleOwner {
		return actorRole == RoleOwner
	}
	return true
}

// CanModifyMember reports whether a member with actorRole may change the role
// of, or remove, a member whose current role is targetCurrent.
func CanModifyMember(actorRole, targetCurrent string) bool {
	if !CanManageMembers(actorRole) {
		return false
	}
	if targetCurrent == RoleOwner {
		return actorRole == RoleOwner
	}
	return true
}

// ---------------------------------------------------------------------------
// Household role policy (#4689)
//
// This is the one place that decides what each household role may do. It is
// a table of resource x action -> roles. middleware.RequirePolicy applies it
// to every protected route (each route names one resource and action), and
// handlers that need a finer, record-level decision (lists, notes, people,
// maintenance, search, activity, meals, budget, guides) ask Can /
// CanSeeEntity against the same table, so the API never checks a role ad hoc.
//
// Summary (see the table for the exact grants):
//   - owner, family_manager: everything; manage members and invitations
//     (CanGrantRole / CanModifyMember keep a family_manager away from owners).
//   - family_member: read and write all household data, including
//     credentials (the secrets handler's own rules still apply); cannot
//     manage members or invitations.
//   - viewer: read only. Money (bills, loans, budget) is readable. No
//     credentials at all: secret routes answer 403 and secret records are
//     dropped from search, links, notes and the activity feed. Agent tokens
//     are read-scoped only.
//   - housesitter: read pets, properties/rooms/assets, guides, vendors
//     (contacts), calendar, lists and meals; check off and add list items;
//     add notes on pets. People: names and allergies only. No money,
//     vehicles, files/documents, credentials, preferences, activity feed,
//     search or agent tokens.
//   - vendor: read properties/rooms/assets and maintenance tasks/schedules;
//     update a maintenance task's status and notes. Nothing else (no people,
//     pets, money, documents, credentials, calendar, activity, agent tokens).
//
// Denials: a route the role may not use answers 403 with a message naming
// the role, action and resource. A record the role may use the route for but
// may not see (a note on a secret for a viewer, a note on a bill for a
// housesitter) is treated as not existing: omitted from lists, 404 on get.
// ---------------------------------------------------------------------------

// Resource is a kind of household data the policy grants access to.
type Resource string

// Resources.
const (
	ResHousehold     Resource = "household"     // the household record and the caller's households
	ResMembers       Resource = "members"       // memberships (people who log in) and their roles
	ResInvites       Resource = "invites"       // invitations
	ResAgentTokens   Resource = "agent_tokens"  // the caller's own agent tokens
	ResAssets        Resource = "assets"        // assets
	ResProperties    Resource = "properties"    // properties and rooms
	ResVehicles      Resource = "vehicles"      // vehicles
	ResPets          Resource = "pets"          // pets
	ResVendors       Resource = "vendors"       // vendors / contacts
	ResCalendar      Resource = "calendar"      // calendars and events
	ResMaintenance   Resource = "maintenance"   // maintenance tasks and schedules
	ResBills         Resource = "bills"         // bills
	ResLoans         Resource = "loans"         // loans
	ResBudget        Resource = "budget"        // budget categories and obligations
	ResNotes         Resource = "notes"         // notes (on any record)
	ResFamilyNotes   Resource = "family_notes"  // notes about the household itself (entity 'household')
	ResNotifications Resource = "notifications" // notifications
	ResFiles         Resource = "files"         // files / documents and document links
	ResSecrets       Resource = "secrets"       // credentials (secrets) and the vault key
	ResLinks         Resource = "links"         // links between records
	ResSearch        Resource = "search"        // household-wide search
	ResPeople        Resource = "people"        // people records (full detail)
	ResPreferences   Resource = "preferences"   // people's preferences
	ResLists         Resource = "lists"         // lists and list items
	ResMeals         Resource = "meals"         // recipes, meal plans, meal feedback
	ResActivity      Resource = "activity"      // the family activity feed
	ResGuides        Resource = "guides"        // guides
)

// Action is something a role may do with a resource.
type Action string

// Actions. ActRead and ActWrite apply to every resource; the others are the
// narrower grants some roles get instead of ActWrite (or ActRead).
const (
	ActRead  Action = "read"
	ActWrite Action = "write"

	// ActReadBasic (people): names, relationship and allergies only.
	ActReadBasic Action = "read_basic"
	// ActManage (lists): create, rename, re-subject, archive, delete lists.
	ActManage Action = "manage"
	// ActEditItems (lists): edit, remove and reorder items.
	ActEditItems Action = "edit_items"
	// ActAddItems (lists): add items.
	ActAddItems Action = "add_items"
	// ActCheckItems (lists): check and uncheck items.
	ActCheckItems Action = "check_items"
	// ActAnnotate (notes): add a note on a record the role may annotate
	// (CanAnnotateEntity); housesitters may add notes on pets.
	ActAnnotate Action = "annotate"
	// ActUpdateStatus (maintenance): change a task's status, completion and
	// notes, nothing else.
	ActUpdateStatus Action = "update_status"
	// ActRateOthers (meals): record feedback on behalf of other people.
	ActRateOthers Action = "rate_others"
	// ActCreate (agent tokens): create a token (read-scoped unless the role
	// also has ActWriteScope).
	ActCreate Action = "create"
	// ActWriteScope (agent tokens): give a token the write scope.
	ActWriteScope Action = "write_scope"
	// ActRevoke (agent tokens): revoke one's own token.
	ActRevoke Action = "revoke"
	// ActMarkRead (notifications): mark one's notification read.
	ActMarkRead Action = "mark_read"
)

// AllRoles lists every household role, most to least privileged.
var AllRoles = []string{RoleOwner, RoleFamilyManager, RoleFamilyMember, RoleViewer, RoleHousesitter, RoleVendor}

var (
	rolesManagers = []string{RoleOwner, RoleFamilyManager}
	rolesFamily   = []string{RoleOwner, RoleFamilyManager, RoleFamilyMember}
	rolesReaders  = []string{RoleOwner, RoleFamilyManager, RoleFamilyMember, RoleViewer}
	rolesSitter   = []string{RoleOwner, RoleFamilyManager, RoleFamilyMember, RoleViewer, RoleHousesitter}
	rolesFabric   = []string{RoleOwner, RoleFamilyManager, RoleFamilyMember, RoleViewer, RoleVendor}
	rolesEveryone = AllRoles
	// family roles plus one outsider role, for the narrow grants below
	rolesFamilyAndSitter = []string{RoleOwner, RoleFamilyManager, RoleFamilyMember, RoleHousesitter}
	rolesFamilyAndVendor = []string{RoleOwner, RoleFamilyManager, RoleFamilyMember, RoleVendor}
)

// policy is the household role policy: resource -> action -> roles allowed.
// A missing resource or action means nobody may do it.
var policy = map[Resource]map[Action][]string{
	ResHousehold: {ActRead: rolesEveryone},
	ResMembers:   {ActRead: rolesSitter, ActWrite: rolesManagers},
	ResInvites:   {ActRead: rolesManagers, ActWrite: rolesManagers},
	ResAgentTokens: {
		ActRead:       rolesEveryone, // list one's own tokens
		ActRevoke:     rolesEveryone, // revoke one's own tokens (e.g. after a demotion)
		ActCreate:     rolesReaders,
		ActWriteScope: rolesFamily,
	},

	ResAssets:     {ActRead: rolesEveryone, ActWrite: rolesFamily},
	ResProperties: {ActRead: rolesEveryone, ActWrite: rolesFamily},
	ResVehicles:   {ActRead: rolesReaders, ActWrite: rolesFamily},
	ResPets:       {ActRead: rolesSitter, ActWrite: rolesFamily},
	ResVendors:    {ActRead: rolesSitter, ActWrite: rolesFamily},
	ResCalendar:   {ActRead: rolesSitter, ActWrite: rolesFamily},
	ResMaintenance: {
		ActRead:         rolesFabric,
		ActWrite:        rolesFamily,
		ActUpdateStatus: rolesFamilyAndVendor,
	},

	ResBills:  {ActRead: rolesReaders, ActWrite: rolesFamily},
	ResLoans:  {ActRead: rolesReaders, ActWrite: rolesFamily},
	ResBudget: {ActRead: rolesReaders, ActWrite: rolesFamily},

	ResNotes:       {ActRead: rolesSitter, ActWrite: rolesFamily, ActAnnotate: rolesFamilyAndSitter},
	ResFamilyNotes: {ActRead: rolesReaders, ActWrite: rolesFamily},
	ResNotifications: {
		ActRead:     rolesEveryone, // one's own notifications
		ActMarkRead: rolesEveryone,
		ActWrite:    rolesFamily,
	},
	ResFiles:   {ActRead: rolesReaders, ActWrite: rolesFamily},
	ResSecrets: {ActRead: rolesFamily, ActWrite: rolesFamily},
	ResLinks:   {ActRead: rolesReaders, ActWrite: rolesFamily},
	ResSearch:  {ActRead: rolesReaders},

	ResPeople:      {ActRead: rolesReaders, ActReadBasic: rolesSitter, ActWrite: rolesFamily},
	ResPreferences: {ActRead: rolesReaders, ActWrite: rolesFamily},
	ResLists: {
		ActRead:       rolesSitter,
		ActManage:     rolesFamily,
		ActEditItems:  rolesFamily,
		ActAddItems:   rolesFamilyAndSitter, // a housesitter, not a viewer,
		ActCheckItems: rolesFamilyAndSitter, // may add and check off items
	},
	ResMeals:    {ActRead: rolesSitter, ActWrite: rolesFamily, ActRateOthers: rolesManagers},
	ResActivity: {ActRead: rolesReaders},
	ResGuides:   {ActRead: rolesSitter, ActWrite: rolesFamily},
}

// Can reports whether a member with role may perform act on res. Unknown
// roles, resources and actions are denied.
func Can(role string, res Resource, act Action) bool {
	for _, r := range policy[res][act] {
		if r == role {
			return true
		}
	}
	return false
}

// entityResources maps an entity kind (the entity_kind domain values used by
// notes, links, files and secrets, and the activity_log entity/parent types)
// to the resource that guards it.
var entityResources = map[string]Resource{
	"household":            ResFamilyNotes,
	"property":             ResProperties,
	"room":                 ResProperties,
	"asset":                ResAssets,
	"vehicle":              ResVehicles,
	"pet":                  ResPets,
	"vendor":               ResVendors,
	"bill":                 ResBills,
	"loan":                 ResLoans,
	"maintenance":          ResMaintenance,
	"maintenance_task":     ResMaintenance,
	"maintenance_schedule": ResMaintenance,
	"calendar":             ResCalendar,
	"calendar_event":       ResCalendar,
	"note":                 ResNotes,
	"file":                 ResFiles,
	"document":             ResFiles,
	"document_link":        ResFiles,
	"secret":               ResSecrets,
	"person":               ResPeople,
	"person_preference":    ResPreferences,
	"budget":               ResBudget,
	"list":                 ResLists,
	"list_item":            ResLists,
	"recipe":               ResMeals,
	"meal_plan":            ResMeals,
	"meal_feedback":        ResMeals,
	"guide":                ResGuides,
}

// EntityResource returns the resource guarding entity kind kind, and false
// for an unknown kind.
func EntityResource(kind string) (Resource, bool) {
	r, ok := entityResources[kind]
	return r, ok
}

// EntityKinds returns every entity kind the policy knows.
func EntityKinds() []string {
	out := make([]string, 0, len(entityResources))
	for k := range entityResources {
		out = append(out, k)
	}
	return out
}

// CanSeeEntity reports whether a member with role may see records of entity
// kind kind (and records attached to them, such as notes). An unknown kind is
// visible only to the family roles, so a new kind fails closed for outsiders.
func CanSeeEntity(role, kind string) bool {
	res, ok := entityResources[kind]
	if !ok {
		return Can(role, ResNotes, ActWrite) // family roles
	}
	return Can(role, res, ActRead)
}

// HiddenEntityKinds returns the known entity kinds role may not see. Feeds and
// search use it to filter records out.
func HiddenEntityKinds(role string) []string {
	var out []string
	for _, k := range EntityKinds() {
		if !CanSeeEntity(role, k) {
			out = append(out, k)
		}
	}
	return out
}

// CanSeeMoney reports whether a member with role may see money: amounts,
// rates, lenders and account numbers on bills, loans and the budget. The
// grant is rolesReaders (the family plus the viewer) through ResBills and
// ResLoans; both are required, so a drift between the two hides the money
// rather than leaking it.
//
// It exists for records that are themselves visible to a role without money.
// A property and a vehicle are readable by housesitters and vendors, and
// their response carries money derived from bills and loans (#4704), so the
// record route would otherwise be a side door around the money policy
// (#4806). Handlers ask this before they derive or serialize an amount,
// instead of naming roles themselves, so one decision covers every money
// field and a field added later is covered by default.
func CanSeeMoney(role string) bool {
	return Can(role, ResBills, ActRead) && Can(role, ResLoans, ActRead)
}

// CanAnnotateEntity reports whether a member with role may add a note on a
// record of kind kind: family roles on anything they can see, a housesitter
// on pets only.
func CanAnnotateEntity(role, kind string) bool {
	if !CanSeeEntity(role, kind) {
		return false
	}
	if Can(role, ResNotes, ActWrite) {
		return true
	}
	return Can(role, ResNotes, ActAnnotate) && kind == "pet"
}
