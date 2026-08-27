// Package config loads application configuration from environment variables.
// All config values are read once at startup and passed to components.
// Missing required variables cause startup to fail with a clear error message.
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config holds all environment-driven configuration for the MCP server.
type Config struct {
	Port            string
	MetadataPort    string
	DatabaseURL     string
	DexJWKSURL      string
	DexIssuer       string
	DexGRPCAddr     string
	LogLevel        string
	TypesenseHost   string
	TypesensePort   string
	TypesenseAPIKey string
}

// Load reads configuration from environment variables and returns a validated Config.
// Returns an error if DATABASE_URL is missing or empty.
func Load() (*Config, error) {
	cfg := &Config{
		Port:            getEnv("PORT", "8082"),
		MetadataPort:    getEnv("METADATA_PORT", "8083"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		DexJWKSURL:      getEnv("DEX_JWKS_URL", "http://home-os-envoy.home-os.svc.cluster.local:8000/dex/keys"),
		DexIssuer:       getEnv("DEX_ISSUER", "http://localhost:8000/dex"),
		DexGRPCAddr:     getEnv("DEX_GRPC_ADDR", "home-os-dex:5557"),
		LogLevel:        getEnv("LOG_LEVEL", "info"),
		TypesenseHost:   getEnv("TYPESENSE_HOST", ""),
		TypesensePort:   getEnv("TYPESENSE_PORT", ""),
		TypesenseAPIKey: os.Getenv("TYPESENSE_API_KEY"),
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
