package note

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the notes table using a pgx connection pool.
// All queries are scoped to household_id to enforce tenant isolation.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new note repository.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// ListByEntity returns all notes for a given entity (type + ID), ordered by
// created_at descending (most recent first).
func (r *Repo) ListByEntity(ctx context.Context, householdID uuid.UUID, entityType string, entityID uuid.UUID) ([]*Note, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT n.id, n.household_id, n.entity_type, n.entity_id, n.title, n.body, n.author_id, u.name as author_name, n.created_at, n.updated_at
		 FROM notes n
		 LEFT JOIN users u ON u.id = n.author_id
		 WHERE n.household_id = $1 AND n.entity_type = $2 AND n.entity_id = $3
		 ORDER BY n.created_at DESC`,
		householdID, entityType, entityID,
	)
	if err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	defer rows.Close()

	notes, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Note])
	if err != nil {
		return nil, fmt.Errorf("collect notes: %w", err)
	}
	return notes, nil
}

// ListByHousehold returns all notes for a household, ordered by created_at DESC.
func (r *Repo) ListByHousehold(ctx context.Context, householdID uuid.UUID) ([]*Note, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT n.id, n.household_id, n.entity_type, n.entity_id, n.title, n.body, n.author_id, u.name as author_name, n.created_at, n.updated_at
		 FROM notes n
		 LEFT JOIN users u ON u.id = n.author_id
		 WHERE n.household_id = $1
		 ORDER BY n.created_at DESC`,
		householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("list household notes: %w", err)
	}
	defer rows.Close()

	notes, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Note])
	if err != nil {
		return nil, fmt.Errorf("collect household notes: %w", err)
	}
	return notes, nil
}

// Get returns a single note by ID, scoped to the household.
// Returns nil if not found.
func (r *Repo) Get(ctx context.Context, householdID, id uuid.UUID) (*Note, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT n.id, n.household_id, n.entity_type, n.entity_id, n.title, n.body, n.author_id, u.name as author_name, n.created_at, n.updated_at
		 FROM notes n
		 LEFT JOIN users u ON u.id = n.author_id
		 WHERE n.id = $1 AND n.household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("get note: %w", err)
	}
	defer rows.Close()

	note, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Note])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect note: %w", err)
	}
	return note, nil
}

// Create inserts a new note and returns the created record.
func (r *Repo) Create(ctx context.Context, n *Note) (*Note, error) {
	// Insert then SELECT with join to get author_name
	var id uuid.UUID
	err := r.pool.QueryRow(ctx,
		`INSERT INTO notes (household_id, entity_type, entity_id, title, body, author_id)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id`,
		n.HouseholdID, n.EntityType, n.EntityID, n.Title, n.Body, n.AuthorID,
	).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("create note: %w", err)
	}

	rows, err := r.pool.Query(ctx,
		`SELECT n.id, n.household_id, n.entity_type, n.entity_id, n.title, n.body, n.author_id, u.name as author_name, n.created_at, n.updated_at
		 FROM notes n
		 LEFT JOIN users u ON u.id = n.author_id
		 WHERE n.id = $1`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("fetch created note: %w", err)
	}
	defer rows.Close()

	note, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Note])
	if err != nil {
		return nil, fmt.Errorf("collect created note: %w", err)
	}
	return note, nil
}

// Update modifies an existing note, scoped to the household.
// Returns nil if the note was not found.
func (r *Repo) Update(ctx context.Context, householdID, id uuid.UUID, n *Note) (*Note, error) {
	// Update then SELECT with join to get author_name
	_, err := r.pool.Exec(ctx,
		`UPDATE notes
		 SET title = $1, body = $2, updated_at = NOW()
		 WHERE id = $3 AND household_id = $4`,
		n.Title, n.Body, id, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("update note: %w", err)
	}

	rows, err := r.pool.Query(ctx,
		`SELECT n.id, n.household_id, n.entity_type, n.entity_id, n.title, n.body, n.author_id, u.name as author_name, n.created_at, n.updated_at
		 FROM notes n
		 LEFT JOIN users u ON u.id = n.author_id
		 WHERE n.id = $1 AND n.household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("fetch updated note: %w", err)
	}
	defer rows.Close()

	note, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Note])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect updated note: %w", err)
	}
	return note, nil
}

// Delete removes a note scoped to the given household.
// Returns pgx.ErrNoRows if not found.
func (r *Repo) Delete(ctx context.Context, householdID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM notes WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}