package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/config"
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
