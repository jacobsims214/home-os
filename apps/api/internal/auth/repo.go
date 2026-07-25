package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the users and memberships tables
// using a pgx connection pool.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new auth repository backed by the given connection pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// GetUserByEmail returns the user with the given email, or nil if no such user exists.
func (r *Repo) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, email, name, password_hash, caldav_password_hash, avatar_url, created_at, updated_at
		 FROM users WHERE email = $1`,
		email,
	)
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	defer rows.Close()

	user, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[User])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect user: %w", err)
	}
	return user, nil
}

// GetUserByID returns the user with the given ID, or nil if no such user exists.
func (r *Repo) GetUserByID(ctx context.Context, userID string) (*User, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, email, name, password_hash, caldav_password_hash, avatar_url, created_at, updated_at
		 FROM users WHERE id = $1`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	defer rows.Close()

	user, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[User])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect user: %w", err)
	}
	return user, nil
}

// CreateUser inserts a new user into the users table and returns the created record.
// The passwordHash should already be bcrypt-hashed before calling this method.
func (r *Repo) CreateUser(ctx context.Context, email, name, passwordHash string) (*User, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO users (email, name, password_hash)
		 VALUES ($1, $2, $3)
		 RETURNING id, email, name, password_hash, caldav_password_hash, avatar_url, created_at, updated_at`,
		email, name, passwordHash,
	)
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	defer rows.Close()

	user, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[User])
	if err != nil {
		return nil, fmt.Errorf("collect created user: %w", err)
	}
	return user, nil
}

// GetMemberships returns all household memberships for the given user.
func (r *Repo) GetMemberships(ctx context.Context, userID string) ([]*Membership, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, user_id, role, created_at
		 FROM memberships WHERE user_id = $1`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("get memberships: %w", err)
	}
	defer rows.Close()

	memberships, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Membership])
	if err != nil {
		return nil, fmt.Errorf("collect memberships: %w", err)
	}
	return memberships, nil
}

// UpdateCaldavPasswordHash updates the caldav_password_hash for the given user.
func (r *Repo) UpdateCaldavPasswordHash(ctx context.Context, userID uuid.UUID, hash string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE users SET caldav_password_hash = $1 WHERE id = $2`,
		hash, userID,
	)
	return err
}

// UpdatePassword updates the password_hash for the given user.
func (r *Repo) UpdatePassword(ctx context.Context, userID string, hash string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE users SET password_hash = $1 WHERE id = $2`,
		hash, userID,
	)
	return err
}

// CreatePasswordResetToken creates a password reset token for the given user.
func (r *Repo) CreatePasswordResetToken(ctx context.Context, userID uuid.UUID, token string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO password_reset_tokens (user_id, token, expires_at) VALUES ($1, $2, NOW() + INTERVAL '1 hour')`,
		userID, token,
	)
	return err
}

// GetPasswordResetToken retrieves the user ID for a valid (unused, unexpired) reset token.
func (r *Repo) GetPasswordResetToken(ctx context.Context, token string) (string, error) {
	var userID string
	err := r.pool.QueryRow(ctx,
		`SELECT user_id FROM password_reset_tokens WHERE token = $1 AND used_at IS NULL AND expires_at > NOW()`, token,
	).Scan(&userID)
	if err != nil {
		return "", err
	}
	return userID, nil
}

// MarkResetTokenUsed marks a reset token as used.
func (r *Repo) MarkResetTokenUsed(ctx context.Context, token string) error {
	_, err := r.pool.Exec(ctx, `UPDATE password_reset_tokens SET used_at = NOW() WHERE token = $1`, token)
	return err
}
