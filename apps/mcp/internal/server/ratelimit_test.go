package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// limitedPost sends a tools/call (or tools/list when tool is "") to /mcp
// with the given bearer token and X-Forwarded-For, and returns the response
// and its body with any SSE framing removed.
func limitedPost(t *testing.T, url, token, xff, tool string) (*http.Response, []byte) {
	t.Helper()
	method := mcp.MethodToolsList
	params := map[string]any{}
	if tool != "" {
		method = mcp.MethodToolsCall
		params["name"] = tool
		params["arguments"] = map[string]any{}
	}
	params["_meta"] = map[string]any{
		mcp.MetaKeyProtocolVersion:    mcp.ProtocolVersion20260728,
		mcp.MetaKeyClientInfo:         map[string]any{"name": "ratelimit-test", "version": "0"},
		mcp.MetaKeyClientCapabilities: map[string]any{},
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(mcp.HeaderProtocolVersion, mcp.ProtocolVersion20260728)
	req.Header.Set(mcp.HeaderMethod, string(method))
	if tool != "" {
		req.Header.Set(mcp.HeaderName, tool)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		for _, line := range strings.Split(string(b), "\n") {
			if data, ok := strings.CutPrefix(line, "data:"); ok {
				b = []byte(strings.TrimSpace(data))
			}
		}
	}
	return resp, b
}

// assertRateLimited checks a 429: Retry-After in whole seconds, no
// challenge, and a JSON-RPC error echoing the request id.
func assertRateLimited(t *testing.T, resp *http.Response, body []byte) {
	t.Helper()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body %s", resp.StatusCode, body)
	}
	if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err != nil || n < 1 {
		t.Errorf("Retry-After = %q, want a positive integer", resp.Header.Get("Retry-After"))
	}
	if v := resp.Header.Get("WWW-Authenticate"); v != "" {
		t.Errorf("429 carries WWW-Authenticate %q", v)
	}
	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				RetryAfterSeconds int `json:"retry_after_seconds"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		t.Fatalf("429 body is not JSON: %v: %s", err, body)
	}
	if rpc.JSONRPC != "2.0" || string(rpc.ID) != "7" || rpc.Error.Code != -32029 ||
		rpc.Error.Message == "" || rpc.Error.Data.RetryAfterSeconds < 1 {
		t.Errorf("429 body = %s, want a JSON-RPC error with id 7, code -32029 and retry_after_seconds", body)
	}
}

// TestRateLimit_PerPrincipal: a burst over the limit from one agent token
// gets 429; another agent token and a Dex session of the same member are
// unaffected.
func TestRateLimit_PerPrincipal(t *testing.T) {
	pool := testutil.NewPool(t)
	_, h := testutil.SeedHousehold(t, pool)
	iss := testutil.NewTokenIssuer(t)

	cfg := testConfig()
	cfg.RateLimitPerMinute = 60
	cfg.RateLimitBurst = 3
	s := server.NewServer(cfg, pool, iss.Verifier(pool))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	tokA, _ := seedAgentToken(t, pool, h, []string{"read"}, nil)
	tokB, _ := seedAgentToken(t, pool, h, []string{"read"}, nil)

	for i := range 3 {
		if resp, b := limitedPost(t, ts.URL, tokA, "", ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d within burst: status %d: %s", i+1, resp.StatusCode, b)
		}
	}
	resp, b := limitedPost(t, ts.URL, tokA, "", "")
	assertRateLimited(t, resp, b)

	if resp, b := limitedPost(t, ts.URL, tokB, "", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("another agent token was limited: status %d: %s", resp.StatusCode, b)
	}
	if resp, b := limitedPost(t, ts.URL, iss.Token("rl-sub", h.Email), "", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("the member's Dex session was limited by an agent token: status %d: %s", resp.StatusCode, b)
	}
}

// TestRateLimit_Unauthenticated: refused requests use up the client IP's
// budget, after which even a valid token from that IP waits; another IP is
// unaffected.
func TestRateLimit_Unauthenticated(t *testing.T) {
	pool := testutil.NewPool(t)
	_, h := testutil.SeedHousehold(t, pool)
	iss := testutil.NewTokenIssuer(t)

	cfg := testConfig()
	cfg.UnauthRateLimitPerMinute = 3
	// httptest connects from loopback: trust it so X-Forwarded-For picks
	// the client, as Traefik/Envoy do in production.
	cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	s := server.NewServer(cfg, pool, iss.Verifier(pool))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	const attacker, other = "198.51.100.7", "203.0.113.9"
	refusals := []string{"", "hos_not-a-real-token", "not-a-jwt"}
	for i, tok := range refusals {
		resp, b := limitedPost(t, ts.URL, tok, attacker, "")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("refusal %d: status %d, want 401: %s", i+1, resp.StatusCode, b)
		}
	}
	resp, b := limitedPost(t, ts.URL, "hos_another-guess", attacker, "")
	assertRateLimited(t, resp, b)
	resp, b = limitedPost(t, ts.URL, iss.Token("rl-sub", h.Email), attacker, "")
	assertRateLimited(t, resp, b)

	if resp, b := limitedPost(t, ts.URL, "", other, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("another IP was limited: status %d: %s", resp.StatusCode, b)
	}
	// Authenticated requests don't spend the IP budget.
	for i := range 5 {
		if resp, b := limitedPost(t, ts.URL, iss.Token("rl-sub", h.Email), other, ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("authenticated request %d from a clean IP: status %d: %s", i+1, resp.StatusCode, b)
		}
	}
}

// TestRateLimit_CredentialReveal: credential_reveal has a tighter
// per-principal sub-limit answered as an isError tool result; other tools
// and other principals keep working.
func TestRateLimit_CredentialReveal(t *testing.T) {
	pool := testutil.NewPool(t)
	_, h := testutil.SeedHousehold(t, pool)
	iss := testutil.NewTokenIssuer(t)

	cfg := testConfig()
	cfg.RevealRateLimitPerMinute = 2
	s := server.NewServer(cfg, pool, iss.Verifier(pool))
	reveals := 0
	ok := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if req.Params.Name == "credential_reveal" {
			reveals++
		}
		return mcp.NewToolResultText("ok"), nil
	}
	for _, name := range []string{"credential_reveal", "whoami"} {
		// Synthetic tools: classify them so the role policy gate lets the
		// owner through and the rate-limit behaviour is what this test sees.
		s.SetToolPolicy(name, auth.Requirement{})
		s.RegisterTool(name, mcp.NewTool(name, mcp.WithDescription(name),
			mcp.WithReadOnlyHintAnnotation(true), mcp.WithDestructiveHintAnnotation(false)), ok)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	tokA, _ := seedAgentToken(t, pool, h, []string{"read"}, nil)
	tokB, _ := seedAgentToken(t, pool, h, []string{"read"}, nil)

	type callResult struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	call := func(tok, tool string) callResult {
		t.Helper()
		resp, b := limitedPost(t, ts.URL, tok, "", tool)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d: %s", tool, resp.StatusCode, b)
		}
		var r callResult
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatalf("%s: %v: %s", tool, err, b)
		}
		return r
	}

	for i := range 2 {
		if r := call(tokA, "credential_reveal"); r.Result.IsError {
			t.Fatalf("reveal %d within the limit was refused", i+1)
		}
	}
	r := call(tokA, "credential_reveal")
	if !r.Result.IsError || len(r.Result.Content) == 0 || !strings.Contains(r.Result.Content[0].Text, "limited to 2 calls a minute") {
		t.Errorf("3rd reveal = %+v, want an isError result naming the limit", r.Result)
	}
	if reveals != 2 {
		t.Errorf("reveal handler ran %d times, want 2", reveals)
	}
	if r := call(tokA, "whoami"); r.Result.IsError {
		t.Error("another tool was limited by the reveal sub-limit")
	}
	if r := call(tokB, "credential_reveal"); r.Result.IsError {
		t.Error("another principal was limited by the first one's reveals")
	}
}

// TestRateLimit_OffByDefaultAndInLocalDev: a Config built by hand (tests,
// the latency benchmark) has every /mcp limit off, and verifier-disabled
// local-dev mode is never limited even with limits configured.
func TestRateLimit_OffByDefaultAndInLocalDev(t *testing.T) {
	pool := testutil.NewPool(t)
	_, h := testutil.SeedHousehold(t, pool)
	iss := testutil.NewTokenIssuer(t)

	s := server.NewServer(testConfig(), pool, iss.Verifier(pool))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	tok, _ := seedAgentToken(t, pool, h, []string{"read"}, nil)
	for i := range 60 {
		if resp, b := limitedPost(t, ts.URL, tok, "", ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d with limits off: status %d: %s", i+1, resp.StatusCode, b)
		}
	}

	disabled, err := auth.NewVerifier(context.Background(), "", "", pool)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.RateLimitPerMinute, cfg.RateLimitBurst, cfg.UnauthRateLimitPerMinute = 1, 1, 1
	dev := httptest.NewServer(server.NewServer(cfg, pool, disabled).Handler())
	t.Cleanup(dev.Close)
	for i := range 5 {
		if resp, b := limitedPost(t, dev.URL, "", "", ""); resp.StatusCode != http.StatusOK {
			t.Fatalf("local-dev request %d: status %d: %s", i+1, resp.StatusCode, b)
		}
	}
}
