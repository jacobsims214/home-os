package dex

// Live mTLS check against a real Dex v2.46.1 gRPC admin listener. This is not
// part of the suite (it needs DEX_LIVE_ADDR and the cert material); it is the
// evidence that VerifyPassword and RevokeRefresh work on the actual #697 path
// rather than only against a fake.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestLiveMTLS(t *testing.T) {
	addr := os.Getenv("DEX_LIVE_ADDR")
	if addr == "" {
		t.Skip("DEX_LIVE_ADDR not set")
	}
	cfg := TLSConfig{
		CertFile:   os.Getenv("DEX_LIVE_CERT"),
		KeyFile:    os.Getenv("DEX_LIVE_KEY"),
		CAFile:     os.Getenv("DEX_LIVE_CA"),
		ServerName: os.Getenv("DEX_LIVE_SERVER_NAME"),
	}
	c, err := NewClient(addr, cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const email, pw, sub = "live-check@example.com", "the-original-password", "CiQ-live-check"
	// Create, or update if a previous run left it.
	if err := SetPassword(ctx, c, email, mustHash(t, pw), sub); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	t.Log("SetPassword over mTLS: ok")

	if err := c.VerifyPassword(ctx, email, pw); err != nil {
		t.Fatalf("VerifyPassword(correct) = %v, want nil", err)
	}
	t.Log("VerifyPassword(correct): nil")

	err = c.VerifyPassword(ctx, email, "wrong-password")
	if !errors.Is(err, ErrPasswordNotVerified) {
		t.Fatalf("VerifyPassword(wrong) = %v, want ErrPasswordNotVerified", err)
	}
	t.Logf("VerifyPassword(wrong): %v (lookup=%v)", err, errors.Is(err, ErrPasswordLookup))

	err = c.VerifyPassword(ctx, "nobody-here@example.com", "anything")
	if !errors.Is(err, ErrPasswordNotVerified) || !errors.Is(err, ErrPasswordLookup) {
		t.Fatalf("VerifyPassword(unknown email) = %v, want NotVerified+Lookup", err)
	}
	t.Logf("VerifyPassword(unknown email): %v (lookup=%v)", err, errors.Is(err, ErrPasswordLookup))

	// The actual change: UpdatePassword then both checks flip.
	if err := c.UpdatePassword(ctx, email, mustHash(t, "the-new-password")); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	if err := c.VerifyPassword(ctx, email, "the-new-password"); err != nil {
		t.Fatalf("VerifyPassword(new) after update = %v, want nil", err)
	}
	if err := c.VerifyPassword(ctx, email, pw); !errors.Is(err, ErrPasswordNotVerified) {
		t.Fatalf("VerifyPassword(old) after update = %v, want ErrPasswordNotVerified", err)
	}
	t.Log("UpdatePassword + both verifications: ok")

	// Refresh-token listing and revocation on the real API, against a subject
	// that really does hold one (DEX_LIVE_SUB and DEX_LIVE_CLIENT come from a
	// real PKCE login; see the completion notes).
	liveSub := os.Getenv("DEX_LIVE_SUB")
	if liveSub == "" {
		t.Skip("DEX_LIVE_SUB not set: pass a real sub from a login")
	}
	clientID := os.Getenv("DEX_LIVE_CLIENT")
	clients, err := c.RefreshClientIDs(ctx, liveSub)
	if err != nil {
		t.Fatalf("RefreshClientIDs: %v", err)
	}
	t.Logf("RefreshClientIDs(%s) = %v", liveSub, clients)

	if err := c.RevokeRefresh(ctx, liveSub, clientID); err != nil {
		t.Fatalf("RevokeRefresh: %v", err)
	}
	t.Logf("RevokeRefresh(%s, %s): ok", liveSub, clientID)

	clients, err = c.RefreshClientIDs(ctx, liveSub)
	if err != nil {
		t.Fatalf("RefreshClientIDs after revoke: %v", err)
	}
	if len(clients) != 0 {
		t.Fatalf("after revoke the user still holds refresh tokens for %v", clients)
	}
	t.Log("after revoke: no refresh tokens left")

	// A second revoke is NotFound, which the handler treats as already gone.
	err = c.RevokeRefresh(ctx, liveSub, clientID)
	if !errors.Is(err, ErrRefreshNotFound) {
		t.Fatalf("second RevokeRefresh = %v, want ErrRefreshNotFound", err)
	}
	t.Log("RevokeRefresh(already gone): ErrRefreshNotFound, as designed")
}

func mustHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return string(h)
}
