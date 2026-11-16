package server

// Rate limits on /mcp (#4693). The MCP spec says servers MUST rate-limit
// tool calls, and /mcp is public: a leaked hos_ token or a runaway agent
// loop could otherwise drive unbounded database work.
//
// AuthMiddleware applies two HTTP-level limits:
//
//   - Unauthenticated callers are limited per client IP
//     (MCP_UNAUTH_RATE_LIMIT_PER_MINUTE, default 20/min, burst the same).
//     Every refused request (no bearer, bad or unknown token, no household,
//     deactivated user) spends a token from the IP's bucket. The bucket is
//     checked before the token is verified, so once an IP has used it up,
//     its requests are refused without touching the database, including
//     requests with a valid token, until a token refills (one every 3s by
//     default). Database failures (500) are our fault and spend nothing.
//   - Authenticated callers are limited per principal
//     (MCP_RATE_LIMIT_PER_MINUTE, default 120/min, MCP_RATE_LIMIT_BURST,
//     default 40). The principal is the agent token id, else the user plus
//     the OAuth client the Dex token was issued to, so two agents of the
//     same member get separate budgets. The burst of 40 lets an agent fire
//     a 20-tool chain in a few seconds with room to spare. One HTTP request
//     spends one token, whatever it contains.
//
// Past either limit the answer is HTTP 429 with Retry-After and a JSON-RPC
// error body (code rateLimitedCode, data.retry_after_seconds), echoing the
// request id when the body is a single message, so a client can show it.
// No WWW-Authenticate header is sent: signing in again would not help.
//
// A tool middleware adds a tighter sub-limit on credential_reveal per
// principal (MCP_REVEAL_RATE_LIMIT_PER_MINUTE, default 10/min). It answers
// with an isError tool result telling the model how long to wait, since a
// single tools/call inside an accepted HTTP request can't become a 429.
//
// The buckets are in memory per pod (internal/ratelimit). There is one MCP
// replica; with N replicas every budget is N times larger, still bounded.
// Idle buckets are swept after ratelimit.IdleTTL. In verifier-disabled
// local-dev mode no limit applies, and a zero limit in Config turns that
// limit off (tests and the latency benchmark build Config by hand).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"home-os/mcp/internal/auth"
	"home-os/mcp/internal/config"
	"home-os/mcp/internal/ratelimit"
	"home-os/mcp/internal/respond"
)

// rateLimitedCode is the JSON-RPC error code of a rate-limited request, in
// the implementation-defined server error range (-32000 to -32099).
const rateLimitedCode = -32029

// revealToolName is the tool the reveal sub-limit applies to.
const revealToolName = "credential_reveal"

// maxIDPeek bounds how much of a refused request's body is read to find its
// JSON-RPC id.
const maxIDPeek = 64 << 10

// mcpLimits holds the /mcp buckets. A nil bucket set means that limit is off.
type mcpLimits struct {
	cfg       *config.Config
	unauth    *ratelimit.Keyed
	principal *ratelimit.Keyed
	reveal    *ratelimit.Keyed
}

func newMCPLimits(cfg *config.Config) *mcpLimits {
	l := &mcpLimits{cfg: cfg}
	if n := cfg.UnauthRateLimitPerMinute; n > 0 {
		l.unauth = ratelimit.NewKeyed(ratelimit.PerMinute(n), n)
	}
	if n := cfg.RateLimitPerMinute; n > 0 {
		l.principal = ratelimit.NewKeyed(ratelimit.PerMinute(n), l.principalBurst())
	}
	if n := cfg.RevealRateLimitPerMinute; n > 0 {
		l.reveal = ratelimit.NewKeyed(ratelimit.PerMinute(n), n)
	}
	return l
}

// principalBurst is the per-principal burst in effect.
func (l *mcpLimits) principalBurst() int {
	if l.cfg.RateLimitBurst > 0 {
		return l.cfg.RateLimitBurst
	}
	return config.DefaultRateLimitBurst
}

// principalKey is the bucket key for an authenticated caller: the agent
// token, else the user plus the OAuth client the token was issued to.
func principalKey(c *auth.Claims) string {
	if c.AgentTokenID != "" {
		return "agent:" + c.AgentTokenID
	}
	return "user:" + c.UserID + "|client:" + c.ClientID
}

// writeRateLimited answers 429 with Retry-After and a JSON-RPC error body.
func writeRateLimited(w http.ResponseWriter, r *http.Request, wait time.Duration, message string) {
	secs := ratelimit.RetryAfterSeconds(wait)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": mcp.JSONRPC_VERSION,
		"id":      requestID(r),
		"error": map[string]any{
			"code":    rateLimitedCode,
			"message": fmt.Sprintf("%s; retry after %d seconds", message, secs),
			"data":    map[string]any{"retry_after_seconds": secs},
		},
	})
}

// requestID returns the id of a single JSON-RPC message body, or nil when
// the body is a batch, too large, or not JSON.
func requestID(r *http.Request) json.RawMessage {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxIDPeek+1))
	if err != nil || len(body) > maxIDPeek {
		return nil
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return nil
	}
	var m struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(body, &m) != nil || len(m.ID) == 0 {
		return nil
	}
	return m.ID
}

// limitReveal is the credential_reveal sub-limit, a tool handler middleware.
func (s *Server) limitReveal(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if req.Params.Name != revealToolName || s.limits.reveal == nil ||
			s.verifier == nil || !s.verifier.Enabled() {
			return next(ctx, req)
		}
		c := ClaimsFromContext(ctx)
		if c == nil {
			return next(ctx, req)
		}
		if ok, wait := s.limits.reveal.Allow(principalKey(c)); !ok {
			secs := ratelimit.RetryAfterSeconds(wait)
			slog.Warn("mcp: credential_reveal rate limited",
				"user_id", c.UserID, "token_id", c.AgentTokenID, "client_id", c.ClientID)
			return respond.Fail(respond.Error{
				Message: fmt.Sprintf("credential_reveal is limited to %d calls a minute and that limit is reached.",
					s.cfg.RevealRateLimitPerMinute),
				Fix: fmt.Sprintf("Wait %d seconds, then retry. Reveal only the credential the user asked for; credential_find lists credentials without revealing them.", secs),
			}), nil
		}
		return next(ctx, req)
	}
}
