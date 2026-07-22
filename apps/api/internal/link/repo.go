package link

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
		`INSERT INTO document_links (entity_type, entity_id, link_type, link_id, title, url)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, entity_type, entity_id, link_type, link_id, title, url, created_at`,
		link.EntityType, link.EntityID, link.LinkType, link.LinkID, link.Title, link.URL,
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
		`SELECT id, entity_type, entity_id, link_type, link_id, title, url, created_at
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

// entityTable maps the link API's entity_type values to their backing
// Postgres table. Every entity that can own a link has a household_id column,
// which is what makes the ownership check possible. This is an allow-list —
// any entity_type not in this map is rejected by EntityOwnedByHousehold.
var entityTable = map[string]string{
	"asset":            "assets",
	"property":         "properties",
	"vehicle":          "vehicles",
	"pet":              "pets",
	"vendor":           "vendors",
	"bill":             "bills",
	"maintenance_task": "maintenance_tasks",
}

// ErrUnknownEntityType is returned by EntityOwnedByHousehold when the
// entity_type string is not one of the supported types in the entityTable map.
var ErrUnknownEntityType = errors.New("unknown entity_type")

// EntityOwnedByHousehold reports whether the entity row identified by
// (entityType, entityID) exists and belongs to the given household.
//
// Returns (false, nil) when the entity does not exist or belongs to a
// different household — callers should treat that as a 404 to avoid leaking
// the existence of entities the caller does not own (cross-tenant IDOR).
// Returns (false, ErrUnknownEntityType) when entityType is not recognized.
// Any other error indicates a database failure.
//
// The table name used in the query comes from the static entityTable allow-list
// (not user input), so it is safe to interpolate into the SQL string. We never
// pass the table name through a bind parameter because pgx does not support
// parameterized identifiers.
func (r *Repo) EntityOwnedByHousehold(ctx context.Context, entityType string, entityID, householdID uuid.UUID) (bool, error) {
	table, ok := entityTable[entityType]
	if !ok {
		return false, ErrUnknownEntityType
	}
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id = $1 AND household_id = $2)`,
		entityID, householdID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("entity ownership check (%s): %w", entityType, err)
	}
	return exists, nil
}

// GetLink returns a single link by ID.
func (r *Repo) GetLink(ctx context.Context, linkID uuid.UUID) (*Link, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, entity_type, entity_id, link_type, link_id, title, url, created_at
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