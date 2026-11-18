package household

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// envelope is the respond wire shape; Data is set for single-object
// results, Items/Total/Truncated/Narrow for lists.
type envelope struct {
	OK         bool            `json:"ok"`
	Summary    string          `json:"summary"`
	Data       json.RawMessage `json:"data"`
	Items      []personRow     `json:"items"`
	Total      int             `json:"total"`
	Truncated  bool            `json:"truncated"`
	Narrow     string          `json:"narrow"`
	NextCursor string          `json:"next_cursor"`
	Next       []string        `json:"next"`
	Error      *struct {
		Message string `json:"message"`
		Fix     string `json:"fix"`
		Field   string `json:"field"`
	} `json:"error"`
}

// info decodes Data as a family_update result.
func (e envelope) info(t *testing.T) householdInfo {
	t.Helper()
	var i householdInfo
	if len(e.Data) > 0 {
		if err := json.Unmarshal(e.Data, &i); err != nil {
			t.Fatalf("decode data: %v", err)
		}
	}
	return i
}

// outputSchema resolves the outputSchema a tool advertises.
func outputSchema(t *testing.T, tool mcp.Tool) *jsonschema.Resolved {
	t.Helper()
	b, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal tool: %v", err)
	}
	var raw struct {
		OutputSchema json.RawMessage `json:"outputSchema"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal tool: %v", err)
	}
	if len(raw.OutputSchema) == 0 {
		t.Fatalf("tool %s declares no outputSchema", tool.Name)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw.OutputSchema, &s); err != nil {
		t.Fatalf("unmarshal outputSchema: %v", err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve outputSchema: %v", err)
	}
	return rs
}

// callEnvelope runs a respond-based handler, checks the text block mirrors
// structuredContent and validates against the schema, and decodes it.
func callEnvelope(t *testing.T, ctx context.Context, h toolHandler, rs *jsonschema.Resolved, args map[string]any) (envelope, *mcp.CallToolResult) {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("handler returned a Go error (protocol fault): %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("want 1 content item, got %d", len(res.Content))
	}
	text := res.Content[0].(mcp.TextContent).Text
	sc, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structuredContent: %v", err)
	}
	if string(sc) != text {
		t.Errorf("text block %s != structuredContent %s", text, sc)
	}
	var instance any
	if err := json.Unmarshal([]byte(text), &instance); err != nil {
		t.Fatalf("decode text: %v", err)
	}
	if err := rs.Validate(instance); err != nil {
		t.Errorf("result does not match outputSchema: %v\n%s", err, text)
	}
	var env envelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.OK == res.IsError {
		t.Errorf("ok=%v but isError=%v", env.OK, res.IsError)
	}
	return env, res
}

// getHousehold reads the family through family_overview and returns it in
// the family_update result shape.
func getHousehold(t *testing.T, ctx context.Context, pool *pgxpool.Pool) householdInfo {
	t.Helper()
	ov := overviewOf(t, ctx, pool)
	return householdInfo{ID: ov.Family.ID, Name: ov.Family.Name, Timezone: ov.Family.Timezone, MemberCount: len(ov.People)}
}

// overviewOf runs family_overview and decodes its data.
func overviewOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool) overview {
	t.Helper()
	_, tool, h := NewFamilyOverviewTool(pool)
	env, _ := callEnvelope(t, ctx, h, outputSchema(t, tool), map[string]any{})
	if !env.OK {
		t.Fatalf("family_overview failed: %+v", env)
	}
	var ov overview
	if err := json.Unmarshal(env.Data, &ov); err != nil {
		t.Fatalf("decode family_overview: %v", err)
	}
	return ov
}

// addMember inserts a user with role into hh's household and returns a
// context carrying their claims.
func addMember(t *testing.T, pool *pgxpool.Pool, hh testutil.Household, role string) context.Context {
	t.Helper()
	ctx := context.Background()
	email := role + "-" + uuid.NewString()[:8] + "@test.homeos.local"
	var uid uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
		email, "Member "+role,
	).Scan(&uid); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
		hh.HouseholdID, uid, role,
	); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	return server.ContextWithClaims(ctx, &auth.Claims{
		Identity:    auth.Identity{Subject: uid.String(), Email: email},
		UserID:      uid.String(),
		HouseholdID: hh.HouseholdID.String(),
		Role:        role,
	})
}

func TestFamilyUpdate(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	otherCtx, other := testutil.SeedHousehold(t, pool)

	name, tool, h := NewFamilyUpdateTool(pool)
	if name != "family_update" || tool.Name != "family_update" {
		t.Fatalf("tool name = %q/%q, want family_update", name, tool.Name)
	}
	rs := outputSchema(t, tool)

	t.Run("annotations are class U", func(t *testing.T) {
		a := tool.Annotations
		if a.Title != "Update family" || a.ReadOnlyHint == nil || *a.ReadOnlyHint ||
			a.DestructiveHint == nil || *a.DestructiveHint ||
			a.IdempotentHint == nil || !*a.IdempotentHint ||
			a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("annotations = %+v", a)
		}
		if len(tool.InputSchema.Required) != 0 {
			t.Errorf("required = %v, want none", tool.InputSchema.Required)
		}
	})

	t.Run("owner sets America/New_York and family_overview shows it", func(t *testing.T) {
		env, _ := callEnvelope(t, ctx, h, rs, map[string]any{"timezone": "America/New_York"})
		if !env.OK {
			t.Fatalf("want ok, got %+v", env)
		}
		if env.info(t).Timezone != "America/New_York" || env.info(t).ID != hh.HouseholdID.String() || env.info(t).MemberCount != 1 {
			t.Errorf("data = %s", env.Data)
		}
		if !strings.Contains(env.Summary, "America/New_York") || len(env.Next) == 0 {
			t.Errorf("summary %q next %v", env.Summary, env.Next)
		}
		if got := getHousehold(t, ctx, pool).Timezone; got != "America/New_York" {
			t.Errorf("family_overview timezone = %q, want America/New_York", got)
		}
		// Other households are untouched.
		if got := getHousehold(t, otherCtx, pool); got.Timezone != "UTC" || got.ID != other.HouseholdID.String() {
			t.Errorf("other household changed: %+v", got)
		}
	})

	t.Run("name only keeps the timezone", func(t *testing.T) {
		env, _ := callEnvelope(t, ctx, h, rs, map[string]any{"name": "  The Sims Family  ", "timezone": ""})
		if !env.OK || env.info(t).Name != "The Sims Family" || env.info(t).Timezone != "America/New_York" {
			t.Fatalf("got %+v", env)
		}
		if strings.Contains(env.Summary, "timezone") {
			t.Errorf("summary mentions timezone though only name changed: %q", env.Summary)
		}
	})

	t.Run("repeat call is idempotent", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			env, _ := callEnvelope(t, ctx, h, rs, map[string]any{"timezone": "America/New_York"})
			if !env.OK || env.info(t).Timezone != "America/New_York" || env.info(t).Name != "The Sims Family" {
				t.Fatalf("call %d: %+v", i, env)
			}
		}
	})

	t.Run("family_manager may update", func(t *testing.T) {
		mctx := addMember(t, pool, hh, "family_manager")
		env, _ := callEnvelope(t, mctx, h, rs, map[string]any{"timezone": "Europe/London"})
		if !env.OK || env.info(t).Timezone != "Europe/London" || env.info(t).MemberCount != 2 {
			t.Fatalf("got %+v", env)
		}
	})

	invalid := []struct {
		name  string
		args  map[string]any
		field string
	}{
		{"invalid zone", map[string]any{"timezone": "Mars/Olympus_Mons"}, "timezone"},
		{"abbreviation", map[string]any{"timezone": "EST5EDT-not-a-zone"}, "timezone"},
		{"offset", map[string]any{"timezone": "-05:00"}, "timezone"},
		{"Local is not a zone", map[string]any{"timezone": "Local"}, "timezone"},
		{"path traversal", map[string]any{"timezone": "../../etc/passwd"}, "timezone"},
		{"nothing to change", map[string]any{}, ""},
		{"blank values", map[string]any{"name": "   ", "timezone": ""}, ""},
		{"name too long", map[string]any{"name": strings.Repeat("a", 256)}, "name"},
	}
	for _, tc := range invalid {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			env, res := callEnvelope(t, ctx, h, rs, tc.args)
			if env.OK || !res.IsError || env.Error == nil {
				t.Fatalf("want isError result, got %+v", env)
			}
			if env.Error.Field != tc.field || env.Error.Fix == "" {
				t.Errorf("error = %+v, want field %q with a fix", env.Error, tc.field)
			}
			if got := getHousehold(t, ctx, pool).Timezone; got != "Europe/London" {
				t.Errorf("timezone changed to %q by a rejected call", got)
			}
		})
	}

	for _, role := range []string{"family_member", "viewer", "housesitter", "vendor"} {
		t.Run("rejects "+role, func(t *testing.T) {
			mctx := addMember(t, pool, hh, role)
			env, res := callEnvelope(t, mctx, h, rs, map[string]any{"timezone": "Asia/Tokyo", "name": "Hijacked"})
			if env.OK || !res.IsError || env.Error == nil || !strings.Contains(env.Error.Message, "owner") {
				t.Fatalf("want permission error, got %+v", env)
			}
			if got := getHousehold(t, ctx, pool); got.Timezone != "Europe/London" || got.Name != "The Sims Family" {
				t.Errorf("household changed by %s: %+v", role, got)
			}
		})
	}

	t.Run("token role is not trusted over the database", func(t *testing.T) {
		mctx := addMember(t, pool, hh, "family_member")
		c := *server.ClaimsFromContext(mctx)
		c.Role = "owner"
		env, res := callEnvelope(t, server.ContextWithClaims(context.Background(), &c), h, rs,
			map[string]any{"timezone": "Asia/Tokyo"})
		if env.OK || !res.IsError {
			t.Fatalf("want permission error, got %+v", env)
		}
	})

	t.Run("owner of another household cannot reach this one", func(t *testing.T) {
		c := *server.ClaimsFromContext(otherCtx)
		c.HouseholdID = hh.HouseholdID.String()
		env, res := callEnvelope(t, server.ContextWithClaims(context.Background(), &c), h, rs,
			map[string]any{"timezone": "Asia/Tokyo"})
		if env.OK || !res.IsError {
			t.Fatalf("want permission error, got %+v", env)
		}
		if got := getHousehold(t, ctx, pool).Timezone; got != "Europe/London" {
			t.Errorf("timezone changed to %q", got)
		}
	})

	t.Run("no claims", func(t *testing.T) {
		env, res := callEnvelope(t, context.Background(), h, rs, map[string]any{"timezone": "Asia/Tokyo"})
		if env.OK || !res.IsError {
			t.Fatalf("want isError, got %+v", env)
		}
	})
}
