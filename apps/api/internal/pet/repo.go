package pet

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the pets table using a pgx connection pool.
// All queries are scoped to household_id to enforce tenant isolation.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new pet repository.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// selectPets reads pets with their vet's name and phone from the vendor row.
// The join repeats the household match so a vendor of another family is
// never shown, even though the composite FK already forbids that link.
const selectPets = `SELECT p.id, p.household_id, p.name, p.species, p.breed, p.date_of_birth,
       p.vet_vendor_id, v.name, v.phone, p.notes, p.created_at, p.updated_at
  FROM pets p
  LEFT JOIN vendors v ON v.id = p.vet_vendor_id AND v.household_id = p.household_id`

// VetRef is a vet given by name and/or phone (the legacy request fields). It
// is resolved to a same-family vendor by ResolveVet.
type VetRef struct {
	Name  *string
	Phone *string
}

func (v VetRef) empty() bool {
	return trimmed(v.Name) == "" && trimmed(v.Phone) == ""
}

func trimmed(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

var nonDigits = regexp.MustCompile(`\D`)

// List returns all pets for the given household.
func (r *Repo) List(ctx context.Context, householdID uuid.UUID) ([]*Pet, error) {
	rows, err := r.pool.Query(ctx,
		selectPets+` WHERE p.household_id = $1 ORDER BY p.created_at DESC`,
		householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("list pets: %w", err)
	}
	defer rows.Close()

	pets, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Pet])
	if err != nil {
		return nil, fmt.Errorf("collect pets: %w", err)
	}
	return pets, nil
}

// Get returns a single pet by ID, scoped to the given household.
// Returns nil if not found.
func (r *Repo) Get(ctx context.Context, householdID, id uuid.UUID) (*Pet, error) {
	return getPet(ctx, r.pool, householdID, id)
}

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func getPet(ctx context.Context, q querier, householdID, id uuid.UUID) (*Pet, error) {
	rows, err := q.Query(ctx,
		selectPets+` WHERE p.id = $1 AND p.household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("get pet: %w", err)
	}
	defer rows.Close()

	pet, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Pet])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("collect pet: %w", err)
	}
	return pet, nil
}

// resolveVet finds the family vendor for a legacy vet name/phone, or creates
// one with trades {vet}. It mirrors migration 043's backfill: with a name, a
// vendor with the same name (case-insensitive) whose phone does not
// contradict; with only a phone, a vendor with the same digits. Vendors that
// already have the vet trade win ties. A matched vendor gets trade vet and a
// blank phone filled with the given one, as 043/060 do. It takes the same
// advisory locks as the MCP pet tools (pet_vet:<household>, then
// contact_create's contact_create:<household>:<lower(name)>), so parallel
// calls naming the same new vet, or a parallel MCP contact_create of that
// name, make one vendor.
// tagVetSQL is 043 section 4 / 060's update of a matched vendor: add trade
// vet when missing and fill a blank phone with the given one ($3, NULL when
// none). It writes nothing when the vendor is already a vet with a phone.
const tagVetSQL = `
UPDATE vendors
   SET trades = CASE WHEN 'vet' = ANY (trades) THEN trades ELSE array_append(trades, 'vet') END,
       phone  = COALESCE(NULLIF(btrim(phone), ''), $3),
       updated_at = NOW()
 WHERE id = $1 AND household_id = $2
   AND (NOT ('vet' = ANY (trades)) OR NULLIF(btrim(phone), '') IS NULL)`

// vetKindSQL promotes a PAYEE that is being used as a vet to 'both' (#5529,
// 080). The trigger in 080 does this whenever trades change, but this path
// can match a vendor that ALREADY has the 'vet' trade and only fills a blank
// phone -- no trade write, so no trigger -- and a payee that the family's pet
// sees must not stay invisible in the contacts list. Idempotent: a
// service_provider or both row is left alone.
const vetKindSQL = `
UPDATE vendors SET kind = 'both', updated_at = NOW()
 WHERE id = $1 AND household_id = $2 AND kind = 'payee'`

func resolveVet(ctx context.Context, q querier, householdID uuid.UUID, ref VetRef) (uuid.UUID, error) {
	name := trimmed(ref.Name)
	phone := trimmed(ref.Phone)
	digits := nonDigits.ReplaceAllString(phone, "")
	var phoneArg any
	if phone != "" {
		phoneArg = phone
	}
	newName := name
	if newName == "" {
		newName = "Vet"
	}
	if _, err := q.Exec(ctx, `
SELECT pg_advisory_xact_lock(hashtextextended('pet_vet:' || $1::text, 0)),
       pg_advisory_xact_lock(hashtextextended('contact_create:' || $1::text || ':' || lower($2), 0))`,
		householdID, newName); err != nil {
		return uuid.Nil, fmt.Errorf("lock vet vendor: %w", err)
	}

	var id uuid.UUID
	var err error
	if name != "" {
		err = q.QueryRow(ctx,
			`SELECT id FROM vendors
			  WHERE household_id = $1
			    AND lower(btrim(name)) = lower($2)
			    AND ($3 = '' OR phone IS NULL OR btrim(phone) = ''
			         OR regexp_replace(phone, '\D', '', 'g') = $3)
			  ORDER BY ('vet' = ANY (trades)) DESC, created_at, id
			  LIMIT 1`,
			householdID, name, digits,
		).Scan(&id)
	} else {
		err = q.QueryRow(ctx,
			`SELECT id FROM vendors
			  WHERE household_id = $1
			    AND $2 <> ''
			    AND regexp_replace(COALESCE(phone, ''), '\D', '', 'g') = $2
			  ORDER BY ('vet' = ANY (trades)) DESC, created_at, id
			  LIMIT 1`,
			householdID, digits,
		).Scan(&id)
	}
	if err == nil {
		if _, err := q.Exec(ctx, tagVetSQL, id, householdID, phoneArg); err != nil {
			return uuid.Nil, fmt.Errorf("tag vet vendor: %w", err)
		}
		if _, err := q.Exec(ctx, vetKindSQL, id, householdID); err != nil {
			return uuid.Nil, fmt.Errorf("promote vet vendor kind: %w", err)
		}
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("find vet vendor: %w", err)
	}

	if err := q.QueryRow(ctx,
		`INSERT INTO vendors (household_id, name, specialty, phone, trades)
		 VALUES ($1, $2, 'Veterinarian', $3, ARRAY['vet'])
		 RETURNING id`,
		householdID, newName, phoneArg,
	).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("create vet vendor: %w", err)
	}
	return id, nil
}

// Create inserts a new pet for the given household. When legacyVet names a
// vet (and p.VetVendorID is nil) the vet is resolved to a family vendor in
// the same transaction. A vet_vendor_id of another family fails with the
// composite FK's foreign_key_violation (23503).
func (r *Repo) Create(ctx context.Context, householdID uuid.UUID, p *Pet, legacyVet VetRef) (*Pet, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create pet: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	vetID, err := vetVendorID(ctx, tx, householdID, p.VetVendorID, legacyVet)
	if err != nil {
		return nil, err
	}

	var id uuid.UUID
	if err := tx.QueryRow(ctx,
		`INSERT INTO pets (household_id, name, species, breed, date_of_birth, vet_vendor_id, notes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id`,
		householdID, p.Name, p.Species, p.Breed, p.DateOfBirth, vetID, p.Notes,
	).Scan(&id); err != nil {
		return nil, fmt.Errorf("create pet: %w", err)
	}

	pet, err := getPet(ctx, tx, householdID, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create pet: %w", err)
	}
	return pet, nil
}

// Update replaces an existing pet scoped to the given household (vet
// included: no vet_vendor_id and no legacy vet clears it). Returns nil if the
// pet was not found.
func (r *Repo) Update(ctx context.Context, householdID, id uuid.UUID, p *Pet, legacyVet VetRef) (*Pet, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update pet: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Look the pet up first so a missing pet never creates a vendor.
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pets WHERE id = $1 AND household_id = $2)`,
		id, householdID,
	).Scan(&exists); err != nil {
		return nil, fmt.Errorf("find pet: %w", err)
	}
	if !exists {
		return nil, nil
	}

	vetID, err := vetVendorID(ctx, tx, householdID, p.VetVendorID, legacyVet)
	if err != nil {
		return nil, err
	}

	tag, err := tx.Exec(ctx,
		`UPDATE pets
		 SET name = $1, species = $2, breed = $3, date_of_birth = $4, vet_vendor_id = $5, notes = $6,
		     updated_at = NOW()
		 WHERE id = $7 AND household_id = $8`,
		p.Name, p.Species, p.Breed, p.DateOfBirth, vetID, p.Notes,
		id, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("update pet: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}

	pet, err := getPet(ctx, tx, householdID, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update pet: %w", err)
	}
	return pet, nil
}

// vetVendorID picks the vendor to store: an explicit vet_vendor_id wins,
// then a legacy name/phone resolved to a family vendor, else none.
func vetVendorID(ctx context.Context, q querier, householdID uuid.UUID, explicit *uuid.UUID, legacy VetRef) (*uuid.UUID, error) {
	if explicit != nil {
		return explicit, nil
	}
	if legacy.empty() {
		return nil, nil
	}
	id, err := resolveVet(ctx, q, householdID, legacy)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// Delete removes a pet scoped to the given household.
// Returns pgx.ErrNoRows if not found.
func (r *Repo) Delete(ctx context.Context, householdID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM pets WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return fmt.Errorf("delete pet: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
