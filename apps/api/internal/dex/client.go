// Package dex provides a client for Dex's gRPC API.
// It is used to manage passwords in Dex's local password database
// when users register or change their passwords.
package dex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	dexapi "github.com/dexidp/dex/api/v2"
	"google.golang.org/grpc"
)

// Client is a gRPC client for Dex's password management API.
type Client struct {
	conn *grpc.ClientConn
	api  dexapi.DexClient
}

// NewClient creates a new Dex gRPC client for the given address, presenting
// the client certificate in tlsCfg and verifying Dex against tlsCfg.CAFile.
// Set tlsCfg.Insecure for local development only: Dex's gRPC API has no
// authentication, so the transport is the only control on it (#697).
//
// grpc.NewClient does not connect; the first RPC establishes the connection
// and gRPC re-establishes it as needed.
func NewClient(addr string, tlsCfg TLSConfig) (*Client, error) {
	creds, err := tlsCfg.TransportCredentials()
	if err != nil {
		return nil, fmt.Errorf("dex client: %w", err)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("dex client: %w", err)
	}
	return &Client{
		conn: conn,
		api:  dexapi.NewDexClient(conn),
	}, nil
}

// ErrPasswordExists is returned by CreatePassword when Dex already has a
// password for the email. Dex answers that case with AlreadyExists and a nil
// error, and leaves the existing entry (its hash and user ID) untouched, so a
// caller that ignored it would believe the new password took effect.
//
// Callers creating a new account must fail closed on it. Only an owner or
// admin reset of that same account (bootstrap, password reset) may follow up
// with UpdatePassword.
var ErrPasswordExists = errors.New("dex: a password already exists for this email")

// ErrPasswordNotFound is returned by UpdatePassword when Dex has no password
// for the email.
var ErrPasswordNotFound = errors.New("dex: no password exists for this email")

// CreatePassword creates a password entry in Dex's local password database.
// The hash must be a bcrypt hash (not plaintext) — Dex does not hash plaintext passwords.
// It never overwrites: an existing entry for email yields ErrPasswordExists.
func (c *Client) CreatePassword(ctx context.Context, email, hash, userID string) error {
	resp, err := c.api.CreatePassword(ctx, &dexapi.CreatePasswordReq{
		Password: &dexapi.Password{
			Email:    email,
			Hash:     []byte(hash),
			UserId:   userID,
			Username: email,
		},
	})
	if err != nil {
		return fmt.Errorf("dex create password: %w", err)
	}
	if resp.GetAlreadyExists() {
		return ErrPasswordExists
	}
	slog.Info("dex: created password", "email", email)
	return nil
}

// UpdatePassword replaces the bcrypt hash of the existing Dex password entry
// for email. The entry's user ID (the Dex subject) and username are kept.
// It returns ErrPasswordNotFound when Dex has no entry for email.
//
// Use it only to reset the password of the account that owns email (admin
// bootstrap, a password reset proven by a reset token), never to take over
// an entry while creating a new account.
func (c *Client) UpdatePassword(ctx context.Context, email, hash string) error {
	resp, err := c.api.UpdatePassword(ctx, &dexapi.UpdatePasswordReq{
		Email:   email,
		NewHash: []byte(hash),
	})
	if err != nil {
		return fmt.Errorf("dex update password: %w", err)
	}
	if resp.GetNotFound() {
		return ErrPasswordNotFound
	}
	slog.Info("dex: updated password", "email", email)
	return nil
}

// ErrPasswordNotVerified means Dex did not accept the password handed to
// VerifyPassword.
var ErrPasswordNotVerified = errors.New("dex: password not verified")

// ErrPasswordLookup means Dex has no password entry for this email at all
// (VerifyPassword answers NotFound). It is deliberately NOT distinguishable
// from a wrong password at the HTTP layer: a change-password screen that
// answered differently for "no such user" and "wrong password" would confirm
// which emails exist.
var ErrPasswordLookup = errors.New("dex: no password entry for this email")

// VerifyPassword asks Dex whether plaintext is the password stored for email.
// It returns nil only when Dex answers verified, and ErrPasswordNotVerified
// in both remaining cases: a wrong password, and an email Dex has no password
// for (which comes back wrapped with ErrPasswordLookup, so a caller may log
// the difference but must not report it). Any transport failure is returned
// as-is so the caller can answer 5xx rather than "wrong password".
//
// The plaintext crosses the mTLS-protected gRPC admin channel (#697) and is
// hashed and compared inside Dex; this client never sees a hash. That is why
// the check is a Dex call and not auth.CheckPassword against
// users.password_hash: that column is a leftover from before Dex became the
// identity store, and it is not the credential that signs anyone in.
func (c *Client) VerifyPassword(ctx context.Context, email, plaintext string) error {
	if plaintext == "" {
		// Dex returns a bare error for an empty password, which a caller
		// would have to read as an outage. An empty password cannot be the
		// stored one; answer the ordinary failed check.
		return ErrPasswordNotVerified
	}
	resp, err := c.api.VerifyPassword(ctx, &dexapi.VerifyPasswordReq{Email: email, Password: plaintext})
	if err != nil {
		return fmt.Errorf("dex verify password: %w", err)
	}
	switch {
	case resp.GetNotFound():
		return fmt.Errorf("%w: %w", ErrPasswordNotVerified, ErrPasswordLookup)
	case !resp.GetVerified():
		return ErrPasswordNotVerified
	}
	return nil
}

// RefreshClientIDs returns the client IDs the user (a Dex "sub") currently
// holds a refresh token for. Dex keeps at most one refresh token per user per
// client (#4649), so this is the list of clients that can mint new id tokens
// for that user.
//
// A user with no offline session (never signed in with offline_access, or
// signed out) yields an empty slice, not an error.
func (c *Client) RefreshClientIDs(ctx context.Context, sub string) ([]string, error) {
	resp, err := c.api.ListRefresh(ctx, &dexapi.ListRefreshReq{UserId: sub})
	if err != nil {
		return nil, fmt.Errorf("dex list refresh: %w", err)
	}
	refs := resp.GetRefreshTokens()
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if id := ref.GetClientId(); id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// ErrRefreshNotFound is returned by RevokeRefresh when Dex holds no refresh
// token for that user and client. That is normal: a client that signed out,
// or one that never asked for offline_access.
var ErrRefreshNotFound = errors.New("dex: no refresh token for this user and client")

// RevokeRefresh deletes the user's refresh token for clientID, so it can no
// longer be redeemed for fresh id tokens. sub is the "sub" claim of the user's
// ID token (Dex's own subject encoding, not users.id).
//
// Only the refresh token goes. Id tokens already issued keep working until
// they expire: the API verifies them against Dex's JWKS and has no revocation
// list. A caller that changes a credential must therefore say what still
// works instead of implying the change ended every session.
func (c *Client) RevokeRefresh(ctx context.Context, sub, clientID string) error {
	resp, err := c.api.RevokeRefresh(ctx, &dexapi.RevokeRefreshReq{UserId: sub, ClientId: clientID})
	if err != nil {
		return fmt.Errorf("dex revoke refresh: %w", err)
	}
	if resp.GetNotFound() {
		return ErrRefreshNotFound
	}
	return nil
}

// PasswordSetter is the part of *Client that SetPassword needs.
type PasswordSetter interface {
	CreatePassword(ctx context.Context, email, hash, userID string) error
	UpdatePassword(ctx context.Context, email, hash string) error
}

// SetPassword creates the Dex password for email, or updates the existing one
// when Dex already has it. Owner/admin resets only.
func SetPassword(ctx context.Context, p PasswordSetter, email, hash, userID string) error {
	err := p.CreatePassword(ctx, email, hash, userID)
	if !errors.Is(err, ErrPasswordExists) {
		return err
	}
	return p.UpdatePassword(ctx, email, hash)
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
