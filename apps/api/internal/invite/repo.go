package invite

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the invitations table.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new invitation repository backed by the given connection pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// Create inserts a new invitation and returns the created record.
func (r *Repo) Create(ctx context.Context, householdID, email, token, role string, invitedByID uuid.UUID, expiresAt time.Time) (*Invitation, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO invitations (household_id, email, token, role, invited_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, household_id, email, token, role, invited_by, expires_at, accepted_at, created_at`,
		householdID, email, token, role, invitedByID, expiresAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create invitation: %w", err)
	}
	defer rows.Close()

	inv, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Invitation])
	if err != nil {
		return nil, fmt.Errorf("collect invitation: %w", err)
	}
	return inv, nil
}

// ListByHousehold returns all pending invitations for a household.
func (r *Repo) ListByHousehold(ctx context.Context, householdID string) ([]Invitation, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, email, token, role, invited_by, expires_at, accepted_at, created_at
		 FROM invitations
		 WHERE household_id = $1
		 ORDER BY created_at`,
		householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	defer rows.Close()

	var invs []Invitation
	for rows.Next() {
		var inv Invitation
		if err := rows.Scan(&inv.ID, &inv.HouseholdID, &inv.Email, &inv.Token, &inv.Role, &inv.InvitedBy, &inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan invitation: %w", err)
		}
		invs = append(invs, inv)
	}
	return invs, nil
}

// GetByToken returns an invitation by its token.
func (r *Repo) GetByToken(ctx context.Context, token string) (*Invitation, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, household_id, email, token, role, invited_by, expires_at, accepted_at, created_at
		 FROM invitations
		 WHERE token = $1`,
		token,
	)

	inv := &Invitation{}
	err := row.Scan(&inv.ID, &inv.HouseholdID, &inv.Email, &inv.Token, &inv.Role, &inv.InvitedBy, &inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get invitation by token: %w", err)
	}
	return inv, nil
}

// Accept marks an invitation as accepted and creates a membership.
func (r *Repo) Accept(ctx context.Context, invitationID uuid.UUID, acceptedAt time.Time, role string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE invitations SET accepted_at = $1 WHERE id = $2`,
		acceptedAt, invitationID,
	)
	if err != nil {
		return fmt.Errorf("accept invitation: %w", err)
	}
	return nil
}
