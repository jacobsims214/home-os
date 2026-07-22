// Package integration manages external service integrations (Home Assistant)
// for a household. Config is stored encrypted at rest using AES-256-GCM.
package integration

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
)

// Integration types matching the integration_type PostgreSQL enum.
const (
	TypeHomeAssistant = "homeassistant"
)

// Integration statuses matching the integration_status PostgreSQL enum.
const (
	StatusConnected    = "connected"
	StatusDisconnected = "disconnected"
	StatusError        = "error"
)

// Integration represents a row from the integrations table.
// Config is never exposed in API responses — only status and metadata.
type Integration struct {
	ID              uuid.UUID  `json:"id"`
	HouseholdID     uuid.UUID  `json:"household_id"`
	Type            string     `json:"type"`
	Config          []byte     `json:"-"` // encrypted JSON; never serialized
	Status          string     `json:"status"`
	LastHealthCheck *time.Time `json:"last_health_check,omitempty"`
	LastSync        *time.Time `json:"last_sync,omitempty"`
	ErrorMessage    *string    `json:"error_message,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// --- Config types (stored as encrypted JSON in the config column) ---

// HomeAssistantConfig holds connection details for a Home Assistant instance.
type HomeAssistantConfig struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
}

// --- Encryption helpers (AES-256-GCM) ---

// deriveKey derives a 32-byte AES-256 key from the encryption key using SHA-256.
func deriveKey(masterKey string) []byte {
	h := sha256.New()
	h.Write([]byte(masterKey))
	return h.Sum(nil)
}

// EncryptConfig marshals the config value to JSON and encrypts it with AES-256-GCM.
// Returns the encrypted blob (nonce + ciphertext).
func EncryptConfig(masterKey string, v any) ([]byte, error) {
	plaintext, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encrypt config: marshal: %w", err)
	}

	key := deriveKey(masterKey)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("encrypt config: new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("encrypt config: new gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("encrypt config: nonce: %w", err)
	}

	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// DecryptConfig decrypts the blob with AES-256-GCM and unmarshals the JSON
// into the target value v (must be a pointer to the appropriate config type).
func DecryptConfig(masterKey string, ciphertext []byte, v any) error {
	key := deriveKey(masterKey)
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("decrypt config: new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("decrypt config: new gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return fmt.Errorf("decrypt config: ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return fmt.Errorf("decrypt config: open: %w", err)
	}

	if err := json.Unmarshal(plaintext, v); err != nil {
		return fmt.Errorf("decrypt config: unmarshal: %w", err)
	}
	return nil
}
