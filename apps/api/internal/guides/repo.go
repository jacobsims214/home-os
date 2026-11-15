// Package guides serves the family's how-tos and runbooks (migration 049):
// "reset the router", "replace the HVAC filter", "winterize the lake house".
//
// A guide has a title, an optional summary, a markdown body and tags, and may
// be about one property, asset or vehicle (the typed subject columns bills and
// loans use; see package subject). Every save writes a guide_revisions row in
// the same transaction as the guide itself, so the newest revision always
// equals guides.body: the database has no snapshot trigger because it can't
// know who is editing.
//
// Every query is scoped to the household the request resolved to, so an id
// from another family behaves exactly like an id that does not exist.
package guides

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"home-os/api/internal/subject"
)

// ErrNotFound: no such guide or revision in this household.
var ErrNotFound = errors.New("not found")

// Guide is a row of guides.
type Guide struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	Title       string
	Summary     *string
	Body        string
	Tags        []string
	subject.Subject
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Revision is a row of guide_revisions, with the editor's display name.
type Revision struct {
	ID           uuid.UUID
	GuideID      uuid.UUID
	HouseholdID  uuid.UUID
	Body         string
	EditedBy     *uuid.UUID
	EditedByName *string
	CreatedAt    time.Time
}

// ListFilter narrows List. Zero values mean "no filter".
type ListFilter struct {
	// PropertyID matches guides about the property, including guides about
	// its assets (property_id is derived from the asset).
	PropertyID *uuid.UUID
	AssetID    *uuid.UUID
	VehicleID  *uuid.UUID
	// Tag matches guides carrying this (normalized, lower-case) tag.
	Tag string
	// FamilyOnly matches guides with no subject at all.
	FamilyOnly bool
}

// Repo is the guides data access layer.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a guides repo.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

const guideCols = `id, household_id, title, summary, body, tags,
	property_id, asset_id, vehicle_id, created_at, updated_at`

func scanGuide(row pgx.Row) (*Guide, error) {
	var g Guide
	if err := row.Scan(&g.ID, &g.HouseholdID, &g.Title, &g.Summary, &g.Body, &g.Tags,
		&g.PropertyID, &g.AssetID, &g.VehicleID, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, err
	}
	if g.Tags == nil {
		g.Tags = []string{}
	}
	return &g, nil
}

// List returns the household's guides matching f, alphabetically.
func (r *Repo) List(ctx context.Context, householdID uuid.UUID, f ListFilter) ([]*Guide, error) {
	q := `SELECT ` + guideCols + ` FROM guides WHERE household_id = $1`
	args := []any{householdID}
	add := func(cond string, v any) {
		args = append(args, v)
		q += fmt.Sprintf(" AND "+cond, len(args))
	}
	if f.PropertyID != nil {
		add("property_id = $%d", *f.PropertyID)
	}
	if f.AssetID != nil {
		add("asset_id = $%d", *f.AssetID)
	}
	if f.VehicleID != nil {
		add("vehicle_id = $%d", *f.VehicleID)
	}
	if f.Tag != "" {
		add("tags @> ARRAY[$%d]::text[]", f.Tag)
	}
	if f.FamilyOnly {
		q += " AND property_id IS NULL AND asset_id IS NULL AND vehicle_id IS NULL"
	}
	q += " ORDER BY lower(title), id"

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("guides: list: %w", err)
	}
	defer rows.Close()
	out := []*Guide{}
	for rows.Next() {
		g, err := scanGuide(rows)
		if err != nil {
			return nil, fmt.Errorf("guides: list scan: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("guides: list rows: %w", err)
	}
	return out, nil
}

// All calls fn for every guide of every household (the reindex tool).
func (r *Repo) All(ctx context.Context, fn func(*Guide) error) error {
	rows, err := r.pool.Query(ctx, `SELECT `+guideCols+` FROM guides ORDER BY id`)
	if err != nil {
		return fmt.Errorf("guides: all: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		g, err := scanGuide(rows)
		if err != nil {
			return fmt.Errorf("guides: all scan: %w", err)
		}
		if err := fn(g); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Get returns one guide of the household.
func (r *Repo) Get(ctx context.Context, householdID, id uuid.UUID) (*Guide, error) {
	g, err := scanGuide(r.pool.QueryRow(ctx,
		`SELECT `+guideCols+` FROM guides WHERE id = $1 AND household_id = $2`, id, householdID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("guides: get: %w", err)
	}
	return g, nil
}

// insertRevision writes the body as saved by editedBy (nil when the editor
// has no users row, e.g. local dev). household_id is filled by trigger.
func insertRevision(ctx context.Context, tx pgx.Tx, guideID uuid.UUID, body string, editedBy *uuid.UUID) error {
	if _, err := tx.Exec(ctx,
		// An editor id with no users row (local dev) is stored as NULL
		// rather than failing the save.
		//
		// created_at is clock_timestamp(), not the column default NOW():
		// NOW() is the transaction's start, so of two racing saves the one
		// that committed last could sort as older. Callers hold the guide's
		// row lock here, so clock_timestamp() orders revisions by commit and
		// the newest revision is always the saved body.
		`INSERT INTO guide_revisions (guide_id, body, edited_by, created_at)
		 VALUES ($1, $2, (SELECT id FROM users WHERE id = $3), clock_timestamp())`,
		guideID, body, editedBy); err != nil {
		return fmt.Errorf("guides: insert revision: %w", err)
	}
	return nil
}

// Create inserts g (ID and timestamps are ignored) and its first revision in
// one transaction. A subject from another household fails with the database's
// 23503 (derive_subject_property).
func (r *Repo) Create(ctx context.Context, g *Guide, editedBy *uuid.UUID) (*Guide, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("guides: create: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	created, err := scanGuide(tx.QueryRow(ctx,
		`INSERT INTO guides (household_id, title, summary, body, tags, property_id, asset_id, vehicle_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING `+guideCols,
		g.HouseholdID, g.Title, g.Summary, g.Body, g.Tags, g.PropertyID, g.AssetID, g.VehicleID))
	if err != nil {
		return nil, fmt.Errorf("guides: create: %w", err)
	}
	if err := insertRevision(ctx, tx, created.ID, created.Body, editedBy); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("guides: create: commit: %w", err)
	}
	return created, nil
}

// Update locks the guide, lets mutate change it, saves it and writes a
// revision, all in one transaction. mutate's error is returned unchanged (so
// a validation error reaches the caller as is); the guide is untouched then.
func (r *Repo) Update(ctx context.Context, householdID, id uuid.UUID, editedBy *uuid.UUID, mutate func(*Guide) error) (*Guide, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("guides: update: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	g, err := scanGuide(tx.QueryRow(ctx,
		`SELECT `+guideCols+` FROM guides WHERE id = $1 AND household_id = $2 FOR UPDATE`, id, householdID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("guides: update: lock: %w", err)
	}
	if err := mutate(g); err != nil {
		return nil, err
	}
	saved, err := saveLocked(ctx, tx, g, editedBy)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("guides: update: commit: %w", err)
	}
	return saved, nil
}

// saveLocked writes g (already locked by the caller's transaction) and a
// revision of its body.
func saveLocked(ctx context.Context, tx pgx.Tx, g *Guide, editedBy *uuid.UUID) (*Guide, error) {
	saved, err := scanGuide(tx.QueryRow(ctx,
		`UPDATE guides
		 SET title = $3, summary = $4, body = $5, tags = $6,
		     property_id = $7, asset_id = $8, vehicle_id = $9
		 WHERE id = $1 AND household_id = $2
		 RETURNING `+guideCols,
		g.ID, g.HouseholdID, g.Title, g.Summary, g.Body, g.Tags, g.PropertyID, g.AssetID, g.VehicleID))
	if err != nil {
		return nil, fmt.Errorf("guides: save: %w", err)
	}
	if err := insertRevision(ctx, tx, saved.ID, saved.Body, editedBy); err != nil {
		return nil, err
	}
	return saved, nil
}

// Restore makes revision revID the guide's body again. The restore is a save
// like any other: it writes a new revision (history is never rewritten).
func (r *Repo) Restore(ctx context.Context, householdID, guideID, revID uuid.UUID, editedBy *uuid.UUID) (*Guide, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("guides: restore: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	g, err := scanGuide(tx.QueryRow(ctx,
		`SELECT `+guideCols+` FROM guides WHERE id = $1 AND household_id = $2 FOR UPDATE`, guideID, householdID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("guides: restore: lock: %w", err)
	}
	err = tx.QueryRow(ctx,
		`SELECT body FROM guide_revisions WHERE id = $1 AND guide_id = $2 AND household_id = $3`,
		revID, guideID, householdID).Scan(&g.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("guides: restore: revision: %w", err)
	}
	saved, err := saveLocked(ctx, tx, g, editedBy)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("guides: restore: commit: %w", err)
	}
	return saved, nil
}

// Delete removes a guide (its revisions cascade; notes, files, secrets, lists
// and links about it are re-homed or removed by 049's trigger).
func (r *Repo) Delete(ctx context.Context, householdID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM guides WHERE id = $1 AND household_id = $2`, id, householdID)
	if err != nil {
		return fmt.Errorf("guides: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const revisionCols = `r.id, r.guide_id, r.household_id, r.body, r.edited_by, u.name, r.created_at`

func scanRevision(row pgx.Row) (*Revision, error) {
	var v Revision
	if err := row.Scan(&v.ID, &v.GuideID, &v.HouseholdID, &v.Body, &v.EditedBy, &v.EditedByName, &v.CreatedAt); err != nil {
		return nil, err
	}
	return &v, nil
}

// ListRevisions returns a guide's revisions, newest first. ErrNotFound if the
// guide is not in the household.
func (r *Repo) ListRevisions(ctx context.Context, householdID, guideID uuid.UUID) ([]*Revision, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM guides WHERE id = $1 AND household_id = $2)`,
		guideID, householdID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("guides: revisions: %w", err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+revisionCols+`
		 FROM guide_revisions r LEFT JOIN users u ON u.id = r.edited_by
		 WHERE r.guide_id = $1 AND r.household_id = $2
		 ORDER BY r.created_at DESC, r.id DESC`, guideID, householdID)
	if err != nil {
		return nil, fmt.Errorf("guides: revisions: %w", err)
	}
	defer rows.Close()
	out := []*Revision{}
	for rows.Next() {
		v, err := scanRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("guides: revisions scan: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("guides: revisions rows: %w", err)
	}
	return out, nil
}

// GetRevision returns one revision of a guide in the household.
func (r *Repo) GetRevision(ctx context.Context, householdID, guideID, revID uuid.UUID) (*Revision, error) {
	v, err := scanRevision(r.pool.QueryRow(ctx,
		`SELECT `+revisionCols+`
		 FROM guide_revisions r LEFT JOIN users u ON u.id = r.edited_by
		 WHERE r.id = $1 AND r.guide_id = $2 AND r.household_id = $3`, revID, guideID, householdID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("guides: revision: %w", err)
	}
	return v, nil
}
