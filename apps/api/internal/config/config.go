// Package config loads application configuration from environment variables.
// All config values are read once at startup and passed to components.
// Missing required variables cause startup to fail with a clear error message.
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config holds all environment-driven configuration for the API service.
type Config struct {
	Port            string
	DatabaseURL     string
	JWTSecret       string
	TypesenseHost   string
	TypesensePort   string
	TypesenseAPIKey string
	MinioEndpoint   string
	MinioAccessKey  string
	MinioSecretKey  string
	MinioUseSSL     bool
	EncryptionKey   string
	LogLevel        string
	DemoMode        bool
}

// Load reads configuration from environment variables and returns a validated Config.
// Returns an error if DATABASE_URL or JWT_SECRET are missing or empty.
func Load() (*Config, error) {
	cfg := &Config{
		Port:            getEnv("PORT", "8080"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		JWTSecret:       os.Getenv("JWT_SECRET"),
		TypesenseHost:   getEnv("TYPESENSE_HOST", "localhost"),
		TypesensePort:   getEnv("TYPESENSE_PORT", "8108"),
		TypesenseAPIKey: os.Getenv("TYPESENSE_API_KEY"),
		MinioEndpoint:   getEnv("MINIO_ENDPOINT", "localhost:9000"),
		MinioAccessKey:  os.Getenv("MINIO_ACCESS_KEY"),
		MinioSecretKey:  os.Getenv("MINIO_SECRET_KEY"),
		MinioUseSSL:     strings.ToLower(os.Getenv("MINIO_USE_SSL")) == "true",
		EncryptionKey:   os.Getenv("ENCRYPTION_KEY"),
		LogLevel:        getEnv("LOG_LEVEL", "info"),
		DemoMode:        strings.ToLower(os.Getenv("DEMO_MODE")) == "true",
	}

	var missing []string
	if cfg.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if cfg.JWTSecret == "" {
		missing = append(missing, "JWT_SECRET")
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
