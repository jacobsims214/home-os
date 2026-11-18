// Package apicontract keeps the API's JSON wire contract explicit.
//
// The bug class it guards is a Go struct shipped to a client with no json
// tags: encoding/json then publishes the Go field names, so a response says
// {"ID","Name","Timezone"} and a UI reading data.timezone gets undefined —
// with no error on either side (#5300). #5297 (pgtype.Numeric losing its
// exponent) and #5301 (a field tagged json:"-") are the same class: a wire
// contract nobody ever defined.
//
// The test is deny-by-default. Every exported field of every exported struct
// under internal/ and pkg/ must carry a json tag, unless its struct is listed
// in untaggedByDesign below with the reason it never reaches a client. A new
// untagged struct therefore fails this test: decide what it is. Tag it (it is
// part of a response), or add it here with a reason.
package apicontract

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"home-os/api/internal/household"
)

// apiRoot is the api module root, relative to this package.
const apiRoot = "../.."

// untaggedByDesign is the audit of exported structs that deliberately carry no
// json tags because they are never serialized to a client. Each entry says
// why. A struct that starts being serialized must be tagged and removed from
// here; a new one must be added only after checking it never reaches a
// response.
//
// The categories:
//   - request:  decoded FROM a client (input), never encoded to one
//   - internal: a repository filter/result or config read from the environment
//   - db:       a database key or actor, used inside a query
//   - storage:  decoded from or encoded to a database column, not the wire
var untaggedByDesign = map[string]string{
	"activity.Entry":               "storage: scanned from the activity_log row it is read from, not the wire (handlers map it to entryResponse)",
	"activity.Filter":              "internal: repository filter/result or runtime config, never encoded",
	"activity.Page":                "internal: repository filter/result or runtime config, never encoded",
	"admin.Caller":                 "db: in-process database key or actor, never encoded",
	"admin.CreateUserInput":        "request: decoded from a client, never encoded to one",
	"admin.CreateUserResult":       "internal: repository filter/result or runtime config, never encoded",
	"agenttoken.Token":             "storage: decoded from or encoded to a database column, not the wire",
	"asset.Asset":                  "storage: decoded from or encoded to a database column, not the wire",
	"auth.Membership":              "storage: decoded from or encoded to a database column, not the wire",
	"auth.ResolvedMember":          "internal: repository filter/result or runtime config, never encoded",
	"auth.User":                    "storage: decoded from or encoded to a database column, not the wire",
	"bill.Bill":                    "storage: decoded from or encoded to a database column, not the wire",
	"budget.Category":              "storage: decoded from or encoded to a database column, not the wire",
	"budget.Obligation":            "internal: repository filter/result or runtime config, never encoded",
	"calendar.EventRange":          "internal: repository filter/result or runtime config, never encoded",
	"config.Config":                "internal: repository filter/result or runtime config, never encoded",
	"db.Actor":                     "db: in-process database key or actor, never encoded",
	"dex.TLSConfig":                "internal: repository filter/result or runtime config, never encoded",
	"file.ListFilter":              "internal: repository filter/result or runtime config, never encoded",
	"file.Patch":                   "request: decoded from a client, never encoded to one",
	"grocery.Ingredient":           "internal: repository filter/result or runtime config, never encoded",
	"grocery.Meal":                 "internal: repository filter/result or runtime config, never encoded",
	"guides.Guide":                 "storage: decoded from or encoded to a database column, not the wire",
	"guides.ListFilter":            "internal: repository filter/result or runtime config, never encoded",
	"guides.Revision":              "storage: decoded from or encoded to a database column, not the wire",
	"household.CreateFamilyResult": "internal: repository filter/result or runtime config, never encoded",
	"household.CreatorIdentity":    "internal: repository filter/result or runtime config, never encoded",
	"household.HouseholdUpdate":    "request: decoded from a client, never encoded to one",
	"household.MemberUpdate":       "request: decoded from a client, never encoded to one",
	"invite.AcceptIdentity":        "internal: repository filter/result or runtime config, never encoded",
	"invite.AcceptResult":          "internal: repository filter/result or runtime config, never encoded",
	"jsonopt.Field":                "internal: repository filter/result or runtime config, never encoded",
	"legacyfinance.BillFields":     "internal: repository filter/result or runtime config, never encoded",
	"legacyfinance.BillKind":       "internal: repository filter/result or runtime config, never encoded",
	"legacyfinance.InvalidError":   "internal: repository filter/result or runtime config, never encoded",
	"legacyfinance.LoanFields":     "internal: repository filter/result or runtime config, never encoded",
	"legacyfinance.LoanKind":       "internal: repository filter/result or runtime config, never encoded",
	"legacyfinance.Subject":        "internal: repository filter/result or runtime config, never encoded",
	"lists.Item":                   "storage: decoded from or encoded to a database column, not the wire",
	"lists.ItemPatch":              "request: decoded from a client, never encoded to one",
	"lists.List":                   "storage: decoded from or encoded to a database column, not the wire",
	"lists.ListFilter":             "internal: repository filter/result or runtime config, never encoded",
	"lists.ListPatch":              "request: decoded from a client, never encoded to one",
	"lists.MergeItem":              "storage: decoded from or encoded to a database column, not the wire",
	"lists.NewItem":                "request: decoded from a client, never encoded to one",
	"lists.NewList":                "request: decoded from a client, never encoded to one",
	"loan.Filter":                  "internal: repository filter/result or runtime config, never encoded",
	"loan.Loan":                    "storage: decoded from or encoded to a database column, not the wire",
	"maintenance.Schedule":         "storage: decoded from or encoded to a database column, not the wire",
	"maintenance.Task":             "storage: decoded from or encoded to a database column, not the wire",
	"meals.Feedback":               "storage: decoded from or encoded to a database column, not the wire",
	"meals.FeedbackInput":          "request: decoded from a client, never encoded to one",
	"meals.Meal":                   "storage: decoded from or encoded to a database column, not the wire",
	"meals.MealFilter":             "internal: repository filter/result or runtime config, never encoded",
	"meals.MealInput":              "request: decoded from a client, never encoded to one",
	"meals.Recipe":                 "storage: decoded from or encoded to a database column, not the wire",
	"meals.RecipeFilter":           "internal: repository filter/result or runtime config, never encoded",
	"meals.RecipeInput":            "request: decoded from a client, never encoded to one",
	"middleware.RouteRule":         "internal: repository filter/result or runtime config, never encoded",
	"note.Note":                    "storage: decoded from or encoded to a database column, not the wire",
	"people.Person":                "storage: decoded from or encoded to a database column, not the wire",
	"people.PersonInput":           "request: decoded from a client, never encoded to one",
	"people.Preference":            "storage: decoded from or encoded to a database column, not the wire",
	"people.PreferenceInput":       "request: decoded from a client, never encoded to one",
	"pet.Pet":                      "storage: decoded from or encoded to a database column, not the wire",
	"pet.VetRef":                   "storage: decoded from or encoded to a database column, not the wire",
	"property.Finance":             "internal: repository filter/result or runtime config, never encoded",
	"property.Room":                "storage: decoded from or encoded to a database column, not the wire",
	"searchrecon.ReconcileStats":   "internal: what a reindex run did, for the CLI and the sweep log; never encoded to a client",
	"search.SearchFilters":         "internal: repository filter/result or runtime config, never encoded",
	"searchtest.Env":               "internal: the Typesense test harness's own settings",
	"smtp.Config":                  "internal: repository filter/result or runtime config, never encoded",
	"vehicle.Finance":              "internal: repository filter/result or runtime config, never encoded",
	"vehicle.Vehicle":              "storage: decoded from or encoded to a database column, not the wire",
	"vendors.Vendor":               "storage: decoded from or encoded to a database column, not the wire",
}

// jsonTagName returns a struct field's json tag name and whether it has one.
func jsonTagName(tag string) (string, bool) {
	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		if tag == "" {
			break
		}
		i = 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' {
			i++
		}
		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			return "", false
		}
		key := tag[:i]
		tag = tag[i+1:]
		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}
			i++
		}
		if i >= len(tag) {
			return "", false
		}
		raw := tag[:i+1]
		tag = tag[i+1:]
		val, err := strconv.Unquote(raw)
		if err != nil {
			return "", false
		}
		if key == "json" {
			return strings.Split(val, ",")[0], true
		}
	}
	return "", false
}

// untaggedStructs walks the api module and returns, per exported struct, the
// exported fields that carry no json tag. It is keyed "pkg.Struct".
func untaggedStructs(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	for _, sub := range []string{"internal", "pkg"} {
		root := filepath.Join(apiRoot, sub)
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			pkg := f.Name.Name
			ast.Inspect(f, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || !ts.Name.IsExported() {
					return true
				}
				for _, fl := range st.Fields.List {
					tag := ""
					if fl.Tag != nil {
						tag, _ = strconv.Unquote(fl.Tag.Value)
					}
					_, has := jsonTagName(tag)
					if has {
						continue
					}
					for _, nm := range fl.Names {
						if !nm.IsExported() {
							continue
						}
						key := pkg + "." + ts.Name.Name
						rel, relErr := filepath.Rel(apiRoot, path)
						require.NoError(t, relErr)
						if _, ok := found[key]; !ok {
							found[key] = rel
						}
					}
				}
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}
	return found
}

// TestEveryExportedStructIsClassified fails when an exported struct with
// untagged exported fields is neither tagged nor listed as untagged-by-design.
func TestEveryExportedStructIsClassified(t *testing.T) {
	found := untaggedStructs(t)
	var unclassified []string
	for key, loc := range found {
		if _, ok := untaggedByDesign[key]; !ok {
			unclassified = append(unclassified, key+" ("+loc+")")
		}
	}
	sort.Strings(unclassified)
	assert.Empty(t, unclassified,
		"exported struct(s) with untagged fields and no classification. "+
			"Tag the fields if the struct is serialized to a client (#5300: untagged structs "+
			"publish Go field names, so a UI reading the lower-case name gets undefined); "+
			"otherwise add it to untaggedByDesign in this file with the reason it never "+
			"reaches a response.")
}

// TestUntaggedByDesignIsAccurate fails when the allowlist names a struct that
// no longer has untagged fields, so the audit cannot rot into a lie: tagging a
// struct means deleting its entry here.
func TestUntaggedByDesignIsAccurate(t *testing.T) {
	found := untaggedStructs(t)
	var stale []string
	for key := range untaggedByDesign {
		if _, ok := found[key]; !ok {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	assert.Empty(t, stale,
		"untaggedByDesign lists struct(s) that have no untagged exported fields any more "+
			"(tagged, renamed, or deleted). Remove them from the list: it is the audit, "+
			"and a stale entry hides a real one.")
}

// TestJSONTagsAreLowercase fails on a json tag that would publish a key a
// client has to guess the case of. Explicit snake_case is the convention every
// existing response follows.
func TestJSONTagsAreLowercase(t *testing.T) {
	var bad []string
	for _, sub := range []string{"internal", "pkg"} {
		root := filepath.Join(apiRoot, sub)
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					return true
				}
				for _, fl := range st.Fields.List {
					if fl.Tag == nil {
						continue
					}
					raw, _ := strconv.Unquote(fl.Tag.Value)
					name, has := jsonTagName(raw)
					if !has || name == "-" || name == "" {
						continue
					}
					if strings.ToLower(name) != name {
						rel, _ := filepath.Rel(apiRoot, path)
						bad = append(bad, rel+": "+ts.Name.Name+" field tagged json:\""+name+"\"")
					}
				}
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}
	sort.Strings(bad)
	assert.Empty(t, bad, "json tags must be lower_case (snake_case) so a client's key is never a guess")
}

// TestHouseholdWireKeys pins the exact response GET /api/v1/households/me
// returns. This is the endpoint #5300 broke: household.Household had no tags,
// so the body was {"ID","Name","Timezone",…} and the UI's data.timezone was
// undefined on every family. A source-level "has a tag" check would not have
// caught it only in the case where somebody tags it wrong, so the keys are
// asserted literally too.
func TestHouseholdWireKeys(t *testing.T) {
	hh := household.Household{
		ID:       uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Name:     "The Sims",
		Timezone: "America/Chicago",
		Settings: []byte(`{"theme":"dark"}`),
	}

	raw, err := json.Marshal(hh)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	assert.Equal(t, "The Sims", got["name"], "the UI reads data.name")
	assert.Equal(t, "America/Chicago", got["timezone"], "the UI reads data.timezone (the #5300 bug)")
	assert.Equal(t, "11111111-1111-1111-1111-111111111111", got["id"])

	for _, capitalised := range []string{"ID", "Name", "Timezone", "Settings", "CreatedAt", "UpdatedAt"} {
		assert.NotContains(t, got, capitalised,
			"the wire key must not be the Go field name: a client reading the lower-case name gets undefined")
	}

	// Settings is internal storage and as []byte would encode as base64.
	assert.NotContains(t, got, "settings", "households.settings is not serialized")

	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"created_at", "id", "name", "timezone", "updated_at"}, keys,
		"the exact key set of GET /api/v1/households/me")
}
