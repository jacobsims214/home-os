package main

import (
	"context"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"home-os/mcp/internal/config"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// hintToolRE matches snake_case words in a next hint.
var hintToolRE = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)

// writeToolsIn returns the catalog write tools (class C, U, D or X) a hint
// names.
func writeToolsIn(hint string) []string {
	var out []string
	for _, w := range hintToolRE.FindAllString(hint, -1) {
		if class, ok := wantToolClass[w]; ok && class != classR && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// TestReadOnlyRolesGetNoWriteHints calls every read tool (catalog class R)
// over the real transport as a viewer, a housesitter and a vendor, and
// fails if any next hint names a write tool (#4771): small models follow
// next hints literally, and every write tool is refused to these roles. The
// owner's guide_get is the control: it does suggest maintenance_create, so
// the check would see a leak.
func TestReadOnlyRolesGetNoWriteHints(t *testing.T) {
	pool := testutil.NewPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	seedBusyFamily(t, pool, hh.HouseholdID, hh.UserID, 1)
	fx := loadFixtures(t, pool, hh.HouseholdID)
	agentKey, _ := seedSharedCredential(t, pool, hh.HouseholdID, &fx)

	cfg := &config.Config{
		PublicBaseURL:       "https://mcp.test",
		ResourceURL:         "https://mcp.test/mcp",
		DexIssuer:           testutil.TestIssuer,
		AcceptedAudiences:   []string{testutil.TestAudience},
		AllowedOrigins:      []string{"https://mcp.test"},
		AgentCredentialsKey: agentKey,
	}
	iss := testutil.NewTokenIssuer(t)
	srv := server.NewServer(cfg, pool, iss.Verifier(pool))
	registerAll(srv, cfg, pool)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	var reads []benchCase
	for _, bc := range benchCases(fx, time.Now()) {
		if wantToolClass[bc.tool] == classR {
			reads = append(reads, bc)
		}
	}
	if len(reads) < 20 {
		t.Fatalf("only %d read tools in benchCases", len(reads))
	}

	call := func(c *benchClient, tool string, args map[string]any) []string {
		t.Helper()
		var res struct {
			Structured struct {
				Next []string `json:"next"`
			} `json:"structuredContent"`
		}
		c.call("tools/call", map[string]any{"name": tool, "arguments": args}, &res)
		return res.Structured.Next
	}

	owner := &benchClient{t: t, url: ts.URL + "/mcp", token: iss.Token("hints-owner", hh.Email)}
	if next := call(owner, "guide_get", map[string]any{"guide_id": fx.Guide}); !slices.ContainsFunc(next, func(h string) bool {
		return slices.Contains(writeToolsIn(h), "maintenance_create")
	}) {
		t.Fatalf("control: owner guide_get next = %q, want a maintenance_create hint", next)
	}

	for _, role := range []string{"viewer", "housesitter", "vendor"} {
		t.Run(role, func(t *testing.T) {
			email := role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
			var uid uuid.UUID
			ctx := context.Background()
			if err := pool.QueryRow(ctx, `INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
				email, "Hints "+role).Scan(&uid); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
				hh.HouseholdID, uid, role); err != nil {
				t.Fatal(err)
			}
			c := &benchClient{t: t, url: ts.URL + "/mcp", token: iss.Token("hints-"+role, email)}
			for _, bc := range reads {
				for _, h := range call(c, bc.tool, bc.args(0)) {
					if w := writeToolsIn(h); len(w) > 0 {
						t.Errorf("%s %s: next hint %q names write tool(s) %s", role, bc.tool, h, strings.Join(w, ", "))
					}
				}
			}
		})
	}
}

// TestWriteToolsIn proves the scanner finds write tools and ignores read
// tools and field names.
func TestWriteToolsIn(t *testing.T) {
	t.Parallel()
	for hint, want := range map[string][]string{
		"Schedule this job with maintenance_create name=… due_date=… asset_id=1.": {"maintenance_create"},
		"Read the steps with guide_get guide_id=1.":                               nil,
		"Pass next_cursor as cursor, or call guide_update or guide_delete.":       {"guide_update", "guide_delete"},
	} {
		if got := writeToolsIn(hint); !slices.Equal(got, want) {
			t.Errorf("writeToolsIn(%q) = %q, want %q", hint, got, want)
		}
	}
}
