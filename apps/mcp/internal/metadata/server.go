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
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	dexapi "github.com/dexidp/dex/api"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"home-os/mcp/internal/config"
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
	// MCP clients (opencode, Claude Code) POST their redirect URIs and client
	// metadata here. The handler creates a client in Dex via gRPC and returns
	// a client_id that the client uses for the OAuth authorization code flow.
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			jsonError(w, "method_not_allowed", "POST required", http.StatusMethodNotAllowed)
			return
		}

		// Parse registration request
		var req struct {
			RedirectURIs            []string `json:"redirect_uris"`
			ClientName              string   `json:"client_name"`
			TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
			GrantTypes              []string `json:"grant_types"`
			ResponseTypes           []string `json:"response_types"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid_request", err.Error(), http.StatusBadRequest)
			return
		}

		// Validate redirect URIs (loopback or custom scheme only)
		for _, uri := range req.RedirectURIs {
			lower := strings.ToLower(uri)
			if !strings.HasPrefix(lower, "http://127.0.0.1") &&
				!strings.HasPrefix(lower, "http://localhost") &&
				!strings.HasPrefix(lower, "http://[::1]") &&
				!strings.Contains(uri, "://") {
				jsonError(w, "invalid_redirect_uri", "only loopback HTTP and custom schemes allowed", http.StatusBadRequest)
				return
			}
		}

		// Connect to Dex gRPC and create client
		conn, err := grpc.NewClient(cfg.DexGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			jsonError(w, "server_error", "cannot connect to auth server", http.StatusInternalServerError)
			return
		}
		defer conn.Close()

		dexClient := dexapi.NewDexClient(conn)
		clientID := DynamicClientIDPrefix + uuid.New().String()

		_, err = dexClient.CreateClient(r.Context(), &dexapi.CreateClientReq{
			Client: &dexapi.Client{
				Id:           clientID,
				Public:       true,
				RedirectUris: req.RedirectURIs,
				Name:         req.ClientName,
			},
		})
		if err != nil {
			jsonError(w, "server_error", "client registration failed", http.StatusInternalServerError)
			return
		}

		// Return RFC 7591 response
		w.Header().Set("Content-Type", "application/json")
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
			"authorization_endpoint":                cfg.PublicBaseURL + "/authorize",
			"token_endpoint":                        cfg.PublicBaseURL + "/dex/token",
			"jwks_uri":                              cfg.PublicBaseURL + "/dex/keys",
			"registration_endpoint":                 cfg.PublicBaseURL + "/register",
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

// jsonError writes a JSON error response with the given OAuth error code,
// description, and HTTP status code.
func jsonError(w http.ResponseWriter, code, description string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error": code, "error_description": description,
	})
}
