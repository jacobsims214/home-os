package secret

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
)

// Known-answer vector shared with apps/mcp/internal/credential/agentkey_test.go.
// Both modules must produce and open exactly these bytes. Computed
// independently with Node.js crypto (hkdfSync + aes-256-gcm).
var (
	katMaster    = bytes.Repeat([]byte{0x42}, 32)
	katHousehold = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	katSecret    = uuid.MustParse("22222222-2222-4222-8222-222222222222")
	katNonce     = []byte("nonce-12byte")
	katPlain     = []byte(`{"password":"hunter2","username":"dana"}`)
	katBlobHex   = "016e6f6e63652d31326279746559ef0ffcbfcafe5b17cbb172e1fb130a79a20a0b973fb4a95f6a76157ccd86cd0f67d44e9095d12f4f59650431465562e6b8612b3b88e763"
)

func TestAgentKey_KnownAnswer(t *testing.T) {
	k := &AgentKey{master: katMaster}
	blob, err := k.sealWithNonce(katHousehold, katSecret, katNonce, katPlain)
	if err != nil {
		t.Fatal(err)
	}
	got := hex.EncodeToString(blob)
	if got != katBlobHex {
		t.Fatalf("known-answer blob changed (update the MCP copy too):\n got  %s\n want %s", got, katBlobHex)
	}
	pt, err := k.Open(katHousehold, katSecret, blob)
	if err != nil || !bytes.Equal(pt, katPlain) {
		t.Fatalf("open: %v %q", err, pt)
	}
}

func TestAgentKey_BoundToHouseholdAndSecret(t *testing.T) {
	k := &AgentKey{master: katMaster}
	blob, err := k.Seal(katHousehold, katSecret, katPlain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("hunter2")) {
		t.Fatal("ciphertext contains the plaintext")
	}
	blob2, _ := k.Seal(katHousehold, katSecret, katPlain)
	if bytes.Equal(blob[1:13], blob2[1:13]) {
		t.Fatal("nonce reused across two seals")
	}
	if _, err := k.Open(uuid.New(), katSecret, blob); err == nil {
		t.Fatal("opened with another household's key")
	}
	if _, err := k.Open(katHousehold, uuid.New(), blob); err == nil {
		t.Fatal("opened as another secret")
	}
	other := &AgentKey{master: bytes.Repeat([]byte{0x43}, 32)}
	if _, err := other.Open(katHousehold, katSecret, blob); err == nil {
		t.Fatal("opened with another master key")
	}
	tampered := append([]byte{}, blob...)
	tampered[len(tampered)-1] ^= 1
	if _, err := k.Open(katHousehold, katSecret, tampered); err == nil {
		t.Fatal("opened a tampered blob")
	}
}

func TestParseAgentKey(t *testing.T) {
	raw := bytes.Repeat([]byte{7}, 32)
	for _, tc := range []struct {
		name, in   string
		ok, nilKey bool
	}{
		{"empty disables", "", true, true},
		{"std", base64.StdEncoding.EncodeToString(raw), true, false},
		{"raw url", base64.RawURLEncoding.EncodeToString(raw), true, false},
		{"short", base64.StdEncoding.EncodeToString(raw[:16]), false, true},
		{"not base64", "!!!not-base64!!!", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, err := ParseAgentKey(tc.in)
			if (err == nil) != tc.ok || (k == nil) != tc.nilKey {
				t.Fatalf("ParseAgentKey(%q) = %v, %v", tc.in, k, err)
			}
			if err != nil && strings.Contains(err.Error(), tc.in) && tc.in != "" {
				t.Fatal("error echoes the key")
			}
			if k != nil && strings.Contains(k.String(), "7") {
				t.Fatal("String() prints key material")
			}
		})
	}
}

func TestFailLimiter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newFailLimiter()
	l.now = func() time.Time { return now }
	hid, other := uuid.New(), uuid.New()
	for i := 0; i < verifyMaxFailures; i++ {
		if l.retryAfter(hid) != 0 {
			t.Fatalf("locked after %d failures", i)
		}
		l.fail(hid)
	}
	if l.retryAfter(hid) != verifyFailWindow {
		t.Fatalf("retryAfter = %v", l.retryAfter(hid))
	}
	if l.retryAfter(other) != 0 {
		t.Fatal("another household is locked too")
	}
	now = now.Add(verifyFailWindow)
	if l.retryAfter(hid) != 0 {
		t.Fatal("still locked after the window")
	}
	l.fail(hid)
	l.reset(hid)
	if _, ok := l.entries[hid]; ok {
		t.Fatal("reset kept the entry")
	}
}

// --- integration (TEST_DATABASE_URL) ---

type member struct {
	hid  uuid.UUID
	user uuid.UUID
	role string
}

func newMember(t *testing.T, pool *pgxpool.Pool, hid uuid.UUID, role string) member {
	t.Helper()
	var uid uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, name, password_hash) VALUES ($1, 'Dana', 'x') RETURNING id`,
		uuid.NewString()+"@test.homeos.local").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, uid) })
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`, hid, uid, role); err != nil {
		t.Fatal(err)
	}
	return member{hid: hid, user: uid, role: role}
}

// do runs fn as m with chi's {id} set and returns status + body.
func do(t *testing.T, fn http.HandlerFunc, m member, method, id string, body any) (int, string) {
	t.Helper()
	var rdr *bytes.Reader
	if s, ok := body.(string); ok {
		rdr = bytes.NewReader([]byte(s))
	} else {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "/", rdr)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", id)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
	ctx = middleware.WithClaims(ctx, &auth.Claims{UserID: m.user.String(), HouseholdID: m.hid.String(), Role: m.role})
	rec := httptest.NewRecorder()
	fn(rec, req.WithContext(ctx))
	return rec.Code, rec.Body.String()
}

// captureLogs sends slog output to a buffer for the rest of the test.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
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

const sharedPassword = "S3cr3t-Netflix-pw!"

func TestAgentShare_Lifecycle(t *testing.T) {
	pool := keyTestDB(t)
	ctx := context.Background()
	logs := captureLogs(t)
	key := &AgentKey{master: katMaster}
	h := NewHandler(NewRepo(pool)).WithAgentKey(key)

	hid := newHousehold(t, pool)
	owner := newMember(t, pool, hid, "owner")
	manager := newMember(t, pool, hid, "family_manager")
	kid := newMember(t, pool, hid, "family_member")
	stranger := newMember(t, pool, newHousehold(t, pool), "owner")

	code, body := do(t, h.CreateSecret, owner, http.MethodPost, "", map[string]any{
		"encrypted_data": []byte("browser-ciphertext"), "iv": []byte("123456789012"),
		"key_version": 1, "name": "Netflix", "secret_type": "login",
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var created struct{ Data Secret }
	_ = json.Unmarshal([]byte(body), &created)
	sid := created.Data.ID.String()
	if created.Data.SharedWithAgents {
		t.Fatal("a new secret is shared by default")
	}

	value := map[string]any{"value": map[string]any{"username": "dana@example.com", "password": sharedPassword}}

	// Who may share.
	if code, _ := do(t, h.ShareWithAgents, kid, http.MethodPut, sid, value); code != http.StatusForbidden {
		t.Fatalf("family_member share: %d", code)
	}
	if code, _ := do(t, h.ShareWithAgents, stranger, http.MethodPut, sid, value); code != http.StatusNotFound {
		t.Fatalf("other household share: %d", code)
	}
	if code, _ := do(t, NewHandler(NewRepo(pool)).ShareWithAgents, owner, http.MethodPut, sid, value); code != http.StatusServiceUnavailable {
		t.Fatalf("share without a key: %d", code)
	}
	for _, bad := range []any{map[string]any{}, map[string]any{"value": "pw"}, map[string]any{"value": map[string]any{}}, `{"value":` + strings.Repeat(" ", 10) + `[1]}`} {
		if code, body := do(t, h.ShareWithAgents, owner, http.MethodPut, sid, bad); code != http.StatusBadRequest {
			t.Fatalf("bad value %v: %d %s", bad, code, body)
		}
	}

	// Share.
	code, body = do(t, h.ShareWithAgents, manager, http.MethodPut, sid, value)
	if code != http.StatusOK || !strings.Contains(body, `"shared_with_agents":true`) {
		t.Fatalf("share: %d %s", code, body)
	}
	if strings.Contains(body, sharedPassword) || strings.Contains(body, "agent_ciphertext") {
		t.Fatalf("share response leaks the value: %s", body)
	}
	var blob []byte
	var by *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT agent_ciphertext, agent_shared_by FROM secrets WHERE id = $1`, sid).Scan(&blob, &by); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte(sharedPassword)) {
		t.Fatal("agent_ciphertext holds the plaintext")
	}
	if by == nil || *by != manager.user {
		t.Fatalf("agent_shared_by = %v", by)
	}
	pt, err := key.Open(hid, uuid.MustParse(sid), blob)
	if err != nil || !strings.Contains(string(pt), sharedPassword) {
		t.Fatalf("stored copy does not open: %v", err)
	}

	// Get/list show the flag but never the server copy.
	for _, fn := range []http.HandlerFunc{h.GetSecret, h.ListSecrets} {
		code, body := do(t, fn, kid, http.MethodGet, sid, nil)
		if code != http.StatusOK || !strings.Contains(body, `"shared_with_agents":true`) || strings.Contains(body, "agent_ciphertext") {
			t.Fatalf("read: %d %s", code, body)
		}
	}

	// A PATCH without agent_value drops the copy (the value may have changed).
	patch := map[string]any{"encrypted_data": []byte("new-ciphertext"), "iv": []byte("abcdefghijkl"), "name": "Netflix", "secret_type": "login"}
	code, body = do(t, h.UpdateSecret, kid, http.MethodPatch, sid, patch)
	if code != http.StatusOK || !strings.Contains(body, `"shared_with_agents":false`) {
		t.Fatalf("patch: %d %s", code, body)
	}
	assertNoServerCopy(t, pool, sid)

	// A family_member can't re-share through PATCH; an owner can.
	patch["agent_value"] = map[string]any{"password": sharedPassword + "2"}
	if code, _ := do(t, h.UpdateSecret, kid, http.MethodPatch, sid, patch); code != http.StatusForbidden {
		t.Fatalf("family_member patch with agent_value: %d", code)
	}
	code, body = do(t, h.UpdateSecret, owner, http.MethodPatch, sid, patch)
	if code != http.StatusOK || !strings.Contains(body, `"shared_with_agents":true`) {
		t.Fatalf("owner patch with agent_value: %d %s", code, body)
	}

	// Unshare deletes the copy immediately; again is a no-op.
	if code, _ := do(t, h.UnshareWithAgents, kid, http.MethodDelete, sid, nil); code != http.StatusForbidden {
		t.Fatalf("family_member unshare: %d", code)
	}
	if code, _ := do(t, h.UnshareWithAgents, stranger, http.MethodDelete, sid, nil); code != http.StatusNotFound {
		t.Fatalf("other household unshare: %d", code)
	}
	for i := 0; i < 2; i++ {
		code, body = do(t, h.UnshareWithAgents, owner, http.MethodDelete, sid, nil)
		if code != http.StatusOK || !strings.Contains(body, `"shared_with_agents":false`) {
			t.Fatalf("unshare #%d: %d %s", i, code, body)
		}
		assertNoServerCopy(t, pool, sid)
	}

	// The activity log names the credential and columns, never the value.
	var dump string
	if err := pool.QueryRow(ctx,
		`SELECT coalesce(string_agg(summary || ' ' || array_to_string(changed_fields, ','), E'\n'), '')
		   FROM activity_log WHERE household_id = $1`, hid).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dump, `secret "Netflix"`) || !strings.Contains(dump, "agent_ciphertext") {
		t.Fatalf("activity log missing share rows:\n%s", dump)
	}
	if strings.Contains(dump, sharedPassword) || strings.Contains(dump, "dana@example.com") {
		t.Fatalf("activity log leaks the value:\n%s", dump)
	}
	if out := logs.String(); strings.Contains(out, sharedPassword) || strings.Contains(out, "dana@example.com") {
		t.Fatalf("logs leak the value:\n%s", out)
	}
}

func assertNoServerCopy(t *testing.T, pool *pgxpool.Pool, sid string) {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM secrets WHERE id = $1 AND agent_ciphertext IS NULL AND agent_shared_at IS NULL AND agent_shared_by IS NULL`,
		sid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("server copy still present")
	}
}

func TestVerifyKey_422AndRateLimit(t *testing.T) {
	pool := keyTestDB(t)
	h := NewHandler(NewRepo(pool))
	hid := newHousehold(t, pool)
	right := sha256.Sum256([]byte("right"))
	wrong := sha256.Sum256([]byte("wrong"))

	// Not set up: 404, never 401.
	if code, _ := call(t, h.VerifyKey, hid, map[string]any{"verifier": right[:], "key_version": 1}); code != http.StatusNotFound {
		t.Fatalf("verify(not set up): %d", code)
	}
	if code, body := call(t, h.SetupKey, hid, map[string]any{"verifier": right[:], "key_salt": []byte("0123456789abcdef"), "key_version": 1}); code != http.StatusCreated {
		t.Fatalf("setup: %d %s", code, body)
	}

	code, body := call(t, h.VerifyKey, hid, map[string]any{"verifier": wrong[:], "key_version": 1})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "INVALID_MASTER_KEY") {
		t.Fatalf("verify(wrong): %d %s", code, body)
	}
	// A correct password resets the count.
	if code, _ := call(t, h.VerifyKey, hid, map[string]any{"verifier": right[:], "key_version": 1}); code != http.StatusOK {
		t.Fatalf("verify(right): %d", code)
	}
	for i := 0; i < verifyMaxFailures; i++ {
		if code, _ := call(t, h.VerifyKey, hid, map[string]any{"verifier": wrong[:], "key_version": 1}); code != http.StatusUnprocessableEntity {
			t.Fatalf("wrong attempt %d: %d", i+1, code)
		}
	}
	// Locked: even the right password waits.
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"verifier":"`+base64.StdEncoding.EncodeToString(right[:])+`","key_version":1}`))
	req = req.WithContext(middleware.WithClaims(req.Context(), &auth.Claims{HouseholdID: hid.String(), Role: "owner"}))
	rec := httptest.NewRecorder()
	h.VerifyKey(rec, req)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "TOO_MANY_ATTEMPTS") {
		t.Fatalf("locked verify: %d %q %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
	// Another household is unaffected.
	other := newHousehold(t, pool)
	if code, _ := call(t, h.SetupKey, other, map[string]any{"verifier": right[:], "key_salt": []byte("0123456789abcdef"), "key_version": 1}); code != http.StatusCreated {
		t.Fatal("setup other")
	}
	if code, _ := call(t, h.VerifyKey, other, map[string]any{"verifier": right[:], "key_version": 1}); code != http.StatusOK {
		t.Fatalf("other household verify: %d", code)
	}
}
