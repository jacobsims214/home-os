package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"home-os/mcp/internal/config"
	"home-os/mcp/internal/server"
	"home-os/mcp/internal/testutil"
)

// TestAuthMiddleware_StatusCodes checks the HTTP mapping of enrichment
// results: member 200 (claims injected), non-member 403, bad token 401,
// database failure 500.
func TestAuthMiddleware_StatusCodes(t *testing.T) {
	pool := testutil.NewPool(t)
	_, h := testutil.SeedHousehold(t, pool) // user has no dex_sub: resolves by email
	iss := testutil.NewTokenIssuer(t)

	newHandler := func() (http.Handler, *string) {
		s := server.NewServer(&config.Config{PublicBaseURL: "https://mcp.test"}, pool, iss.Verifier(pool))
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
	tests := []struct {
		name  string
		token string
		want  int
	}{
		{"member", iss.Token("sub-owner", h.Email), http.StatusOK},
		{"valid token, no membership", iss.Token("sub-stranger", "stranger@test.local"), http.StatusForbidden},
		{"bad signature", other.Token("sub-owner", h.Email), http.StatusUnauthorized},
		{"garbage token", "not-a-jwt", http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler, gotHousehold := newHandler()
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(context.Background())
			req.Header.Set("Authorization", "Bearer "+tc.token)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusOK && *gotHousehold != h.HouseholdID.String() {
				t.Fatalf("household in context = %q, want %s", *gotHousehold, h.HouseholdID)
			}
		})
	}

	t.Run("database failure", func(t *testing.T) {
		broken := testutil.NewPool(t)
		broken.Close()
		s := server.NewServer(&config.Config{PublicBaseURL: "https://mcp.test"}, broken, iss.Verifier(broken))
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
