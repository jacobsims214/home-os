package credential

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// Known-answer vector shared with apps/api/internal/secret/agentshare_test.go
// (computed independently with Node.js crypto: hkdfSync + aes-256-gcm). The
// API seals, the MCP opens: both must agree on these exact bytes.
func TestAgentKey_KnownAnswerMatchesAPI(t *testing.T) {
	k := &AgentKey{master: bytes.Repeat([]byte{0x42}, 32)}
	hid := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	sid := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	plain := []byte(`{"password":"hunter2","username":"dana"}`)
	const want = "016e6f6e63652d31326279746559ef0ffcbfcafe5b17cbb172e1fb130a79a20a0b973fb4a95f6a76157ccd86cd0f67d44e9095d12f4f59650431465562e6b8612b3b88e763"
	blob, err := k.sealWithNonce(hid, sid, []byte("nonce-12byte"), plain)
	if err != nil || hex.EncodeToString(blob) != want {
		t.Fatalf("seal = %x, %v", blob, err)
	}
	b, _ := hex.DecodeString(want)
	got, err := k.Open(hid, sid, b)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("open: %v %q", err, got)
	}
	if _, err := k.Open(uuid.New(), sid, b); err == nil {
		t.Fatal("opened with another household's key")
	}
}

const (
	secretPassword = "Pl41n-Netflix-Pw!"
	secretUser     = "family@example.com"
)

type fixture struct {
	pool   *pgxpool.Pool
	key    *AgentKey
	hh     testutil.Household
	token  uuid.UUID
	logs   *syncBuffer
	find   func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	reveal func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.NewPool(t)
	_, hh := testutil.SeedHousehold(t, pool)
	key := &AgentKey{master: bytes.Repeat([]byte{9}, 32)}
	f := &fixture{pool: pool, key: key, hh: hh, logs: captureLogs(t)}
	f.token = insertID(t, pool, `INSERT INTO agent_tokens (user_id, household_id, name, token_prefix, token_hash, scopes)
		VALUES ($1, $2, 'Claude', 'hos_abcd', sha256(gen_random_uuid()::text::bytea), '{read}') RETURNING id`, hh.UserID, hh.HouseholdID)
	_, _, f.find = NewCredentialFindTool(pool)
	_, _, f.reveal = NewCredentialRevealTool(pool, key)
	return f
}

// agentCtx is the caller as the member's agent token with scopes.
func (f *fixture) agentCtx(scopes ...string) context.Context {
	c := *f.hh.Claims
	c.AgentTokenID = f.token.String()
	c.Scopes = scopes
	return server.ContextWithClaims(context.Background(), &c)
}

func (f *fixture) addSecret(t *testing.T, hid uuid.UUID, name string) uuid.UUID {
	t.Helper()
	return insertID(t, f.pool, `INSERT INTO secrets (household_id, entity_type, entity_id, encrypted_data, iv, name, secret_type)
		VALUES ($1, 'household', $1, '\x656e637279707465642d62792d62726f77736572', '\x313233343536373839303132', $2, 'login') RETURNING id`, hid, name)
}

// share does what PUT /api/v1/secrets/{id}/agent-share does.
func (f *fixture) share(t *testing.T, hid, sid uuid.UUID) {
	t.Helper()
	blob, err := f.key.Seal(hid, sid, []byte(`{"username":"`+secretUser+`","password":"`+secretPassword+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.pool, `UPDATE secrets SET agent_ciphertext = $2, agent_shared_at = now() WHERE id = $1`, sid, blob)
}

func (f *fixture) unshare(t *testing.T, sid uuid.UUID) {
	t.Helper()
	mustExec(t, f.pool, `UPDATE secrets SET agent_ciphertext = NULL, agent_shared_at = NULL, agent_shared_by = NULL WHERE id = $1`, sid)
}

type result struct {
	isError bool
	text    string
	env     struct {
		OK      bool `json:"ok"`
		Summary string
		Data    *Credential
		Items   []Credential
		Error   *struct{ Message, Fix string }
	}
}

func call(t *testing.T, h func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error), ctx context.Context, args map[string]any) result {
	t.Helper()
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	res, err := h(ctx, req)
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	var r result
	r.isError = res.IsError
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			r.text += tc.Text
		}
	}
	// The text block mirrors structuredContent exactly (respond package).
	if err := json.Unmarshal([]byte(r.text), &r.env); err != nil {
		t.Fatalf("result is not the respond envelope: %v\n%s", err, r.text)
	}
	return r
}

func (r result) hasValue() bool {
	return strings.Contains(r.text, secretPassword) || strings.Contains(r.text, secretUser)
}

func auditRows(t *testing.T, pool *pgxpool.Pool, hid uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT concat_ws('|', action, entity_type, entity_id::text, coalesce(actor_user_id::text, '-'),
		                  coalesce(actor_agent_token_id::text, '-'), summary)
		   FROM activity_log WHERE household_id = $1 AND action = 'read' ORDER BY id`, hid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestReveal_ShareReadUnshare(t *testing.T) {
	f := newFixture(t)
	sid := f.addSecret(t, f.hh.HouseholdID, "Netflix")
	ctx := f.agentCtx(auth.ScopeRead)

	// Not shared yet: no value, says where to find it, nothing audited.
	r := call(t, f.reveal, ctx, map[string]any{"credential_id": sid.String()})
	if !r.isError || r.hasValue() || !strings.Contains(r.text, "Home OS → Credentials → Netflix") {
		t.Fatalf("unshared reveal: %s", r.text)
	}
	if n := len(auditRows(t, f.pool, f.hh.HouseholdID)); n != 0 {
		t.Fatalf("%d audit rows for a refused read", n)
	}

	// Shared: the agent reads the value, and the read is audited.
	f.share(t, f.hh.HouseholdID, sid)
	r = call(t, f.reveal, ctx, map[string]any{"credential_id": sid.String()})
	if r.isError || r.env.Data == nil || r.env.Data.Value["password"] != secretPassword || r.env.Data.Value["username"] != secretUser {
		t.Fatalf("shared reveal: %s", r.text)
	}
	if strings.Contains(r.env.Summary, secretPassword) {
		t.Fatal("summary echoes the value")
	}
	rows := auditRows(t, f.pool, f.hh.HouseholdID)
	want := strings.Join([]string{"read", "secret", sid.String(), f.hh.UserID.String(), f.token.String(), `revealed secret "Netflix"`}, "|")
	if len(rows) != 1 || rows[0] != want {
		t.Fatalf("audit rows = %q, want [%q]", rows, want)
	}

	// By exact name, case-insensitively, also works (and is audited).
	r = call(t, f.reveal, ctx, map[string]any{"credential_id": "netflix"})
	if r.isError || r.env.Data == nil || r.env.Data.Value["password"] != secretPassword {
		t.Fatalf("reveal by name: %s", r.text)
	}
	if n := len(auditRows(t, f.pool, f.hh.HouseholdID)); n != 2 {
		t.Fatalf("audit rows after second read = %d", n)
	}

	// Unshared again: no value.
	f.unshare(t, sid)
	r = call(t, f.reveal, ctx, map[string]any{"credential_id": sid.String()})
	if !r.isError || r.hasValue() {
		t.Fatalf("reveal after unshare: %s", r.text)
	}

	// No value anywhere in the log tables, notifications or process logs.
	var leaked int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM activity_log WHERE summary LIKE '%' || $1 || '%' OR summary LIKE '%' || $2 || '%')
		      + (SELECT count(*) FROM notifications WHERE title LIKE '%' || $1 || '%' OR body LIKE '%' || $1 || '%')`,
		secretPassword, secretUser).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("value found in %d activity/notification rows", leaked)
	}
	if out := f.logs.String(); strings.Contains(out, secretPassword) || strings.Contains(out, secretUser) {
		t.Fatalf("logs leak the value:\n%s", out)
	}
}

func TestReveal_Refusals(t *testing.T) {
	f := newFixture(t)
	sid := f.addSecret(t, f.hh.HouseholdID, "Wi-Fi")
	f.share(t, f.hh.HouseholdID, sid)

	// Another household: its own shared credential is invisible here.
	_, other := testutil.SeedHousehold(t, f.pool)
	otherSID := f.addSecret(t, other.HouseholdID, "Bank")
	f.share(t, other.HouseholdID, otherSID)
	r := call(t, f.reveal, f.agentCtx(auth.ScopeRead), map[string]any{"credential_id": otherSID.String()})
	if !r.isError || strings.Contains(r.text, "Bank") || r.hasValue() {
		t.Fatalf("cross-household reveal: %s", r.text)
	}
	if r := call(t, f.find, f.agentCtx(auth.ScopeRead), map[string]any{"query": "bank"}); len(r.env.Items) != 0 {
		t.Fatalf("cross-household find: %s", r.text)
	}

	// A copy moved onto another row (or household) fails to open.
	moved := f.addSecret(t, f.hh.HouseholdID, "Moved")
	mustExec(t, f.pool, `UPDATE secrets SET agent_ciphertext = (SELECT agent_ciphertext FROM secrets WHERE id = $2), agent_shared_at = now() WHERE id = $1`, moved, sid)
	if r := call(t, f.reveal, f.agentCtx(auth.ScopeRead), map[string]any{"credential_id": moved.String()}); !r.isError || r.hasValue() {
		t.Fatalf("moved blob opened: %s", r.text)
	}

	// An agent token without the read scope.
	if r := call(t, f.reveal, f.agentCtx(auth.ScopeWrite), map[string]any{"credential_id": sid.String()}); !r.isError || r.hasValue() {
		t.Fatalf("no read scope: %s", r.text)
	}

	// No server key configured.
	_, _, noKey := NewCredentialRevealTool(f.pool, nil)
	if r := call(t, noKey, f.agentCtx(auth.ScopeRead), map[string]any{"credential_id": sid.String()}); !r.isError || r.hasValue() {
		t.Fatalf("no key: %s", r.text)
	}

	// viewer, housesitter and vendor get nothing, not even names (#4689).
	for _, role := range []string{"viewer", "housesitter", "vendor"} {
		mustExec(t, f.pool, `UPDATE memberships SET role = $3 WHERE household_id = $1 AND user_id = $2`, f.hh.HouseholdID, f.hh.UserID, role)
		if r := call(t, f.reveal, f.agentCtx(auth.ScopeRead), map[string]any{"credential_id": sid.String()}); !r.isError || r.hasValue() {
			t.Fatalf("%s reveal: %s", role, r.text)
		}
		if r := call(t, f.find, f.agentCtx(auth.ScopeRead), map[string]any{}); !r.isError || strings.Contains(r.text, "Wi-Fi") {
			t.Fatalf("%s find: %s", role, r.text)
		}
	}
	// A removed member gets nothing either.
	mustExec(t, f.pool, `DELETE FROM memberships WHERE household_id = $1 AND user_id = $2`, f.hh.HouseholdID, f.hh.UserID)
	if r := call(t, f.reveal, f.agentCtx(auth.ScopeRead), map[string]any{"credential_id": sid.String()}); !r.isError || r.hasValue() {
		t.Fatalf("removed member reveal: %s", r.text)
	}

	if rows := auditRows(t, f.pool, f.hh.HouseholdID); len(rows) != 0 {
		t.Fatalf("refused reads were audited as reads: %q", rows)
	}
	if rows := auditRows(t, f.pool, other.HouseholdID); len(rows) != 0 {
		t.Fatalf("other household has audit rows: %q", rows)
	}
}

func TestFind_NamesAndWhereOnly(t *testing.T) {
	f := newFixture(t)
	shared := f.addSecret(t, f.hh.HouseholdID, "Netflix")
	f.addSecret(t, f.hh.HouseholdID, "Chase bank")
	f.share(t, f.hh.HouseholdID, shared)

	// Dex session (no agent token).
	ctx := server.ContextWithClaims(context.Background(), f.hh.Claims)
	r := call(t, f.find, ctx, map[string]any{})
	if r.isError || len(r.env.Items) != 2 {
		t.Fatalf("find: %s", r.text)
	}
	byName := map[string]Credential{}
	for _, c := range r.env.Items {
		byName[c.Name] = c
	}
	if !byName["Netflix"].SharedWithAgents || byName["Chase bank"].SharedWithAgents {
		t.Fatalf("shared flags: %+v", byName)
	}
	if byName["Chase bank"].Where != "Home OS → Credentials → Chase bank" || byName["Chase bank"].AboutType != "family" {
		t.Fatalf("where/about: %+v", byName["Chase bank"])
	}
	if r.hasValue() || strings.Contains(r.text, "encrypted-by-browser") || strings.Contains(r.text, "ZW5jcnlwdGVk") {
		t.Fatalf("find leaks a value or ciphertext: %s", r.text)
	}
	if !strings.Contains(r.text, "credential_reveal credential_id="+shared.String()) {
		t.Fatalf("no reveal hint for the shared credential: %s", r.text)
	}

	r = call(t, f.find, f.agentCtx(auth.ScopeRead), map[string]any{"query": "chase", "limit": 1})
	if r.isError || len(r.env.Items) != 1 || r.env.Items[0].Name != "Chase bank" {
		t.Fatalf("query: %s", r.text)
	}
	if r := call(t, f.find, ctx, map[string]any{"id": "not-a-uuid"}); !r.isError {
		t.Fatalf("bad id accepted: %s", r.text)
	}
	if r := call(t, f.find, ctx, map[string]any{"about_type": "family"}); r.isError || len(r.env.Items) != 2 {
		t.Fatalf("about_type family: %s", r.text)
	}
	if n := len(auditRows(t, f.pool, f.hh.HouseholdID)); n != 0 {
		t.Fatalf("find wrote %d read rows", n)
	}
}

// --- helpers ---

func insertID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("insert: %v", err)
	}
	return id
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}
