// Package config loads the calendar service configuration from environment
// variables. All configuration is read at startup — the service does not
// support hot-reload of config values.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all configuration for the calendar service.
type Config struct {
	// Port is the TCP port the HTTP server listens on. Default: 8081.
	Port int

	// DatabaseURL is the PostgreSQL connection string (postgres://...).
	DatabaseURL string

	// CalendarSecret is a shared secret used by the calendar service to
	// authenticate internal requests from the core API.
	CalendarSecret string
}

// Load reads configuration from environment variables and returns a validated
// Config. Uses defaults where environment variables are not set.
func Load() (*Config, error) {
	cfg := &Config{}

	portStr := os.Getenv("PORT")
	if portStr == "" {
		portStr = "8081"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("config: invalid PORT %q: %w", portStr, err)
	}
	cfg.Port = port

	cfg.DatabaseURL = os.Getenv("DATABASE_URL")
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("config: DATABASE_URL is required")
	}

	cfg.CalendarSecret = os.Getenv("CALENDAR_SECRET")

	return cfg, nil
}
