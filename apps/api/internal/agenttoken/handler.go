package agenttoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

const (
	// TokenPrefix starts every agent token, so tokens are recognisable in
	// config files and secret scanners.
	TokenPrefix = "hos_"
	// secretBytes is the random entropy in each token (256 bits).
	secretBytes = 32
	// displayPrefixLen is how much of the plaintext is kept in token_prefix
	// ("hos_" plus 8 secret characters) so members can tell tokens apart.
	displayPrefixLen = 12
	maxNameLen       = 255
)

// Scopes an agent token can carry.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
)

var validScopes = map[string]bool{ScopeRead: true, ScopeWrite: true}

// HashToken returns the SHA-256 digest stored in agent_tokens.token_hash for
// a plaintext token.
func HashToken(plaintext string) []byte {
	sum := sha256.Sum256([]byte(plaintext))
	return sum[:]
}

// generateToken returns a new plaintext token: "hos_" + base64url(32 random bytes).
func generateToken() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate agent token: %w", err)
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// Handler serves the agent token endpoints for the authenticated member.
type Handler struct {
	repo *Repo
	now  func() time.Time
}

// NewHandler creates a new agent token handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo, now: time.Now}
}

// RegisterRoutes mounts the agent token routes. The router must already run
// middleware.RequireAuth.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Post("/api/v1/agent-tokens", h.Create)
	r.Get("/api/v1/agent-tokens", h.List)
	r.Delete("/api/v1/agent-tokens/{id}", h.Revoke)
}

// --- request / response types ---

type createRequest struct {
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// tokenResponse is a token's metadata. It never carries the secret or hash.
type tokenResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TokenPrefix string   `json:"token_prefix"`
	Scopes      []string `json:"scopes"`
	LastUsedAt  *string  `json:"last_used_at"`
	ExpiresAt   *string  `json:"expires_at"`
	RevokedAt   *string  `json:"revoked_at"`
	CreatedAt   string   `json:"created_at"`
}

// createdResponse adds the plaintext token, returned only from Create.
type createdResponse struct {
	tokenResponse
	Token string `json:"token"`
}

func fmtTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func toResponse(t *Token) tokenResponse {
	scopes := t.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return tokenResponse{
		ID:          t.ID.String(),
		Name:        t.Name,
		TokenPrefix: t.TokenPrefix,
		Scopes:      scopes,
		LastUsedAt:  fmtTime(t.LastUsedAt),
		ExpiresAt:   fmtTime(t.ExpiresAt),
		RevokedAt:   fmtTime(t.RevokedAt),
		CreatedAt:   t.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// caller returns the authenticated member's household and user IDs.
func caller(r *http.Request) (householdID, userID uuid.UUID, ok bool) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		return uuid.Nil, uuid.Nil, false
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		return uuid.Nil, uuid.Nil, false
	}
	uid, err := uuid.Parse(claims.UserID)
	if err != nil {
		return uuid.Nil, uuid.Nil, false
	}
	return hid, uid, true
}

// normalizeScopes validates and de-duplicates scopes, keeping request order.
func normalizeScopes(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, errors.New("scopes is required (read, write)")
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if !validScopes[s] {
			return nil, fmt.Errorf("unknown scope %q (allowed: read, write)", s)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// Create issues a new token for the caller and returns the plaintext once.
// POST /api/v1/agent-tokens  {"name", "scopes": ["read","write"], "expires_at"?: RFC3339}
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	hid, uid, ok := caller(r)
	if !ok {
		apierr.Forbidden(w, "missing member context")
		return
	}

	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}
	if utf8.RuneCountInString(name) > maxNameLen {
		apierr.BadRequest(w, "name must be at most 255 characters")
		return
	}
	scopes, err := normalizeScopes(req.Scopes)
	if err != nil {
		apierr.BadRequest(w, err.Error())
		return
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(h.now()) {
		apierr.BadRequest(w, "expires_at must be in the future")
		return
	}

	plaintext, err := generateToken()
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	created, err := h.repo.Create(r.Context(), &Token{
		UserID:      uid,
		HouseholdID: hid,
		Name:        name,
		TokenPrefix: plaintext[:displayPrefixLen],
		Scopes:      scopes,
		ExpiresAt:   req.ExpiresAt,
	}, HashToken(plaintext))
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	apierr.JSON(w, http.StatusCreated, map[string]any{"data": createdResponse{
		tokenResponse: toResponse(created),
		Token:         plaintext,
	}})
}

// List returns the caller's tokens without secrets.
// GET /api/v1/agent-tokens
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	hid, uid, ok := caller(r)
	if !ok {
		apierr.Forbidden(w, "missing member context")
		return
	}
	tokens, err := h.repo.ListByUser(r.Context(), hid, uid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	out := make([]tokenResponse, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, toResponse(t))
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": out})
}

// Revoke sets revoked_at on one of the caller's tokens. Other members' tokens
// answer 404.
// DELETE /api/v1/agent-tokens/{id}
func (h *Handler) Revoke(w http.ResponseWriter, r *http.Request) {
	hid, uid, ok := caller(r)
	if !ok {
		apierr.Forbidden(w, "missing member context")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid agent token id")
		return
	}
	switch err := h.repo.Revoke(r.Context(), hid, uid, id); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrNotFound):
		apierr.NotFound(w, "agent token not found")
	default:
		apierr.InternalError(w, err)
	}
}
