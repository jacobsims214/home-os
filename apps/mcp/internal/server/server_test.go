package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/config"
)

const testPublicBaseURL = "https://home.example.test"

// newTestRouter serves the production router (Handler) with auth in local-dev
// mode and one trivial tool, so transport rules are tested without Dex or a DB.
func newTestRouter(t *testing.T) *httptest.Server {
	t.Helper()
	verifier, err := auth.NewVerifier(context.Background(), "", "", nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	s := NewServer(&config.Config{PublicBaseURL: testPublicBaseURL}, nil, verifier)
	s.RegisterTool("echo", mcp.NewTool("echo",
		mcp.WithDescription("Echo back who is calling."),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := ClaimsFromContext(ctx)
		if claims == nil {
			return mcp.NewToolResultError("no claims in context"), nil
		}
		return mcp.NewToolResultText(claims.UserID), nil
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, method, url string, body []byte, header http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func legacyInitialize() []byte {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "router-test", "version": "0"},
		},
	})
	return b
}

func modernRequest(method mcp.MCPMethod, params map[string]any) ([]byte, http.Header) {
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{
		mcp.MetaKeyProtocolVersion:    mcp.ProtocolVersion20260728,
		mcp.MetaKeyClientInfo:         map[string]any{"name": "router-test", "version": "0"},
		mcp.MetaKeyClientCapabilities: map[string]any{},
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	h := jsonHeaders()
	h.Set(mcp.HeaderProtocolVersion, mcp.ProtocolVersion20260728)
	h.Set(mcp.HeaderMethod, string(method))
	if name, ok := params["name"].(string); ok {
		h.Set(mcp.HeaderName, name)
	}
	return b, h
}

func jsonHeaders() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json, text/event-stream")
	return h
}

// TestMCPEndpoint_PostOnly: GET and DELETE (and anything else) on /mcp get
// 405 with Allow: POST, whatever protocol version the client claims.
func TestMCPEndpoint_PostOnly(t *testing.T) {
	ts := newTestRouter(t)
	tests := []struct {
		method  string
		version string
	}{
		{http.MethodGet, ""},
		{http.MethodGet, "2025-06-18"},
		{http.MethodGet, mcp.ProtocolVersion20260728},
		{http.MethodDelete, ""},
		{http.MethodDelete, "2025-03-26"},
		{http.MethodPut, ""},
		{http.MethodPatch, ""},
		{http.MethodOptions, ""},
		{http.MethodHead, ""},
	}
	for _, tt := range tests {
		t.Run(tt.method+"_"+tt.version, func(t *testing.T) {
			h := http.Header{}
			h.Set("Accept", "text/event-stream")
			h.Set("Mcp-Session-Id", "client-supplied")
			if tt.version != "" {
				h.Set(mcp.HeaderProtocolVersion, tt.version)
			}
			resp := do(t, tt.method, ts.URL+"/mcp", nil, h)
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", resp.StatusCode)
			}
			if got := resp.Header.Get("Allow"); got != http.MethodPost {
				t.Errorf("Allow = %q, want POST", got)
			}
			if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
				t.Errorf("Mcp-Session-Id = %q, want none", got)
			}
		})
	}
}

// TestLegacySSEEndpointsRemoved: the deprecated HTTP+SSE transport is gone.
func TestLegacySSEEndpointsRemoved(t *testing.T) {
	ts := newTestRouter(t)
	for _, path := range []string{"/mcp/sse", "/mcp/message", "/mcp/message?sessionId=abc"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(method+" "+path, func(t *testing.T) {
				resp := do(t, method, ts.URL+path, legacyInitialize(), jsonHeaders())
				if resp.StatusCode != http.StatusNotFound {
					t.Fatalf("status = %d, want 404", resp.StatusCode)
				}
				if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
					t.Errorf("Content-Type = %q: an SSE stream is still served", ct)
				}
			})
		}
	}
}

// TestMCPEndpoint_NoSessionID: neither era's POST mints or echoes a session
// id, even when the client sends one.
func TestMCPEndpoint_NoSessionID(t *testing.T) {
	ts := newTestRouter(t)

	listBody, listHeader := modernRequest(mcp.MethodToolsList, nil)
	callBody, callHeader := modernRequest(mcp.MethodToolsCall, map[string]any{"name": "echo", "arguments": map[string]any{}})
	discoverBody, discoverHeader := modernRequest(mcp.MethodServerDiscover, nil)

	tests := []struct {
		name   string
		body   []byte
		header http.Header
	}{
		{"legacy initialize", legacyInitialize(), jsonHeaders()},
		{"modern server/discover", discoverBody, discoverHeader},
		{"modern tools/list", listBody, listHeader},
		{"modern tools/call", callBody, callHeader},
	}
	for _, tt := range tests {
		for _, sendID := range []bool{false, true} {
			name := tt.name
			if sendID {
				name += " with client session id"
			}
			t.Run(name, func(t *testing.T) {
				h := tt.header.Clone()
				if sendID {
					h.Set("Mcp-Session-Id", "client-supplied")
				}
				resp := do(t, http.MethodPost, ts.URL+"/mcp", tt.body, h)
				body, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status = %d: %s", resp.StatusCode, body)
				}
				if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
					t.Errorf("Mcp-Session-Id = %q, want none", got)
				}
				if bytes.Contains(body, []byte(`"error"`)) {
					t.Errorf("JSON-RPC error: %s", body)
				}
			})
		}
	}
}

// TestMCPEndpoint_ModernResults: a 2026-07-28 client gets cache hints on the
// tool list, and the call reaches the handler with the auth claims.
func TestMCPEndpoint_ModernResults(t *testing.T) {
	ts := newTestRouter(t)

	body, h := modernRequest(mcp.MethodToolsList, nil)
	var list struct {
		Result struct {
			ResultType string `json:"resultType"`
			TTLMs      *int64 `json:"ttlMs"`
			CacheScope string `json:"cacheScope"`
			Tools      []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	decode(t, do(t, http.MethodPost, ts.URL+"/mcp", body, h), &list)
	if list.Result.ResultType != "complete" {
		t.Errorf("resultType = %q", list.Result.ResultType)
	}
	if list.Result.TTLMs == nil || *list.Result.TTLMs != catalogueCacheTTLMs {
		t.Errorf("ttlMs = %v, want %d", list.Result.TTLMs, catalogueCacheTTLMs)
	}
	if list.Result.CacheScope != string(mcp.CacheScopePrivate) {
		t.Errorf("cacheScope = %q, want private", list.Result.CacheScope)
	}
	if len(list.Result.Tools) != 1 || list.Result.Tools[0].Name != "echo" {
		t.Errorf("tools = %+v", list.Result.Tools)
	}

	body, h = modernRequest(mcp.MethodToolsCall, map[string]any{"name": "echo", "arguments": map[string]any{}})
	var call struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	decode(t, do(t, http.MethodPost, ts.URL+"/mcp", body, h), &call)
	if call.Result.IsError || len(call.Result.Content) != 1 || call.Result.Content[0].Text != "local-dev" {
		t.Errorf("echo returned %+v", call.Result)
	}
}

func decode(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var last string
		for _, line := range strings.Split(string(body), "\n") {
			if data, ok := strings.CutPrefix(line, "data:"); ok {
				last = strings.TrimSpace(data)
			}
		}
		body = []byte(last)
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

// TestMCPEndpoint_Origin: a browser Origin other than PUBLIC_BASE_URL's (or
// loopback) gets 403; no Origin (non-browser clients) passes.
func TestMCPEndpoint_Origin(t *testing.T) {
	ts := newTestRouter(t)
	tests := []struct {
		origin string
		want   int
	}{
		{"", http.StatusOK},
		{testPublicBaseURL, http.StatusOK},
		{"HTTPS://HOME.EXAMPLE.TEST", http.StatusOK},
		{"http://localhost:6274", http.StatusOK},
		{"http://127.0.0.1:3000", http.StatusOK},
		{"http://[::1]:3000", http.StatusOK},
		{"https://evil.example", http.StatusForbidden},
		{"http://home.example.test", http.StatusForbidden}, // scheme differs
		{"https://home.example.test.evil.example", http.StatusForbidden},
		{"null", http.StatusForbidden},
		{"file://", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.origin, func(t *testing.T) {
			h := jsonHeaders()
			if tt.origin != "" {
				h.Set("Origin", tt.origin)
			}
			resp := do(t, http.MethodPost, ts.URL+"/mcp", legacyInitialize(), h)
			if resp.StatusCode != tt.want {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tt.want, b)
			}
		})
	}
}

func TestHealthStillServed(t *testing.T) {
	ts := newTestRouter(t)
	for _, path := range []string{"/health", "/mcp/health", "/.well-known/oauth-protected-resource"} {
		if resp := do(t, http.MethodGet, ts.URL+path, nil, nil); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
		}
	}
}
