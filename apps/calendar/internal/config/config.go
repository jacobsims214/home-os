// Package config loads the calendar service configuration from environment
// variables. All configuration is read at startup — the service does not
// support hot-reload of config values.
package config

import (
	"fmt"
	"os"
	"strconv"

	"home-os/calendar/internal/auth"
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

	// AuthRateLimit holds the CalDAV auth rate limits and trusted proxies:
	//   CALDAV_TRUSTED_PROXIES                comma-separated CIDRs/IPs whose
	//                                         X-Forwarded-For is believed (default none)
	//   CALDAV_AUTH_REQUESTS_PER_MINUTE       per-IP requests (default 120, burst half)
	//   CALDAV_AUTH_FAILURES_PER_MINUTE       per-IP failed logins (default 10, burst same)
	//   CALDAV_AUTH_ACCOUNT_FAILURES_PER_HOUR per-account failed logins (default 30, burst same)
	AuthRateLimit auth.RateLimitConfig
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

	rl, err := loadAuthRateLimit()
	if err != nil {
		return nil, err
	}
	cfg.AuthRateLimit = rl

	return cfg, nil
}

// loadAuthRateLimit reads the CALDAV_* rate-limit variables.
func loadAuthRateLimit() (auth.RateLimitConfig, error) {
	rl := auth.DefaultRateLimitConfig()

	trusted, err := auth.ParseTrustedProxies(os.Getenv("CALDAV_TRUSTED_PROXIES"))
	if err != nil {
		return rl, fmt.Errorf("config: invalid CALDAV_TRUSTED_PROXIES: %w", err)
	}
	rl.TrustedProxies = trusted

	if n, set, err := positiveInt("CALDAV_AUTH_REQUESTS_PER_MINUTE"); err != nil {
		return rl, err
	} else if set {
		rl.RequestsPerMinute = n
		rl.RequestBurst = max(1, n/2)
	}
	if n, set, err := positiveInt("CALDAV_AUTH_FAILURES_PER_MINUTE"); err != nil {
		return rl, err
	} else if set {
		rl.IPFailuresPerMinute = n
		rl.IPFailureBurst = n
	}
	if n, set, err := positiveInt("CALDAV_AUTH_ACCOUNT_FAILURES_PER_HOUR"); err != nil {
		return rl, err
	} else if set {
		rl.AccountFailuresPerHour = n
		rl.AccountFailureBurst = n
	}
	return rl, nil
}

// positiveInt reads env var name as an integer > 0. set is false when the
// variable is unset or empty.
func positiveInt(name string) (n int, set bool, err error) {
	v := os.Getenv(name)
	if v == "" {
		return 0, false, nil
	}
	n, err = strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false, fmt.Errorf("config: %s must be a positive integer, got %q", name, v)
	}
	return n, true, nil
}
