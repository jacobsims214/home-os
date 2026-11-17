// Package config loads application configuration from environment variables.
// All config values are read once at startup and passed to components.
// Missing required variables cause startup to fail with a clear error message.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all environment-driven configuration for the API service.
type Config struct {
	Port              string
	DatabaseURL       string
	DexJWKSURL        string
	DexIssuer         string
	TypesenseHost     string
	TypesensePort     string
	TypesenseAPIKey   string
	MinioEndpoint     string
	MinioAccessKey    string
	MinioSecretKey    string
	MinioUseSSL       bool
	EncryptionKey     string
	LogLevel          string
	DemoMode          bool
	SMTPHost          string
	SMTPPort          int
	SMTPUsername      string
	SMTPPassword      string
	SMTPFrom          string
	PublicURL         string
	AllowRegistration bool
	DexGRPCAddr       string
	AdminEmail        string // ADMIN_EMAIL, default ""
	AdminPassword     string // ADMIN_PASSWORD, default ""
	AdminName         string // ADMIN_NAME, default "Admin"
	// AgentCredentialsKey is the base64 32-byte master key that seals
	// credentials shared with agents (AGENT_CREDENTIALS_KEY, from a
	// Kubernetes Secret, #4446). Empty disables sharing. Never logged.
	AgentCredentialsKey string

	// LocalDev (LOCAL_DEV=true, local docker-desktop/compose only) lets CORS
	// accept localhost origins besides the PUBLIC_URL origin.
	LocalDev bool

	// Rate limit for the unauthenticated routes (invite accept, household
	// create, forgot/reset password), per client IP (#4685):
	//   API_PUBLIC_RATE_PER_MINUTE  default 10
	//   API_PUBLIC_RATE_BURST       default 5
	//   API_TRUSTED_PROXIES         comma-separated CIDRs/IPs of the reverse
	//                               proxies in front of the API; only then is
	//                               X-Forwarded-For believed. Empty = TCP peer.
	PublicRatePerMinute int
	PublicRateBurst     int
	TrustedProxies      string

	// Rate limit for POST /api/v1/auth/change-password, which takes a password
	// guess and so is a guessing surface for whoever holds a live session
	// (#4822). Its own limiter instance, not the public one, and enforced both
	// per client IP and per account:
	//   API_CHANGE_PASSWORD_RATE_PER_MINUTE  default 5
	//   API_CHANGE_PASSWORD_RATE_BURST       default 3
	ChangePasswordRatePerMinute int
	ChangePasswordRateBurst     int
}

// Load reads configuration from environment variables and returns a validated Config.
// Returns an error if DATABASE_URL is missing or empty.
func Load() (*Config, error) {
	cfg := &Config{
		Port:              getEnv("PORT", "8080"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		DexJWKSURL:        getEnv("DEX_JWKS_URL", "http://home-os-envoy.home-os.svc.cluster.local:8000/dex/keys"),
		DexIssuer:         getEnv("DEX_ISSUER", "http://localhost:8000/dex"),
		TypesenseHost:     getEnv("TYPESENSE_HOST", "localhost"),
		TypesensePort:     getEnv("TYPESENSE_PORT", "8108"),
		TypesenseAPIKey:   os.Getenv("TYPESENSE_API_KEY"),
		MinioEndpoint:     getEnv("MINIO_ENDPOINT", "localhost:9000"),
		MinioAccessKey:    os.Getenv("MINIO_ACCESS_KEY"),
		MinioSecretKey:    os.Getenv("MINIO_SECRET_KEY"),
		MinioUseSSL:       strings.ToLower(os.Getenv("MINIO_USE_SSL")) == "true",
		EncryptionKey:     os.Getenv("ENCRYPTION_KEY"),
		LogLevel:          getEnv("LOG_LEVEL", "info"),
		DemoMode:          strings.ToLower(os.Getenv("DEMO_MODE")) == "true",
		SMTPHost:          os.Getenv("SMTP_HOST"),
		SMTPPort:          getEnvInt("SMTP_PORT", "587"),
		SMTPUsername:      os.Getenv("SMTP_USERNAME"),
		SMTPPassword:      os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:          getEnv("SMTP_FROM", "noreply@homeos.local"),
		PublicURL:         getEnv("PUBLIC_URL", "http://localhost:3000"),
		AllowRegistration: strings.ToLower(os.Getenv("ALLOW_REGISTRATION")) == "true",
		DexGRPCAddr:       getEnv("DEX_GRPC_ADDR", "home-os-dex:5557"),
		AdminEmail:        os.Getenv("ADMIN_EMAIL"),
		AdminPassword:     os.Getenv("ADMIN_PASSWORD"),
		AdminName:         getEnv("ADMIN_NAME", "Admin"),

		AgentCredentialsKey: os.Getenv("AGENT_CREDENTIALS_KEY"),

		LocalDev:            strings.ToLower(os.Getenv("LOCAL_DEV")) == "true",
		PublicRatePerMinute: getEnvInt("API_PUBLIC_RATE_PER_MINUTE", "10"),
		PublicRateBurst:     getEnvInt("API_PUBLIC_RATE_BURST", "5"),
		TrustedProxies:      os.Getenv("API_TRUSTED_PROXIES"),

		ChangePasswordRatePerMinute: getEnvInt("API_CHANGE_PASSWORD_RATE_PER_MINUTE", "5"),
		ChangePasswordRateBurst:     getEnvInt("API_CHANGE_PASSWORD_RATE_BURST", "3"),
	}

	var missing []string
	if cfg.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("config: missing required environment variables: %s", strings.Join(missing, ", "))
	}

	return cfg, nil
}

// getEnv returns the value of the environment variable if set, otherwise returns fallback.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getEnvInt returns the integer value of the environment variable if set, otherwise returns fallback.
func getEnvInt(key, fallback string) int {
	v := os.Getenv(key)
	if v == "" {
		v = fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}
