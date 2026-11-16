package server_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/config"
	"home-os/mcp/internal/metadata"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// testConfig mirrors what config.Load produces for PUBLIC_BASE_URL
// https://mcp.test, with a non-default resource URL so the tests prove the
// configured value (not one derived from PUBLIC_BASE_URL) is used.
func testConfig() *config.Config {
	return &config.Config{
		PublicBaseURL:     "https://mcp.test",
		ResourceURL:       "https://mcp.test/mcp",
		DexIssuer:         testutil.TestIssuer,
		AcceptedAudiences: []string{testutil.TestAudience},
		AllowedOrigins:    []string{"https://mcp.test"},
	}
}

const wantResourceMetadata = `resource_metadata="https://mcp.test/.well-known/oauth-protected-resource/mcp"`

// TestAuthMiddleware_StatusCodes checks the HTTP mapping: member 200 (claims
// injected), non-member 403, any invalid token 401 with the RFC 9728
// challenge, database failure 500.
func TestAuthMiddleware_StatusCodes(t *testing.T) {
	pool := testutil.NewPool(t)
	_, h := testutil.SeedHousehold(t, pool) // user has no dex_sub: resolves by email
	iss := testutil.NewTokenIssuer(t)

	newHandler := func() (http.Handler, *string) {
		s := server.NewServer(testConfig(), pool, iss.Verifier(pool))
		var gotHousehold string
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c := server.ClaimsFromContext(r.Context()); c != nil {
				gotHousehold = c.HouseholdID
			}
			w.WriteHeader(http.StatusOK)
		})
		return s.AuthMiddleware()(next), &gotHousehold
	}

	other := testutil.NewTokenIssuer(t)
	expired := time.Now().Add(-time.Minute).Unix()
	tests := []struct {
		name          string
		authorization string
		want          int
		invalidToken  bool // challenge must carry error="invalid_token"
	}{
		{"member", "Bearer " + iss.Token("sub-owner", h.Email), http.StatusOK, false},
		{"lowercase scheme", "bearer " + iss.Token("sub-owner", h.Email), http.StatusOK, false},
		{"valid token, no membership", "Bearer " + iss.Token("sub-stranger", "stranger@test.local"), http.StatusForbidden, false},
		{"missing header", "", http.StatusUnauthorized, false},
		{"basic auth", "Basic dXNlcjpwYXNz", http.StatusUnauthorized, false},
		{"empty bearer", "Bearer ", http.StatusUnauthorized, false},
		{"wrong audience", "Bearer " + iss.TokenWith("sub-owner", h.Email, map[string]any{"aud": "home-os-api"}), http.StatusUnauthorized, true},
		{"expired", "Bearer " + iss.TokenWith("sub-owner", h.Email, map[string]any{"exp": expired}), http.StatusUnauthorized, true},
		{"unsigned", "Bearer " + iss.UnsignedToken("sub-owner", h.Email), http.StatusUnauthorized, true},
		{"wrong issuer", "Bearer " + iss.TokenWith("sub-owner", h.Email, map[string]any{"iss": "https://evil.example/dex"}), http.StatusUnauthorized, true},
		{"bad signature", "Bearer " + other.Token("sub-owner", h.Email), http.StatusUnauthorized, true},
		{"garbage token", "Bearer not-a-jwt", http.StatusUnauthorized, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler, gotHousehold := newHandler()
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(context.Background())
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			challenge := rec.Header().Get("WWW-Authenticate")
			switch tc.want {
			case http.StatusOK:
				if *gotHousehold != h.HouseholdID.String() {
					t.Fatalf("household in context = %q, want %s", *gotHousehold, h.HouseholdID)
				}
			case http.StatusUnauthorized:
				if !strings.HasPrefix(challenge, "Bearer ") || !strings.Contains(challenge, wantResourceMetadata) {
					t.Fatalf("WWW-Authenticate = %q, want Bearer challenge with %s", challenge, wantResourceMetadata)
				}
				if !strings.Contains(challenge, `scope="openid email profile"`) {
					t.Errorf("WWW-Authenticate = %q, want scope", challenge)
				}
				if got := strings.Contains(challenge, `error="invalid_token"`); got != tc.invalidToken {
					t.Errorf("WWW-Authenticate = %q: invalid_token present = %v, want %v", challenge, got, tc.invalidToken)
				}
			case http.StatusForbidden:
				if challenge != "" {
					t.Errorf("403 carries WWW-Authenticate %q; a step-up challenge cannot fix a missing membership", challenge)
				}
			}
		})
	}

	t.Run("database failure", func(t *testing.T) {
		broken := testutil.NewPool(t)
		broken.Close()
		s := server.NewServer(testConfig(), broken, iss.Verifier(broken))
		handler := s.AuthMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+iss.Token("sub-owner", h.Email))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
	})
}

// productionVerifier builds the verifier exactly as cmd/mcp does with the
// default config: config.DefaultAcceptedAudiences plus the resource URL, and
// the DCR client prefix.
func productionVerifier(iss *testutil.TokenIssuer, pool *pgxpool.Pool, cfg *config.Config) *auth.Verifier {
	auds := append(append([]string{}, config.DefaultAcceptedAudiences...), cfg.ResourceURL)
	return iss.Verifier(pool,
		auth.WithAudiences(auds...),
		auth.WithAudiencePrefixes(metadata.DynamicClientIDPrefix),
	)
}

// TestAuth_AudiencePolicy checks #4692 with the production audience wiring:
// tokens issued to the Home OS UI's Dex client (home-os-ui), directly or as a
// cross-client token, get 401 invalid_token at /mcp. Tokens for home-os-mcp,
// the resource URL and mcp-<uuid> DCR clients pass, and the OAuth client id
// (azp) reaches the request context.
func TestAuth_AudiencePolicy(t *testing.T) {
	pool := testutil.NewPool(t)
	_, h := testutil.SeedHousehold(t, pool)
	iss := testutil.NewTokenIssuer(t)
	cfg := testConfig()
	cfg.AcceptedAudiences = append([]string(nil), config.DefaultAcceptedAudiences...)
	s := server.NewServer(cfg, pool, productionVerifier(iss, pool, cfg))

	const dcr = "mcp-019a8b7c-1d2e-7f00-8a1b-2c3d4e5f6a7b"
	tests := []struct {
		name       string
		claims     map[string]any
		want       int
		wantClient string
	}{
		{"home-os-mcp", map[string]any{"aud": "home-os-mcp"}, http.StatusOK, "home-os-mcp"},
		{"resource URL", map[string]any{"aud": cfg.ResourceURL}, http.StatusOK, cfg.ResourceURL},
		{"DCR client", map[string]any{"aud": dcr}, http.StatusOK, dcr},
		{"DCR client with azp", map[string]any{"aud": []string{dcr, cfg.ResourceURL}, "azp": dcr}, http.StatusOK, dcr},
		{"home-os-ui", map[string]any{"aud": "home-os-ui"}, http.StatusUnauthorized, ""},
		{"home-os-ui cross-client for the MCP", map[string]any{"aud": []string{"home-os-mcp", "home-os-ui"}, "azp": "home-os-ui"}, http.StatusUnauthorized, ""},
		{"home-os-ui with MCP audience, no azp", map[string]any{"aud": []string{"home-os-mcp", "home-os-ui"}}, http.StatusUnauthorized, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotClient string
			reached := false
			handler := s.AuthMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				if c := server.ClaimsFromContext(r.Context()); c != nil {
					gotClient = c.ClientID
				}
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header.Set("Authorization", "Bearer "+iss.TokenWith("sub-owner", h.Email, tc.claims))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusUnauthorized {
				if reached {
					t.Fatal("a refused token reached the handler")
				}
				if ch := rec.Header().Get("WWW-Authenticate"); !strings.Contains(ch, `error="invalid_token"`) {
					t.Fatalf("WWW-Authenticate = %q, want error=\"invalid_token\"", ch)
				}
				return
			}
			if gotClient != tc.wantClient {
				t.Fatalf("ClientID in context = %q, want %q", gotClient, tc.wantClient)
			}
		})
	}

	t.Run("agent token still accepted, no client id", func(t *testing.T) {
		tok, _ := seedAgentToken(t, pool, h, []string{auth.ScopeRead}, nil)
		var got *auth.Claims
		handler := s.AuthMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = server.ClaimsFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || got == nil {
			t.Fatalf("status = %d, claims %v (body %s)", rec.Code, got, rec.Body.String())
		}
		if got.AgentTokenID == "" || got.ClientID != "" {
			t.Fatalf("claims = %+v, want agent token id and empty ClientID", got)
		}
	})
}

// TestAuth_ClaimsReachTools drives the production router (Origin, POST-only,
// auth, Streamable HTTP transport) with real tokens: a member's tools/call
// sees the household claims, a non-member gets 403 and a wrong-audience token
// 401, both before any tool runs.
func TestAuth_ClaimsReachTools(t *testing.T) {
	pool := testutil.NewPool(t)
	_, h := testutil.SeedHousehold(t, pool)
	iss := testutil.NewTokenIssuer(t)

	s := server.NewServer(testConfig(), pool, iss.Verifier(pool))
	calls := 0
	s.RegisterTool("whoami", mcp.NewTool("whoami",
		mcp.WithDescription("Return the caller's household and role."),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls++
		c := server.ClaimsFromContext(ctx)
		if c == nil {
			return mcp.NewToolResultError("no claims"), nil
		}
		b, _ := json.Marshal(c)
		return mcp.NewToolResultText(string(b)), nil
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	call := func(t *testing.T, token string) (*http.Response, []byte) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": mcp.MethodToolsCall,
			"params": map[string]any{
				"name": "whoami", "arguments": map[string]any{},
				"_meta": map[string]any{
					mcp.MetaKeyProtocolVersion:    mcp.ProtocolVersion20260728,
					mcp.MetaKeyClientInfo:         map[string]any{"name": "auth-test", "version": "0"},
					mcp.MetaKeyClientCapabilities: map[string]any{},
				},
			},
		})
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set(mcp.HeaderProtocolVersion, mcp.ProtocolVersion20260728)
		req.Header.Set(mcp.HeaderMethod, string(mcp.MethodToolsCall))
		req.Header.Set(mcp.HeaderName, "whoami")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /mcp: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}

	t.Run("member", func(t *testing.T) {
		resp, body := call(t, iss.Token("sub-owner", h.Email))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, body)
		}
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			for _, line := range strings.Split(string(body), "\n") {
				if data, ok := strings.CutPrefix(line, "data:"); ok {
					body = []byte(strings.TrimSpace(data))
				}
			}
		}
		var rpc struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &rpc); err != nil || rpc.Result.IsError || len(rpc.Result.Content) != 1 {
			t.Fatalf("tool result %s (err %v)", body, err)
		}
		var got auth.Claims
		if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &got); err != nil {
			t.Fatalf("claims %q: %v", rpc.Result.Content[0].Text, err)
		}
		if got.HouseholdID != h.HouseholdID.String() || got.UserID != h.UserID.String() || got.Role == "" || got.Subject != "sub-owner" {
			t.Fatalf("tool saw claims %+v, want household %s user %s", got, h.HouseholdID, h.UserID)
		}
	})

	before := calls
	t.Run("non-member", func(t *testing.T) {
		resp, body := call(t, iss.Token("sub-stranger", "stranger@test.local"))
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", resp.StatusCode, body)
		}
	})
	t.Run("wrong audience", func(t *testing.T) {
		resp, body := call(t, iss.TokenWith("sub-owner", h.Email, map[string]any{"aud": "home-os-api"}))
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401: %s", resp.StatusCode, body)
		}
		if c := resp.Header.Get("WWW-Authenticate"); !strings.Contains(c, wantResourceMetadata) {
			t.Fatalf("WWW-Authenticate = %q", c)
		}
	})
	if calls != before {
		t.Fatalf("tool ran %d time(s) for rejected requests", calls-before)
	}
}

// seedAgentToken stores a fresh hos_ token for the seeded owner and returns
// the plaintext and its id.
func seedAgentToken(t *testing.T, pool *pgxpool.Pool, h testutil.Household, scopes []string, expiresAt *time.Time) (string, string) {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	raw := auth.AgentTokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO agent_tokens (user_id, household_id, name, token_prefix, token_hash, scopes, expires_at)
		 VALUES ($1, $2, 'agent', $3, $4, $5, $6) RETURNING id::text`,
		h.UserID, h.HouseholdID, raw[:12], auth.HashAgentToken(raw), scopes, expiresAt,
	).Scan(&id); err != nil {
		t.Fatalf("insert agent token: %v", err)
	}
	return raw, id
}

// rpcPost sends one JSON-RPC request (2026-07-28 stateless shape) to /mcp and
// returns the status, headers and the JSON body (SSE framing removed).
func rpcPost(t *testing.T, url, token string, method mcp.MCPMethod, name string, params map[string]any) (*http.Response, []byte) {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{
		mcp.MetaKeyProtocolVersion:    mcp.ProtocolVersion20260728,
		mcp.MetaKeyClientInfo:         map[string]any{"name": "agent-token-test", "version": "0"},
		mcp.MetaKeyClientCapabilities: map[string]any{},
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(mcp.HeaderProtocolVersion, mcp.ProtocolVersion20260728)
	req.Header.Set(mcp.HeaderMethod, string(method))
	if name != "" {
		req.Header.Set(mcp.HeaderName, name)
	}
	req.Header.Set("Authorization", "Bearer "+token)
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

// TestAgentTokens_EndToEnd drives the production router with per-member
// agent tokens: a read token lists tools and calls a read tool with the
// member's household claims, is refused a write tool with 403, a write token
// may call it, and revoked, expired and deactivated-user tokens get the same
// 401 challenge as a bad JWT. The Dex path is covered by the tests above.
func TestAgentTokens_EndToEnd(t *testing.T) {
	pool := testutil.NewPool(t)
	_, h := testutil.SeedHousehold(t, pool)
	iss := testutil.NewTokenIssuer(t)

	s := server.NewServer(testConfig(), pool, iss.Verifier(pool))
	whoamiCalls, writeCalls := 0, 0
	s.RegisterTool("whoami", mcp.NewTool("whoami",
		mcp.WithDescription("Return the caller's claims."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		whoamiCalls++
		b, _ := json.Marshal(server.ClaimsFromContext(ctx))
		return mcp.NewToolResultText(string(b)), nil
	})
	s.RegisterTool("add_note", mcp.NewTool("add_note",
		mcp.WithDescription("Write something."),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		writeCalls++
		return mcp.NewToolResultText("written"), nil
	})
	// readOnlyHint=true but destructiveHint=true: contradictory, so treated as a write.
	s.RegisterTool("purge", mcp.NewTool("purge",
		mcp.WithDescription("Contradictory annotations."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(true),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		writeCalls++
		return mcp.NewToolResultText("purged"), nil
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	readTok, readID := seedAgentToken(t, pool, h, []string{"read"}, nil)
	rwTok, _ := seedAgentToken(t, pool, h, []string{"read", "write"}, nil)

	type toolResult struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}

	t.Run("read token lists tools", func(t *testing.T) {
		resp, body := rpcPost(t, ts.URL, readTok, mcp.MethodToolsList, "", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, body)
		}
		var r toolResult
		if err := json.Unmarshal(body, &r); err != nil || len(r.Result.Tools) != 3 {
			t.Fatalf("tools/list = %s (err %v)", body, err)
		}
	})

	t.Run("read token calls a read tool with household claims", func(t *testing.T) {
		resp, body := rpcPost(t, ts.URL, readTok, mcp.MethodToolsCall, "whoami",
			map[string]any{"name": "whoami", "arguments": map[string]any{}})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, body)
		}
		var r toolResult
		if err := json.Unmarshal(body, &r); err != nil || r.Result.IsError || len(r.Result.Content) != 1 {
			t.Fatalf("tool result %s (err %v)", body, err)
		}
		var got auth.Claims
		if err := json.Unmarshal([]byte(r.Result.Content[0].Text), &got); err != nil {
			t.Fatal(err)
		}
		if got.HouseholdID != h.HouseholdID.String() || got.UserID != h.UserID.String() ||
			got.Role != "owner" || got.AgentTokenID != readID {
			t.Fatalf("tool saw claims %+v", got)
		}
	})

	for _, tool := range []string{"add_note", "purge"} {
		t.Run("read token refused "+tool, func(t *testing.T) {
			before := writeCalls
			resp, body := rpcPost(t, ts.URL, readTok, mcp.MethodToolsCall, tool,
				map[string]any{"name": tool, "arguments": map[string]any{}})
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", resp.StatusCode, body)
			}
			if !strings.Contains(string(body), "read-only") || !strings.Contains(string(body), "write scope") {
				t.Fatalf("403 body %s lacks a clear scope message", body)
			}
			if writeCalls != before {
				t.Fatal("write tool ran for a read-only token")
			}
		})
	}

	t.Run("write token calls a write tool", func(t *testing.T) {
		before := writeCalls
		resp, body := rpcPost(t, ts.URL, rwTok, mcp.MethodToolsCall, "add_note",
			map[string]any{"name": "add_note", "arguments": map[string]any{}})
		if resp.StatusCode != http.StatusOK || writeCalls != before+1 {
			t.Fatalf("status = %d calls %d: %s", resp.StatusCode, writeCalls-before, body)
		}
	})

	// Household role policy (#4689): the role is re-read on every call, so
	// demoting the member to a read-only role stops even a write-scoped
	// token from writing, while read tools keep working.
	for _, role := range []string{"viewer", "housesitter", "vendor"} {
		t.Run("write token refused write tool as "+role, func(t *testing.T) {
			if _, err := pool.Exec(context.Background(),
				`UPDATE memberships SET role = $3 WHERE household_id = $1 AND user_id = $2`,
				h.HouseholdID, h.UserID, role); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(),
					`UPDATE memberships SET role = 'owner' WHERE household_id = $1 AND user_id = $2`, h.HouseholdID, h.UserID)
			})
			before := writeCalls
			resp, body := rpcPost(t, ts.URL, rwTok, mcp.MethodToolsCall, "add_note",
				map[string]any{"name": "add_note", "arguments": map[string]any{}})
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", resp.StatusCode, body)
			}
			if !strings.Contains(string(body), "the "+role+" role is read-only") {
				t.Fatalf("403 body %s lacks the role message", body)
			}
			if writeCalls != before {
				t.Fatal("write tool ran for a read-only role")
			}
			resp, body = rpcPost(t, ts.URL, rwTok, mcp.MethodToolsCall, "whoami",
				map[string]any{"name": "whoami", "arguments": map[string]any{}})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("read tool status = %d: %s", resp.StatusCode, body)
			}
		})
	}

	ctx := context.Background()
	revokedTok, revokedID := seedAgentToken(t, pool, h, []string{"read", "write"}, nil)
	if _, err := pool.Exec(ctx, `UPDATE agent_tokens SET revoked_at = NOW() WHERE id = $1`, revokedID); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Second)
	expiredTok, _ := seedAgentToken(t, pool, h, []string{"read"}, &past)

	_, h2 := testutil.SeedHousehold(t, pool)
	inactiveTok, _ := seedAgentToken(t, pool, h2, []string{"read"}, nil)
	if _, err := pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, h2.UserID); err != nil {
		t.Fatal(err)
	}

	before := whoamiCalls
	for name, tok := range map[string]string{
		"revoked":       revokedTok,
		"expired":       expiredTok,
		"inactive user": inactiveTok,
		"unknown":       auth.AgentTokenPrefix + strings.Repeat("A", 43),
		"malformed":     "hos_nope",
	} {
		t.Run(name+" gets 401", func(t *testing.T) {
			resp, body := rpcPost(t, ts.URL, tok, mcp.MethodToolsCall, "whoami",
				map[string]any{"name": "whoami", "arguments": map[string]any{}})
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %s", resp.StatusCode, body)
			}
			c := resp.Header.Get("WWW-Authenticate")
			if !strings.Contains(c, wantResourceMetadata) || !strings.Contains(c, `error="invalid_token"`) {
				t.Fatalf("WWW-Authenticate = %q, want the bad-JWT challenge", c)
			}
			if !strings.Contains(string(body), "invalid or expired token") {
				t.Fatalf("body %s differs from the bad-JWT 401", body)
			}
		})
	}
	if whoamiCalls != before {
		t.Fatal("tool ran for a rejected agent token")
	}
}

// TestRequireWriteScope_Backstop: even if a call reaches the tool chain
// without AuthMiddleware's HTTP check, a read-only agent token's call to a
// write tool returns a tool error and the handler never runs.
func TestRequireWriteScope_Backstop(t *testing.T) {
	s := server.NewServer(testConfig(), nil, testutil.NewTokenIssuer(t).Verifier(nil))
	ran := false
	s.RegisterTool("add_note", mcp.NewTool("add_note", mcp.WithDescription("w")),
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			ran = true
			return mcp.NewToolResultText("written"), nil
		})
	ctx := server.ContextWithClaims(context.Background(), &auth.Claims{
		UserID: "u", HouseholdID: "h", AgentTokenID: "t", Scopes: []string{"read"},
	})
	msg, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": mcp.MethodToolsCall,
		"params": map[string]any{"name": "add_note", "arguments": map[string]any{}},
	})
	out, _ := json.Marshal(s.MCPServer().HandleMessage(ctx, msg))
	if ran {
		t.Fatal("write tool ran for a read-only token")
	}
	if !strings.Contains(string(out), "read-only") {
		t.Fatalf("response %s lacks the read-only message", out)
	}
}

// TestAuth_DeactivatedUser drives the production router with tokens issued
// while the user was active, then deactivates the user (users.active=false)
// without touching the tokens. Deactivation is immediate because the flag is
// read on every request:
//   - Dex JWT: 403, no WWW-Authenticate, clear message (the not-a-member
//     treatment: signing in again cannot fix it).
//   - hos_ agent token: 401 with the bad-JWT challenge (#4405: every agent
//     token refusal, including an owner who left the household, is the same
//     401).
//
// Neither reaches the tool, and reactivating the user restores both.
func TestAuth_DeactivatedUser(t *testing.T) {
	pool := testutil.NewPool(t)
	_, h := testutil.SeedHousehold(t, pool)
	iss := testutil.NewTokenIssuer(t)

	s := server.NewServer(testConfig(), pool, iss.Verifier(pool))
	calls := 0
	s.RegisterTool("whoami", mcp.NewTool("whoami",
		mcp.WithDescription("Return the caller's claims."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls++
		b, _ := json.Marshal(server.ClaimsFromContext(ctx))
		return mcp.NewToolResultText(string(b)), nil
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	jwt := iss.Token("sub-owner", h.Email)
	agentTok, _ := seedAgentToken(t, pool, h, []string{"read"}, nil)
	call := func(t *testing.T, token string) (*http.Response, []byte) {
		t.Helper()
		return rpcPost(t, ts.URL, token, mcp.MethodToolsCall, "whoami",
			map[string]any{"name": "whoami", "arguments": map[string]any{}})
	}
	setActive := func(t *testing.T, active bool) {
		t.Helper()
		if _, err := pool.Exec(context.Background(), `UPDATE users SET active = $1 WHERE id = $2`, active, h.UserID); err != nil {
			t.Fatal(err)
		}
	}

	for name, tok := range map[string]string{"jwt": jwt, "agent token": agentTok} {
		if resp, body := call(t, tok); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s while active: status = %d: %s", name, resp.StatusCode, body)
		}
	}

	setActive(t, false)
	before := calls

	t.Run("jwt gets 403", func(t *testing.T) {
		resp, body := call(t, jwt)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", resp.StatusCode, body)
		}
		if c := resp.Header.Get("WWW-Authenticate"); c != "" {
			t.Errorf("403 carries WWW-Authenticate %q; re-authenticating cannot reactivate an account", c)
		}
		if !strings.Contains(string(body), "deactivated") {
			t.Errorf("body %s does not say the account is deactivated", body)
		}
	})
	t.Run("agent token gets 401", func(t *testing.T) {
		resp, body := call(t, agentTok)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401: %s", resp.StatusCode, body)
		}
		c := resp.Header.Get("WWW-Authenticate")
		if !strings.Contains(c, wantResourceMetadata) || !strings.Contains(c, `error="invalid_token"`) {
			t.Fatalf("WWW-Authenticate = %q, want the bad-JWT challenge", c)
		}
		if !strings.Contains(string(body), "invalid or expired token") {
			t.Fatalf("body %s differs from the bad-JWT 401", body)
		}
	})
	if calls != before {
		t.Fatalf("tool ran %d time(s) for a deactivated user", calls-before)
	}

	setActive(t, true)
	for name, tok := range map[string]string{"jwt": jwt, "agent token": agentTok} {
		if resp, body := call(t, tok); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s after reactivation: status = %d: %s", name, resp.StatusCode, body)
		}
	}
}

// TestRequireWriteScope_ReadOnlyRoles: a Dex session (no agent token) of a
// viewer, housesitter or vendor cannot run a write tool (#4689); the tool
// error names the role.
func TestRequireWriteScope_ReadOnlyRoles(t *testing.T) {
	for _, role := range []string{"viewer", "housesitter", "vendor"} {
		t.Run(role, func(t *testing.T) {
			s := server.NewServer(testConfig(), nil, testutil.NewTokenIssuer(t).Verifier(nil))
			ran := false
			s.RegisterTool("add_note", mcp.NewTool("add_note", mcp.WithDescription("w")),
				func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					ran = true
					return mcp.NewToolResultText("written"), nil
				})
			ctx := server.ContextWithClaims(context.Background(), &auth.Claims{UserID: "u", HouseholdID: "h", Role: role})
			msg, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": mcp.MethodToolsCall,
				"params": map[string]any{"name": "add_note", "arguments": map[string]any{}},
			})
			out, _ := json.Marshal(s.MCPServer().HandleMessage(ctx, msg))
			if ran {
				t.Fatal("write tool ran for a read-only role")
			}
			if !strings.Contains(string(out), "the "+role+" role is read-only") {
				t.Fatalf("response %s lacks the role message", out)
			}
		})
	}
}
