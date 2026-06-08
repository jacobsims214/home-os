package property

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the properties and rooms tables
// using a pgx connection pool. All property methods are scoped to a
// household_id to enforce multi-tenant isolation.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new property repository backed by the given connection pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// ListProperties returns all properties for a household.
func (r *Repo) ListProperties(ctx context.Context, householdID uuid.UUID) ([]*Property, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, name, address, property_type, notes, created_at, updated_at
		 FROM properties WHERE household_id = $1 ORDER BY name`,
		householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("list properties: %w", err)
	}
	defer rows.Close()

	properties, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Property])
	if err != nil {
		return nil, fmt.Errorf("collect properties: %w", err)
	}
	return properties, nil
}

// GetProperty returns a single property by ID, scoped to the household.
func (r *Repo) GetProperty(ctx context.Context, propertyID, householdID uuid.UUID) (*Property, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, name, address, property_type, notes, created_at, updated_at
		 FROM properties WHERE id = $1 AND household_id = $2`,
		propertyID, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("get property: %w", err)
	}
	defer rows.Close()

	property, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Property])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect property: %w", err)
	}
	return property, nil
}

// CreateProperty inserts a new property and returns the created record.
func (r *Repo) CreateProperty(ctx context.Context, householdID uuid.UUID, name string, address, propertyType, notes *string) (*Property, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO properties (household_id, name, address, property_type, notes)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, household_id, name, address, property_type, notes, created_at, updated_at`,
		householdID, name, address, propertyType, notes,
	)
	if err != nil {
		return nil, fmt.Errorf("create property: %w", err)
	}
	defer rows.Close()

	property, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Property])
	if err != nil {
		return nil, fmt.Errorf("collect created property: %w", err)
	}
	return property, nil
}

// UpdateProperty updates an existing property and returns the updated record.
// Only updates fields that are non-nil to allow partial updates.
func (r *Repo) UpdateProperty(ctx context.Context, propertyID, householdID uuid.UUID, name, address, propertyType, notes *string) (*Property, error) {
	rows, err := r.pool.Query(ctx,
		`UPDATE properties
		 SET name = COALESCE($3, name),
		     address = $4,
		     property_type = $5,
		     notes = $6,
		     updated_at = NOW()
		 WHERE id = $1 AND household_id = $2
		 RETURNING id, household_id, name, address, property_type, notes, created_at, updated_at`,
		propertyID, householdID, name, address, propertyType, notes,
	)
	if err != nil {
		return nil, fmt.Errorf("update property: %w", err)
	}
	defer rows.Close()

	property, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Property])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect updated property: %w", err)
	}
	return property, nil
}

// DeleteProperty deletes a property by ID, scoped to the household.
// Returns false if the property was not found.
func (r *Repo) DeleteProperty(ctx context.Context, propertyID, householdID uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM properties WHERE id = $1 AND household_id = $2`,
		propertyID, householdID,
	)
	if err != nil {
		return false, fmt.Errorf("delete property: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListRooms returns all rooms for a property.
func (r *Repo) ListRooms(ctx context.Context, propertyID uuid.UUID) ([]*Room, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, property_id, name, floor, notes, created_at
		 FROM rooms WHERE property_id = $1 ORDER BY floor, name`,
		propertyID,
	)
	if err != nil {
		return nil, fmt.Errorf("list rooms: %w", err)
	}
	defer rows.Close()

	rooms, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Room])
	if err != nil {
		return nil, fmt.Errorf("collect rooms: %w", err)
	}
	return rooms, nil
}

// CreateRoom inserts a new room under a property and returns the created record.
func (r *Repo) CreateRoom(ctx context.Context, propertyID uuid.UUID, name string, floor *int, notes *string) (*Room, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO rooms (property_id, name, floor, notes)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, property_id, name, floor, notes, created_at`,
		propertyID, name, floor, notes,
	)
	if err != nil {
		return nil, fmt.Errorf("create room: %w", err)
	}
	defer rows.Close()

	room, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Room])
	if err != nil {
		return nil, fmt.Errorf("collect created room: %w", err)
	}
	return room, nil
}
