package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultTikaURL matches the Tika service name in deploy/docker-compose.yml.
// The Helm chart always sets TIKA_URL explicitly.
const DefaultTikaURL = "http://tika:9998"

// DefaultFileProcessorPollInterval is how often the file OCR processor
// polls for pending files when FILE_PROCESSOR_POLL_INTERVAL is unset.
const DefaultFileProcessorPollInterval = 10 * time.Second

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

	// TikaURL is the base URL of the Apache Tika server used for OCR /
	// text extraction (TIKA_URL). No trailing slash.
	TikaURL string
	// FileProcessorPollInterval is the file OCR processor's polling
	// interval (FILE_PROCESSOR_POLL_INTERVAL).
	FileProcessorPollInterval time.Duration
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

	fpRaw := os.Getenv("FILE_PROCESSOR_POLL_INTERVAL")
	if fpRaw == "" {
		cfg.FileProcessorPollInterval = DefaultFileProcessorPollInterval
	} else {
		d, err := time.ParseDuration(fpRaw)
		if err != nil {
			return nil, fmt.Errorf("parse FILE_PROCESSOR_POLL_INTERVAL: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("FILE_PROCESSOR_POLL_INTERVAL must be positive, got %s", d)
		}
		cfg.FileProcessorPollInterval = d
	}

	tikaURL, err := parseTikaURL(os.Getenv("TIKA_URL"))
	if err != nil {
		return nil, err
	}
	cfg.TikaURL = tikaURL

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

// parseTikaURL validates TIKA_URL: empty means DefaultTikaURL; otherwise it
// must be an absolute http(s) URL with a host. The trailing slash is trimmed
// so callers can append paths like "/tika".
func parseTikaURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultTikaURL, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse TIKA_URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("TIKA_URL must use http or https, got %q", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("TIKA_URL must include a host, got %q", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}
