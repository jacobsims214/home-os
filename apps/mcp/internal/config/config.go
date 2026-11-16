// Package config loads application configuration from environment variables.
// All config values are read once at startup and passed to components.
// Missing or invalid variables cause startup to fail with a clear error message.
//
// Environment variables:
//
//	DATABASE_URL            (required) Postgres connection string.
//	PORT                    MCP listen port. Default 8082.
//	METADATA_PORT           OAuth metadata listen port. Default 8083.
//	PUBLIC_BASE_URL         Public origin of Home OS as browsers and OAuth clients
//	                        see it (scheme://host[:port], no path). Default
//	                        http://localhost:8000. A trailing slash is trimmed.
//	MCP_RESOURCE_URL        Canonical URI of this MCP server (RFC 8707 resource,
//	                        RFC 9728 "resource"). Default <PUBLIC_BASE_URL>/mcp.
//	MCP_ACCEPTED_AUDIENCES  Comma-separated token audiences the server accepts.
//	                        Dex sets aud to the OAuth client_id, so the default is
//	                        home-os-ui,home-os-mcp.
//	DEX_ISSUER              OIDC issuer; must equal the token iss claim. Default
//	                        <PUBLIC_BASE_URL>/dex.
//	DEX_JWKS_URL            JWKS endpoint used to verify token signatures. Default
//	                        the in-cluster Envoy /dex/keys route.
//	MCP_ALLOWED_ORIGINS     Comma-separated browser Origins allowed to call /mcp
//	                        (scheme://host[:port]). Default the origin of
//	                        PUBLIC_BASE_URL. Requests without Origin and loopback
//	                        origins are handled by the transport middleware.
//	DEX_GRPC_ADDR           Dex gRPC API address. Default home-os-dex:5557.
//	MCP_TRUSTED_PROXIES     Comma-separated CIDRs or IPs of the reverse proxies in
//	                        front of the metadata server (Traefik, Envoy). Their
//	                        X-Forwarded-For entries are believed when keying the
//	                        /register rate limit. Default 10.244.0.0/16 (the Talos
//	                        pod CIDR); set it to an empty value to trust none and
//	                        key on the TCP peer only.
//	MCP_REGISTER_PER_MINUTE Dynamic client registrations per client IP per minute
//	                        (burst the same). Default 5.
//	MCP_REGISTER_PER_HOUR   Dynamic client registrations per hour across all
//	                        clients, per pod (burst the same). Default 60.
//	LOG_LEVEL               Default info.
//	TYPESENSE_HOST, TYPESENSE_PORT, TYPESENSE_API_KEY  Optional search backend.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// DefaultAcceptedAudiences are the Dex client IDs whose tokens the MCP accepts.
// Dex puts the client_id in the aud claim.
var DefaultAcceptedAudiences = []string{"home-os-ui", "home-os-mcp"}

// Defaults for the dynamic client registration (/register) limits.
const (
	// DefaultTrustedProxies is the Talos pod CIDR, which covers Traefik and
	// Envoy, the proxies between the internet and the metadata server.
	DefaultTrustedProxies    = "10.244.0.0/16"
	DefaultRegisterPerMinute = 5
	DefaultRegisterPerHour   = 60
)

// Config holds all environment-driven configuration for the MCP server.
type Config struct {
	Port         string
	MetadataPort string
	// PublicBaseURL is the public origin (scheme://host[:port]) without a
	// trailing slash.
	PublicBaseURL string
	// ResourceURL is this server's canonical URI, advertised as the RFC 9728
	// resource and matched against the RFC 8707 resource parameter.
	ResourceURL string
	// AcceptedAudiences lists the aud values a token may carry.
	AcceptedAudiences []string
	// AllowedOrigins lists the browser origins (scheme://host[:port],
	// lowercase) allowed to call /mcp.
	AllowedOrigins  []string
	DatabaseURL     string
	DexJWKSURL      string
	DexIssuer       string
	DexGRPCAddr     string
	LogLevel        string
	TypesenseHost   string
	TypesensePort   string
	TypesenseAPIKey string
	// AgentCredentialsKey is the base64 32-byte master key that opens
	// credentials shared with agents (AGENT_CREDENTIALS_KEY, from a
	// Kubernetes Secret, #4446). Empty disables credential_reveal. Never
	// logged.
	AgentCredentialsKey string
	// TrustedProxies are the networks whose X-Forwarded-For entries are
	// believed when finding the client IP for the /register rate limit
	// (MCP_TRUSTED_PROXIES). Empty means the TCP peer is the client.
	TrustedProxies []netip.Prefix
	// RegisterPerMinute is the per-client-IP /register budget
	// (MCP_REGISTER_PER_MINUTE). Zero means DefaultRegisterPerMinute.
	RegisterPerMinute int
	// RegisterPerHour is the global /register ceiling per pod
	// (MCP_REGISTER_PER_HOUR). Zero means DefaultRegisterPerHour.
	RegisterPerHour int
}

// Load reads configuration from environment variables and returns a validated Config.
// Every problem found is reported in one error so a misconfigured deployment
// can be fixed in a single pass.
func Load() (*Config, error) {
	publicBaseURL := strings.TrimRight(getEnv("PUBLIC_BASE_URL", "http://localhost:8000"), "/")

	cfg := &Config{
		Port:            getEnv("PORT", "8082"),
		MetadataPort:    getEnv("METADATA_PORT", "8083"),
		PublicBaseURL:   publicBaseURL,
		ResourceURL:     getEnv("MCP_RESOURCE_URL", publicBaseURL+"/mcp"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		DexJWKSURL:      getEnv("DEX_JWKS_URL", "http://home-os-envoy.home-os.svc.cluster.local:8000/dex/keys"),
		DexIssuer:       getEnv("DEX_ISSUER", publicBaseURL+"/dex"),
		DexGRPCAddr:     getEnv("DEX_GRPC_ADDR", "home-os-dex:5557"),
		LogLevel:        getEnv("LOG_LEVEL", "info"),
		TypesenseHost:   getEnv("TYPESENSE_HOST", ""),
		TypesensePort:   getEnv("TYPESENSE_PORT", ""),
		TypesenseAPIKey: os.Getenv("TYPESENSE_API_KEY"),

		AgentCredentialsKey: os.Getenv("AGENT_CREDENTIALS_KEY"),
	}

	var missing []string
	if cfg.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}

	var errs []error
	if len(missing) > 0 {
		errs = append(errs, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", ")))
	}

	if err := validateOrigin("PUBLIC_BASE_URL", cfg.PublicBaseURL); err != nil {
		errs = append(errs, err)
	}
	if err := validateHTTPURL("MCP_RESOURCE_URL", cfg.ResourceURL); err != nil {
		errs = append(errs, err)
	} else if u, _ := url.Parse(cfg.ResourceURL); u.Fragment != "" {
		errs = append(errs, fmt.Errorf("MCP_RESOURCE_URL %q must not contain a fragment", cfg.ResourceURL))
	}
	if err := validateHTTPURL("DEX_ISSUER", cfg.DexIssuer); err != nil {
		errs = append(errs, err)
	}
	if err := validateHTTPURL("DEX_JWKS_URL", cfg.DexJWKSURL); err != nil {
		errs = append(errs, err)
	}

	if v, ok := os.LookupEnv("MCP_ACCEPTED_AUDIENCES"); ok && strings.TrimSpace(v) != "" {
		cfg.AcceptedAudiences = splitList(v)
		if len(cfg.AcceptedAudiences) == 0 {
			errs = append(errs, fmt.Errorf("MCP_ACCEPTED_AUDIENCES %q has no audiences", v))
		}
	} else {
		cfg.AcceptedAudiences = append([]string(nil), DefaultAcceptedAudiences...)
	}

	if v, ok := os.LookupEnv("MCP_ALLOWED_ORIGINS"); ok && strings.TrimSpace(v) != "" {
		origins := splitList(v)
		if len(origins) == 0 {
			errs = append(errs, fmt.Errorf("MCP_ALLOWED_ORIGINS %q has no origins", v))
		}
		for _, o := range origins {
			o = strings.TrimRight(o, "/")
			if err := validateOrigin("MCP_ALLOWED_ORIGINS", o); err != nil {
				errs = append(errs, err)
				continue
			}
			cfg.AllowedOrigins = append(cfg.AllowedOrigins, strings.ToLower(o))
		}
	} else if validateOrigin("PUBLIC_BASE_URL", cfg.PublicBaseURL) == nil {
		cfg.AllowedOrigins = []string{strings.ToLower(cfg.PublicBaseURL)}
	}

	// An explicitly empty MCP_TRUSTED_PROXIES means "trust no proxy", so
	// LookupEnv rather than getEnv.
	proxies, ok := os.LookupEnv("MCP_TRUSTED_PROXIES")
	if !ok {
		proxies = DefaultTrustedProxies
	}
	if trusted, err := ParseTrustedProxies(proxies); err != nil {
		errs = append(errs, fmt.Errorf("MCP_TRUSTED_PROXIES %q: %w", proxies, err))
	} else {
		cfg.TrustedProxies = trusted
	}

	var err error
	if cfg.RegisterPerMinute, err = positiveIntEnv("MCP_REGISTER_PER_MINUTE", DefaultRegisterPerMinute); err != nil {
		errs = append(errs, err)
	}
	if cfg.RegisterPerHour, err = positiveIntEnv("MCP_REGISTER_PER_HOUR", DefaultRegisterPerHour); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// validateHTTPURL checks that v is an absolute http or https URL with a host.
func validateHTTPURL(name, v string) error {
	u, err := url.Parse(v)
	if err != nil {
		return fmt.Errorf("%s %q is not a valid URL: %v", name, v, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s %q must be an absolute http or https URL", name, v)
	}
	if u.Host == "" {
		return fmt.Errorf("%s %q has no host", name, v)
	}
	return nil
}

// validateOrigin checks that v is an origin: an http(s) URL with a host and
// no path, query or fragment.
func validateOrigin(name, v string) error {
	if err := validateHTTPURL(name, v); err != nil {
		return err
	}
	u, _ := url.Parse(v)
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("%s %q must be an origin (scheme://host[:port]) with no path, query or credentials", name, v)
	}
	return nil
}

// splitList splits a comma-separated value, trimming spaces and dropping
// empty entries and duplicates.
func splitList(v string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// ParseTrustedProxies parses a comma-separated list of CIDRs or bare IPs (a
// bare IP is a /32 or /128). Empty input gives an empty list. Same format as
// the calendar's CALDAV_TRUSTED_PROXIES.
func ParseTrustedProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			p, err := netip.ParsePrefix(part)
			if err != nil {
				return nil, err
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, err
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// positiveIntEnv reads a positive integer from key, or returns def when the
// variable is unset or empty.
func positiveIntEnv(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s %q must be a positive integer", key, v)
	}
	return n, nil
}

// getEnv returns the value of the environment variable if set, otherwise returns fallback.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
