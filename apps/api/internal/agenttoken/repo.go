// Package agenttoken manages per-member, revocable, scoped API tokens that a
// family member hands to their own agent. Only a SHA-256 hash of each token is
// stored (agent_tokens.token_hash, migration 034); the plaintext is returned
// once, at creation.
package agenttoken

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a token does not exist or does not belong to
// the caller.
var ErrNotFound = errors.New("agent token not found")

// Token is an agent_tokens row without its hash. The hash never leaves the repo.
type Token struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	HouseholdID uuid.UUID
	Name        string
	TokenPrefix string
	Scopes      []string
	LastUsedAt  *time.Time
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
	CreatedAt   time.Time
}

// Repo reads and writes agent_tokens. Every query is scoped to the owning
// user and household.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new agent token repository.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

const tokenColumns = `id, user_id, household_id, name, token_prefix, scopes, last_used_at, expires_at, revoked_at, created_at`

// Create inserts a token row with the given hash and returns the stored row.
func (r *Repo) Create(ctx context.Context, t *Token, hash []byte) (*Token, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO agent_tokens (user_id, household_id, name, token_prefix, token_hash, scopes, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING `+tokenColumns,
		t.UserID, t.HouseholdID, t.Name, t.TokenPrefix, hash, t.Scopes, t.ExpiresAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert agent token: %w", err)
	}
	out, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByPos[Token])
	if err != nil {
		return nil, fmt.Errorf("insert agent token: %w", err)
	}
	return out, nil
}

// ListByUser returns the user's tokens in a household, newest first,
// including revoked and expired ones.
func (r *Repo) ListByUser(ctx context.Context, householdID, userID uuid.UUID) ([]*Token, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+tokenColumns+`
		 FROM agent_tokens
		 WHERE household_id = $1 AND user_id = $2
		 ORDER BY created_at DESC, id`,
		householdID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list agent tokens: %w", err)
	}
	tokens, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Token])
	if err != nil {
		return nil, fmt.Errorf("collect agent tokens: %w", err)
	}
	return tokens, nil
}

// Revoke sets revoked_at on the user's token. Revoking an already revoked
// token keeps its original revoked_at. Returns ErrNotFound when the token is
// not the user's.
func (r *Repo) Revoke(ctx context.Context, householdID, userID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE agent_tokens
		 SET revoked_at = COALESCE(revoked_at, NOW())
		 WHERE id = $1 AND household_id = $2 AND user_id = $3`,
		id, householdID, userID,
	)
	if err != nil {
		return fmt.Errorf("revoke agent token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
