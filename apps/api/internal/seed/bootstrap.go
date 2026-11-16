package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"home-os/api/internal/config"
	"home-os/api/internal/dex"
)

// BootstrapAdmin makes sure the ADMIN_EMAIL user exists, is the instance
// admin, and that Dex holds ADMIN_PASSWORD for that email.
//
// ADMIN_PASSWORD is authoritative: bootstrap is an operator reset of the
// admin account, so when Dex already has a password for ADMIN_EMAIL (set
// earlier, or by whoever created a stale entry) it is overwritten with
// UpdatePassword rather than silently kept. A failure to overwrite an
// existing entry is returned, not logged: otherwise whoever set that password
// would keep instance-admin sign-in. Other Dex failures (Dex unreachable) are
// logged and startup continues, as before.
//
// Pass a nil interface, not a nil *dex.Client, when Dex is unavailable.
func BootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, dexClient dex.PasswordSetter) error {
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
		// The bootstrap admin is always the instance admin (idempotent; also
		// upgrades admins created before migration 037).
		if _, err := pool.Exec(ctx,
			`UPDATE users SET is_instance_admin = true WHERE email = $1 AND NOT is_instance_admin`,
			cfg.AdminEmail,
		); err != nil {
			return fmt.Errorf("flag admin as instance admin: %w", err)
		}
		// Backfill default calendars for households the admin owns (idempotent).
		if err := ensureOwnedHouseholdCalendars(ctx, pool, cfg.AdminEmail); err != nil {
			return err
		}
		// Still sync password to Dex in case it was rotated
		if dexClient != nil {
			if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, cfg.AdminEmail).Scan(&userID); err != nil {
				return fmt.Errorf("get admin user id: %w", err)
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), 12)
			if err != nil {
				return fmt.Errorf("hash password for dex sync: %w", err)
			}
			if err := syncAdminPassword(ctx, dexClient, cfg.AdminEmail, string(hash), userID); err != nil {
				return err
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
		`INSERT INTO users (email, name, password_hash, is_instance_admin) VALUES ($1, $2, $3, true) RETURNING id`,
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

	if err := EnsureDefaultCalendars(ctx, pool, householdID); err != nil {
		return err
	}
	if err := ensureStarterListsTx(ctx, pool, householdID); err != nil {
		return err
	}

	// Sync to Dex
	if dexClient != nil {
		if err := syncAdminPassword(ctx, dexClient, cfg.AdminEmail, string(hash), userID); err != nil {
			return err
		}
	} else {
		slog.Warn("bootstrap: Dex client not available, password not synced to Dex")
	}

	slog.Info("bootstrap: admin user setup complete", "email", cfg.AdminEmail, "user_id", userID, "household_id", householdID)
	return nil
}

// syncAdminPassword stores hash as the Dex password for the admin email:
// created when Dex has none, overwritten when it has one. Only an existing
// entry that could not be overwritten is an error; any other Dex failure is
// logged so the API still starts without Dex.
func syncAdminPassword(ctx context.Context, d dex.PasswordSetter, email, hash, userID string) error {
	err := d.CreatePassword(ctx, email, hash, userID)
	switch {
	case err == nil:
		slog.Info("bootstrap: created admin password in Dex", "email", email)
		return nil
	case !errors.Is(err, dex.ErrPasswordExists):
		slog.Warn("bootstrap: failed to sync password to Dex", "email", email, "error", err)
		return nil
	}
	if err := d.UpdatePassword(ctx, email, hash); err != nil {
		return fmt.Errorf("bootstrap: Dex already has a password for the admin email and it could not be replaced: %w", err)
	}
	slog.Info("bootstrap: replaced admin password in Dex", "email", email)
	return nil
}

// defaultCalendars are created for every bootstrapped household so agents and
// the UI have somewhere to put events from day one.
var defaultCalendars = []struct {
	name    string
	calType string
	color   string
}{
	{"Family", "family", "#3B82F6"},
	{"Bills", "bills", "#EF4444"},
	{"Maintenance", "maintenance", "#F59E0B"},
}

// Execer is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx, so
// EnsureDefaultCalendars can run inside a caller's transaction.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// EnsureDefaultCalendars creates the Family, Bills and Maintenance calendars
// for a household. It is idempotent: a calendar is only inserted when the
// household has no calendar of that type yet. Used by admin bootstrap and by
// household creation.
func EnsureDefaultCalendars(ctx context.Context, db Execer, householdID string) error {
	created := 0
	for _, c := range defaultCalendars {
		tag, err := db.Exec(ctx,
			`INSERT INTO calendars (household_id, name, type, color, caldav_uid)
			 SELECT $1, $2, $3::calendar_type, $4, gen_random_uuid()::text
			 WHERE NOT EXISTS (
			     SELECT 1 FROM calendars WHERE household_id = $1 AND type = $3::calendar_type
			 )`,
			householdID, c.name, c.calType, c.color,
		)
		if err != nil {
			return fmt.Errorf("create default calendar %s: %w", c.name, err)
		}
		created += int(tag.RowsAffected())
	}
	if created > 0 {
		slog.Info("created default calendars", "household_id", householdID, "count", created)
	}
	return nil
}

// ensureOwnedHouseholdCalendars backfills default calendars for every
// household the given user owns.
func ensureOwnedHouseholdCalendars(ctx context.Context, pool *pgxpool.Pool, email string) error {
	rows, err := pool.Query(ctx,
		`SELECT m.household_id FROM memberships m
		 JOIN users u ON u.id = m.user_id
		 WHERE u.email = $1 AND m.role = 'owner'`, email)
	if err != nil {
		return fmt.Errorf("list admin households: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan admin household: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list admin households: %w", err)
	}
	for _, id := range ids {
		if err := EnsureDefaultCalendars(ctx, pool, id); err != nil {
			return err
		}
	}
	return nil
}
