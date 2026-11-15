package link

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the document_links table.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new link repository backed by the given connection pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// CreateLink inserts a new link between an entity and an external resource.
func (r *Repo) CreateLink(ctx context.Context, link *Link) (*Link, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO document_links (entity_type, entity_id, link_type, link_id, title, url, household_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, entity_type, entity_id, link_type, link_id, title, url, created_at, household_id`,
		link.EntityType, link.EntityID, link.LinkType, link.LinkID, link.Title, link.URL, link.HouseholdID,
	)
	if err != nil {
		return nil, fmt.Errorf("create link: %w", err)
	}
	defer rows.Close()

	created, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Link])
	if err != nil {
		return nil, fmt.Errorf("create link: collect: %w", err)
	}
	return created, nil
}

// GetLinks returns all links for a given entity, ordered by created_at descending.
func (r *Repo) GetLinks(ctx context.Context, entityType string, entityID uuid.UUID) ([]*Link, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, entity_type, entity_id, link_type, link_id, title, url, created_at, household_id
		 FROM document_links
		 WHERE entity_type = $1 AND entity_id = $2
		 ORDER BY created_at DESC`,
		entityType, entityID,
	)
	if err != nil {
		return nil, fmt.Errorf("get links: %w", err)
	}
	defer rows.Close()

	links, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Link])
	if err != nil {
		return nil, fmt.Errorf("collect links: %w", err)
	}
	return links, nil
}

// DeleteLink removes a link by its ID. Returns an error if the link does not exist.
func (r *Repo) DeleteLink(ctx context.Context, linkID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM document_links WHERE id = $1`,
		linkID,
	)
	if err != nil {
		return fmt.Errorf("delete link: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete link: %w", pgx.ErrNoRows)
	}
	return nil
}

// ErrUnknownEntityType is returned by EntityOwnedByHousehold when the
// entity_type string is not a member of the entity_kind domain.
var ErrUnknownEntityType = errors.New("unknown entity_type")

// EntityOwnedByHousehold reports whether the entity row identified by
// (entityType, entityID) exists and belongs to the given household. It uses
// the database's entity_household() registry (migration 038), so every kind
// the ownership triggers know about, 'household' included, is accepted here
// too, with no list kept in Go.
//
// Returns (false, nil) when the entity does not exist or belongs to a
// different household — callers should treat that as a 404 to avoid leaking
// the existence of entities the caller does not own (cross-tenant IDOR).
// Returns (false, ErrUnknownEntityType) when entityType is not recognized.
// Any other error indicates a database failure.
func (r *Repo) EntityOwnedByHousehold(ctx context.Context, entityType string, entityID, householdID uuid.UUID) (bool, error) {
	var owned bool
	err := r.pool.QueryRow(ctx,
		`SELECT entity_household($1::text::entity_kind, $2) IS NOT DISTINCT FROM $3::uuid`,
		entityType, entityID, householdID,
	).Scan(&owned)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23514" {
			return false, ErrUnknownEntityType
		}
		return false, fmt.Errorf("entity ownership check (%s): %w", entityType, err)
	}
	return owned, nil
}

// GetLink returns a single link by ID.
func (r *Repo) GetLink(ctx context.Context, linkID uuid.UUID) (*Link, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, entity_type, entity_id, link_type, link_id, title, url, created_at, household_id
		 FROM document_links
		 WHERE id = $1`,
		linkID,
	)
	if err != nil {
		return nil, fmt.Errorf("get link: %w", err)
	}
	defer rows.Close()

	link, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Link])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect link: %w", err)
	}
	return link, nil
}
