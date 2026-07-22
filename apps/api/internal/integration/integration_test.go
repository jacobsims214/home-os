package integration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncryptDecrypt_HomeAssistant(t *testing.T) {
	cfg := HomeAssistantConfig{BaseURL: "https://ha.example.com", Token: "ha-token-789"}
	key := "test-key-32-chars-long-xxxxxxxxxxxxxxxx"

	encrypted, err := EncryptConfig(key, cfg)
	require.NoError(t, err)
	require.NotEmpty(t, encrypted)

	var decrypted HomeAssistantConfig
	err = DecryptConfig(key, encrypted, &decrypted)
	require.NoError(t, err)
	assert.Equal(t, cfg.BaseURL, decrypted.BaseURL)
	assert.Equal(t, cfg.Token, decrypted.Token)
}

func TestDecrypt_WrongKey(t *testing.T) {
	cfg := HomeAssistantConfig{BaseURL: "http://example.com", Token: "token123"}
	key := "test-key-32-chars-long-xxxxxxxxxxxxxxxx"

	encrypted, err := EncryptConfig(key, cfg)
	require.NoError(t, err)

	wrongKey := "different-key-32-chars-long-xxxxxxxxxxxxx"

	var decrypted HomeAssistantConfig
	err = DecryptConfig(wrongKey, encrypted, &decrypted)
	assert.Error(t, err, "decrypting with wrong key should fail")
	assert.Contains(t, err.Error(), "open", "error should mention cipher opening failure")
}

func TestDecrypt_BadCiphertext(t *testing.T) {
	key := "test-key-32-chars-long-xxxxxxxxxxxxxxxx"

	var decrypted HomeAssistantConfig
	err := DecryptConfig(key, []byte("too-short"), &decrypted)
	assert.Error(t, err, "decrypting garbage should fail")
	assert.Contains(t, err.Error(), "ciphertext too short", "should report ciphertext too short")
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	cfg := HomeAssistantConfig{BaseURL: "http://example.com", Token: "token123"}
	key := "test-key-32-chars-long-xxxxxxxxxxxxxxxx"

	encrypted, err := EncryptConfig(key, cfg)
	require.NoError(t, err)

	// Tamper with the ciphertext (after the nonce)
	tampered := make([]byte, len(encrypted))
	copy(tampered, encrypted)
	if len(tampered) > 13 {
		tampered[13] ^= 0xFF // flip some bits
	}

	var decrypted HomeAssistantConfig
	err = DecryptConfig(key, tampered, &decrypted)
	assert.Error(t, err, "decrypting tampered data should fail (GCM auth)")
}

func TestEncryptDecrypt_EmptyFields(t *testing.T) {
	// Verify that configs with empty string fields still roundtrip correctly.
	cfg := HomeAssistantConfig{
		BaseURL: "http://ha.example.com",
		Token:   "",
	}
	key := "test-key-32-chars-long-xxxxxxxxxxxxxxxx"

	encrypted, err := EncryptConfig(key, cfg)
	require.NoError(t, err)

	var decrypted HomeAssistantConfig
	err = DecryptConfig(key, encrypted, &decrypted)
	require.NoError(t, err)
	assert.Equal(t, cfg.BaseURL, decrypted.BaseURL)
	assert.Equal(t, cfg.Token, decrypted.Token)
}

func TestDeriveKey_Consistency(t *testing.T) {
	// deriveKey should produce the same key for the same input.
	key1 := deriveKey("my-master-key")
	key2 := deriveKey("my-master-key")

	assert.Equal(t, key1, key2, "deriveKey should be deterministic")
	assert.Len(t, key1, 32, "AES-256 key should be 32 bytes")
}

func TestDeriveKey_DifferentInputs(t *testing.T) {
	// Different master keys should produce different derived keys.
	key1 := deriveKey("key-one")
	key2 := deriveKey("key-two")

	assert.NotEqual(t, key1, key2, "different master keys should produce different derived keys")
}
