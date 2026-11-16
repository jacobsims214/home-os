package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"home-os/mcp/internal/config"
	"home-os/mcp/internal/server"
)

// catalogToolNames is every tool name the catalog knows
// (architecture/mcp-tool-catalog.md): the v2 area_verb names, built or not,
// and the v1 names they replace. A string literal in internal/ that equals
// one of these must be a registered tool, because it is either a next-step
// hint, an `open` tool, or text a tool description serves.
//
// The meal_* names are not
// registered yet. When the ticket that registers one adds it
// to wantTools (protocol_test.go), hints and `open` maps may name it again:
// this test then passes for it without any change here.
var catalogToolNames = []string{
	// catalog v2
	"activity_find", "agenda_get",
	"asset_create", "asset_delete", "asset_find", "asset_update",
	"bill_create", "bill_delete", "bill_find", "bill_pay", "bill_update",
	"budget_set_category", "budget_summary",
	"calendar_create", "calendar_find", "calendar_find_free_time",
	"contact_create", "contact_delete", "contact_find", "contact_update",
	"credential_find", "credential_reveal",
	"document_delete", "document_find", "document_get", "document_update", "document_upload",
	"event_create", "event_delete", "event_find", "event_update",
	"family_overview", "family_update",
	"guide_create", "guide_delete", "guide_find", "guide_get", "guide_update",
	"list_add_items", "list_check_items", "list_create", "list_find", "list_get",
	"list_remove_items", "list_update", "list_update_item",
	"loan_create", "loan_delete", "loan_find", "loan_update",
	"maintenance_complete", "maintenance_create", "maintenance_delete", "maintenance_find", "maintenance_update",
	"meal_build_grocery_list", "meal_rate", "meal_set", "meal_suggest",
	"note_create", "note_delete", "note_find", "note_get", "note_update",
	"notification_find", "notification_mark_read",
	"person_create", "person_delete", "person_find", "person_get", "person_update",
	"pet_create", "pet_delete", "pet_find", "pet_update",
	"property_create", "property_delete", "property_find", "property_update",
	"recipe_create", "recipe_delete", "recipe_find", "recipe_get", "recipe_update",
	"room_delete", "search_all",
	"vehicle_create", "vehicle_delete", "vehicle_find", "vehicle_update",
	// v1 names
	"check_conflicts", "create_asset", "create_bill", "create_event", "create_loan",
	"create_maintenance_task", "create_note", "delete_asset", "delete_bill", "delete_event",
	"delete_loan", "delete_note", "find_available_slots", "get_asset", "get_bill_summary",
	"get_daily_briefing", "get_file", "get_financial_summary", "get_household", "get_loan",
	"get_note", "get_pet", "get_property", "get_unread_count", "get_upcoming_bills",
	"get_upcoming_maintenance", "get_vehicle", "get_vendor", "list_assets", "list_bills",
	"list_calendars", "list_events", "list_files", "list_loans", "list_maintenance_tasks",
	"list_members", "list_notes", "list_notifications", "list_pets", "list_properties",
	"list_vehicles", "list_vendors", "mark_bill_paid", "search_files", "update_asset",
	"update_bill", "update_event", "update_household", "update_loan", "update_note",
	"upload_file",
}

// toolDescriptions returns tool name → its description plus every input
// property's description, as tools/list serves them.
func toolDescriptions(t *testing.T, srv *server.Server) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, st := range srv.MCPServer().ListTools() {
		var b strings.Builder
		b.WriteString(st.Tool.Description + "\n")
		for _, p := range st.Tool.InputSchema.Properties {
			if m, ok := p.(map[string]any); ok {
				if d, ok := m["description"].(string); ok {
					b.WriteString(d + "\n")
				}
			}
		}
		out[name] = b.String()
	}
	return out
}

// sourceLiteral is one Go string literal and where it is.
type sourceLiteral struct {
	pos  string
	text string
}

// internalLiterals parses every non-test Go file under internal/ and returns
// its string literals. Next-step hints and `open` tools are built from these,
// so scanning them covers every hint a tool can emit without calling it.
func internalLiterals(t *testing.T) []sourceLiteral {
	t.Helper()
	var out []sourceLiteral
	fset := token.NewFileSet()
	root := filepath.Join("..", "..", "internal")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				s = lit.Value
			}
			out = append(out, sourceLiteral{pos: fset.Position(lit.Pos()).String(), text: s})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatalf("no string literals found under %s", root)
	}
	return out
}

// unregisteredCatalogTools returns the catalog tool names in text that the
// server doesn't register.
func unregisteredCatalogTools(text string, registered map[string]bool) []string {
	var bad []string
	for _, name := range catalogToolNames {
		if registered[name] || slices.Contains(bad, name) {
			continue
		}
		if regexp.MustCompile(`\b` + name + `\b`).MatchString(text) {
			bad = append(bad, name)
		}
	}
	return bad
}

// withoutFieldNames drops id/type field names such as pet_id or event_type:
// descriptions name output fields, which are not tools.
func withoutFieldNames(names []string) []string {
	var out []string
	for _, n := range names {
		if !strings.HasSuffix(n, "_id") && !strings.HasSuffix(n, "_ids") && !strings.HasSuffix(n, "_type") {
			out = append(out, n)
		}
	}
	return out
}

// TestToolTextNamesOnlyRegisteredTools fails when a tool description, a
// param description, or a string literal a tool can emit as a next-step hint
// or `open` tool names a tool tools/list doesn't serve. Small models call
// whatever they are told to and fail on an unknown tool.
func TestToolTextNamesOnlyRegisteredTools(t *testing.T) {
	t.Parallel()
	srv := server.NewServer(&config.Config{}, nil, nil)
	registerAll(srv, &config.Config{}, nil)
	tools := srv.MCPServer().ListTools()
	params := paramNames(t, srv)
	registered := map[string]bool{}
	for name := range tools {
		registered[name] = true
	}

	for name, text := range toolDescriptions(t, srv) {
		if bad := withoutFieldNames(unknownTools(text, tools, params)); len(bad) > 0 {
			t.Errorf("tool %s description names unregistered tools %v", name, bad)
		}
	}

	for _, lit := range internalLiterals(t) {
		if bad := unregisteredCatalogTools(lit.text, registered); len(bad) > 0 {
			t.Errorf("%s: string %q names unregistered tools %v", lit.pos, lit.text, bad)
		}
	}
}

// TestUnregisteredCatalogToolsDetects proves the literal scanner flags an
// unregistered catalog tool and leaves registered tools, param names and
// column names alone.
func TestUnregisteredCatalogToolsDetects(t *testing.T) {
	t.Parallel()
	registered := map[string]bool{"note_create": true, "contact_find": true}
	tests := []struct {
		text string
		want []string
	}{
		{"note_create", nil},
		{"call contact_find with contact_id", nil},
		{"SELECT list_id, maintenance_tasks FROM list_items", nil},
		{"list_create", []string{"list_create"}},
		{"Add them with contact_create, then recipe_get.", []string{"contact_create", "recipe_get"}},
		{"Call list_events.", []string{"list_events"}},
	}
	for _, tt := range tests {
		if got := unregisteredCatalogTools(tt.text, registered); !slices.Equal(got, tt.want) {
			t.Errorf("unregisteredCatalogTools(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}
