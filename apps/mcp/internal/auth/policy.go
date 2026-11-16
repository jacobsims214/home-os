package auth

// Household role policy for the MCP (#4689, #4717).
//
// apps/api/internal/auth/roles.go is the policy: a table of resource x action
// -> roles, applied to every protected API route by
// middleware.RequirePolicy and to record-level decisions by the handlers.
// The MCP needs the same rules and the two apps are separate Go modules, so
// this file is a MIRROR of that table rather than an import. Three things
// keep the mirror honest:
//
//   - packages/db/testdata/household_role_policy.json is the shared fixture,
//     generated from roles.go. TestRolePolicyMatchesFixture here and in
//     apps/api/internal/auth fails when either table drifts from it.
//   - TestCanMatchesTable and TestEntityResourcesComplete re-derive the
//     helpers from this file's own table, so Can/CanSeeEntity/CanSeeMoney
//     cannot disagree with it.
//   - apps/mcp/cmd/mcp's TestToolPolicyCoversEveryTool requires a policy
//     decision for every registered tool, so a new tool cannot ship
//     unclassified (the way this bug class recurred).
//
// What each role may do through the MCP (see the table for the exact grants,
// and KB decisions/2026-11-15-role-policy.md for the prose):
//
//   - owner, family_manager, family_member: everything the tools offer.
//   - viewer: read only. Money is readable. No credentials at all: the
//     credential tools refuse, and credential records (and notes and
//     documents attached to them) are dropped from search.
//   - housesitter: reads pets, properties/rooms/assets, guides, vendors
//     (contacts), calendar, lists, meals and notes; may add and check off
//     list items and add notes on pets. People: names, relationship and
//     allergies only. No money, vehicles, files/documents, credentials or
//     activity feed. Search is allowed but returns only those areas.
//   - vendor: reads properties/rooms/assets and maintenance tasks and
//     schedules (their own jobs when the membership is linked to a vendors
//     row); may update a maintenance task's status, completion and notes.
//     Nothing else: no people, pets, money, documents, credentials,
//     calendar, lists, notes or activity. Search is allowed but returns only
//     those areas.
//   - An unknown or empty role may do nothing (fails closed).
//
// Denial semantics match the API: a tool the role may not use answers an
// isError naming the role and what it cannot see. A record the role may use
// the tool for but may not see is treated as absent (omitted from lists,
// "not found" on get).

// Resource is a kind of household data the policy grants access to.
type Resource string

// Resources. The names match apps/api/internal/auth/roles.go and the shared
// fixture, so a drift is visible rather than silently unmapped.
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

// AllRoles lists every household role, most to least privileged. It mirrors
// the membership_role enum (migration 001) and roles.go's AllRoles.
var AllRoles = []string{RoleOwner, RoleFamilyManager, RoleFamilyMember, RoleViewer, RoleHousesitter, RoleVendor}

// Role name constants, as the membership_role enum spells them.
const (
	RoleOwner         = "owner"
	RoleFamilyManager = "family_manager"
	RoleFamilyMember  = "family_member"
	RoleViewer        = "viewer"
	RoleHousesitter   = "housesitter"
	RoleVendor        = "vendor"
)

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

// Policy is the household role policy: resource -> action -> roles allowed.
// A missing resource or action means nobody may do it. Keep it identical to
// roles.go's policy (TestRolePolicyMatchesFixture).
var Policy = map[Resource]map[Action][]string{
	// The household record itself: everyone reads it, but only owner and
	// family_manager may change its name or timezone (#4820).
	ResHousehold: {ActRead: rolesEveryone, ActWrite: rolesManagers},
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

	ResNotes:         {ActRead: rolesSitter, ActWrite: rolesFamily, ActAnnotate: rolesFamilyAndSitter},
	ResFamilyNotes:   {ActRead: rolesReaders, ActWrite: rolesFamily},
	ResNotifications: {ActRead: rolesEveryone, ActMarkRead: rolesEveryone, ActWrite: rolesFamily},
	ResFiles:         {ActRead: rolesReaders, ActWrite: rolesFamily},
	ResSecrets:       {ActRead: rolesFamily, ActWrite: rolesFamily},
	ResLinks:         {ActRead: rolesReaders, ActWrite: rolesFamily},
	// Search is allowed to every role, including a housesitter and a vendor
	// (#4717): search_all returns only the areas the caller may read
	// (auth.CanSeeEntity per area and per hit), the rule the MCP has served
	// since #4696. Narrower than the whole family's index, wider than "no
	// search at all".
	ResSearch: {ActRead: rolesEveryone},

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
	for _, r := range Policy[res][act] {
		if r == role {
			return true
		}
	}
	return false
}

// ValidRole reports whether role is a membership_role enum value.
func ValidRole(role string) bool {
	for _, r := range AllRoles {
		if r == role {
			return true
		}
	}
	return false
}

// EntityResources maps an entity kind (the entity_kind domain values used by
// notes, links, files and secrets, and the activity_log entity/parent types)
// to the resource that guards it. Keep it identical to roles.go's
// entityResources (TestRolePolicyMatchesFixture).
var EntityResources = map[string]Resource{
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
	r, ok := EntityResources[kind]
	return r, ok
}

// EntityKinds returns every entity kind the policy knows.
func EntityKinds() []string {
	out := make([]string, 0, len(EntityResources))
	for k := range EntityResources {
		out = append(out, k)
	}
	return out
}

// CanSeeEntity reports whether a member with role may see records of entity
// kind kind (and records attached to them, such as notes). An unknown kind is
// visible only to the family roles, so a new kind fails closed for outsiders.
func CanSeeEntity(role, kind string) bool {
	res, ok := EntityResources[kind]
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

// CanSeeMoney reports whether role may see money at all: bills, loans and the
// budget (KB decisions/2026-11-16-money-redaction.md, #4806). Both resources
// are required, so a future drift between them hides rather than leaks. Use
// it wherever a record the role MAY read carries an amount, a rate or an
// account number derived from elsewhere (a property's mortgage, a vehicle's
// value and related loans). Do not write `role == housesitter || role ==
// vendor` anywhere.
func CanSeeMoney(role string) bool {
	return Can(role, ResBills, ActRead) && Can(role, ResLoans, ActRead)
}

// RoleLabel names a role in a message; "" becomes "current", as the tool
// packages' own labels do.
func RoleLabel(role string) string {
	if role == "" {
		return "current"
	}
	return role
}

// CanSeeKind reports whether role may see records of entity kind kind in a
// listing or a search hit. It is CanSeeEntity plus the one case where a role
// reads part of a record: a housesitter reads people by name and
// relationship (ActReadBasic), so person records are visible to them there
// even though full people detail is not (CanSeeEntity, which the API's
// search uses and which stays strict). Search_all uses this for its areas,
// and reduces what a housesitter may match on to the fields they may see
// (area.SitterDoc), so the wider visibility never widens the detail.
func CanSeeKind(role, kind string) bool {
	if CanSeeEntity(role, kind) {
		return true
	}
	return kind == "person" && Can(role, ResPeople, ActReadBasic)
}
