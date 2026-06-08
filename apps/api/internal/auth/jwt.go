// Package auth provides JWT signing, verification, and claims extraction
// for the Home OS API. Tokens carry user identity and household context
// and are validated on all protected routes.
package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"home-os/api/internal/config"
)

// Claims represents the JWT claims carried by every Home OS token.
// UserID and HouseholdID are required; Role and standard registered claims
// are set automatically by SignToken.
type Claims struct {
	UserID      string `json:"user_id"`
	HouseholdID string `json:"household_id"`
	Role        string `json:"role"`
	jwt.RegisteredClaims
}

// SignToken creates a signed JWT string with HS256 signing, 24-hour expiry,
// and the standard issued-at / not-before timestamps.
func SignToken(cfg *config.Config, claims Claims) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("sign token: config is nil")
	}
	if cfg.JWTSecret == "" {
		return "", fmt.Errorf("sign token: JWTSecret is empty")
	}

	now := time.Now()
	claims.RegisteredClaims = jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour)),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(cfg.JWTSecret))
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signed, nil
}

// VerifyToken parses and validates a JWT string. It returns the extracted
// claims on success, or an error if the token is expired, tampered with,
// or otherwise invalid.
func VerifyToken(cfg *config.Config, tokenStr string) (*Claims, error) {
	if cfg == nil {
		return nil, fmt.Errorf("verify token: config is nil")
	}
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("verify token: JWTSecret is empty")
	}

	token, err := jwt.ParseWithClaims(tokenStr, &Claims{},
		func(t *jwt.Token) (any, error) {
			// Validate the signing algorithm matches what we expect.
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("verify token: unexpected signing method: %v", t.Header["alg"])
			}
			return []byte(cfg.JWTSecret), nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("verify token: invalid claims")
	}
	return claims, nil
}
