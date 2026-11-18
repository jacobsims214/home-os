package config

import (
	"net/netip"
	"reflect"
	"testing"

	"home-os/calendar/internal/auth"
)

func TestLoad_AuthRateLimitDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	for _, k := range []string{"CALDAV_TRUSTED_PROXIES", "CALDAV_AUTH_REQUESTS_PER_MINUTE", "CALDAV_AUTH_FAILURES_PER_MINUTE", "CALDAV_AUTH_ACCOUNT_FAILURES_PER_HOUR"} {
		t.Setenv(k, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.AuthRateLimit, auth.DefaultRateLimitConfig()) {
		t.Fatalf("AuthRateLimit = %+v, want defaults", cfg.AuthRateLimit)
	}
}

func TestLoad_AuthRateLimitFromEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("CALDAV_TRUSTED_PROXIES", "10.244.0.0/16, 10.0.0.5")
	t.Setenv("CALDAV_AUTH_REQUESTS_PER_MINUTE", "300")
	t.Setenv("CALDAV_AUTH_FAILURES_PER_MINUTE", "5")
	t.Setenv("CALDAV_AUTH_ACCOUNT_FAILURES_PER_HOUR", "12")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := auth.RateLimitConfig{
		TrustedProxies:         []netip.Prefix{netip.MustParsePrefix("10.244.0.0/16"), netip.MustParsePrefix("10.0.0.5/32")},
		RequestsPerMinute:      300,
		RequestBurst:           150,
		IPFailuresPerMinute:    5,
		IPFailureBurst:         5,
		AccountFailuresPerHour: 12,
		AccountFailureBurst:    12,
	}
	if !reflect.DeepEqual(cfg.AuthRateLimit, want) {
		t.Fatalf("AuthRateLimit = %+v, want %+v", cfg.AuthRateLimit, want)
	}
}

func TestLoad_AuthRateLimitInvalid(t *testing.T) {
	for k, v := range map[string]string{
		"CALDAV_TRUSTED_PROXIES":                "10.244.0.0/99",
		"CALDAV_AUTH_REQUESTS_PER_MINUTE":       "0",
		"CALDAV_AUTH_FAILURES_PER_MINUTE":       "-1",
		"CALDAV_AUTH_ACCOUNT_FAILURES_PER_HOUR": "lots",
	} {
		t.Run(k, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://x")
			t.Setenv(k, v)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%q accepted", k, v)
			}
		})
	}
}
