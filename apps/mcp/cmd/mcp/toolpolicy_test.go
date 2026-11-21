package main

// #4717: every registered MCP tool follows the household role policy. These
// tests are the guard that keeps a new tool from leaking by default:
//
//   - TestToolPolicyCoversEveryTool: every tool the registry serves has an
//     entry in auth.ToolPolicy, and every entry names a served tool. A tool
//     added without a decision fails here, and would be refused at runtime
//     (server.deniedByRole treats a missing entry as denied).
//   - TestToolPolicyMatrix: the full inventory, one row per tool, checked
//     against an independently written expected role set. That table is the
//     review artifact: it says, for each of the 90 tools, which roles may
//     call it, so a change in the policy shows up as a diff in one place.
//   - TestMoneyAndPrivateToolsRefusedToOutsiders: the specific leaks this
//     ticket closes (bill_find, loan_find, budget_summary, vehicle_find,
//     family_overview.people, agenda bills, the vendor own-jobs views,
//     maintenance_due notifications) stay closed.
//   - TestNarrowWritesReachTheRightRoles: the writes the role policy allows
//     (housesitter list add/check-off and pet notes, vendor maintenance
//     status) are not refused by the gate.

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/config"
	"home-os/mcp/internal/server"
)

// allRoles is every household role, for the matrix.
var allRoles = auth.AllRoles

// roleSet is a role set as a sorted slice, for readable diffs.
func roleSet(roles ...string) []string {
	out := slices.Clone(roles)
	sort.Strings(out)
	return out
}

// everyone/readers/sitters/fabric/family role sets, named as in
// apps/api/cmd/api/routes_test.go so the two tables read the same way.
var (
	roleEveryone = roleSet(auth.AllRoles...)
	roleReaders  = roleSet(auth.RoleOwner, auth.RoleFamilyManager, auth.RoleFamilyMember, auth.RoleViewer)
	roleSitters  = roleSet(auth.RoleOwner, auth.RoleFamilyManager, auth.RoleFamilyMember, auth.RoleViewer, auth.RoleHousesitter)
	roleFabric   = roleSet(auth.RoleOwner, auth.RoleFamilyManager, auth.RoleFamilyMember, auth.RoleViewer, auth.RoleVendor)
	roleFamily   = roleSet(auth.RoleOwner, auth.RoleFamilyManager, auth.RoleFamilyMember)
	roleSitterN  = roleSet(auth.RoleOwner, auth.RoleFamilyManager, auth.RoleFamilyMember, auth.RoleHousesitter)
	roleVendorN  = roleSet(auth.RoleOwner, auth.RoleFamilyManager, auth.RoleFamilyMember, auth.RoleVendor)
	roleManager  = roleSet(auth.RoleOwner, auth.RoleFamilyManager)
	roleNobody   = []string{}
)

// wantToolRoles is the policy decision for every registered tool: which
// household roles may call it at all. A tool whose result is trimmed for a
// role that may call it is marked "(trim)" in its comment; the trim itself is
// covered by the package's own tests and by
// TestMoneyAndPrivateToolsRefusedToOutsiders.
//
// This table is the inventory the ticket asks for. Keep it in step with
// auth.ToolPolicy (TestToolPolicyCoversEveryTool fails if a tool is missing)
// and with the catalog (architecture/mcp-tool-catalog.md).
var wantToolRoles = map[string][]string{
	// ---- reads everyone may call (trimmed where the role sees less) ----
	"agenda_get":        roleEveryone, // trim: bills (money), maintenance (own jobs), meals, lists
	"family_overview":   roleEveryone, // trim: people/ages, bills_due, counts, recent_changes, own jobs
	"asset_find":        roleEveryone, // trim: purchase_price/current_value are #4808's
	"property_find":     roleEveryone, // trim: mortgage/tax/insurance are #4808's
	"notification_find": roleEveryone, // trim: maintenance_due limited to a linked vendor's own jobs
	"search_all":        roleEveryone, // trim: only the areas the role may read

	// ---- reads gated on a resource ----
	"activity_find":           roleReaders,
	"bill_find":               roleReaders, // money
	"budget_summary":          roleReaders, // money
	"loan_find":               roleReaders, // money
	"document_find":           roleReaders,
	"document_get":            roleReaders,
	"credential_find":         roleFamily, // no credentials for viewer either
	"credential_reveal":       roleFamily,
	"vehicle_find":            roleReaders, // VIN, value, related loans and bills
	"calendar_find":           roleSitters,
	"calendar_find_free_time": roleSitters,
	"event_find":              roleSitters, // trim: bill mirrors are #4808's
	"contact_find":            roleSitters,
	"guide_find":              roleSitters,
	"guide_get":               roleSitters,
	"kb_get":                  roleEveryone,
	"kb_list":                 roleEveryone,
	"kb_search":               roleEveryone,
	"list_find":               roleSitters,
	"list_get":                roleSitters,
	"meal_suggest":            roleSitters,
	"recipe_find":             roleSitters,
	"recipe_get":              roleSitters,
	"note_find":               roleSitters, // trim: notes on hidden records omitted
	"note_get":                roleSitters, // trim: a note on a hidden record is not found
	"maintenance_find":        roleFabric,  // trim: a linked vendor sees only their own jobs
	"person_find":             roleSitters, // trim: name/relationship/allergy only for a housesitter
	"person_get":              roleSitters, // trim: same
	"pet_find":                roleSitters,

	// ---- writes ----
	"asset_create":        roleFamily,
	"asset_delete":        roleFamily,
	"asset_update":        roleFamily,
	"bill_create":         roleFamily,
	"bill_delete":         roleFamily,
	"bill_pay":            roleFamily,
	"bill_update":         roleFamily,
	"budget_set_category": roleFamily,
	"calendar_create":     roleFamily,
	"contact_create":      roleFamily,
	"contact_delete":      roleFamily,
	"contact_update":      roleFamily,
	"document_delete":     roleFamily,
	"document_update":     roleFamily,
	"document_upload":     roleFamily,
	"event_create":        roleFamily,
	"event_delete":        roleFamily,
	"event_update":        roleFamily,
	"family_update":       roleManager, // #4820: renaming the family or setting its timezone is a management act
	"guide_create":        roleFamily,
	"guide_delete":        roleFamily,
	"guide_update":        roleFamily,
	"kb_write":            roleFamily,
	"list_create":         roleFamily,
	"list_remove_items":   roleFamily,
	"list_update":         roleFamily,
	"list_update_item":    roleFamily,
	"loan_create":         roleFamily,
	"loan_delete":         roleFamily,
	"loan_update":         roleFamily,
	"maintenance_create":  roleFamily,
	"maintenance_delete":  roleFamily,
	"meal_rate":           roleFamily,
	"meal_set":            roleFamily,
	"note_delete":         roleFamily,
	"note_update":         roleFamily,
	"person_create":       roleFamily,
	"person_delete":       roleFamily,
	"person_update":       roleFamily,
	"pet_create":          roleFamily,
	"pet_delete":          roleFamily,
	"pet_update":          roleFamily,
	"property_create":     roleFamily,
	"property_delete":     roleFamily,
	"property_update":     roleFamily,
	"recipe_create":       roleFamily,
	"recipe_delete":       roleFamily,
	"recipe_update":       roleFamily,
	"room_delete":         roleFamily,
	"vehicle_create":      roleFamily,
	"vehicle_delete":      roleFamily,
	"vehicle_update":      roleFamily,

	// ---- narrow writes the role policy allows ----
	"list_add_items":          roleSitterN,  // a housesitter may add items
	"list_check_items":        roleSitterN,  // a housesitter may check them off
	"note_create":             roleSitterN,  // a housesitter may note a pet (the handler checks the kind)
	"maintenance_complete":    roleVendorN,  // a vendor may mark a job done
	"maintenance_update":      roleVendorN,  // a vendor may change status/notes of their own jobs
	"meal_build_grocery_list": roleSitterN,  // reads meals and adds list items; a sitter may not start a list
	"notification_mark_read":  roleEveryone, // one's own notifications
}

// servedTools registers everything exactly as main does and returns the
// served tool names.
func servedTools(t *testing.T) []string {
	t.Helper()
	srv := server.NewServer(&config.Config{}, nil, nil)
	registerAll(srv, &config.Config{}, nil)
	var names []string
	for name := range srv.MCPServer().ListTools() {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestToolPolicyCoversEveryTool is the leak guard: a registered tool with no
// auth.ToolPolicy entry, or a policy entry for a tool that is not served,
// fails here. The runtime gate refuses a tool with no entry, so this test is
// what turns "refused at runtime, silently" into a failing build.
func TestToolPolicyCoversEveryTool(t *testing.T) {
	t.Parallel()
	served := servedTools(t)

	var missing []string
	for _, name := range served {
		if _, ok := auth.RequirementFor(name); !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d registered tool(s) have no auth.ToolPolicy entry, so the role policy gate refuses them: %v\nAdd an entry in apps/mcp/internal/auth/toolpolicy.go and a row in wantToolRoles (apps/mcp/cmd/mcp/toolpolicy_test.go).",
			len(missing), missing)
	}
	var extra []string
	for name := range auth.ToolPolicy {
		if !slices.Contains(served, name) {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("auth.ToolPolicy has %d entries for tools that are not registered: %v", len(extra), extra)
	}
}

// TestToolPolicyMatrix is the inventory: for every registered tool and every
// role, the gate's answer must equal wantToolRoles. This is where a policy
// change is reviewed, and where a tool that quietly widened (or narrowed) its
// access shows up.
func TestToolPolicyMatrix(t *testing.T) {
	t.Parallel()
	served := servedTools(t)
	if len(served) != len(wantTools) {
		t.Fatalf("registry serves %d tools, wantTools lists %d", len(served), len(wantTools))
	}
	for _, name := range served {
		want, ok := wantToolRoles[name]
		if !ok {
			t.Errorf("%s has no row in wantToolRoles: add its policy decision", name)
			continue
		}
		req, ok := auth.RequirementFor(name)
		if !ok {
			t.Errorf("%s has no auth.ToolPolicy entry", name)
			continue
		}
		var got []string
		for _, role := range allRoles {
			if req.Allows(role) {
				got = append(got, role)
			}
		}
		sort.Strings(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: roles %v may call it, want %v", name, got, want)
		}
	}
}

// TestMoneyAndPrivateToolsRefusedToOutsiders pins the specific leaks #4717
// closes, so a later table edit cannot quietly reopen one.
func TestMoneyAndPrivateToolsRefusedToOutsiders(t *testing.T) {
	t.Parallel()
	money := []string{"bill_find", "budget_summary", "loan_find", "vehicle_find"}
	private := []string{"credential_find", "credential_reveal"}
	for _, name := range money {
		req, ok := auth.RequirementFor(name)
		if !ok {
			t.Fatalf("%s has no policy entry", name)
		}
		for _, role := range []string{auth.RoleHousesitter, auth.RoleVendor} {
			if req.Allows(role) {
				t.Errorf("%s is money and must be refused to %s", name, role)
			}
		}
	}
	for _, name := range private {
		req, _ := auth.RequirementFor(name)
		for _, role := range []string{auth.RoleHousesitter, auth.RoleVendor, auth.RoleViewer} {
			if req.Allows(role) {
				t.Errorf("%s is private and must be refused to %s", name, role)
			}
		}
	}
	// Documents and the activity feed are readers-only: a housesitter and a
	// vendor get neither, a viewer gets both.
	for _, name := range []string{"activity_find", "document_find", "document_get"} {
		req, _ := auth.RequirementFor(name)
		for _, role := range []string{auth.RoleHousesitter, auth.RoleVendor} {
			if req.Allows(role) {
				t.Errorf("%s is readers-only and must be refused to %s", name, role)
			}
		}
		if !req.Allows(auth.RoleViewer) {
			t.Errorf("%s must stay readable by a viewer", name)
		}
	}
	// The entry points stay open to every role: they trim instead.
	for _, name := range []string{"family_overview", "agenda_get", "notification_find", "search_all"} {
		req, ok := auth.RequirementFor(name)
		if !ok {
			t.Fatalf("%s has no policy entry", name)
		}
		for _, role := range allRoles {
			if !req.Allows(role) {
				t.Errorf("%s must stay callable by %s (it trims, it does not refuse)", name, role)
			}
		}
	}
	// A vendor sees no people at all; a housesitter sees names only.
	people, _ := auth.RequirementFor("person_find")
	if people.Allows(auth.RoleVendor) {
		t.Error("person_find must be refused to a vendor")
	}
	if !people.Allows(auth.RoleHousesitter) {
		t.Error("person_find must stay callable by a housesitter (names only)")
	}
	// The maintenance tools: a vendor keeps its narrow write, a housesitter
	// keeps none.
	if !mustReq(t, "maintenance_complete").Allows(auth.RoleVendor) {
		t.Error("maintenance_complete must be callable by a vendor (own jobs)")
	}
	if mustReq(t, "maintenance_complete").Allows(auth.RoleHousesitter) {
		t.Error("maintenance_complete must be refused to a housesitter")
	}
}

func mustReq(t *testing.T, name string) auth.Requirement {
	t.Helper()
	req, ok := auth.RequirementFor(name)
	if !ok {
		t.Fatalf("%s has no policy entry", name)
	}
	return req
}

// TestNarrowWritesReachTheRightRoles proves the gate lets the writes the role
// policy allows through for the roles it allows, and refuses them to the
// others: the blanket "read-only role" refusal is gone.
func TestNarrowWritesReachTheRightRoles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tool string
		role string
		want bool
	}{
		{"list_check_items", auth.RoleHousesitter, true},
		{"list_add_items", auth.RoleHousesitter, true},
		{"note_create", auth.RoleHousesitter, true},
		{"maintenance_complete", auth.RoleVendor, true},
		{"maintenance_update", auth.RoleVendor, true},
		// ... and not the writes beyond the narrow grant.
		{"list_create", auth.RoleHousesitter, false},
		{"list_update_item", auth.RoleHousesitter, false},
		{"list_remove_items", auth.RoleHousesitter, false},
		{"maintenance_create", auth.RoleVendor, false},
		{"maintenance_delete", auth.RoleVendor, false},
		{"note_update", auth.RoleHousesitter, false},
		// A viewer gets none of them.
		{"list_check_items", auth.RoleViewer, false},
		{"note_create", auth.RoleViewer, false},
	}
	for _, tc := range cases {
		if got := mustReq(t, tc.tool).Allows(tc.role); got != tc.want {
			t.Errorf("%s for %s = %v, want %v", tc.tool, tc.role, got, tc.want)
		}
	}
}

// TestGateRefusesUnknownToolAtRuntime proves the runtime gate (not just the
// test) refuses a tool with no policy entry, so a tool registered in a build
// without its table entry cannot be served to anyone.
func TestGateRefusesUnknownToolAtRuntime(t *testing.T) {
	t.Parallel()
	s := server.NewServer(&config.Config{}, nil, nil)
	ran := false
	s.RegisterTool("unclassified_tool", mcp.NewTool("unclassified_tool", mcp.WithDescription("x")),
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			ran = true
			return mcp.NewToolResultText("ran"), nil
		})
	ctx := server.ContextWithClaims(context.Background(), &auth.Claims{UserID: "u", HouseholdID: "h", Role: auth.RoleOwner})
	msg, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": mcp.MethodToolsCall,
		"params": map[string]any{"name": "unclassified_tool", "arguments": map[string]any{}},
	})
	out, _ := json.Marshal(s.MCPServer().HandleMessage(ctx, msg))
	if ran {
		t.Fatal("a tool with no policy entry ran")
	}
	if !json.Valid(out) {
		t.Fatalf("response is not JSON: %s", out)
	}
}
