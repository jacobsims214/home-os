// Package metadata provides a lightweight HTTP server for health checks and
// OAuth 2.0 protected resource metadata. It runs on a separate port from the
// MCP SSE server to avoid route conflicts with the mark3labs/mcp-go SSE handler.
package metadata

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	dexapi "github.com/dexidp/dex/api"

	"home-os/mcp/internal/config"
)

// Start creates and starts an HTTP server on the given address that serves
// health check and OAuth protected resource metadata endpoints.
// The server runs in a background goroutine. Callers should call Shutdown
// on the returned *http.Server during graceful shutdown.
func Start(addr string, cfg *config.Config) *http.Server {
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
		clientID := "mcp-" + uuid.New().String()

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

	// OAuth 2.0 Authorization Server Metadata (RFC 8414).
	// Tells OAuth clients (opencode) where the authorization server is.
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"issuer":                              "http://localhost:8000/dex",
			"authorization_endpoint":              "http://localhost:8000/authorize",
			"token_endpoint":                      "http://localhost:8000/dex/token",
			"jwks_uri":                            "http://localhost:8000/dex/keys",
			"registration_endpoint":               "http://localhost:8000/register",
			"scopes_supported":                    []string{"openid", "email", "profile"},
			"response_types_supported":            []string{"code"},
			"code_challenge_methods_supported":    []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
			"grant_types_supported":               []string{"authorization_code", "refresh_token"},
		})
	})

	// OAuth 2.0 Protected Resource Metadata (RFC 9728).
	// Tells MCP clients (opencode, Claude Desktop, etc.) where to authenticate
	// via OAuth 2.0 Device Authorization Grant.
	// Served from a separate port to avoid interference from the MCP SSE handler.
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"resource":                 "http://localhost:8000/mcp",
			"authorization_servers":    []string{"http://localhost:8000"},
			"client_id":                "home-os-mcp",
			"scopes_supported":         []string{"openid", "email", "profile"},
			"bearer_methods_supported": []string{"header"},
			"resource_name":            "Home OS MCP",
		})
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("metadata server error", "error", err)
		}
	}()
	return srv
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