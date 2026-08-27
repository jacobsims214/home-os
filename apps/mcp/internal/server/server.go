// Package server provides the MCP server setup for Home OS.
// It wraps the mark3labs/mcp-go SDK with SSE transport, OIDC authentication,
// and a tool registry for domain-specific tools.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
	sseServer *server.SSEServer
	cfg       *config.Config
	pool      *pgxpool.Pool
	verifier  *auth.Verifier
	toolCount int
}

// NewServer creates a new MCP server with the given configuration, database pool, and token verifier.
// It initializes the underlying MCPServer and SSE transport.
func NewServer(cfg *config.Config, pool *pgxpool.Pool, verifier *auth.Verifier) *Server {
	s := &Server{
		cfg:      cfg,
		pool:     pool,
		verifier: verifier,
	}

	// Create the MCPServer with tool capabilities.
	s.mcpServer = server.NewMCPServer(
		"Home OS MCP",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	// Create the SSE server with a static base path.
	// The SSE endpoint is at /mcp/sse, and the message endpoint is at /mcp/message.
	//
	// WithSSEContextFunc bridges JWT claims from the message POST request context
	// into the MCP tool handler context. This is required because mcp-go v0.48.0's
	// SSEServer.handleMessage (sdk/server/sse.go:573-577) builds the tool-handler
	// context from the message POST's *request* context — it does NOT propagate the
	// SSE connection's request context. Since auth is enforced on both endpoints,
	// the claims from the message request must be manually copied into the context
	// that tool handlers receive.
	s.sseServer = server.NewSSEServer(s.mcpServer,
		server.WithStaticBasePath("/mcp"),
		server.WithSSEContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			if claims := ClaimsFromContext(r.Context()); claims != nil {
				return context.WithValue(ctx, claimsKey, claims)
			}
			return ctx
		}),
	)

	return s
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

// Start begins serving the MCP server on the configured port.
// It sets up a chi router with JWT auth middleware on the SSE endpoint
// and starts the HTTP server with graceful shutdown.
func (s *Server) Start(ctx context.Context) error {
	addr := fmt.Sprintf(":%s", s.cfg.Port)

	// Create the chi router.
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
			"resource":                 "http://localhost:8000/mcp",
			"authorization_servers":    []string{"http://localhost:8000/dex"},
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

	// SSE endpoint — requires OIDC Bearer token.
	// The SSE stream handler is mounted at /mcp/sse with Bearer token validation.
	r.With(s.AuthMiddleware()).Handle("/mcp/sse", s.sseServer.SSEHandler())

	// Message endpoint — requires OIDC Bearer token.
	// MCP clients send the Authorization header on both SSE and message POST requests,
	// so the same AuthMiddleware is applied here. The WithSSEContextFunc option
	// (configured in NewServer) bridges these claims into the tool handler context.
	r.With(s.AuthMiddleware()).Handle("/mcp/message", s.sseServer.MessageHandler())

	slog.Info("starting MCP server", "addr", addr, "tools", s.toolCount)

	httpSrv := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	// Start graceful shutdown in a goroutine.
	go func() {
		<-ctx.Done()
		slog.Info("shutting down MCP server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.sseServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("SSE server shutdown error", "error", err)
		}
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			slog.Error("HTTP server shutdown error", "error", err)
		}
	}()

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	return nil
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
				w.Header().Set("WWW-Authenticate", `Bearer realm="http://localhost:8000/.well-known/oauth-protected-resource"`)
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
