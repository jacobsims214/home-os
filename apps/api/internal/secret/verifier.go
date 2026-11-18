package secret

// Master-password verifier hashing (#4440, migration 051).
//
// The browser derives an AES key from the master password (PBKDF2 + the
// household salt) and sends a verifier computed from that key: today
// SHA-256(raw key), sent as key_hash. The API never stores that value. It
// stores argon2id(verifier, random per-row salt) as a PHC string in
// secret_keys.verifier, so a database leak gives neither a value that can be
// replayed to /verify nor a fast way to test password guesses.
//
// Nothing in this file logs, returns or formats verifier bytes or hashes.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Cost parameters for new verifiers. OWASP's argon2id baseline (19 MiB, 2
// passes, 1 lane). They are recorded in each PHC string, so raising them only
// needs a change here: rows with older parameters are rehashed on their next
// successful unlock.
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonKeyLen    = 32
	argonSaltLen   = 16

	// Bounds on a stored PHC string's parameters, so a tampered row cannot
	// make the API allocate unbounded memory or spin.
	maxArgonMemoryKiB = 256 * 1024
	maxArgonTime      = 10
	maxArgonThreads   = 16

	// Bounds on the client-sent verifier (bytes, after base64 decoding).
	minVerifierLen = 16
	maxVerifierLen = 128

	// maxConcurrentHashes caps simultaneous argon2id computations so a burst
	// of /setup or /verify calls cannot exhaust the pod's memory
	// (maxConcurrentHashes * argonMemoryKiB at most).
	maxConcurrentHashes = 4
)

var hashSlots = make(chan struct{}, maxConcurrentHashes)

// errMalformedVerifier means a stored verifier string could not be parsed.
var errMalformedVerifier = errors.New("secret: malformed stored verifier")

// argonParams are the cost parameters encoded in a PHC string.
type argonParams struct {
	memoryKiB uint32
	time      uint32
	threads   uint8
}

var currentParams = argonParams{memoryKiB: argonMemoryKiB, time: argonTime, threads: argonThreads}

// acquireHashSlot blocks until an argon2id slot is free or ctx is done.
func acquireHashSlot(ctx context.Context) (release func(), err error) {
	select {
	case hashSlots <- struct{}{}:
		return func() { <-hashSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// validClientVerifier reports whether a client-sent verifier has a sane length.
func validClientVerifier(v []byte) bool {
	return len(v) >= minVerifierLen && len(v) <= maxVerifierLen
}

// hashVerifier returns the argon2id PHC string for a client-sent verifier,
// with a fresh random salt and the current cost parameters.
func hashVerifier(ctx context.Context, clientVerifier []byte) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("secret: read salt: %w", err)
	}
	release, err := acquireHashSlot(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	p := currentParams
	sum := argon2.IDKey(clientVerifier, salt, p.time, p.memoryKiB, p.threads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memoryKiB, p.time, p.threads, b64.EncodeToString(salt), b64.EncodeToString(sum)), nil
}

// checkVerifier compares a client-sent verifier with a stored PHC string in
// constant time. needsRehash is true on a match whose stored parameters differ
// from the current ones.
func checkVerifier(ctx context.Context, encoded string, clientVerifier []byte) (ok, needsRehash bool, err error) {
	p, salt, want, err := parsePHC(encoded)
	if err != nil {
		return false, false, err
	}
	release, err := acquireHashSlot(ctx)
	if err != nil {
		return false, false, err
	}
	defer release()

	got := argon2.IDKey(clientVerifier, salt, p.time, p.memoryKiB, p.threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	return true, p != currentParams || len(want) != argonKeyLen, nil
}

// parsePHC parses "$argon2id$v=19$m=..,t=..,p=..$salt$hash".
func parsePHC(encoded string) (argonParams, []byte, []byte, error) {
	var p argonParams
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, errMalformedVerifier
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return p, nil, nil, errMalformedVerifier
	}
	seen := 0
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, found := strings.Cut(kv, "=")
		if !found {
			return p, nil, nil, errMalformedVerifier
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil || n == 0 {
			return p, nil, nil, errMalformedVerifier
		}
		switch k {
		case "m":
			if n > maxArgonMemoryKiB {
				return p, nil, nil, errMalformedVerifier
			}
			p.memoryKiB = uint32(n)
		case "t":
			if n > maxArgonTime {
				return p, nil, nil, errMalformedVerifier
			}
			p.time = uint32(n)
		case "p":
			if n > maxArgonThreads {
				return p, nil, nil, errMalformedVerifier
			}
			p.threads = uint8(n)
		default:
			return p, nil, nil, errMalformedVerifier
		}
		seen++
	}
	if seen != 3 || p.memoryKiB == 0 || p.time == 0 || p.threads == 0 || p.memoryKiB < 8*uint32(p.threads) {
		return p, nil, nil, errMalformedVerifier
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return p, nil, nil, errMalformedVerifier
	}
	hash, err := b64.DecodeString(parts[5])
	if err != nil || len(hash) < 16 || len(hash) > 64 {
		return p, nil, nil, errMalformedVerifier
	}
	return p, salt, hash, nil
}
