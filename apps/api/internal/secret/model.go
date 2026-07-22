// Package secret manages the native Home OS secrets manager — a zero-knowledge
// store where the API holds only encrypted blobs and the browser derives the
// AES-256-GCM key from the user's master password via the Web Crypto API
// (PBKDF2, 100K iterations). Plaintext never touches the server.
//
// The secrets table holds the encrypted payload plus plaintext metadata
// (name, secret_type) for listing/search. The secret_keys table holds the
// master-password verification material (key_hash + key_salt, both BYTEA) so
// the /unlock endpoint can confirm a derived key without ever storing the key
// itself. Key rotation is supported via key_version on both tables.
//
// BYTEA columns (encrypted_data, iv, key_hash, key_salt) are modeled as []byte
// in Go. encoding/json marshals []byte as a standard base64 string by default,
// which is exactly what the browser expects when it pulls a blob back to
// decrypt it. Unmarshal accepts the same base64 form on write.
package secret

import (
	"time"

	"github.com/google/uuid"
)

// SecretType constants enumerate the kinds of secrets a household can store.
// Stored as plain text in the secrets.secret_type column so the API can filter
// listings by type without touching the encrypted payload. Values match the
// secret_type strings the frontend sends and must remain stable across
// migrations — see architecture/secrets-manager-research.md.
const (
	// SecretTypeLogin is a username + password + URL credential.
	SecretTypeLogin = "login"
	// SecretTypeNote is a free-form encrypted note.
	SecretTypeNote = "note"
	// SecretTypeAPIKey is a named API key value with an optional URL.
	SecretTypeAPIKey = "api_key"
	// SecretTypeCard is a credit card: cardholder name, number, expiry, CVV.
	SecretTypeCard = "card"
)

// EntityType constants enumerate the polymorphic entity kinds that a secret
// can be attached to via secrets.entity_type + secrets.entity_id. These mirror
// the values used by the file and note packages — keep them in sync with
// apps/api/internal/file/model.go, apps/api/internal/note/model.go, and
// apps/api/internal/seed/demo.go.
const (
	EntityTypeProperty    = "property"
	EntityTypeVehicle     = "vehicle"
	EntityTypePet         = "pet"
	EntityTypeBill        = "bill"
	EntityTypeNote        = "note"
	EntityTypeAsset       = "asset"
	EntityTypeMaintenance = "maintenance"
	EntityTypeVendor      = "vendor"
)

// Secret represents a row from the secrets table — an encrypted blob plus the
// plaintext metadata needed to list and search secrets without decrypting.
//
// EncryptedData and IV are modeled as []byte so encoding/json emits them as
// standard base64 strings — the form the browser expects for client-side
// AES-256-GCM decryption. The server never reads or interprets these bytes.
//
// Columns match the schema in architecture/secrets-manager-research.md.
type Secret struct {
	ID            uuid.UUID `json:"id"`
	HouseholdID   uuid.UUID `json:"household_id"`
	EntityType    string    `json:"entity_type"`
	EntityID      uuid.UUID `json:"entity_id"`
	EncryptedData []byte    `json:"encrypted_data"` // base64 on the wire; AES-256-GCM ciphertext
	IV            []byte    `json:"iv"`             // base64 on the wire; 12-byte GCM nonce
	KeyVersion    int       `json:"key_version"`
	Name          string    `json:"name"`
	SecretType    string    `json:"secret_type"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// SecretListItem is the metadata-only view of a secret (no encrypted_data/iv).
// Used by ListSecrets so ciphertext is never fetched from the DB.
type SecretListItem struct {
	ID          uuid.UUID `json:"id"`
	HouseholdID uuid.UUID `json:"household_id"`
	EntityType  string    `json:"entity_type"`
	EntityID    uuid.UUID `json:"entity_id"`
	KeyVersion  int       `json:"key_version"`
	Name        string    `json:"name"`
	SecretType  string    `json:"secret_type"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SecretKey represents a row from the secret_keys table — the material used to
// verify a user's master password / derived key without ever storing the key
// itself. KeyHash is a verifier (e.g. HMAC or hash of a known plaintext under
// the derived key) and KeySalt is the per-household PBKDF2 salt. Both are
// BYTEA and surface as base64 strings in JSON for the browser.
//
// key_version ties a verifier to a specific key version so rotation can be
// staged: a new row is inserted with the next version, then secrets are
// re-encrypted and the old row is dropped once migration is complete.
type SecretKey struct {
	ID          uuid.UUID `json:"id"`
	HouseholdID uuid.UUID `json:"household_id"`
	KeyHash     []byte    `json:"key_hash"` // base64 on the wire; verifier for the derived key
	KeySalt     []byte    `json:"key_salt"` // base64 on the wire; PBKDF2 salt
	KeyVersion  int       `json:"key_version"`
	CreatedAt   time.Time `json:"created_at"`
}
