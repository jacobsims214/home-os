package integration

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the integrations table using a pgx connection pool.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new integration repository.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// GetAll returns all integrations for a household (one per type).
// Config is returned as raw encrypted bytes — decryption happens in the handler layer.
func (r *Repo) GetAll(ctx context.Context, householdID uuid.UUID) ([]*Integration, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, type, config, status,
		        last_health_check, last_sync, error_message,
		        created_at, updated_at
		 FROM integrations
		 WHERE household_id = $1
		 ORDER BY type`,
		householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("list integrations: %w", err)
	}
	defer rows.Close()

	integrations, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Integration])
	if err != nil {
		return nil, fmt.Errorf("collect integrations: %w", err)
	}
	return integrations, nil
}

// Upsert inserts or updates an integration for the given household and type.
// If an integration already exists for this (household_id, type), it is updated.
// Otherwise a new row is inserted.
func (r *Repo) Upsert(ctx context.Context, householdID uuid.UUID, integrationType string, encryptedConfig []byte) (*Integration, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO integrations (household_id, type, config, status)
		 VALUES ($1, $2, $3, 'connected')
		 ON CONFLICT (household_id, type)
		 DO UPDATE SET config = $3, status = 'connected', error_message = NULL, updated_at = NOW()
		 RETURNING id, household_id, type, config, status,
		           last_health_check, last_sync, error_message,
		           created_at, updated_at`,
		householdID, integrationType, encryptedConfig,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert integration: %w", err)
	}
	defer rows.Close()

	integration, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Integration])
	if err != nil {
		return nil, fmt.Errorf("upsert integration: collect: %w", err)
	}
	return integration, nil
}

// Delete disconnects an integration by clearing its config and setting status to disconnected.
func (r *Repo) Delete(ctx context.Context, householdID uuid.UUID, integrationType string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE integrations
		 SET config = NULL, status = 'disconnected', error_message = NULL, updated_at = NOW()
		 WHERE household_id = $1 AND type = $2`,
		householdID, integrationType,
	)
	if err != nil {
		return fmt.Errorf("delete integration: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete integration: %w", pgx.ErrNoRows)
	}
	return nil
}

// GetByType returns a single integration by household and type.
// Returns nil if not found.
func (r *Repo) GetByType(ctx context.Context, householdID uuid.UUID, integrationType string) (*Integration, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, type, config, status,
		        last_health_check, last_sync, error_message,
		        created_at, updated_at
		 FROM integrations
		 WHERE household_id = $1 AND type = $2`,
		householdID, integrationType,
	)
	if err != nil {
		return nil, fmt.Errorf("get integration: %w", err)
	}
	defer rows.Close()

	integration, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Integration])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect integration: %w", err)
	}
	return integration, nil
}

// UpdateStatus sets the status, error_message, and optionally last_health_check
// for an integration.
func (r *Repo) UpdateStatus(ctx context.Context, householdID uuid.UUID, integrationType string, status string, errorMsg *string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE integrations
		 SET status = $3, error_message = $4, last_health_check = NOW(), updated_at = NOW()
		 WHERE household_id = $1 AND type = $2`,
		householdID, integrationType, status, errorMsg,
	)
	if err != nil {
		return fmt.Errorf("update integration status: %w", err)
	}
	return nil
}