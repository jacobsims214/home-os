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
		PublicURL:          getEnv("PUBLIC_URL", "http://localhost:3000"),
		AllowRegistration: strings.ToLower(os.Getenv("ALLOW_REGISTRATION")) == "true",
		DexGRPCAddr:       getEnv("DEX_GRPC_ADDR", "home-os-dex:5557"),
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
