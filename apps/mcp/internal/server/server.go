// Package server provides the MCP server setup for Home OS.
// It wraps the mark3labs/mcp-go SDK with the stateless Streamable HTTP
// transport (POST /mcp only), OIDC authentication, and a tool registry for
// domain-specific tools. The legacy HTTP+SSE transport (/mcp/sse,
// /mcp/message) was removed: it is deprecated in MCP 2026-07-28.
package server

import (
	"context"
	"encoding/json"
	"errors"
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
	"home-os/mcp/internal/metadata"
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

	// OAuth 2.0 Protected Resource Metadata (RFC 9728): tells MCP clients the
	// configured resource URI (MCP_RESOURCE_URL) and authorization server
	// (DEX_ISSUER). Served at /.well-known/oauth-protected-resource and the
	// path-qualified /.well-known/oauth-protected-resource/mcp (mcp-go's
	// ProtectedResourceMetadataPath), plus the legacy /mcp-prefixed path. The
	// metadata server (Envoy's route for /.well-known/oauth-protected-resource)
	// serves the identical document.
	prm := metadata.ProtectedResourceHandler(s.cfg)
	for _, p := range metadata.ProtectedResourcePaths(s.cfg) {
		r.Handle(p, prm)
	}
	r.Handle("/mcp"+server.WellKnownProtectedResourcePath, prm)

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
// one of MCP_ALLOWED_ORIGINS (default: the origin of PUBLIC_BASE_URL), or a
// loopback origin (local tools such as the MCP inspector; a remote page cannot
// claim a loopback origin).
func (s *Server) OriginMiddleware() func(http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range s.cfg.AllowedOrigins {
		if n := normalizeOrigin(o); n != "" {
			allowed[n] = true
		}
	}
	if len(allowed) == 0 {
		// Config built without config.Load (tests): fall back to the
		// PUBLIC_BASE_URL origin, which is also config.Load's default.
		if n := normalizeOrigin(s.cfg.PublicBaseURL); n != "" {
			allowed[n] = true
		}
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

// normalizeOrigin returns the lowercase scheme://host[:port] of an http(s)
// URL, or "" when it has no usable origin.
func normalizeOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// originAllowed reports whether a browser Origin header value may call /mcp.
func originAllowed(origin string, allowed map[string]bool) bool {
	n := normalizeOrigin(origin)
	if n == "" {
		return false // includes the opaque "null" origin and non-http schemes
	}
	if allowed[n] {
		return true
	}
	u, _ := url.Parse(n)
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// AuthMiddleware makes /mcp an OAuth 2.1 resource server. Every request needs
// an Authorization: Bearer token that the Verifier accepts: Dex signature,
// issuer (DEX_ISSUER), expiry, and an audience this server accepts
// (MCP_ACCEPTED_AUDIENCES, MCP_RESOURCE_URL, or a client registered through
// /register). The token is then resolved to a Home OS member (dex_sub, then
// email) and the *auth.Claims are put in the request context, from where
// StreamableHTTPOptions' context func hands them to tool handlers. The token
// itself is never forwarded anywhere (no token passthrough).
//
//   - no or non-Bearer credentials: 401 with
//     WWW-Authenticate: Bearer resource_metadata="...", scope="..."
//   - invalid, expired, unsigned, wrong-issuer or wrong-audience token: 401
//     with the same challenge plus error="invalid_token" (RFC 6750)
//   - valid token, no household membership: 403
//   - database failure during the lookup: 500
//
// When the verifier is in disabled mode (no issuer URL configured), the middleware
// passes through with an anonymous local-dev identity. This enables local development
// without a running Dex instance.
func (s *Server) AuthMiddleware() func(http.Handler) http.Handler {
	resourceMetadata := metadata.ResourceMetadataURL(s.cfg)
	scope := strings.Join(metadata.ScopesSupported, " ")
	challenge := func(w http.ResponseWriter, invalidToken bool) {
		v := `Bearer resource_metadata="` + resourceMetadata + `", scope="` + scope + `"`
		if invalidToken {
			v += `, error="invalid_token", error_description="the access token is invalid, expired or not issued for this server"`
		}
		w.Header().Set("WWW-Authenticate", v)
	}
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

			scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
			token = strings.TrimSpace(token)
			if !strings.EqualFold(scheme, "Bearer") || token == "" {
				// No bearer credentials at all: RFC 6750 section 3.1 says to
				// send the challenge without an error code.
				challenge(w, false)
				writeJSONError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}

			claims, err := s.verifier.VerifyTokenAndEnrich(r.Context(), s.pool, token)
			switch {
			case err == nil:
			case errors.Is(err, auth.ErrNoMembership):
				slog.Warn("auth middleware: token valid but no household membership", "error", err)
				writeJSONError(w, http.StatusForbidden, "authenticated, but this account is not a member of any household")
				return
			case errors.Is(err, auth.ErrMemberLookup):
				slog.Error("auth middleware: membership lookup failed", "error", err)
				writeJSONError(w, http.StatusInternalServerError, "could not resolve household membership")
				return
			default:
				slog.Warn("auth middleware: token verification failed", "error", err)
				challenge(w, true)
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
