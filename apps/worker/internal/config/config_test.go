package config

import (
	"strings"
	"testing"
	"time"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("TYPESENSE_HOST", "typesense")
	t.Setenv("TYPESENSE_PORT", "8108")
	t.Setenv("TYPESENSE_API_KEY", "k")
}

func TestLoadTikaAndFileProcessor(t *testing.T) {
	tests := []struct {
		name       string
		tikaURL    string
		fpInterval string
		wantURL    string
		wantFP     time.Duration
		wantErr    string
	}{
		{name: "defaults", wantURL: DefaultTikaURL, wantFP: DefaultFileProcessorPollInterval},
		{name: "explicit url and interval", tikaURL: "http://home-os-tika:9998", fpInterval: "30s", wantURL: "http://home-os-tika:9998", wantFP: 30 * time.Second},
		{name: "trailing slash trimmed", tikaURL: "https://tika.example:9998/", wantURL: "https://tika.example:9998", wantFP: DefaultFileProcessorPollInterval},
		{name: "bad scheme", tikaURL: "ftp://tika:9998", wantErr: "http or https"},
		{name: "missing host", tikaURL: "http://", wantErr: "include a host"},
		{name: "no scheme", tikaURL: "tika:9998", wantErr: "http or https"},
		{name: "bad interval", fpInterval: "soon", wantErr: "parse FILE_PROCESSOR_POLL_INTERVAL"},
		{name: "zero interval", fpInterval: "0s", wantErr: "must be positive"},
		{name: "negative interval", fpInterval: "-5s", wantErr: "must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("TIKA_URL", tt.tikaURL)
			t.Setenv("FILE_PROCESSOR_POLL_INTERVAL", tt.fpInterval)

			cfg, err := Load()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if cfg.TikaURL != tt.wantURL {
				t.Errorf("TikaURL = %q, want %q", cfg.TikaURL, tt.wantURL)
			}
			if cfg.FileProcessorPollInterval != tt.wantFP {
				t.Errorf("FileProcessorPollInterval = %s, want %s", cfg.FileProcessorPollInterval, tt.wantFP)
			}
		})
	}
}
