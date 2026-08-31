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

func BootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, dexClient *dex.Client) error {
	if cfg.AdminEmail == "" || cfg.AdminPassword == "" {
		slog.Info("bootstrap: ADMIN_EMAIL and ADMIN_PASSWORD not set, skipping admin bootstrap")
		return nil
	}

	// Check if this specific admin user already exists
	var userID string
	var exists bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE email = $1)`, cfg.AdminEmail).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check admin exists: %w", err)
	}

	if exists {
		slog.Info("bootstrap: admin user already exists", "email", cfg.AdminEmail)
		// Still sync password to Dex in case it was rotated
		if dexClient != nil {
			if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, cfg.AdminEmail).Scan(&userID); err != nil {
				return fmt.Errorf("get admin user id: %w", err)
			}
			if err := dexClient.CreatePassword(ctx, cfg.AdminEmail, cfg.AdminPassword, userID); err != nil {
				slog.Warn("bootstrap: failed to sync password to Dex", "email", cfg.AdminEmail, "error", err)
			} else {
				slog.Info("bootstrap: synced admin password to Dex", "email", cfg.AdminEmail)
			}
		}
		return nil
	}

	slog.Info("bootstrap: creating admin user", "email", cfg.AdminEmail)

	// Create user
	hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), 12)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		cfg.AdminEmail, cfg.AdminName, string(hash),
	).Scan(&userID); err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}
	slog.Info("bootstrap: created user", "email", cfg.AdminEmail, "user_id", userID)

	// Create household
	var householdID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO households (name) VALUES ($1) RETURNING id`,
		cfg.AdminName+"'s Home",
	).Scan(&householdID); err != nil {
		return fmt.Errorf("create admin household: %w", err)
	}
	slog.Info("bootstrap: created household", "household_id", householdID)

	// Create membership
	if _, err := pool.Exec(ctx,
		`INSERT INTO memberships (household_id, user_id, role) VALUES ($1, $2, $3)`,
		householdID, userID, "owner",
	); err != nil {
		return fmt.Errorf("create admin membership: %w", err)
	}
	slog.Info("bootstrap: created owner membership")

	// Sync to Dex
	if dexClient != nil {
		if err := dexClient.CreatePassword(ctx, cfg.AdminEmail, cfg.AdminPassword, userID); err != nil {
			slog.Warn("bootstrap: failed to sync password to Dex", "email", cfg.AdminEmail, "error", err)
		} else {
			slog.Info("bootstrap: synced admin password to Dex", "email", cfg.AdminEmail)
		}
	} else {
		slog.Warn("bootstrap: Dex client not available, password not synced to Dex")
	}

	slog.Info("bootstrap: admin user setup complete", "email", cfg.AdminEmail, "user_id", userID, "household_id", householdID)
	return nil
}