// Package server provides the MCP server setup for Home OS.
// It wraps the mark3labs/mcp-go SDK with the stateless Streamable HTTP
// transport (POST /mcp only), OIDC authentication, and a tool registry for
// domain-specific tools. The legacy HTTP+SSE transport (/mcp/sse,
// /mcp/message) was removed: it is deprecated in MCP 2026-07-28.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/config"
)

// claimsCtxKey is an unexported type used for context keys to prevent
// collisions with keys from other packages.
type claimsCtxKey string

const claimsKey claimsCtxKey = "claims"

// Server wraps the MCP server with configuration, database pool, token verifier, and tool registration.
type Server struct {
	mcpServer *server.MCPServer
	// httpServer serves the stateless Streamable HTTP transport at /mcp. It
	// answers both 2026-07-28 clients (per-request _meta, server/discover)
	// and 2025-era clients (initialize handshake) on every request.
	httpServer *server.StreamableHTTPServer
	cfg        *config.Config
	pool       *pgxpool.Pool
	verifier   *auth.Verifier
	toolCount  int
}

// Cache hints (SEP-2549) advertised on list/discover/read results to
// 2026-07-28 clients. The tool, prompt and template catalogue is identical for
// every caller, so it may be cached for a few minutes; it is still marked
// private because it sits behind household auth. resources/read returns live
// household data, so clients must revalidate every time.
const (
	catalogueCacheTTLMs int64 = 5 * 60 * 1000
)

// NewServer creates a new MCP server with the given configuration, database pool, and token verifier.
// It initializes the underlying MCPServer and the stateless Streamable HTTP transport.
func NewServer(cfg *config.Config, pool *pgxpool.Pool, verifier *auth.Verifier) *Server {
	s := &Server{
		cfg:      cfg,
		pool:     pool,
		verifier: verifier,
	}

	// Create the MCPServer with tool capabilities. mcp-go returns tools/list
	// sorted by name, so the list is deterministic for prompt-cache hits.
	s.mcpServer = server.NewMCPServer(
		"Home OS MCP",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithCacheHints(catalogueCacheTTLMs, mcp.CacheScopePrivate),
		server.WithMethodCacheHints(mcp.MethodResourcesRead, 0, mcp.CacheScopePrivate),
	)

	s.httpServer = server.NewStreamableHTTPServer(s.mcpServer, s.StreamableHTTPOptions()...)

	return s
}

// StreamableHTTPOptions returns the options the /mcp Streamable HTTP
// transport is built with: stateless (mcp-go v1.x serves both the 2026-07-28
// stateless protocol and legacy initialize-handshake clients on each request)
// and a context func that copies the AuthMiddleware claims into the tool
// handler context. Tests pass these to servertest so they exercise the same
// transport configuration as production.
func (s *Server) StreamableHTTPOptions() []server.StreamableHTTPOption {
	return []server.StreamableHTTPOption{
		server.WithEndpointPath("/mcp"),
		// Stateless: no Mcp-Session-Id is ever minted or echoed (SEP-2567).
		server.WithStateLess(true),
		// Advertise every version mcp-go implements: 2026-07-28 plus the
		// 2025-x/2024-11-05 initialize-era versions moltis and opencode use.
		server.WithStreamableHTTPProtocolVersions(mcp.ValidProtocolVersions...),
		server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			if claims := ClaimsFromContext(r.Context()); claims != nil {
				return context.WithValue(ctx, claimsKey, claims)
			}
			return ctx
		}),
	}
}

// MCPServer returns the underlying mcp-go server with every registered tool,
// prompt and resource. Tests use it with mcp-go's server/servertest helpers.
func (s *Server) MCPServer() *server.MCPServer {
	return s.mcpServer
}

// ToolCount returns how many tools have been registered via RegisterTool.
func (s *Server) ToolCount() int {
	return s.toolCount
}

// RegisterTool registers a new tool with the MCP server.
func (s *Server) RegisterTool(name string, tool mcp.Tool, handler func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
	s.mcpServer.AddTool(tool, handler)
	s.toolCount++
	slog.Info("registered MCP tool", "name", name, "total", s.toolCount)
}

// RegisterPrompt registers a new prompt handler with the MCP server.
func (s *Server) RegisterPrompt(name string, prompt mcp.Prompt, handler func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error)) {
	s.mcpServer.AddPrompt(prompt, handler)
	slog.Info("registered MCP prompt", "name", name)
}

// RegisterResource registers a new resource with the MCP server.
func (s *Server) RegisterResource(resource mcp.Resource, handler func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error)) {
	s.mcpServer.AddResource(resource, handler)
	slog.Info("registered MCP resource", "name", resource.Name, "uri", resource.URI)
}

// RegisterResourceTemplate registers a new resource template with the MCP server.
func (s *Server) RegisterResourceTemplate(template mcp.ResourceTemplate, handler func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error)) {
	s.mcpServer.AddResourceTemplate(template, handler)
	slog.Info("registered MCP resource template", "name", template.Name, "uriTemplate", template.URITemplate.Raw())
}

// Handler returns the HTTP router: health, OAuth protected resource metadata,
// and the MCP endpoint. POST /mcp is the only MCP route; requests to it pass
// Origin validation (403), then the POST-only rule (405 for GET, DELETE and
// anything else), then Bearer auth, before reaching the transport.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()

	// Health check endpoint (no auth required).
	// Mounted at both /health and /mcp/health to support direct access and
	// access through Envoy's /mcp/ prefix routing (Envoy does not strip prefixes).
	healthHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"service": "home-os-mcp",
		})
	}
	r.Get("/health", healthHandler)
	r.Get("/mcp/health", healthHandler)

	// OAuth 2.0 Protected Resource Metadata (RFC 9728) — tells MCP clients like
	// opencode where to authenticate via OAuth 2.0 Device Authorization Grant.
	// We serve at both the bare path and the /mcp-prefixed path because Envoy
	// routes /.well-known/oauth-protected-resource directly to the MCP cluster,
	// and also routes /mcp/* through with the full /mcp prefix intact.
	//
	// NOTE: We use a custom handler instead of the SDK's
	// NewProtectedResourceMetadataHandler because opencode needs the `client_id`
	// field to discover the pre-registered OAuth client ID. Dex does not support
	// dynamic client registration (RFC 7591), so opencode must know the client ID
	// in advance. The SDK's ProtectedResourceMetadataConfig struct does not include
	// a ClientID field, so we emit the JSON directly.
	wellKnownHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"resource":                 s.cfg.PublicBaseURL + "/mcp",
			"authorization_servers":    []string{s.cfg.PublicBaseURL + "/dex"},
			"client_id":                "home-os-mcp",
			"scopes_supported":         []string{"openid", "email", "profile"},
			"bearer_methods_supported": []string{"header"},
			"resource_name":            "Home OS MCP",
		})
	}
	wellKnownOptionsHandler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.WriteHeader(http.StatusNoContent)
	}
	r.Get("/.well-known/oauth-protected-resource", wellKnownHandler)
	r.Options("/.well-known/oauth-protected-resource", wellKnownOptionsHandler)
	r.Get("/mcp/.well-known/oauth-protected-resource", wellKnownHandler)
	r.Options("/mcp/.well-known/oauth-protected-resource", wellKnownOptionsHandler)

	// Streamable HTTP endpoint: stateless, so every POST carries its own
	// identity and no session affinity is needed.
	r.With(s.OriginMiddleware(), postOnly, s.AuthMiddleware()).Handle("/mcp", s.httpServer)

	return r
}

// Start begins serving the MCP server on the configured port with graceful
// shutdown when ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	addr := fmt.Sprintf(":%s", s.cfg.Port)

	slog.Info("starting MCP server", "addr", addr, "tools", s.toolCount)

	httpSrv := &http.Server{
		Addr:    addr,
		Handler: s.Handler(),
	}

	// Start graceful shutdown in a goroutine.
	go func() {
		<-ctx.Done()
		slog.Info("shutting down MCP server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			slog.Error("HTTP server shutdown error", "error", err)
		}
	}()

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	return nil
}

// postOnly enforces the 2026-07-28 Streamable HTTP rule that the MCP
// endpoint accepts POST only: GET (the removed standalone SSE stream),
// DELETE (session termination; there are no sessions) and every other method
// get 405 with Allow: POST. mcp-go would still open a GET stream for a
// 2025-era client, so this is enforced here rather than left to the SDK.
func postOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSONError(w, http.StatusMethodNotAllowed, "the MCP endpoint accepts POST only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// OriginMiddleware validates the Origin header as the MCP transport spec
// requires (DNS-rebinding protection) and answers 403 when it is not allowed.
// Requests without an Origin header (CLI clients, moltis, opencode, server to
// server) pass: only browsers send Origin. A browser Origin passes when it is
// the origin of PUBLIC_BASE_URL, or a loopback origin (local tools such as the
// MCP inspector; a remote page cannot claim a loopback origin).
func (s *Server) OriginMiddleware() func(http.Handler) http.Handler {
	allowed := ""
	if u, err := url.Parse(s.cfg.PublicBaseURL); err == nil && u.Scheme != "" && u.Host != "" {
		allowed = strings.ToLower(u.Scheme + "://" + u.Host)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && !originAllowed(origin, allowed) {
				slog.Warn("rejected MCP request with disallowed Origin", "origin", origin)
				writeJSONError(w, http.StatusForbidden, "origin not allowed")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// originAllowed reports whether a browser Origin header value may call /mcp.
func originAllowed(origin, allowed string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false // includes the opaque "null" origin
	}
	if allowed != "" && strings.ToLower(u.Scheme+"://"+u.Host) == allowed {
		return true
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// AuthMiddleware returns a chi-compatible middleware that validates Dex-issued
// OIDC Bearer tokens. It extracts the Authorization header, parses the bearer
// token, verifies it via the Verifier, and injects the resulting *auth.Claims
// into the request context.
//
// When the verifier is in disabled mode (no issuer URL configured), the middleware
// passes through with an anonymous local-dev identity. This enables local development
// without a running Dex instance.
func (s *Server) AuthMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !s.verifier.Enabled() {
				// Local dev mode — inject anonymous identity.
				ctx := context.WithValue(r.Context(), claimsKey, &auth.Claims{
					Identity: auth.Identity{Subject: "local-dev", Email: "local-dev@homeos.local", Name: "Local Dev"},
					UserID:   "local-dev",
				})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.cfg.PublicBaseURL+`/.well-known/oauth-protected-resource"`)
				writeJSONError(w, http.StatusUnauthorized, "missing authorization header")
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				writeJSONError(w, http.StatusUnauthorized, "invalid authorization header format")
				return
			}

			claims, err := s.verifier.VerifyTokenAndEnrich(r.Context(), s.pool, parts[1])
			if err != nil {
				slog.Warn("auth middleware: token verification failed", "error", err)
				writeJSONError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), claimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ContextWithClaims returns a copy of ctx carrying claims, in the shape
// ClaimsFromContext reads. Middleware uses the same key; tests use this to
// call tool handlers directly as an authenticated household member.
func ContextWithClaims(ctx context.Context, claims *auth.Claims) context.Context {
	return context.WithValue(ctx, claimsKey, claims)
}

// ClaimsFromContext extracts the *auth.Claims that were injected by the
// AuthMiddleware middleware. Returns nil if the middleware was not used.
func ClaimsFromContext(ctx context.Context) *auth.Claims {
	if claims, ok := ctx.Value(claimsKey).(*auth.Claims); ok {
		return claims
	}
	return nil
}

// writeJSONError writes a JSON error response with the given status code and message.
func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"code":    http.StatusText(status),
			"message": message,
		},
	})
}
