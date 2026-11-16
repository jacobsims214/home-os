// Package dex provides a client for Dex's gRPC API.
// It is used to manage passwords in Dex's local password database
// when users register or change their passwords.
package dex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	dexapi "github.com/dexidp/dex/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client is a gRPC client for Dex's password management API.
type Client struct {
	conn *grpc.ClientConn
	api  dexapi.DexClient
}

// NewClient creates a new Dex gRPC client connected to the given address.
// For local development, it uses insecure credentials (no TLS).
func NewClient(addr string) (*Client, error) {
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
