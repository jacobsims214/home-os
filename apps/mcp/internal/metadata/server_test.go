package metadata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"

	dexapi "github.com/dexidp/dex/api/v2"
	"github.com/google/uuid"
	"google.golang.org/grpc"

	"home-os/mcp/internal/config"
)

func TestResourceMetadataURL(t *testing.T) {
	tests := []struct {
		cfg  config.Config
		want string
	}{
		{config.Config{ResourceURL: "https://home.example/mcp"}, "https://home.example/.well-known/oauth-protected-resource/mcp"},
		{config.Config{ResourceURL: "https://mcp.example"}, "https://mcp.example/.well-known/oauth-protected-resource"},
		{config.Config{PublicBaseURL: "http://localhost:8000"}, "http://localhost:8000/.well-known/oauth-protected-resource/mcp"},
	}
	for _, tt := range tests {
		if got := ResourceMetadataURL(&tt.cfg); got != tt.want {
			t.Errorf("ResourceMetadataURL(%+v) = %q, want %q", tt.cfg, got, tt.want)
		}
	}
}

// TestHandler_Metadata: the metadata server (Envoy's target for
// /.well-known/oauth-*) serves the protected resource document at both paths
// with the configured resource and issuer, and the authorization server
// document with that issuer at the bare and the issuer path-inserted URL.
func TestHandler_Metadata(t *testing.T) {
	cfg := &config.Config{
		PublicBaseURL: "https://home.example",
		ResourceURL:   "https://home.example/mcp",
		DexIssuer:     "https://home.example/dex",
	}
	ts := httptest.NewServer(NewHandler(cfg))
	t.Cleanup(ts.Close)

	get := func(t *testing.T, path string, out any) {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", path, resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}

	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		var doc struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
			ClientID             string   `json:"client_id"`
		}
		get(t, path, &doc)
		if doc.Resource != cfg.ResourceURL || len(doc.AuthorizationServers) != 1 ||
			doc.AuthorizationServers[0] != cfg.DexIssuer || doc.ClientID != PreRegisteredClientID {
			t.Errorf("%s = %+v", path, doc)
		}
	}

	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-authorization-server/dex"} {
		var doc struct {
			Issuer                string `json:"issuer"`
			AuthorizationEndpoint string `json:"authorization_endpoint"`
			RegistrationEndpoint  string `json:"registration_endpoint"`
		}
		get(t, path, &doc)
		if doc.Issuer != cfg.DexIssuer || doc.AuthorizationEndpoint != "https://home.example/oauth/authorize" ||
			doc.RegistrationEndpoint != "https://home.example/oauth/register" {
			t.Errorf("%s = %+v", path, doc)
		}
	}
}

// fakeDex records CreateClient calls.
type fakeDex struct {
	mu    sync.Mutex
	reqs  []*dexapi.CreateClientReq
	dials int
}

func (f *fakeDex) CreateClient(_ context.Context, in *dexapi.CreateClientReq, _ ...grpc.CallOption) (*dexapi.CreateClientResp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, in)
	return &dexapi.CreateClientResp{Client: in.Client}, nil
}

func (f *fakeDex) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

// withFakeDex swaps dialDex for one returning dex and counting dials.
func withFakeDex(t *testing.T) *fakeDex {
	t.Helper()
	dex := &fakeDex{}
	orig := dialDex
	dialDex = func(string) (dexClientCreator, error) {
		dex.mu.Lock()
		dex.dials++
		dex.mu.Unlock()
		return dex, nil
	}
	t.Cleanup(func() { dialDex = orig })
	return dex
}

const validRegistration = `{"redirect_uris":["http://127.0.0.1:5555/cb"],"client_name":"opencode"}`

// register POSTs body to h's /register from remote, with optional XFF.
func register(h http.Handler, remote, xff, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body))
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error
}

func assertRateLimited(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	if n, err := strconv.Atoi(w.Header().Get("Retry-After")); err != nil || n < 1 {
		t.Errorf("Retry-After = %q, want a positive integer", w.Header().Get("Retry-After"))
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if code := errorCode(t, w); code != "temporarily_unavailable" {
		t.Errorf("error = %q, want temporarily_unavailable", code)
	}
}

// TestRegister_PerIPLimit: past the per-IP budget one client gets 429 with
// Retry-After before Dex is called; other IPs are unaffected.
func TestRegister_PerIPLimit(t *testing.T) {
	dex := withFakeDex(t)
	h := NewHandler(&config.Config{DexGRPCAddr: "dex:5557", RegisterPerMinute: 5, RegisterPerHour: 100})

	for i := range 5 {
		if w := register(h, "198.51.100.7:1000", "", validRegistration); w.Code != http.StatusCreated {
			t.Fatalf("request %d: status = %d, want 201 (%s)", i+1, w.Code, w.Body)
		}
	}
	assertRateLimited(t, register(h, "198.51.100.7:1001", "", validRegistration))
	// Invalid requests spend the per-IP budget too: still limited.
	assertRateLimited(t, register(h, "198.51.100.7:1002", "", `{}`))
	if got := dex.calls(); got != 5 {
		t.Errorf("Dex CreateClient calls = %d, want 5", got)
	}

	if w := register(h, "203.0.113.9:1000", "", validRegistration); w.Code != http.StatusCreated {
		t.Errorf("other IP: status = %d, want 201", w.Code)
	}
}

// TestRegister_ForwardedForOnlyFromTrustedProxies: X-Forwarded-For picks the
// bucket only when the peer is a trusted proxy.
func TestRegister_ForwardedForOnlyFromTrustedProxies(t *testing.T) {
	withFakeDex(t)
	h := NewHandler(&config.Config{
		DexGRPCAddr:       "dex:5557",
		RegisterPerMinute: 1,
		RegisterPerHour:   100,
		TrustedProxies:    []netip.Prefix{netip.MustParsePrefix("10.244.0.0/16")},
	})

	// Untrusted peer: rotating XFF values does not buy fresh buckets.
	if w := register(h, "198.51.100.7:1000", "1.1.1.1", validRegistration); w.Code != http.StatusCreated {
		t.Fatalf("first: status = %d", w.Code)
	}
	assertRateLimited(t, register(h, "198.51.100.7:1000", "2.2.2.2", validRegistration))

	// Trusted proxy: each forwarded client has its own bucket, and the
	// client-supplied leftmost entry is ignored.
	if w := register(h, "10.244.1.5:1000", "9.9.9.9, 203.0.113.9", validRegistration); w.Code != http.StatusCreated {
		t.Fatalf("via proxy, client A: status = %d", w.Code)
	}
	assertRateLimited(t, register(h, "10.244.1.5:1000", "8.8.8.8, 203.0.113.9", validRegistration))
	if w := register(h, "10.244.1.5:1000", "203.0.113.10", validRegistration); w.Code != http.StatusCreated {
		t.Errorf("via proxy, client B: status = %d, want 201", w.Code)
	}
}

// TestRegister_GlobalCeiling: the global ceiling caps Dex clients across
// IPs, and requests that fail validation don't spend it.
func TestRegister_GlobalCeiling(t *testing.T) {
	dex := withFakeDex(t)
	h := NewHandler(&config.Config{DexGRPCAddr: "dex:5557", RegisterPerMinute: 100, RegisterPerHour: 2})

	for i := range 5 {
		if w := register(h, "198.51.100.7:1000", "", `{"redirect_uris":["https://evil.example/cb"]}`); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid %d: status = %d, want 400", i, w.Code)
		}
	}
	for i, ip := range []string{"192.0.2.1", "192.0.2.2"} {
		if w := register(h, ip+":1000", "", validRegistration); w.Code != http.StatusCreated {
			t.Fatalf("valid %d: status = %d, want 201", i, w.Code)
		}
	}
	assertRateLimited(t, register(h, "192.0.2.3:1000", "", validRegistration))
	if got := dex.calls(); got != 2 {
		t.Errorf("Dex CreateClient calls = %d, want 2", got)
	}
}

// TestRegister_BodyAndNameCaps: bodies over 16 KiB get 413 and over-long
// client names 400, neither reaching Dex.
func TestRegister_BodyAndNameCaps(t *testing.T) {
	dex := withFakeDex(t)
	h := NewHandler(&config.Config{DexGRPCAddr: "dex:5557", RegisterPerMinute: 100, RegisterPerHour: 100})

	pad := strings.Repeat("a", MaxRegisterBodyBytes)
	big := `{"redirect_uris":["http://127.0.0.1/cb"],"software_id":"` + pad + `"}`
	w := register(h, "192.0.2.1:1000", "", big)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: status = %d, want 413", w.Code)
	}
	if code := errorCode(t, w); code != "invalid_request" {
		t.Errorf("oversized body: error = %q", code)
	}

	long := `{"redirect_uris":["http://127.0.0.1/cb"],"client_name":"` + strings.Repeat("é", MaxClientNameLen+1) + `"}`
	w = register(h, "192.0.2.1:1000", "", long)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("long client_name: status = %d, want 400", w.Code)
	}
	if code := errorCode(t, w); code != "invalid_client_metadata" {
		t.Errorf("long client_name: error = %q", code)
	}
	if got := dex.calls(); got != 0 {
		t.Errorf("Dex called %d times for rejected requests", got)
	}

	// Exactly at the caps is fine: 100 multi-byte characters.
	okName := `{"redirect_uris":["http://127.0.0.1/cb"],"client_name":"` + strings.Repeat("é", MaxClientNameLen) + `"}`
	if w := register(h, "192.0.2.1:1000", "", okName); w.Code != http.StatusCreated {
		t.Errorf("100-char client_name: status = %d, want 201", w.Code)
	}

	w = register(h, "192.0.2.1:1000", "", `{not json`)
	if w.Code != http.StatusBadRequest || errorCode(t, w) != "invalid_request" {
		t.Errorf("malformed JSON: status = %d", w.Code)
	}
}

// TestRegister_ReusesDexClient: one Dex client serves every request, and
// each registration gets a fresh mcp-<UUIDv7> id.
func TestRegister_ReusesDexClient(t *testing.T) {
	dex := withFakeDex(t)
	h := NewHandler(&config.Config{DexGRPCAddr: "dex:5557", RegisterPerMinute: 100, RegisterPerHour: 100})

	seen := map[string]bool{}
	for i := range 4 {
		w := register(h, "192.0.2.1:1000", "", validRegistration)
		if w.Code != http.StatusCreated {
			t.Fatalf("request %d: status = %d", i, w.Code)
		}
		var resp struct {
			ClientID   string `json:"client_id"`
			ClientName string `json:"client_name"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		id, err := uuid.Parse(strings.TrimPrefix(resp.ClientID, DynamicClientIDPrefix))
		if !strings.HasPrefix(resp.ClientID, DynamicClientIDPrefix) || err != nil || id.Version() != 7 {
			t.Errorf("client_id %q is not mcp-<uuidv7>", resp.ClientID)
		}
		if seen[resp.ClientID] {
			t.Errorf("client_id %q reused", resp.ClientID)
		}
		seen[resp.ClientID] = true
		if resp.ClientName != "opencode" {
			t.Errorf("client_name = %q", resp.ClientName)
		}
	}
	if dex.dials != 1 {
		t.Errorf("Dex client built %d times, want 1", dex.dials)
	}
	if got := dex.calls(); got != 4 {
		t.Errorf("CreateClient calls = %d, want 4", got)
	}
	if c := dex.reqs[0].Client; !c.Public || c.Name != "opencode" || len(c.RedirectUris) != 1 {
		t.Errorf("Dex client = %+v", c)
	}
}
