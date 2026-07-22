// Package secret — repository layer.
//
// Design notes (mirrors apps/api/internal/file/repo.go):
//
//  - Every method takes householdID and filters by it. A missing row and a
//    cross-tenant row are indistinguishable: Get/GetKey return (nil, nil) and
//    Delete/Update return pgx.ErrNoRows so callers can translate both to 404
//    without leaking cross-tenant existence. This is the same tenant-isolation
//    contract used by file/repo.go and note/repo.go.
//
//  - ListSecrets selects METADATA ONLY — id, name, secret_type, entity_type,
//    entity_id, created_at, updated_at. The encrypted_data column is never
//    selected by ListSecrets so the listing endpoint stays cheap and never
//    exposes ciphertext over the wire. GetSecret is the only method that
//    returns encrypted_data + iv, and only for a single id that the caller
//    has already chosen to decrypt client-side.
//
//  - The polymorphic entity filter (entityType + entityID) uses the same
//    sentinel convention as file/repo.go: when both are zero (entityType=="" &&
//    entityID==uuid.Nil) the whole household is listed; otherwise the query is
//    narrowed to (entity_type, entity_id). Standalone secrets (no entity) are
//    stored with entity_type="" and entity_id=uuid.Nil and surface in the
//    household-wide listing.
//
//  - The repo never touches plaintext. encrypted_data/iv arrive already
//    encrypted from the client (Web Crypto API, AES-256-GCM) and are stored /
//    returned as opaque BYTEA. key_hash/key_salt for the master-password
//    verification key are likewise opaque bytes.
//
//  - Secret and SecretKey types are defined in model.go (same package). The
//    binary fields (EncryptedData, IV, KeyHash, KeySalt) are []byte which
//    encoding/json marshals as base64 automatically — matching the file
//    package's FileBlob.Data pattern.
package secret

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the secrets and secret_keys tables via a
// pgx connection pool. All queries are scoped to household_id to enforce
// tenant isolation, mirroring apps/api/internal/file/repo.go.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new secret repository backed by the given pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// CreateSecret inserts a new secret row and returns the fully-populated
// persisted record (including the DB-generated id and timestamps).
//
// The caller must supply a *Secret whose EncryptedData, IV, KeyVersion, Name,
// and SecretType are already set (encryption happens client-side — the repo
// never sees plaintext). EntityType/EntityID may be zero values for a
// standalone secret. householdID is passed separately and overrides any value
// on the struct so a handler can never accidentally write into another
// household.
func (r *Repo) CreateSecret(ctx context.Context, householdID uuid.UUID, s *Secret) (*Secret, error) {
	var created Secret
	err := r.pool.QueryRow(ctx,
		`INSERT INTO secrets (household_id, entity_type, entity_id, encrypted_data, iv, key_version, name, secret_type)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, household_id, entity_type, entity_id, encrypted_data, iv, key_version,
		           name, secret_type, created_at, updated_at`,
		householdID, s.EntityType, s.EntityID, s.EncryptedData, s.IV, s.KeyVersion, s.Name, s.SecretType,
	).Scan(
		&created.ID, &created.HouseholdID, &created.EntityType, &created.EntityID,
		&created.EncryptedData, &created.IV, &created.KeyVersion,
		&created.Name, &created.SecretType, &created.CreatedAt, &created.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create secret: %w", err)
	}
	return &created, nil
}

// GetSecret returns a single secret by ID, scoped to householdID. Unlike
// ListSecrets, this SELECT includes encrypted_data + iv so the client can
// decrypt the blob (AES-256-GCM via Web Crypto API). The repo still never
// decrypts — it only returns the opaque bytes.
//
// Returns (nil, nil) when the secret does not exist or belongs to a different
// household; callers MUST treat both cases as 404 so an attacker cannot
// distinguish "no such secret" from "secret exists but isn't yours" (matching
// file/repo.go's GetFile contract).
func (r *Repo) GetSecret(ctx context.Context, householdID, id uuid.UUID) (*Secret, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, entity_type, entity_id, encrypted_data, iv, key_version,
		        name, secret_type, created_at, updated_at
		 FROM secrets
		 WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("get secret: %w", err)
	}
	defer rows.Close()

	s, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Secret])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get secret: collect: %w", err)
	}
	return s, nil
}

// ListSecrets returns secret METADATA for a household, optionally narrowed to
// a specific (entity_type, entity_id) polymorphic association. Results are
// ordered newest-first to match the dashboard's "recent secrets" use case
// (backed by idx_secrets_household).
//
// IMPORTANT: this query NEVER selects encrypted_data or iv. Listing endpoints
// must not expose ciphertext — only id/name/secret_type/entity info for the
// UI to render a list. The client fetches the full encrypted blob via GetSecret
// only when the user opens an individual secret.
//
// When entityType != "" && entityID != uuid.Nil the query is scoped to secrets
// attached to that entity; otherwise the entire household is listed (including
// standalone secrets whose entity_type/entity_id are the zero values). An empty
// result set returns a non-nil empty slice (not nil) so JSON marshalling
// produces [] rather than null — same convention as file/repo.go.
func (r *Repo) ListSecrets(ctx context.Context, householdID uuid.UUID, entityType string, entityID uuid.UUID) ([]*SecretListItem, error) {
	var (
		query string
		args  []any
	)
	if entityType != "" && entityID != uuid.Nil {
		query = `SELECT id, household_id, entity_type, entity_id, key_version, name, secret_type, created_at, updated_at
		         FROM secrets
		         WHERE household_id = $1 AND entity_type = $2 AND entity_id = $3
		         ORDER BY created_at DESC`
		args = []any{householdID, entityType, entityID}
	} else {
		query = `SELECT id, household_id, entity_type, entity_id, key_version, name, secret_type, created_at, updated_at
		         FROM secrets
		         WHERE household_id = $1
		         ORDER BY created_at DESC`
		args = []any{householdID}
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	defer rows.Close()

	secrets, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[SecretListItem])
	if err != nil {
		return nil, fmt.Errorf("list secrets: collect: %w", err)
	}
	if secrets == nil {
		secrets = []*SecretListItem{}
	}
	return secrets, nil
}

// UpdateSecret patches the mutable fields of a secret — encrypted_data, iv,
// name, and secret_type — scoped to householdID. The client must supply a
// fresh encrypted_data + iv pair (re-encrypted with the same key_version) on
// every update; partial updates of just the name are not supported at the repo
// layer because the ciphertext must always travel with its nonce.
//
// entity_type/entity_id and key_version are intentionally NOT updatable here:
// re-parenting a secret to a different entity is a separate operation, and key
// rotation is handled by a dedicated rotation flow (re-encrypt + UpdateSecret
// with the new key_version's blob — see secrets-manager-research.md).
//
// Returns the fully-populated updated *Secret (including encrypted_data + iv),
// or (nil, nil) if the secret does not exist or belongs to a different
// household — same not-found-vs-forbidden contract as GetSecret.
func (r *Repo) UpdateSecret(ctx context.Context, householdID, id uuid.UUID, s *Secret) (*Secret, error) {
	var updated Secret
	err := r.pool.QueryRow(ctx,
		`UPDATE secrets
		 SET encrypted_data = $1,
		     iv = $2,
		     name = $3,
		     secret_type = $4,
		     updated_at = NOW()
		 WHERE id = $5 AND household_id = $6
		 RETURNING id, household_id, entity_type, entity_id, encrypted_data, iv, key_version,
		           name, secret_type, created_at, updated_at`,
		s.EncryptedData, s.IV, s.Name, s.SecretType, id, householdID,
	).Scan(
		&updated.ID, &updated.HouseholdID, &updated.EntityType, &updated.EntityID,
		&updated.EncryptedData, &updated.IV, &updated.KeyVersion,
		&updated.Name, &updated.SecretType, &updated.CreatedAt, &updated.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("update secret: %w", err)
	}
	return &updated, nil
}

// DeleteSecret removes a secret by ID, scoped to householdID. Returns
// pgx.ErrNoRows if the secret does not exist or belongs to a different
// household — the caller can translate that to 404 without leaking
// cross-tenant existence (matching file/repo.go's DeleteFile contract).
func (r *Repo) DeleteSecret(ctx context.Context, householdID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM secrets WHERE id = $1 AND household_id = $2`,
		id, householdID,
	)
	if err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// SetupKey inserts a new master-password verification key for a household.
// keyHash is a hash of the PBKDF2-derived key (used to verify the user entered
// the right master password without storing the key itself), keySalt is the
// salt fed to PBKDF2, and keyVersion namespaces this key for rotation. The
// (household_id, key_version) pair is unique per the migration, so a duplicate
// setup for the same version will fail with a unique-violation that the caller
// can translate to 409.
//
// Returns the persisted *SecretKey (with DB-generated id + created_at).
func (r *Repo) SetupKey(ctx context.Context, householdID uuid.UUID, keyHash, keySalt []byte, keyVersion int) (*SecretKey, error) {
	var created SecretKey
	err := r.pool.QueryRow(ctx,
		`INSERT INTO secret_keys (household_id, key_hash, key_salt, key_version)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, household_id, key_hash, key_salt, key_version, created_at`,
		householdID, keyHash, keySalt, keyVersion,
	).Scan(
		&created.ID, &created.HouseholdID, &created.KeyHash, &created.KeySalt,
		&created.KeyVersion, &created.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("setup key: %w", err)
	}
	return &created, nil
}

// GetKey retrieves the master-password verification key (key_hash + key_salt)
// for a household + key_version. The client uses key_salt to re-derive the
// PBKDF2 key from the entered master password, then compares the derived
// key's hash against key_hash to confirm the password is correct before
// attempting decryption.
//
// Returns (nil, nil) when no key exists for the household + version — callers
// translate that to "secrets not yet set up" (the UI prompts for SetupKey).
// Same not-found-vs-forbidden indistinguishability as the secret CRUD methods.
func (r *Repo) GetKey(ctx context.Context, householdID uuid.UUID, keyVersion int) (*SecretKey, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, key_hash, key_salt, key_version, created_at
		 FROM secret_keys
		 WHERE household_id = $1 AND key_version = $2`,
		householdID, keyVersion,
	)
	if err != nil {
		return nil, fmt.Errorf("get key: %w", err)
	}
	defer rows.Close()

	k, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[SecretKey])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get key: collect: %w", err)
	}
	return k, nil
}
