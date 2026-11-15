package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/mark3labs/mcp-go/server/servertest"

	"home-os/mcp/internal/config"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// TestLegacyInitializeClient proves that a pre-2026-07-28 client (one that
// opens with the initialize handshake, as moltis does) can still list and
// call every tool on the stateless Streamable HTTP transport after the
// mcp-go v1.x upgrade. It speaks raw JSON-RPC so nothing in the mcp-go
// client papers over a protocol difference.
func TestLegacyInitializeClient(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	claims := server.ClaimsFromContext(ctx)
	if claims == nil {
		t.Fatal("SeedHousehold context carries no claims")
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'property', gen_random_uuid(), 'Legacy client note', 'hello')`,
		hh.HouseholdID,
	); err != nil {
		t.Fatalf("insert note: %v", err)
	}

	srv := server.NewServer(&config.Config{}, pool, nil)
	registerAll(srv, &config.Config{}, pool)

	// Production options, plus a context func standing in for AuthMiddleware
	// (servertest serves the transport without the chi router in front).
	opts := append(srv.StreamableHTTPOptions(),
		mcpserver.WithHTTPContextFunc(func(ctx context.Context, _ *http.Request) context.Context {
			return server.ContextWithClaims(ctx, claims)
		}),
	)
	ts := servertest.NewTestStreamableHTTPServer(srv.MCPServer(), opts...)
	t.Cleanup(ts.Close)
	endpoint := ts.URL + "/mcp"

	for _, version := range []string{"2025-03-26", "2025-06-18"} {
		t.Run(version, func(t *testing.T) {
			c := &legacyClient{t: t, url: endpoint}

			var initRes struct {
				ProtocolVersion string `json:"protocolVersion"`
				ServerInfo      struct {
					Name string `json:"name"`
				} `json:"serverInfo"`
				Capabilities struct {
					Tools *json.RawMessage `json:"tools"`
				} `json:"capabilities"`
			}
			c.call("initialize", map[string]any{
				"protocolVersion": version,
				"capabilities":    map[string]any{},
				"clientInfo":      map[string]any{"name": "legacy-test", "version": "0.0.1"},
			}, &initRes)
			if initRes.ProtocolVersion != version {
				t.Fatalf("initialize negotiated %q, want %q", initRes.ProtocolVersion, version)
			}
			if initRes.ServerInfo.Name != "Home OS MCP" {
				t.Errorf("serverInfo.name = %q", initRes.ServerInfo.Name)
			}
			if initRes.Capabilities.Tools == nil {
				t.Error("initialize result advertises no tools capability")
			}
			c.version = version
			c.notify("notifications/initialized")

			var list struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			c.call("tools/list", map[string]any{}, &list)
			seen := map[string]bool{}
			for _, tool := range list.Tools {
				if seen[tool.Name] {
					t.Errorf("tool %q listed twice", tool.Name)
				}
				seen[tool.Name] = true
			}
			if len(seen) != srv.ToolCount() {
				t.Fatalf("tools/list returned %d tools, %d registered", len(seen), srv.ToolCount())
			}
			t.Logf("tools/list over protocol %s returned %d tools", version, len(seen))
			for _, name := range []string{"ping", "list_notes", "create_event", "get_financial_summary", "create_bill", "bill_update", "bill_delete", "bill_pay"} {
				if !seen[name] {
					t.Errorf("tools/list is missing %q", name)
				}
			}

			type callResult struct {
				IsError bool `json:"isError"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			var ping callResult
			c.call("tools/call", map[string]any{"name": "ping", "arguments": map[string]any{}}, &ping)
			if ping.IsError || len(ping.Content) != 1 || ping.Content[0].Text != `{"pong":true}` {
				t.Fatalf("ping returned %+v", ping)
			}

			// A DB-backed tool: proves the claims context reaches the handler.
			var notes callResult
			c.call("tools/call", map[string]any{"name": "list_notes", "arguments": map[string]any{}}, &notes)
			if notes.IsError || len(notes.Content) != 1 || !strings.Contains(notes.Content[0].Text, "Legacy client note") {
				t.Fatalf("list_notes returned %+v", notes)
			}
		})
	}
}

// TestModernStatelessClient proves a 2026-07-28 client (no initialize; every
// request carries its protocol version, client info and capabilities in
// _meta plus the MCP-Protocol-Version/Mcp-Method/Mcp-Name headers) can
// discover the server, list every tool in a stable order with cache hints,
// and call DB-backed tools, on the same stateless endpoint legacy clients use.
func TestModernStatelessClient(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx, hh := testutil.SeedHousehold(t, pool)
	claims := server.ClaimsFromContext(ctx)
	if claims == nil {
		t.Fatal("SeedHousehold context carries no claims")
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO notes (household_id, entity_type, entity_id, title, body) VALUES ($1, 'property', gen_random_uuid(), 'Modern client note', 'hello')`,
		hh.HouseholdID,
	); err != nil {
		t.Fatalf("insert note: %v", err)
	}

	srv := server.NewServer(&config.Config{}, pool, nil)
	registerAll(srv, &config.Config{}, pool)
	opts := append(srv.StreamableHTTPOptions(),
		mcpserver.WithHTTPContextFunc(func(ctx context.Context, _ *http.Request) context.Context {
			return server.ContextWithClaims(ctx, claims)
		}),
	)
	ts := servertest.NewTestStreamableHTTPServer(srv.MCPServer(), opts...)
	t.Cleanup(ts.Close)
	c := &legacyClient{t: t, url: ts.URL + "/mcp", version: mcp.ProtocolVersion20260728, modern: true}

	var discover struct {
		SupportedVersions []string `json:"supportedVersions"`
		ResultType        string   `json:"resultType"`
		Meta              map[string]struct {
			Name string `json:"name"`
		} `json:"_meta"`
	}
	c.call(string(mcp.MethodServerDiscover), map[string]any{}, &discover)
	for _, v := range []string{mcp.ProtocolVersion20260728, "2025-11-25", "2025-06-18", "2025-03-26"} {
		if !slices.Contains(discover.SupportedVersions, v) {
			t.Errorf("server/discover supportedVersions %v lacks %s", discover.SupportedVersions, v)
		}
	}
	if discover.ResultType != "complete" {
		t.Errorf("server/discover resultType = %q", discover.ResultType)
	}
	if got := discover.Meta[mcp.MetaKeyServerInfo].Name; got != "Home OS MCP" {
		t.Errorf("server/discover serverInfo.name = %q", got)
	}

	type toolList struct {
		TTLMs      *int64 `json:"ttlMs"`
		CacheScope string `json:"cacheScope"`
		NextCursor string `json:"nextCursor"`
		Tools      []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	names := func(l toolList) []string {
		out := make([]string, 0, len(l.Tools))
		for _, tool := range l.Tools {
			out = append(out, tool.Name)
		}
		return out
	}
	var first, second toolList
	c.call(string(mcp.MethodToolsList), map[string]any{}, &first)
	c.call(string(mcp.MethodToolsList), map[string]any{}, &second)
	if first.NextCursor != "" {
		t.Fatalf("tools/list paginated (nextCursor %q); the test expects one page", first.NextCursor)
	}
	if len(first.Tools) != srv.ToolCount() {
		t.Fatalf("tools/list returned %d tools, %d registered", len(first.Tools), srv.ToolCount())
	}
	if !slices.IsSorted(names(first)) {
		t.Errorf("tools/list is not in name order: %v", names(first))
	}
	if !slices.Equal(names(first), names(second)) {
		t.Errorf("tools/list order differs between calls")
	}
	if first.TTLMs == nil || *first.TTLMs <= 0 || first.CacheScope != string(mcp.CacheScopePrivate) {
		t.Errorf("tools/list cache hints ttlMs=%v cacheScope=%q", first.TTLMs, first.CacheScope)
	}
	t.Logf("tools/list over protocol %s returned %d tools", mcp.ProtocolVersion20260728, len(first.Tools))

	type callResult struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	var ping callResult
	c.call(string(mcp.MethodToolsCall), map[string]any{"name": "ping", "arguments": map[string]any{}}, &ping)
	if ping.IsError || len(ping.Content) != 1 || ping.Content[0].Text != `{"pong":true}` {
		t.Fatalf("ping returned %+v", ping)
	}
	var notes callResult
	c.call(string(mcp.MethodToolsCall), map[string]any{"name": "list_notes", "arguments": map[string]any{}}, &notes)
	if notes.IsError || len(notes.Content) != 1 || !strings.Contains(notes.Content[0].Text, "Modern client note") {
		t.Fatalf("list_notes returned %+v", notes)
	}
}

// legacyClient sends JSON-RPC over Streamable HTTP the way a 2025-era client
// does: no _meta protocol keys, MCP-Protocol-Version header after initialize.
// With modern set it speaks 2026-07-28 instead: _meta protocol keys on every
// request plus the Mcp-Method/Mcp-Name headers, and no initialize.
type legacyClient struct {
	t       *testing.T
	url     string
	version string
	modern  bool
	nextID  int
}

func (c *legacyClient) post(body map[string]any) *http.Response {
	c.t.Helper()
	method, _ := body["method"].(string)
	var mcpName string
	if c.modern {
		params, _ := body["params"].(map[string]any)
		if params == nil {
			params = map[string]any{}
		}
		params["_meta"] = map[string]any{
			mcp.MetaKeyProtocolVersion:    c.version,
			mcp.MetaKeyClientInfo:         map[string]any{"name": "modern-test", "version": "0.0.1"},
			mcp.MetaKeyClientCapabilities: map[string]any{},
		}
		body["params"] = params
		mcpName, _ = params["name"].(string)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		c.t.Fatalf("marshal: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	c.t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(raw))
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.version != "" {
		req.Header.Set("MCP-Protocol-Version", c.version)
	}
	if c.modern {
		req.Header.Set(mcp.HeaderMethod, method)
		if mcpName != "" {
			req.Header.Set(mcp.HeaderName, mcpName)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("POST %v: %v", body["method"], err)
	}
	return resp
}

func (c *legacyClient) notify(method string) {
	c.t.Helper()
	resp := c.post(map[string]any{"jsonrpc": "2.0", "method": method})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		c.t.Fatalf("%s: status %d: %s", method, resp.StatusCode, b)
	}
}

func (c *legacyClient) call(method string, params any, out any) {
	c.t.Helper()
	c.nextID++
	resp := c.post(map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params})
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("%s: read body: %v", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("%s: status %d: %s", method, resp.StatusCode, body)
	}
	if resp.Header.Get("Mcp-Session-Id") != "" {
		c.t.Errorf("%s: stateless server minted a session id", method)
	}
	payload := body
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		payload, err = lastSSEData(body)
		if err != nil {
			c.t.Fatalf("%s: %v in %s", method, err, body)
		}
	}
	var envelope struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		c.t.Fatalf("%s: decode %s: %v", method, payload, err)
	}
	if envelope.Error != nil {
		c.t.Fatalf("%s: JSON-RPC error %d: %s", method, envelope.Error.Code, envelope.Error.Message)
	}
	if envelope.ID != c.nextID {
		c.t.Fatalf("%s: response id %d, want %d", method, envelope.ID, c.nextID)
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		c.t.Fatalf("%s: decode result %s: %v", method, envelope.Result, err)
	}
}

// lastSSEData returns the data of the last event in an SSE body: the final
// JSON-RPC response follows any progress/log notifications.
func lastSSEData(body []byte) ([]byte, error) {
	var last []byte
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
			last = []byte(strings.TrimSpace(data))
		}
	}
	if last == nil {
		return nil, fmt.Errorf("no data event")
	}
	return last, sc.Err()
}
