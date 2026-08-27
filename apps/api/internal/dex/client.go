// Package dex provides a client for Dex's gRPC API.
// It is used to manage passwords in Dex's local password database
// when users register or change their passwords.
package dex

import (
	"context"
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

// CreatePassword creates a password entry in Dex's local password database.
// The hash must be a bcrypt hash (not plaintext) — Dex does not hash plaintext passwords.
func (c *Client) CreatePassword(ctx context.Context, email, hash, userID string) error {
	_, err := c.api.CreatePassword(ctx, &dexapi.CreatePasswordReq{
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
	slog.Info("dex: created password", "email", email)
	return nil
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}