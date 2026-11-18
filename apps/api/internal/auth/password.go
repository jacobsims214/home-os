package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// DefaultCost is the bcrypt cost used for password hashing.
// A cost of 12 balances security with acceptable latency (~250ms on modern hardware).
const DefaultCost = 12

// Password length rules. These are the rules invite signup and admin user
// creation already enforce (8 characters minimum; 72 bytes maximum, because
// bcrypt ignores and newer versions of the library refuse anything past 72
// bytes rather than truncating it). They live here so every endpoint that sets
// a password — signup, admin create, reset and change — answers the same way
// instead of each keeping a copy that can drift.
const (
	MinPasswordLength = 8
	MaxPasswordLength = 72
)

// ErrPasswordTooShort and ErrPasswordTooLong are the two ways
// ValidatePasswordLength refuses a password.
var (
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	ErrPasswordTooLong  = fmt.Errorf("password must be at most %d bytes", MaxPasswordLength)
)

// ValidatePasswordLength applies the shared password length rules: at least
// MinPasswordLength bytes, at most MaxPasswordLength bytes. The errors it
// returns are the messages the API has always sent for a password it will not
// accept, so handlers can pass them straight to the client.
func ValidatePasswordLength(password string) error {
	switch {
	case len(password) < MinPasswordLength:
		return ErrPasswordTooShort
	case len(password) > MaxPasswordLength:
		return ErrPasswordTooLong
	}
	return nil
}

// HashPassword returns the bcrypt hash of the given plaintext password.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// CheckPassword compares a plaintext password against a bcrypt hash.
// Returns nil if the password matches, or an error if it does not.
func CheckPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// bcryptCostHash is a bcrypt cost-12 hash of 32 random bytes that were
// discarded when it was generated: no password in this system matches it, and
// nobody can supply the plaintext because it no longer exists anywhere.
// passwordCheckCost compares against it purely to spend the time a real
// comparison costs and throws the result away.
//
// It exists because the cost of a bcrypt comparison is the only thing that
// makes a wrong-password check slow. Dex spends it on a real comparison; a path
// that finds no password entry at all spends nothing, and would then answer
// noticeably faster. That difference is how an attacker with a live session
// could tell "wrong password" from "not a password account" without a single
// hint in the response body.
const bcryptCostHash = "$2a$12$f9UeX8unQk.J7Qelfol60.Px5zoUwnP8cnROnKyTRCnTh60ap01WO"

// passwordCheckCost spends one bcrypt comparison at DefaultCost. It always
// returns false: the hash is nobody's password, so there is nothing to report.
// Callers use it to make a credential check that could not run a comparison
// cost the same as one that did.
func passwordCheckCost(password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(bcryptCostHash), []byte(password)) == nil
}
