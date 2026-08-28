// Package seed provides database seeding functions.
package seed

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/crypto/bcrypt"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/config"
	"home-os/api/internal/dex"
)

// BootstrapAdmin creates the initial admin user if the users table is empty.
// It is intended to run on first API startup in production deployments where
// no demo seed data exists. The admin credentials come from ADMIN_EMAIL and
// ADMIN_PASSWORD environment variables.
//
// If users already exist, BootstrapAdmin is a no-op — this prevents it from
// running again on subsequent restarts or alongside DEMO_MODE.
func BootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, dexClient *dex.Client) error {
	// Check if any users exist
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return fmt.Errorf("check existing users: %w", err)
	}
	if count > 0 {
		slog.Info("bootstrap: users already exist, skipping admin creation")
		return nil
	}

	if cfg.AdminEmail == "" || cfg.AdminPassword == "" {
		slog.Warn("bootstrap: ADMIN_EMAIL and ADMIN_PASSWORD must be set to create initial admin user")
		return nil
	}

	slog.Info("bootstrap: creating initial admin user", "email", cfg.AdminEmail)

	// Create user with bcrypt-hashed password
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), 12)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	var userID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		cfg.AdminEmail, cfg.AdminName, string(hash),
	).Scan(&userID); err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	// Create household
	var householdID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ($1) RETURNING id`,
		cfg.AdminName+"'s Home",
	).Scan(&householdID); err != nil {
		return fmt.Errorf("create admin household: %w", err)
	}

	// Create owner membership
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
		householdID, userID, "owner",
	); err != nil {
		return fmt.Errorf("create admin membership: %w", err)
	}

	// Sync password to Dex's local password database
	if dexClient != nil {
		if err := dexClient.CreatePassword(ctx, cfg.AdminEmail, string(hash), userID); err != nil {
			slog.Warn("bootstrap: failed to sync password to Dex", "error", err)
		}
	}

	slog.Info("bootstrap: admin user created", "email", cfg.AdminEmail, "user_id", userID, "household_id", householdID)
	return nil
}