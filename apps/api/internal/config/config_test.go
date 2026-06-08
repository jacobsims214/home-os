package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Run("returns error when DATABASE_URL is missing", func(t *testing.T) {
		os.Clearenv()
		os.Setenv("JWT_SECRET", "test-secret")

		cfg, err := Load()
		if err == nil {
			t.Fatal("expected error for missing DATABASE_URL, got nil")
		}
		if cfg != nil {
			t.Fatal("expected nil config on error")
		}
		if !strings.Contains(err.Error(), "DATABASE_URL") {
			t.Errorf("error should mention DATABASE_URL, got: %v", err)
		}
	})

	t.Run("returns error when JWT_SECRET is missing", func(t *testing.T) {
		os.Clearenv()
		os.Setenv("DATABASE_URL", "postgres://localhost/test")

		cfg, err := Load()
		if err == nil {
			t.Fatal("expected error for missing JWT_SECRET, got nil")
		}
		if cfg != nil {
			t.Fatal("expected nil config on error")
		}
		if !strings.Contains(err.Error(), "JWT_SECRET") {
			t.Errorf("error should mention JWT_SECRET, got: %v", err)
		}
	})

	t.Run("returns error when both required vars are missing", func(t *testing.T) {
		os.Clearenv()

		cfg, err := Load()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if cfg != nil {
			t.Fatal("expected nil config on error")
		}
		if !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "JWT_SECRET") {
			t.Errorf("error should mention both vars, got: %v", err)
		}
	})

	t.Run("succeeds when all required vars are set", func(t *testing.T) {
		os.Clearenv()
		os.Setenv("DATABASE_URL", "postgres://localhost/test")
		os.Setenv("JWT_SECRET", "test-secret")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if cfg.DatabaseURL != "postgres://localhost/test" {
			t.Errorf("DatabaseURL = %q, want %q", cfg.DatabaseURL, "postgres://localhost/test")
		}
		if cfg.JWTSecret != "test-secret" {
			t.Errorf("JWTSecret = %q, want %q", cfg.JWTSecret, "test-secret")
		}
	})

	t.Run("sets defaults for optional fields", func(t *testing.T) {
		os.Clearenv()
		os.Setenv("DATABASE_URL", "postgres://localhost/test")
		os.Setenv("JWT_SECRET", "test-secret")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Port != "8080" {
			t.Errorf("Port default = %q, want %q", cfg.Port, "8080")
		}
		if cfg.TypesenseHost != "localhost" {
			t.Errorf("TypesenseHost default = %q, want %q", cfg.TypesenseHost, "localhost")
		}
		if cfg.TypesensePort != "8108" {
			t.Errorf("TypesensePort default = %q, want %q", cfg.TypesensePort, "8108")
		}
		if cfg.MinioEndpoint != "localhost:9000" {
			t.Errorf("MinioEndpoint default = %q, want %q", cfg.MinioEndpoint, "localhost:9000")
		}
		if cfg.LogLevel != "info" {
			t.Errorf("LogLevel default = %q, want %q", cfg.LogLevel, "info")
		}
	})

	t.Run("reads MINIO_USE_SSL as bool", func(t *testing.T) {
		os.Clearenv()
		os.Setenv("DATABASE_URL", "postgres://localhost/test")
		os.Setenv("JWT_SECRET", "test-secret")

		// Test false by default
		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.MinioUseSSL {
			t.Error("MinioUseSSL should default to false")
		}

		// Test true
		os.Setenv("MINIO_USE_SSL", "true")
		cfg, err = Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.MinioUseSSL {
			t.Error("MinioUseSSL should be true when set to 'true'")
		}
	})

	t.Run("reads all optional fields when set", func(t *testing.T) {
		os.Clearenv()
		os.Setenv("DATABASE_URL", "postgres://localhost/test")
		os.Setenv("JWT_SECRET", "test-secret")
		os.Setenv("PORT", "3000")
		os.Setenv("TYPESENSE_HOST", "typesense.local")
		os.Setenv("TYPESENSE_PORT", "9999")
		os.Setenv("TYPESENSE_API_KEY", "ts-key")
		os.Setenv("MINIO_ENDPOINT", "minio.local:9001")
		os.Setenv("MINIO_ACCESS_KEY", "minio-user")
		os.Setenv("MINIO_SECRET_KEY", "minio-pass")
		os.Setenv("MINIO_USE_SSL", "true")
		os.Setenv("ENCRYPTION_KEY", "enc-key")
		os.Setenv("LOG_LEVEL", "debug")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		checks := map[string]string{
			"Port":            cfg.Port,
			"TypesenseHost":   cfg.TypesenseHost,
			"TypesensePort":   cfg.TypesensePort,
			"TypesenseAPIKey": cfg.TypesenseAPIKey,
			"MinioEndpoint":   cfg.MinioEndpoint,
			"MinioAccessKey":  cfg.MinioAccessKey,
			"MinioSecretKey":  cfg.MinioSecretKey,
			"EncryptionKey":   cfg.EncryptionKey,
			"LogLevel":        cfg.LogLevel,
		}
		expected := map[string]string{
			"Port":            "3000",
			"TypesenseHost":   "typesense.local",
			"TypesensePort":   "9999",
			"TypesenseAPIKey": "ts-key",
			"MinioEndpoint":   "minio.local:9001",
			"MinioAccessKey":  "minio-user",
			"MinioSecretKey":  "minio-pass",
			"EncryptionKey":   "enc-key",
			"LogLevel":        "debug",
		}
		for field, got := range checks {
			if got != expected[field] {
				t.Errorf("%s = %q, want %q", field, got, expected[field])
			}
		}
		if !cfg.MinioUseSSL {
			t.Error("MinioUseSSL should be true")
		}
	})
}
