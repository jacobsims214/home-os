// Package people serves the family's records of people (migration 041):
// members who log in (user_id set) and kids, grandparents, nannies who don't
// (user_id NULL), plus each person's preferences (likes, dislikes, dietary
// needs).
//
// Every query is scoped to the household the request resolved to, so an id
// from another family behaves exactly like an id that does not exist.
package people

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrNotFound: no such person/preference in this household.
	ErrNotFound = errors.New("not found")
	// ErrLinkedMember: the person is linked to a current member; remove the
	// member instead of deleting the person.
	ErrLinkedMember = errors.New("person is linked to a household member")
	// ErrDuplicate: the person already has this preference (same kind,
	// category and value, ignoring case).
	ErrDuplicate = errors.New("duplicate preference")
	// ErrConfirmRequired: records point at the person (see References) and
	// the caller did not confirm the delete.
	ErrConfirmRequired = errors.New("person delete needs confirmation")
)

// Person is a row of people.
type Person struct {
	ID           uuid.UUID
	HouseholdID  uuid.UUID
	UserID       *uuid.UUID
	DisplayName  string
	Relationship *string
	Birthdate    *time.Time
	School       *string
	Grade        *string
	Allergies    []string
	Notes        *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// PersonInput holds the fields a caller may set on a person. user_id is not
// settable: it is managed by the memberships triggers.
type PersonInput struct {
	DisplayName  string
	Relationship *string
	Birthdate    *time.Time
	School       *string
	Grade        *string
	Allergies    []string
	Notes        *string
}

// Preference is a row of person_preferences.
type Preference struct {
	ID          uuid.UUID
	HouseholdID uuid.UUID
	PersonID    uuid.UUID
	Kind        string
	Category    string
	Value       string
	Notes       *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PreferenceInput holds the fields a caller may set on a preference.
type PreferenceInput struct {
	Kind     string
	Category string
	Value    string
	Notes    *string
}

// Repo reads and writes people and person_preferences.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a people repository.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

const personCols = `id, household_id, user_id, display_name, relationship, birthdate,
	school, grade, allergies, notes, created_at, updated_at`

const prefCols = `id, household_id, person_id, kind::text, category, value, notes, created_at, updated_at`

func scanPerson(row pgx.Row) (*Person, error) {
	var p Person
	err := row.Scan(&p.ID, &p.HouseholdID, &p.UserID, &p.DisplayName, &p.Relationship, &p.Birthdate,
		&p.School, &p.Grade, &p.Allergies, &p.Notes, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if p.Allergies == nil {
		p.Allergies = []string{}
	}
	return &p, nil
}

func scanPref(row pgx.Row) (*Preference, error) {
	var p Preference
	err := row.Scan(&p.ID, &p.HouseholdID, &p.PersonID, &p.Kind, &p.Category, &p.Value, &p.Notes,
		&p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func allergies(a []string) []string {
	if a == nil {
		return []string{}
	}
	return a
}

// MemberRole returns userID's role in householdID, read from memberships, or
// "" if the user is not a member there.
func (r *Repo) MemberRole(ctx context.Context, householdID, userID uuid.UUID) (string, error) {
	var role string
	err := r.pool.QueryRow(ctx,
		`SELECT role::text FROM memberships WHERE household_id = $1 AND user_id = $2`,
		householdID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("member role: %w", err)
	}
	return role, nil
}

// List returns the household's people, ordered by name.
func (r *Repo) List(ctx context.Context, householdID uuid.UUID) ([]*Person, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+personCols+` FROM people WHERE household_id = $1
		 ORDER BY lower(display_name), created_at, id`, householdID)
	if err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	defer rows.Close()
	out := []*Person{}
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, fmt.Errorf("scan person: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	return out, nil
}

// Get returns one person of the household, or ErrNotFound.
func (r *Repo) Get(ctx context.Context, householdID, id uuid.UUID) (*Person, error) {
	p, err := scanPerson(r.pool.QueryRow(ctx,
		`SELECT `+personCols+` FROM people WHERE id = $1 AND household_id = $2`, id, householdID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("get person: %w", err)
	}
	return p, err
}

// Create adds a person without a login to the household.
func (r *Repo) Create(ctx context.Context, householdID uuid.UUID, in PersonInput) (*Person, error) {
	p, err := scanPerson(r.pool.QueryRow(ctx,
		`INSERT INTO people (household_id, display_name, relationship, birthdate, school, grade, allergies, notes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING `+personCols,
		householdID, in.DisplayName, in.Relationship, in.Birthdate, in.School, in.Grade,
		allergies(in.Allergies), in.Notes))
	if err != nil {
		return nil, fmt.Errorf("create person: %w", err)
	}
	return p, nil
}

// Update replaces a person's editable fields, or returns ErrNotFound.
func (r *Repo) Update(ctx context.Context, householdID, id uuid.UUID, in PersonInput) (*Person, error) {
	p, err := scanPerson(r.pool.QueryRow(ctx,
		`UPDATE people SET display_name = $3, relationship = $4, birthdate = $5, school = $6,
		        grade = $7, allergies = $8, notes = $9, updated_at = NOW()
		  WHERE id = $1 AND household_id = $2
		 RETURNING `+personCols,
		id, householdID, in.DisplayName, in.Relationship, in.Birthdate, in.School, in.Grade,
		allergies(in.Allergies), in.Notes))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("update person: %w", err)
	}
	return p, err
}

// References counts the records that point at a person and are deleted
// (notes) or unlinked (the rest) when the person is deleted. The counts come
// from the SQL function person_reference_counts (migration 063), which the
// MCP person_delete tool uses too, so the API and MCP ask for the same
// confirmation.
type References struct {
	Notes       int `json:"notes"`
	Documents   int `json:"documents"`
	ListItems   int `json:"list_items"`
	MealRatings int `json:"meal_ratings"`
	Events      int `json:"events"`
}

// Any reports whether anything points at the person.
func (r References) Any() bool {
	return r.Notes+r.Documents+r.ListItems+r.MealRatings+r.Events > 0
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func references(ctx context.Context, q querier, householdID, id uuid.UUID) (References, error) {
	var refs References
	err := q.QueryRow(ctx,
		`SELECT notes, documents, list_items, meal_ratings, events
		   FROM person_reference_counts($1, $2)`, householdID, id).
		Scan(&refs.Notes, &refs.Documents, &refs.ListItems, &refs.MealRatings, &refs.Events)
	if err != nil {
		return References{}, fmt.Errorf("count person references: %w", err)
	}
	return refs, nil
}

// References returns what points at a person of the household. It does not
// check that the person exists.
func (r *Repo) References(ctx context.Context, householdID, id uuid.UUID) (References, error) {
	return references(ctx, r.pool, householdID, id)
}

// Delete removes a person and (by cascade) their preferences, in one
// transaction with the people row locked FOR UPDATE (as MCP person_delete
// does), so nothing can be attached between the count and the delete:
//   - ErrNotFound: no such person in the household;
//   - ErrLinkedMember: the person is linked to a current member (remove the
//     member instead); checked before the references;
//   - ErrConfirmRequired, with the counts: records point at the person and
//     confirm is false. Nothing is deleted.
//
// With confirm, or when nothing points at the person, the person is deleted
// and the returned counts say what was deleted or unlinked with them.
func (r *Repo) Delete(ctx context.Context, householdID, id uuid.UUID, confirm bool) (References, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return References{}, fmt.Errorf("delete person: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var linked bool
	err = tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM memberships m
		                 WHERE m.household_id = p.household_id AND m.user_id = p.user_id)
		   FROM people p
		  WHERE p.id = $1 AND p.household_id = $2
		    FOR UPDATE OF p`,
		id, householdID).Scan(&linked)
	if errors.Is(err, pgx.ErrNoRows) {
		return References{}, ErrNotFound
	}
	if err != nil {
		return References{}, fmt.Errorf("delete person: %w", err)
	}
	if linked {
		return References{}, ErrLinkedMember
	}

	refs, err := references(ctx, tx, householdID, id)
	if err != nil {
		return References{}, err
	}
	if refs.Any() && !confirm {
		return refs, ErrConfirmRequired
	}

	// The membership check is repeated in the DELETE: a membership insert
	// doesn't take the people row lock.
	tag, err := tx.Exec(ctx,
		`DELETE FROM people p
		  WHERE p.id = $1 AND p.household_id = $2
		    AND NOT EXISTS (SELECT 1 FROM memberships m
		                     WHERE m.household_id = p.household_id AND m.user_id = p.user_id)`,
		id, householdID)
	if err != nil {
		return References{}, fmt.Errorf("delete person: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return References{}, ErrLinkedMember
	}
	if err := tx.Commit(ctx); err != nil {
		return References{}, fmt.Errorf("delete person: %w", err)
	}
	return refs, nil
}

// ListPreferences returns a person's preferences (the person must be in the
// household; the caller checks that first).
func (r *Repo) ListPreferences(ctx context.Context, householdID, personID uuid.UUID) ([]*Preference, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+prefCols+` FROM person_preferences
		  WHERE household_id = $1 AND person_id = $2
		  ORDER BY kind, lower(category), lower(value), id`, householdID, personID)
	if err != nil {
		return nil, fmt.Errorf("list preferences: %w", err)
	}
	defer rows.Close()
	out := []*Preference{}
	for rows.Next() {
		p, err := scanPref(rows)
		if err != nil {
			return nil, fmt.Errorf("scan preference: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list preferences: %w", err)
	}
	return out, nil
}

func prefWriteErr(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrDuplicate
		case "23503": // person gone, or in another household (composite FK)
			return ErrNotFound
		}
	}
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}

// CreatePreference adds a preference to a person of the household.
func (r *Repo) CreatePreference(ctx context.Context, householdID, personID uuid.UUID, in PreferenceInput) (*Preference, error) {
	p, err := scanPref(r.pool.QueryRow(ctx,
		`INSERT INTO person_preferences (household_id, person_id, kind, category, value, notes)
		 VALUES ($1, $2, $3::person_preference_kind, $4, $5, $6)
		 RETURNING `+prefCols,
		householdID, personID, in.Kind, in.Category, in.Value, in.Notes))
	if err != nil {
		return nil, prefWriteErr("create preference", err)
	}
	return p, nil
}

// UpdatePreference replaces a preference's fields, or returns ErrNotFound.
func (r *Repo) UpdatePreference(ctx context.Context, householdID, personID, id uuid.UUID, in PreferenceInput) (*Preference, error) {
	p, err := scanPref(r.pool.QueryRow(ctx,
		`UPDATE person_preferences
		    SET kind = $4::person_preference_kind, category = $5, value = $6, notes = $7, updated_at = NOW()
		  WHERE id = $1 AND household_id = $2 AND person_id = $3
		 RETURNING `+prefCols,
		id, householdID, personID, in.Kind, in.Category, in.Value, in.Notes))
	if err != nil {
		return nil, prefWriteErr("update preference", err)
	}
	return p, nil
}

// DeletePreference removes a preference, or returns ErrNotFound.
func (r *Repo) DeletePreference(ctx context.Context, householdID, personID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM person_preferences WHERE id = $1 AND household_id = $2 AND person_id = $3`,
		id, householdID, personID)
	if err != nil {
		return fmt.Errorf("delete preference: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
