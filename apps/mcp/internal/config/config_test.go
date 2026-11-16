package config

import (
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"
)

func equal(t *testing.T, want, got any, msg ...string) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Errorf("%v: want %#v, got %#v", msg, want, got)
	}
}

func mustLoad(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	return cfg
}

var configEnvVars = []string{
	"DATABASE_URL", "PORT", "METADATA_PORT", "PUBLIC_BASE_URL", "MCP_RESOURCE_URL",
	"MCP_ACCEPTED_AUDIENCES", "DEX_ISSUER", "DEX_JWKS_URL", "MCP_ALLOWED_ORIGINS",
	"DEX_GRPC_ADDR", "LOG_LEVEL", "TYPESENSE_HOST", "TYPESENSE_PORT", "TYPESENSE_API_KEY",
	"MCP_REGISTER_PER_MINUTE", "MCP_REGISTER_PER_HOUR",
}

// unsetEnvVars are cleared by unsetting rather than blanking, because an
// empty value means something different from an unset one.
var unsetEnvVars = []string{"MCP_TRUSTED_PROXIES"}

// setEnv clears every config variable for the test, then applies env.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range configEnvVars {
		t.Setenv(k, "")
	}
	for _, k := range unsetEnvVars {
		t.Setenv(k, "") // registers the restore at cleanup
		os.Unsetenv(k)
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func TestLoadDefaults(t *testing.T) {
	setEnv(t, map[string]string{"DATABASE_URL": "postgres://x"})

	cfg := mustLoad(t)

	equal(t, "8082", cfg.Port)
	equal(t, "8083", cfg.MetadataPort)
	equal(t, "http://localhost:8000", cfg.PublicBaseURL)
	equal(t, "http://localhost:8000/mcp", cfg.ResourceURL)
	equal(t, []string{"home-os-mcp"}, cfg.AcceptedAudiences)
	equal(t, "http://localhost:8000/dex", cfg.DexIssuer)
	equal(t, "http://home-os-envoy.home-os.svc.cluster.local:8000/dex/keys", cfg.DexJWKSURL)
	equal(t, []string{"http://localhost:8000"}, cfg.AllowedOrigins)
	equal(t, "home-os-dex:5557", cfg.DexGRPCAddr)
	equal(t, "info", cfg.LogLevel)
	equal(t, []netip.Prefix{netip.MustParsePrefix("10.244.0.0/16")}, cfg.TrustedProxies)
	equal(t, DefaultRegisterPerMinute, cfg.RegisterPerMinute)
	equal(t, DefaultRegisterPerHour, cfg.RegisterPerHour)
}

func TestLoadDerivesFromPublicBaseURL(t *testing.T) {
	setEnv(t, map[string]string{
		"DATABASE_URL":    "postgres://x",
		"PUBLIC_BASE_URL": "https://Home.Example.com/",
	})

	cfg := mustLoad(t)

	equal(t, "https://Home.Example.com", cfg.PublicBaseURL, "trailing slash trimmed")
	equal(t, "https://Home.Example.com/mcp", cfg.ResourceURL)
	equal(t, "https://Home.Example.com/dex", cfg.DexIssuer)
	equal(t, []string{"https://home.example.com"}, cfg.AllowedOrigins)
}

func TestLoadExplicitValues(t *testing.T) {
	setEnv(t, map[string]string{
		"DATABASE_URL":           "postgres://x",
		"PUBLIC_BASE_URL":        "https://home.example.com",
		"MCP_RESOURCE_URL":       "https://mcp.example.com/mcp",
		"MCP_ACCEPTED_AUDIENCES": " home-os-mcp , https://mcp.example.com/mcp,,home-os-mcp ",
		"DEX_ISSUER":             "https://auth.example.com/dex",
		"DEX_JWKS_URL":           "http://dex.internal:5556/dex/keys",
		"MCP_ALLOWED_ORIGINS":    "https://home.example.com, https://App.Example.com/",
	})

	cfg := mustLoad(t)

	equal(t, "https://mcp.example.com/mcp", cfg.ResourceURL)
	equal(t, []string{"home-os-mcp", "https://mcp.example.com/mcp"}, cfg.AcceptedAudiences)
	equal(t, "https://auth.example.com/dex", cfg.DexIssuer)
	equal(t, "http://dex.internal:5556/dex/keys", cfg.DexJWKSURL)
	equal(t, []string{"https://home.example.com", "https://app.example.com"}, cfg.AllowedOrigins)
}

func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		omitDB  bool // leave DATABASE_URL unset
		wantErr []string
	}{
		{
			name:    "missing DATABASE_URL",
			env:     map[string]string{},
			omitDB:  true,
			wantErr: []string{"missing required environment variables: DATABASE_URL"},
		},
		{
			name:    "PUBLIC_BASE_URL without scheme",
			env:     map[string]string{"PUBLIC_BASE_URL": "home.example.com"},
			wantErr: []string{"PUBLIC_BASE_URL", "absolute http or https URL"},
		},
		{
			name:    "PUBLIC_BASE_URL with path",
			env:     map[string]string{"PUBLIC_BASE_URL": "https://home.example.com/app"},
			wantErr: []string{"PUBLIC_BASE_URL", "must be an origin"},
		},
		{
			name:    "MCP_RESOURCE_URL relative",
			env:     map[string]string{"MCP_RESOURCE_URL": "/mcp"},
			wantErr: []string{"MCP_RESOURCE_URL", "absolute http or https URL"},
		},
		{
			name:    "MCP_RESOURCE_URL with fragment",
			env:     map[string]string{"MCP_RESOURCE_URL": "https://home.example.com/mcp#x"},
			wantErr: []string{"MCP_RESOURCE_URL", "fragment"},
		},
		{
			name:    "DEX_ISSUER not a URL",
			env:     map[string]string{"DEX_ISSUER": "dex"},
			wantErr: []string{"DEX_ISSUER"},
		},
		{
			name:    "DEX_JWKS_URL wrong scheme",
			env:     map[string]string{"DEX_JWKS_URL": "ftp://dex/keys"},
			wantErr: []string{"DEX_JWKS_URL", "absolute http or https URL"},
		},
		{
			name:    "MCP_ACCEPTED_AUDIENCES only separators",
			env:     map[string]string{"MCP_ACCEPTED_AUDIENCES": " , ,"},
			wantErr: []string{"MCP_ACCEPTED_AUDIENCES", "no audiences"},
		},
		{
			name:    "MCP_ALLOWED_ORIGINS only separators",
			env:     map[string]string{"MCP_ALLOWED_ORIGINS": ","},
			wantErr: []string{"MCP_ALLOWED_ORIGINS", "no origins"},
		},
		{
			name:    "MCP_ALLOWED_ORIGINS entry with path",
			env:     map[string]string{"MCP_ALLOWED_ORIGINS": "https://ok.example.com,https://bad.example.com/x"},
			wantErr: []string{"MCP_ALLOWED_ORIGINS", "bad.example.com/x", "must be an origin"},
		},
		{
			name:    "MCP_ALLOWED_ORIGINS null",
			env:     map[string]string{"MCP_ALLOWED_ORIGINS": "null"},
			wantErr: []string{"MCP_ALLOWED_ORIGINS"},
		},
		{
			name: "all errors reported together",
			env: map[string]string{
				"DEX_ISSUER":   "dex",
				"DEX_JWKS_URL": "keys",
			},
			omitDB:  true,
			wantErr: []string{"DATABASE_URL", "DEX_ISSUER", "DEX_JWKS_URL"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{}
			if !tt.omitDB {
				env["DATABASE_URL"] = "postgres://x"
			}
			for k, v := range tt.env {
				env[k] = v
			}
			setEnv(t, env)

			cfg, err := Load()
			if err == nil {
				t.Fatalf("Load: want error, got config %+v", cfg)
			}
			if cfg != nil {
				t.Errorf("Load: want nil config on error")
			}
			if !strings.HasPrefix(err.Error(), "config: ") {
				t.Errorf("error %q should be prefixed with \"config: \"", err)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q should contain %q", err, want)
				}
			}
		})
	}
}

func TestLoadBlankListsUseDefaults(t *testing.T) {
	setEnv(t, map[string]string{
		"DATABASE_URL":           "postgres://x",
		"MCP_ACCEPTED_AUDIENCES": "   ",
		"MCP_ALLOWED_ORIGINS":    "   ",
	})

	cfg := mustLoad(t)
	equal(t, DefaultAcceptedAudiences, cfg.AcceptedAudiences)
	equal(t, []string{"http://localhost:8000"}, cfg.AllowedOrigins)
}

func TestLoadRegisterRateLimit(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantTrusted []netip.Prefix
		wantMinute  int
		wantHour    int
		wantErr     string
	}{
		{
			name:        "explicit empty trusts no proxy",
			env:         map[string]string{"MCP_TRUSTED_PROXIES": ""},
			wantTrusted: nil, wantMinute: 5, wantHour: 60,
		},
		{
			name: "list of CIDRs and bare IPs",
			env: map[string]string{
				"MCP_TRUSTED_PROXIES":     " 10.244.0.0/16, 173.245.48.1/20 ,192.0.2.7, 2606:4700::/32",
				"MCP_REGISTER_PER_MINUTE": "3",
				"MCP_REGISTER_PER_HOUR":   "100",
			},
			wantTrusted: []netip.Prefix{
				netip.MustParsePrefix("10.244.0.0/16"),
				netip.MustParsePrefix("173.245.48.0/20"), // masked
				netip.MustParsePrefix("192.0.2.7/32"),
				netip.MustParsePrefix("2606:4700::/32"),
			},
			wantMinute: 3, wantHour: 100,
		},
		{name: "bad CIDR", env: map[string]string{"MCP_TRUSTED_PROXIES": "10.0.0.0/33"}, wantErr: "MCP_TRUSTED_PROXIES"},
		{name: "bad IP", env: map[string]string{"MCP_TRUSTED_PROXIES": "not-an-ip"}, wantErr: "MCP_TRUSTED_PROXIES"},
		{name: "zero per minute", env: map[string]string{"MCP_REGISTER_PER_MINUTE": "0"}, wantErr: "MCP_REGISTER_PER_MINUTE"},
		{name: "negative per hour", env: map[string]string{"MCP_REGISTER_PER_HOUR": "-1"}, wantErr: "MCP_REGISTER_PER_HOUR"},
		{name: "non-numeric per hour", env: map[string]string{"MCP_REGISTER_PER_HOUR": "lots"}, wantErr: "MCP_REGISTER_PER_HOUR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{"DATABASE_URL": "postgres://x"}
			for k, v := range tt.env {
				env[k] = v
			}
			setEnv(t, env)
			cfg, err := Load()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load error = %v, want one mentioning %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			equal(t, tt.wantTrusted, cfg.TrustedProxies, "TrustedProxies")
			equal(t, tt.wantMinute, cfg.RegisterPerMinute, "RegisterPerMinute")
			equal(t, tt.wantHour, cfg.RegisterPerHour, "RegisterPerHour")
		})
	}
}
