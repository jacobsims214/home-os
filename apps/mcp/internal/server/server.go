// Package server provides the MCP server setup for Home OS.
// It wraps the mark3labs/mcp-go SDK with the stateless Streamable HTTP
// transport (POST /mcp only), OIDC authentication, and a tool registry for
// domain-specific tools. The legacy HTTP+SSE transport (/mcp/sse,
// /mcp/message) was removed: it is deprecated in MCP 2026-07-28.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/config"
	"home-os/mcp/internal/metadata"
	"home-os/mcp/internal/ratelimit"
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
	// limits are the /mcp rate limits (ratelimit.go, #4693).
	limits *mcpLimits
	// toolPolicy holds per-server policy overrides (SetToolPolicy). In
	// production it is empty and every tool's requirement comes from
	// auth.ToolPolicy; tests register synthetic tools and say what each may
	// do, and a deployment that serves a tool outside the catalog can
	// classify it here instead of editing the shared table.
	policyMu   sync.RWMutex
	toolPolicy map[string]auth.Requirement
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
		cfg:        cfg,
		pool:       pool,
		verifier:   verifier,
		limits:     newMCPLimits(cfg),
		toolPolicy: map[string]auth.Requirement{},
	}

	// Create the MCPServer with tool capabilities. mcp-go returns tools/list
	// sorted by name, so the list is deterministic for prompt-cache hits.
	s.mcpServer = server.NewMCPServer(
		"Home OS MCP",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithCacheHints(catalogueCacheTTLMs, mcp.CacheScopePrivate),
		server.WithMethodCacheHints(mcp.MethodResourcesRead, 0, mcp.CacheScopePrivate),
		// The household role policy (#4717) and the read-only agent token
		// backstop: AuthMiddleware already answers 403 before the transport
		// for both, this catches anything it could not see (e.g. an
		// unparseable or unusual request shape) and is the check every tool
		// goes through.
		server.WithToolHandlerMiddleware(s.requirePolicy),
		// Per-principal sub-limit on credential_reveal (#4693).
		server.WithToolHandlerMiddleware(s.limitReveal),
		// A caller who can't write is never pointed at a write tool (#4771).
		server.WithToolHandlerMiddleware(s.dropWriteHints),
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

// readOnlyScopeMessage is the error a read-only agent token gets for a tool
// that may change data.
const readOnlyScopeMessage = "this agent token is read-only: tool %q can change data and needs an agent token with the write scope"

// deniedMessage explains why c may not call tool name, in the order the
// checks apply: the household role policy first (#4717), then the agent
// token's scope (#4689).
func (s *Server) deniedMessage(c *auth.Claims, name string) string {
	if c != nil && s.deniedByRole(c.Role, name) {
		return auth.RoleCannotMessage(c.Role, name)
	}
	return fmt.Sprintf(readOnlyScopeMessage, name)
}

// requirementFor returns the policy requirement for a tool: this server's
// override (SetToolPolicy) when it has one, else auth.ToolPolicy. ok is false
// for a tool nobody classified.
func (s *Server) requirementFor(tool string) (auth.Requirement, bool) {
	s.policyMu.RLock()
	req, ok := s.toolPolicy[tool]
	s.policyMu.RUnlock()
	if ok {
		return req, true
	}
	return auth.RequirementFor(tool)
}

// SetToolPolicy classifies a tool registered on this server, overriding
// auth.ToolPolicy for it. Production registers every catalog tool through
// registerAll, so the shared table already covers them; this is for tests
// that register a synthetic tool and for a tool served outside the catalog.
func (s *Server) SetToolPolicy(name string, req auth.Requirement) {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	if s.toolPolicy == nil {
		s.toolPolicy = map[string]auth.Requirement{}
	}
	s.toolPolicy[name] = req
}

// deniedByRole reports whether the caller's household role may not call
// tool. A tool nobody classified is denied to every role, so a newly
// registered tool cannot be served before its policy decision is made. An
// empty role means auth-disabled local development (no Dex configured),
// which has always had full access; every real caller (a Dex session, an
// agent token) carries the role re-read from memberships.
func (s *Server) deniedByRole(role, tool string) bool {
	if role == "" {
		return false
	}
	req, ok := s.requirementFor(tool)
	if !ok {
		return true
	}
	return !req.Allows(role)
}

// toolIsReadOnly reports whether a registered tool may be called with a
// read-only agent token: it must be annotated readOnlyHint=true and not
// destructiveHint=true. Tools without annotations count as writes.
func (s *Server) toolIsReadOnly(name string) bool {
	st := s.mcpServer.GetTool(name)
	if st == nil {
		return false
	}
	a := st.Tool.Annotations
	if a.DestructiveHint != nil && *a.DestructiveHint {
		return false
	}
	return a.ReadOnlyHint != nil && *a.ReadOnlyHint
}

// requirePolicy is the one place the household role policy is applied to
// MCP tool calls (#4717). It asks auth.ToolPolicy for the tool's requirement
// and refuses the call when the caller's role does not hold it, so a tool
// cannot leak by forgetting its own check: the gate is registered on every
// tool through NewServer, and a tool with no policy entry is refused
// outright. A read-only agent token is still limited to read-only tools
// (#4689): a role that may make a narrow write does not give its tokens one.
//
// Handlers still trim what a role may see (a section, a field, another
// vendor's job); this only decides who may call the tool at all.
func (s *Server) requirePolicy(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c := ClaimsFromContext(ctx)
		if c == nil {
			return next(ctx, req) // no claims: the handler answers "not signed in"
		}
		name := req.Params.Name
		if s.deniedByRole(c.Role, name) {
			return mcp.NewToolResultError(s.deniedMessage(c, name)), nil
		}
		if c.AgentTokenID != "" && !c.CanWrite() && !s.toolIsReadOnly(name) {
			return mcp.NewToolResultError(s.deniedMessage(c, name)), nil
		}
		return next(ctx, req)
	}
}

// maxScopeCheckBody bounds how much of a read-only token's request body
// AuthMiddleware buffers to find tools/call names.
const maxScopeCheckBody = 8 << 20

// deniedToolCalled returns the name of the first tools/call in a JSON-RPC
// body (a single message or a batch) that the caller may not run, or "".
// That is a tool the caller's household role policy denies, or a tool that
// may change data when the caller is a read-only agent token. Bodies it
// cannot parse return "": the transport rejects them anyway and
// requirePolicy still guards the handlers.
func (s *Server) deniedToolCalled(body []byte, c *auth.Claims) string {
	type rpcMsg struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	var msgs []rpcMsg
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if json.Unmarshal(trimmed, &msgs) != nil {
			return ""
		}
	} else {
		var m rpcMsg
		if json.Unmarshal(trimmed, &m) != nil {
			return ""
		}
		msgs = []rpcMsg{m}
	}
	for _, m := range msgs {
		if m.Method != string(mcp.MethodToolsCall) || s.mcpServer.GetTool(m.Params.Name) == nil {
			continue // unknown tool: the transport answers "not found"
		}
		if s.deniedByRole(c.Role, m.Params.Name) {
			return m.Params.Name
		}
		if c.AgentTokenID != "" && !c.CanWrite() && !s.toolIsReadOnly(m.Params.Name) {
			return m.Params.Name
		}
	}
	return ""
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
// A bearer starting with "hos_" is a per-member agent token (auth.
// VerifyAgentToken): it is never parsed as a JWT, and an unknown, revoked,
// expired or deactivated-user token gets the same 401 as a bad JWT (#4405:
// agent-token refusals are deliberately indistinguishable, and a token whose
// owner left its household is also 401). A Dex JWT whose user is deactivated
// (auth.ErrUserDeactivated) gets 403 with no challenge, like a JWT whose user
// is not a household member. A token
// without the write scope may only call tools annotated readOnlyHint=true;
// a tools/call for any other tool is answered 403 with a clear message
// before it reaches the transport.
//
// Requests are rate-limited (#4693, see ratelimit.go): refused requests per
// client IP, authenticated ones per principal; past a limit the answer is
// 429 with Retry-After and a JSON-RPC error, without a challenge.
//
// When the verifier is in disabled mode (no issuer URL configured), the middleware
// passes through with an anonymous local-dev identity and no rate limit. This
// enables local development without a running Dex instance.
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

			// Per-IP limit on refused requests: an IP that used up its
			// budget is refused before its token costs a lookup.
			ip := ""
			refused := func() {}
			if s.limits.unauth != nil {
				ip = ratelimit.ClientIP(r, s.cfg.TrustedProxies)
				if ex, wait := s.limits.unauth.Exhausted(ip); ex {
					slog.Info("auth middleware: rate limited", "scope", "ip", "remote_ip", ip)
					writeRateLimited(w, r, wait, "too many unauthenticated requests from this address")
					return
				}
				refused = func() { s.limits.unauth.Allow(ip) }
			}

			scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
			token = strings.TrimSpace(token)
			if !strings.EqualFold(scheme, "Bearer") || token == "" {
				refused()
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
				refused()
				slog.Warn("auth middleware: token valid but no household membership", "error", err)
				writeJSONError(w, http.StatusForbidden, "authenticated, but this account is not a member of any household")
				return
			case errors.Is(err, auth.ErrUserDeactivated):
				// Same treatment as a missing membership: 403 with no
				// challenge, since signing in again cannot reactivate the
				// account. users.active is read on every request.
				refused()
				slog.Warn("auth middleware: token valid but user is deactivated", "error", err)
				writeJSONError(w, http.StatusForbidden, "authenticated, but this account has been deactivated")
				return
			case errors.Is(err, auth.ErrMemberLookup):
				slog.Error("auth middleware: membership lookup failed", "error", err)
				writeJSONError(w, http.StatusInternalServerError, "could not resolve household membership")
				return
			default:
				refused()
				slog.Warn("auth middleware: token verification failed", "error", err)
				challenge(w, true)
				writeJSONError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}

			if s.limits.principal != nil {
				if ok, wait := s.limits.principal.Allow(principalKey(claims)); !ok {
					slog.Warn("auth middleware: rate limited", "scope", "principal",
						"user_id", claims.UserID, "token_id", claims.AgentTokenID, "client_id", claims.ClientID)
					writeRateLimited(w, r, wait, fmt.Sprintf(
						"rate limit exceeded: at most %d requests a minute (burst %d) per agent token or app",
						s.cfg.RateLimitPerMinute, s.limits.principalBurst()))
					return
				}
			}

			// A tools/call the caller's role policy denies (or a write for a
			// read-only agent token) is refused here, before the transport.
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxScopeCheckBody))
			if err != nil {
				writeJSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			if tool := s.deniedToolCalled(body, claims); tool != "" {
				slog.Info("auth middleware: caller refused a tool by the role policy",
					"token_id", claims.AgentTokenID, "role", claims.Role, "tool", tool)
				// No WWW-Authenticate step-up challenge: agent tokens are
				// static secrets and a role is not an OAuth scope, so no
				// OAuth flow can widen either.
				writeJSONError(w, http.StatusForbidden, s.deniedMessage(claims, tool))
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
