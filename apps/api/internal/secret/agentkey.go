package secret

// Server-side encryption for credentials "shared with agents" (#4446, KB
// decisions/2026-11-15-agent-credential-access.md).
//
// Zero-knowledge stays the default: a secret's encrypted_data is sealed in the
// browser and the server can't open it. Only when an owner or family_manager
// shares one credential with agents does the browser send its plaintext (once,
// over TLS), and the API seals a second copy into secrets.agent_ciphertext.
//
// Format of agent_ciphertext (identical in apps/mcp/internal/credential, which
// is the only process that opens it; keep the two files in sync, the
// known-answer test in both pins the bytes):
//
//	0x01 || nonce (12 random bytes) || AES-256-GCM(ciphertext || tag)
//
//   - Key: HKDF-SHA256(master, salt = agentKeySalt, info = "household:" + id),
//     32 bytes. One key per household, so a blob can never be opened with
//     another household's key.
//   - AAD: agentAADPrefix + household id + "|" + secret id, so a blob copied
//     to another row (or household) fails authentication.
//   - master: 32 random bytes from env AGENT_CREDENTIALS_KEY (base64), held in
//     a Kubernetes Secret. Never stored in Postgres and never logged.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
)

const (
	agentBlobVersion byte = 1
	agentNonceSize        = 12
	agentKeySalt          = "home-os/agent-credentials/v1"
	agentAADPrefix        = "home-os/agent-credential/v1|"
	// AgentKeyEnv names the env var holding the base64 master key.
	AgentKeyEnv = "AGENT_CREDENTIALS_KEY"
)

// AgentKey seals credential values for agents. A nil *AgentKey means agent
// sharing is not configured.
type AgentKey struct {
	master []byte
}

// ParseAgentKey decodes a base64 (standard or URL alphabet, padded or not)
// 32-byte master key. An empty string returns (nil, nil): sharing disabled.
// Errors never include the key material.
func ParseAgentKey(s string) (*AgentKey, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) != 32 {
				return nil, fmt.Errorf("%s must decode to 32 bytes, got %d", AgentKeyEnv, len(b))
			}
			return &AgentKey{master: b}, nil
		}
	}
	return nil, fmt.Errorf("%s is not valid base64", AgentKeyEnv)
}

// String never prints the key.
func (k *AgentKey) String() string { return "AgentKey(redacted)" }

func (k *AgentKey) aead(householdID uuid.UUID) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, k.master, []byte(agentKeySalt), "household:"+householdID.String(), 32)
	if err != nil {
		return nil, fmt.Errorf("derive household key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func agentAAD(householdID, secretID uuid.UUID) []byte {
	return []byte(agentAADPrefix + householdID.String() + "|" + secretID.String())
}

// Seal encrypts plaintext for (householdID, secretID) with a fresh random nonce.
func (k *AgentKey) Seal(householdID, secretID uuid.UUID, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, agentNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	return k.sealWithNonce(householdID, secretID, nonce, plaintext)
}

func (k *AgentKey) sealWithNonce(householdID, secretID uuid.UUID, nonce, plaintext []byte) ([]byte, error) {
	if k == nil {
		return nil, errAgentSharingDisabled
	}
	g, err := k.aead(householdID)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+agentNonceSize+len(plaintext)+g.Overhead())
	out = append(out, agentBlobVersion)
	out = append(out, nonce...)
	return g.Seal(out, nonce, plaintext, agentAAD(householdID, secretID)), nil
}

// Open decrypts a blob made by Seal. The API never calls it in production
// (only the MCP reveals values); it exists for tests.
func (k *AgentKey) Open(householdID, secretID uuid.UUID, blob []byte) ([]byte, error) {
	if k == nil {
		return nil, errAgentSharingDisabled
	}
	if len(blob) < 1+agentNonceSize || blob[0] != agentBlobVersion {
		return nil, errors.New("agent ciphertext: unknown format")
	}
	g, err := k.aead(householdID)
	if err != nil {
		return nil, err
	}
	pt, err := g.Open(nil, blob[1:1+agentNonceSize], blob[1+agentNonceSize:], agentAAD(householdID, secretID))
	if err != nil {
		return nil, errors.New("agent ciphertext: authentication failed")
	}
	return pt, nil
}

var errAgentSharingDisabled = errors.New("agent sharing is not configured")
