// Package metadata provides a lightweight HTTP server for health checks and
// the OAuth endpoints MCP clients discover: RFC 9728 protected resource
// metadata, RFC 8414 authorization server metadata, the /authorize shim and
// dynamic client registration. It runs on its own port (METADATA_PORT), and
// Envoy routes /.well-known/oauth-* and /authorize to it, while /mcp itself is
// served by the MCP server (internal/server, stateless Streamable HTTP).
//
// The protected resource metadata is also served by the MCP router; both use
// ProtectedResourceHandler so the two documents are always identical.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"google.golang.org/grpc"

	dexapi "github.com/dexidp/dex/api/v2"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"home-os/mcp/internal/config"
	"home-os/mcp/internal/ratelimit"
)

// DynamicClientIDPrefix starts every client_id the /register endpoint mints
// in Dex. Dex puts the client_id in the token aud, so the MCP verifier accepts
// audiences with this prefix: such clients exist only to call this server.
const DynamicClientIDPrefix = "mcp-"

// PreRegisteredClientID is the static Dex client MCP clients without dynamic
// registration use (opencode reads it from the client_id metadata field).
const PreRegisteredClientID = "home-os-mcp"

// ScopesSupported are the scopes MCP clients request from Dex. They are
// advertised in the protected resource metadata and the 401 challenge.
var ScopesSupported = []string{"openid", "email", "profile"}

// ResourceURL returns the server's canonical URI (MCP_RESOURCE_URL), falling
// back to <PUBLIC_BASE_URL>/mcp for configs built without config.Load.
func ResourceURL(cfg *config.Config) string {
	if cfg.ResourceURL != "" {
		return cfg.ResourceURL
	}
	return strings.TrimRight(cfg.PublicBaseURL, "/") + "/mcp"
}

// Issuer returns the OIDC issuer tokens must carry (DEX_ISSUER), which is
// also the authorization server identifier advertised to clients. It falls
// back to <PUBLIC_BASE_URL>/dex for configs built without config.Load.
func Issuer(cfg *config.Config) string {
	if cfg.DexIssuer != "" {
		return cfg.DexIssuer
	}
	return strings.TrimRight(cfg.PublicBaseURL, "/") + "/dex"
}

// ProtectedResourceMetadata is the RFC 9728 document: mcp-go's
// ProtectedResourceMetadataConfig plus client_id, which opencode needs to find
// the pre-registered client (the mcp-go type has no field for it).
type ProtectedResourceMetadata struct {
	mcpserver.ProtectedResourceMetadataConfig
	ClientID string `json:"client_id,omitempty"`
}

// NewProtectedResourceMetadata builds the document from the config: the
// configured resource URI, and the configured issuer as the only
// authorization server.
func NewProtectedResourceMetadata(cfg *config.Config) ProtectedResourceMetadata {
	return ProtectedResourceMetadata{
		ProtectedResourceMetadataConfig: mcpserver.ProtectedResourceMetadataConfig{
			Resource:               ResourceURL(cfg),
			AuthorizationServers:   []string{Issuer(cfg)},
			ScopesSupported:        ScopesSupported,
			BearerMethodsSupported: []string{"header"},
			ResourceName:           "Home OS MCP",
		},
		ClientID: PreRegisteredClientID,
	}
}

// ProtectedResourcePaths returns the paths the metadata is served at: the
// bare well-known path and the RFC 9728 path-qualified one for the resource
// (/.well-known/oauth-protected-resource/mcp for .../mcp), via mcp-go's
// ProtectedResourceMetadataPath.
func ProtectedResourcePaths(cfg *config.Config) []string {
	paths := []string{mcpserver.WellKnownProtectedResourcePath}
	if p := mcpserver.ProtectedResourceMetadataPath(ResourceURL(cfg)); p != paths[0] {
		paths = append(paths, p)
	}
	return paths
}

// ResourceMetadataURL is the absolute metadata URL for the resource
// (RFC 9728 section 3.1: the well-known segment goes between host and path).
// It is the resource_metadata value of the 401 WWW-Authenticate challenge.
func ResourceMetadataURL(cfg *config.Config) string {
	resource := ResourceURL(cfg)
	u, err := url.Parse(resource)
	if err != nil || u.Host == "" {
		return strings.TrimRight(cfg.PublicBaseURL, "/") + mcpserver.WellKnownProtectedResourcePath
	}
	return u.Scheme + "://" + u.Host + mcpserver.ProtectedResourceMetadataPath(resource)
}

// ProtectedResourceHandler serves the RFC 9728 document with the same
// semantics as mcp-go's NewProtectedResourceMetadataHandler: GET/HEAD return
// the JSON, OPTIONS is a CORS preflight, anything else is 405; permissive CORS
// and Cache-Control: no-store.
func ProtectedResourceHandler(cfg *config.Config) http.Handler {
	body, err := json.Marshal(NewProtectedResourceMetadata(cfg))
	if err != nil {
		panic("metadata: marshal protected resource metadata: " + err.Error()) // only plain types
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		switch r.Method {
		case http.MethodOptions:
			w.WriteHeader(http.StatusNoContent)
			return
		case http.MethodGet, http.MethodHead:
		default:
			w.Header().Set("Allow", "GET, HEAD, OPTIONS")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	})
}

// authorizationServerMetadataPaths returns where the RFC 8414 document is
// served: the bare path (older clients) and the path-inserted form for the
// issuer (/.well-known/oauth-authorization-server/dex for .../dex), which
// clients derive from the authorization_servers entry.
func authorizationServerMetadataPaths(cfg *config.Config) []string {
	const base = "/.well-known/oauth-authorization-server"
	paths := []string{base}
	if u, err := url.Parse(Issuer(cfg)); err == nil {
		if p := strings.Trim(u.Path, "/"); p != "" {
			paths = append(paths, path.Join(base, p))
		}
	}
	return paths
}

// Start creates and starts an HTTP server on the given address that serves
// NewHandler. The server runs in a background goroutine. Callers should call
// Shutdown on the returned *http.Server during graceful shutdown.
func Start(addr string, cfg *config.Config) *http.Server {
	srv := &http.Server{Addr: addr, Handler: NewHandler(cfg)}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("metadata server error", "error", err)
		}
	}()
	return srv
}

// NewHandler returns the metadata server's routes: health, the /authorize
// shim, dynamic client registration, and the authorization server and
// protected resource metadata documents.
func NewHandler(cfg *config.Config) http.Handler {
	mux := http.NewServeMux()

	// Health check endpoint — used by Kubernetes liveness/readiness probes.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "home-os-mcp"})
	})

	// OAuth2 authorize endpoint — injects openid scope for Dex compatibility.
	// MCP clients (opencode, Claude Code) often omit the openid scope per RFC 8707,
	// but Dex requires it as an OIDC provider. This handler injects it before
	// proxying to Dex's real auth endpoint.
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		scope := q.Get("scope")
		if !strings.Contains(scope, "openid") {
			if scope != "" {
				q.Set("scope", scope+" openid")
			} else {
				q.Set("scope", "openid")
			}
		}
		r.URL.RawQuery = q.Encode()

		// Redirect to Dex's auth endpoint through Envoy
		r.URL.Path = "/dex/auth"
		http.Redirect(w, r, r.URL.String(), http.StatusFound)
	})

	// Dynamic Client Registration (RFC 7591).
	mux.Handle("/register", newRegisterHandler(cfg))

	// OAuth 2.0 Authorization Server Metadata (RFC 8414). Tells OAuth clients
	// (opencode) where the authorization server's endpoints are. The issuer
	// is the configured DEX_ISSUER (the token iss and the authorization_servers
	// entry of the protected resource metadata), and the document is also
	// served at the issuer's path-inserted well-known URL so clients that
	// follow RFC 8414 section 3.1 find it.
	asMetadata := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"issuer":                                Issuer(cfg),
			"authorization_endpoint":                cfg.PublicBaseURL + "/oauth/authorize",
			"token_endpoint":                        cfg.PublicBaseURL + "/dex/token",
			"jwks_uri":                              cfg.PublicBaseURL + "/dex/keys",
			"registration_endpoint":                 cfg.PublicBaseURL + "/oauth/register",
			"scopes_supported":                      ScopesSupported,
			"response_types_supported":              []string{"code"},
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		})
	}
	for _, p := range authorizationServerMetadataPaths(cfg) {
		mux.HandleFunc(p, asMetadata)
	}

	// OAuth 2.0 Protected Resource Metadata (RFC 9728): the configured
	// resource and issuer, at the bare and the /mcp path-qualified well-known
	// paths. Identical to the document the MCP router serves.
	prm := ProtectedResourceHandler(cfg)
	for _, p := range ProtectedResourcePaths(cfg) {
		mux.Handle(p, prm)
	}

	return mux
}

// Bounds on a registration request (#4691).
const (
	// MaxRegisterBodyBytes caps the /register request body.
	MaxRegisterBodyBytes = 16 << 10
	// MaxClientNameLen caps client_name, in characters.
	MaxClientNameLen = 100
	// dexCallTimeout bounds the Dex CreateClient call.
	dexCallTimeout = 10 * time.Second
)

// dexClientCreator is the part of the Dex API /register uses.
type dexClientCreator interface {
	CreateClient(ctx context.Context, in *dexapi.CreateClientReq, opts ...grpc.CallOption) (*dexapi.CreateClientResp, error)
}

// dialDex builds the Dex API client over mTLS. grpc.NewClient does not
// connect; the connection is made on first use and re-established by gRPC as
// needed. It is a variable so tests can substitute a fake and count calls.
//
// Dex's gRPC API has no authentication of its own — CreateClient here, and
// CreatePassword from the API, are trusted by transport alone — so this
// connection presents a client certificate and verifies Dex against the shared
// private CA (DEX_GRPC_TLS_*, #697). It never falls back to plaintext: with no
// material and no explicit DEX_GRPC_INSECURE the registration endpoint answers
// 500 rather than opening an unauthenticated admin channel.
var dialDex = func(addr string) (dexClientCreator, error) {
	creds, err := DexTLSConfigFromEnv().TransportCredentials()
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, err
	}
	return dexapi.NewDexClient(conn), nil
}

// newRegisterHandler serves dynamic client registration (RFC 7591). MCP
// clients (opencode, Claude Code) POST their redirect URIs and client
// metadata, and get back a client_id for a new public client in Dex.
//
// It is public and unauthenticated, so it is bounded before anything
// reaches Dex: a per-client-IP and a global rate limit (ratelimit.go), a
// 16 KiB body cap, a client_name cap, and the redirect URI policy
// (ValidateRedirectURIs, which also caps the URI count).
//
// The Dex gRPC client is built once here and shared by every request; the
// handler (and so the connection) lives for the life of the process.
//
// Client ids are "mcp-" plus a UUIDv7, whose first 48 bits are the creation
// time in Unix milliseconds. Dex's Client has no metadata field to carry a
// timestamp, so a later janitor that deletes unused DCR clients can read
// the creation time from the id instead.
func newRegisterHandler(cfg *config.Config) http.Handler {
	limiter := newRegisterLimiter(cfg)
	dex, dexErr := dialDex(cfg.DexGRPCAddr)
	if dexErr != nil {
		slog.Error("dcr: cannot create Dex gRPC client", "addr", cfg.DexGRPCAddr, "error", dexErr)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonError(w, "method_not_allowed", "POST required", http.StatusMethodNotAllowed)
			return
		}

		ip := clientIP(r, limiter.trusted)
		if ok, wait := limiter.allowIP(ip); !ok {
			slog.Info("dcr: rate limited", "scope", "ip", "remote_ip", ip)
			tooManyRequests(w, wait)
			return
		}

		var req struct {
			RedirectURIs            []string `json:"redirect_uris"`
			ClientName              string   `json:"client_name"`
			TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
			GrantTypes              []string `json:"grant_types"`
			ResponseTypes           []string `json:"response_types"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxRegisterBodyBytes)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				jsonError(w, "invalid_request", fmt.Sprintf("request body exceeds %d bytes", MaxRegisterBodyBytes), http.StatusRequestEntityTooLarge)
				return
			}
			jsonError(w, "invalid_request", "request body is not valid JSON", http.StatusBadRequest)
			return
		}

		if utf8.RuneCountInString(req.ClientName) > MaxClientNameLen {
			jsonError(w, "invalid_client_metadata", fmt.Sprintf("client_name must be at most %d characters", MaxClientNameLen), http.StatusBadRequest)
			return
		}

		// Validate redirect URIs before anything reaches Dex: loopback HTTP or
		// a reverse-DNS private-use scheme only (see ValidateRedirectURIs).
		if err := ValidateRedirectURIs(req.RedirectURIs); err != nil {
			jsonError(w, "invalid_redirect_uri", err.Error(), http.StatusBadRequest)
			return
		}

		// The global ceiling is spent only by registrations that would
		// otherwise create a Dex client.
		if ok, wait := limiter.allowGlobal(); !ok {
			slog.Warn("dcr: global registration ceiling reached", "remote_ip", ip)
			tooManyRequests(w, wait)
			return
		}

		if dexErr != nil {
			jsonError(w, "server_error", "cannot connect to auth server", http.StatusInternalServerError)
			return
		}

		id, err := uuid.NewV7()
		if err != nil {
			jsonError(w, "server_error", "cannot generate client id", http.StatusInternalServerError)
			return
		}
		clientID := DynamicClientIDPrefix + id.String()

		ctx, cancel := context.WithTimeout(r.Context(), dexCallTimeout)
		defer cancel()
		_, err = dex.CreateClient(ctx, &dexapi.CreateClientReq{
			Client: &dexapi.Client{
				Id:           clientID,
				Public:       true,
				RedirectUris: req.RedirectURIs,
				Name:         req.ClientName,
			},
		})
		if err != nil {
			slog.Error("dcr: Dex CreateClient failed", "remote_ip", ip, "error", err)
			jsonError(w, "server_error", "client registration failed", http.StatusInternalServerError)
			return
		}

		slog.Info("dcr: client registered", "client_id", clientID, "client_name", req.ClientName, "remote_ip", ip)

		// RFC 7591 response.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"client_id":                  clientID,
			"client_id_issued_at":        time.Now().Unix(),
			"redirect_uris":              req.RedirectURIs,
			"token_endpoint_auth_method": "none",
			"grant_types":                []string{"authorization_code", "refresh_token"},
			"response_types":             []string{"code"},
			"client_name":                req.ClientName,
		})
	})
}

// tooManyRequests answers 429 with Retry-After and an OAuth-style JSON error.
func tooManyRequests(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetryAfterSeconds(wait)))
	jsonError(w, "temporarily_unavailable", "too many registration requests; retry later", http.StatusTooManyRequests)
}

// MaxRedirectURIs caps how many redirect URIs one registration may carry.
const MaxRedirectURIs = 10

// forbiddenSchemes are never accepted as private-use redirect schemes, even
// if they somehow pass the reverse-DNS check.
var forbiddenSchemes = map[string]bool{
	"http": true, "https": true, "javascript": true, "data": true,
	"file": true, "vbscript": true,
}

// ValidateRedirectURIs enforces the dynamic client registration redirect
// policy. Dex skips its approval screen and every registered client is a
// public "mcp-" client the MCP verifier accepts, so an authorization code
// must only ever be delivered to the user's own device. Allowed:
//
//   - http with a host of exactly 127.0.0.1, ::1 or localhost, any port and
//     path (RFC 8252 section 7.3 loopback redirects), and
//   - a private-use URI scheme in reverse-DNS form, i.e. one containing a
//     "." such as com.example.app:/cb (RFC 8252 section 7.1). Only the app
//     that claimed the scheme on the device can receive it.
//
// Everything else is rejected: https and any other http host (a code sent
// to a web server is a code an attacker can collect), look-alike hosts such
// as localhost.evil.example, userinfo, fragments, relative URIs, dangerous
// schemes, and lists that are empty or longer than MaxRedirectURIs.
func ValidateRedirectURIs(uris []string) error {
	if len(uris) == 0 {
		return errors.New("at least one redirect_uri is required")
	}
	if len(uris) > MaxRedirectURIs {
		return fmt.Errorf("at most %d redirect_uris are allowed", MaxRedirectURIs)
	}
	for _, raw := range uris {
		if err := validateRedirectURI(raw); err != nil {
			return fmt.Errorf("redirect_uri %q: %w", raw, err)
		}
	}
	return nil
}

func validateRedirectURI(raw string) error {
	if raw == "" {
		return errors.New("must not be empty")
	}
	// A "#" anywhere means a fragment (even an empty one, which url.Parse
	// does not report); RFC 6749 section 3.1.2 forbids them.
	if strings.Contains(raw, "#") {
		return errors.New("must not contain a fragment")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("is not a valid URI")
	}
	if u.Scheme == "" {
		return errors.New("must be an absolute URI")
	}
	if u.User != nil {
		return errors.New("must not contain userinfo")
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case scheme == "http":
		if u.Opaque != "" || !isLoopbackHost(u.Hostname()) {
			return errors.New("http is only allowed for 127.0.0.1, [::1] or localhost")
		}
		return nil
	case forbiddenSchemes[scheme]:
		return fmt.Errorf("scheme %q is not allowed", scheme)
	case !strings.Contains(scheme, "."):
		return errors.New("custom schemes must be reverse-DNS private-use schemes (e.g. com.example.app)")
	}
	return nil
}

// isLoopbackHost reports whether host (url.URL.Hostname, so brackets are
// already stripped) is exactly a loopback name, never a prefix match.
func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "::1" || strings.EqualFold(host, "localhost")
}

// jsonError writes a JSON error response with the given OAuth error code,
// description, and HTTP status code.
func jsonError(w http.ResponseWriter, code, description string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error": code, "error_description": description,
	})
}
