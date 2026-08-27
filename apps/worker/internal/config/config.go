package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all configuration for the worker service.
// All values are loaded from environment variables at startup.
type Config struct {
	DatabaseURL     string
	TypesenseHost   string
	TypesensePort   string
	TypesenseAPIKey string
	PollInterval    time.Duration
	SMTPHost        string
	SMTPPort        int
	SMTPUsername    string
	SMTPPassword    string
	SMTPFrom        string
}

// Load reads configuration from environment variables and returns a validated Config.
// Missing required variables cause an immediate error — fail fast.
func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		TypesenseHost:   os.Getenv("TYPESENSE_HOST"),
		TypesensePort:   os.Getenv("TYPESENSE_PORT"),
		TypesenseAPIKey: os.Getenv("TYPESENSE_API_KEY"),
		SMTPHost:        os.Getenv("SMTP_HOST"),
		SMTPUsername:    os.Getenv("SMTP_USERNAME"),
		SMTPPassword:    os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:        os.Getenv("SMTP_FROM"),
	}

	if cfg.SMTPFrom == "" {
		cfg.SMTPFrom = "noreply@homeos.local"
	}

	portRaw := os.Getenv("SMTP_PORT")
	if portRaw == "" {
		cfg.SMTPPort = 587
	} else {
		p, err := strconv.Atoi(portRaw)
		if err != nil {
			return nil, fmt.Errorf("parse SMTP_PORT: %w", err)
		}
		cfg.SMTPPort = p
	}

	pollRaw := os.Getenv("POLL_INTERVAL")
	if pollRaw == "" {
		cfg.PollInterval = 5 * time.Second
	} else {
		d, err := time.ParseDuration(pollRaw)
		if err != nil {
			return nil, fmt.Errorf("parse POLL_INTERVAL: %w", err)
		}
		cfg.PollInterval = d
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.TypesenseHost == "" {
		return nil, fmt.Errorf("TYPESENSE_HOST is required")
	}
	if cfg.TypesensePort == "" {
		return nil, fmt.Errorf("TYPESENSE_PORT is required")
	}
	if cfg.TypesenseAPIKey == "" {
		return nil, fmt.Errorf("TYPESENSE_API_KEY is required")
	}

	return cfg, nil
}
