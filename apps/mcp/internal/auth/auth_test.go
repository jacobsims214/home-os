package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/testutil"
)

const testResource = "https://home.example.test/mcp"

// TestVerify_ResourceServerChecks covers the token checks an OAuth resource
// server must make: signature, issuer, expiry and audience. No database is
// needed: Verify does not enrich.
func TestVerify_ResourceServerChecks(t *testing.T) {
	iss := testutil.NewTokenIssuer(t)
	other := testutil.NewTokenIssuer(t)
	v := iss.Verifier(nil,
		auth.WithAudiences("home-os-ui", testutil.TestAudience, testResource),
		auth.WithAudiencePrefixes("mcp-"),
	)
	expired := time.Now().Add(-time.Hour).Unix()

	tests := []struct {
		name    string
		token   string
		wantErr bool
		wantAud bool // error must be ErrInvalidAudience
	}{
		{"valid client-id audience", iss.Token("sub-1", "a@test.local"), false, false},
		{"valid ui audience", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": "home-os-ui"}), false, false},
		{"resource URI audience (RFC 8707)", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": testResource}), false, false},
		{"audience list containing an accepted value", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": []string{"other-api", testutil.TestAudience}}), false, false},
		{"dynamically registered client", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": "mcp-0f8e2c4a"}), false, false},
		{"wrong audience", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": "home-os-api"}), true, true},
		{"audience list without an accepted value", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": []string{"other-api", "billing"}}), true, true},
		{"resource URI of another server", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": "https://other.example.test/mcp"}), true, true},
		{"no audience", iss.TokenWith("sub-1", "a@test.local", map[string]any{"aud": nil}), true, true},
		{"expired", iss.TokenWith("sub-1", "a@test.local", map[string]any{"exp": expired}), true, false},
		{"wrong issuer", iss.TokenWith("sub-1", "a@test.local", map[string]any{"iss": "https://evil.example/dex"}), true, false},
		{"unsigned (alg none)", iss.UnsignedToken("sub-1", "a@test.local"), true, false},
		{"signed by another key", other.Token("sub-1", "a@test.local"), true, false},
		{"empty sub", iss.Token("", "a@test.local"), true, false},
		{"garbage", "not-a-jwt", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ident, err := v.Verify(context.Background(), tc.token)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("Verify: %v", err)
				}
				if ident.Subject != "sub-1" || ident.Email != "a@test.local" {
					t.Fatalf("identity = %+v", ident)
				}
				return
			}
			if err == nil {
				t.Fatal("Verify accepted the token")
			}
			if got := errors.Is(err, auth.ErrInvalidAudience); got != tc.wantAud {
				t.Fatalf("errors.Is(err, ErrInvalidAudience) = %v, want %v (err %v)", got, tc.wantAud, err)
			}
			if errors.Is(err, auth.ErrNoMembership) || errors.Is(err, auth.ErrMemberLookup) {
				t.Fatalf("token error classified as membership error: %v", err)
			}
		})
	}
}

// TestVerify_FailsClosedWithoutAudiences: an enabled verifier with no
// accepted audience rejects even a perfectly signed token.
func TestVerify_FailsClosedWithoutAudiences(t *testing.T) {
	iss := testutil.NewTokenIssuer(t)
	v := iss.Verifier(nil, auth.WithAudiences(" ", ""), auth.WithAudiencePrefixes(""))
	_, err := v.Verify(context.Background(), iss.Token("sub-1", "a@test.local"))
	if !errors.Is(err, auth.ErrInvalidAudience) {
		t.Fatalf("err = %v, want ErrInvalidAudience", err)
	}
}

func TestNewVerifier(t *testing.T) {
	ctx := context.Background()

	v, err := auth.NewVerifier(ctx, "", "", nil)
	if err != nil || v.Enabled() {
		t.Fatalf("no issuer: v.Enabled()=%v err=%v, want disabled verifier", v != nil && v.Enabled(), err)
	}

	if _, err := auth.NewVerifier(ctx, testutil.TestIssuer, "http://127.0.0.1:1/keys", nil); err == nil ||
		!strings.Contains(err.Error(), "audiences") {
		t.Fatalf("issuer without audiences: err = %v, want a configuration error", err)
	}

	v, err = auth.NewVerifier(ctx, testutil.TestIssuer, "http://127.0.0.1:1/keys", nil,
		auth.WithAudiences(testutil.TestAudience))
	if err != nil || !v.Enabled() {
		t.Fatalf("issuer with audiences: err = %v", err)
	}
}
