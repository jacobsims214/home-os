// Handler implementations for the native secrets manager module. These are the
// REST API endpoints the UI calls for creating, listing, viewing, updating, and
// deleting encrypted secrets, plus master-password setup and verification.
//
// All handlers sit behind the RequireAuth middleware (registered in main.go by
// task #1286) and extract the caller's household_id from the JWT claims via
// middleware.ClaimsFromContext — every query is household-scoped so one tenant
// can never read another tenant's secrets.
//
// Zero-knowledge architecture: the API NEVER sees plaintext secret data. The
// browser encrypts secrets client-side via the Web Crypto API (AES-256-GCM with
// a PBKDF2-derived key) and sends only { encrypted_data, iv, key_version } to
// the API. The API stores the encrypted blob and returns it on demand for the
// browser to decrypt. The name and secret_type are stored in plaintext so the
// list endpoint can show human-readable labels without decryption.
//
// Master-password verification: the browser derives a key from the user's master
// password (PBKDF2 + per-household salt) and sends a verifier computed from it
// (today SHA-256 of the raw key). Since migration 051 (#4440) the API stores
// only argon2id(verifier, random per-row salt) and checks it in constant time
// on /verify, so the database never holds a value that could be replayed or
// cheaply guessed against. Rows set up before 051 still hold the legacy
// key_hash; they upgrade to an argon2id verifier on their next successful
// unlock. See verifier.go.
//
// The handler depends on the Repo defined in repo.go (task #1284). The Repo
// method signatures the handlers call:
//
//	CreateSecret(ctx, householdID uuid.UUID, s *Secret) (*Secret, error)
//	    Inserts a secrets row. s.EncryptedData/IV/KeyVersion/Name/SecretType/
//	    EntityType/EntityID are set by the handler; household_id comes from the
//	    JWT. Returns the fully-populated Secret.
//	GetSecret(ctx, householdID, id uuid.UUID) (*Secret, error)
//	    Returns the full secret including encrypted_data + iv (for client-side
//	    decrypt). Returns (nil, nil) if not found or cross-tenant.
//	ListSecrets(ctx, householdID uuid.UUID, entityType string, entityID uuid.UUID) ([]*SecretListItem, error)
//	    Metadata only — never selects encrypted_data. When entityType == "" /
//	    entityID == uuid.Nil the entity filter is ignored and all household
//	    secrets are returned.
//	UpdateSecret(ctx, householdID, id uuid.UUID, s *Secret) (*Secret, error)
//	    UPDATE encrypted_data, iv, name, secret_type. Returns (nil, nil) if not
//	    found or cross-tenant.
//	DeleteSecret(ctx, householdID, id uuid.UUID) error
//	    Removes the secrets row. Returns pgx.ErrNoRows if absent or cross-tenant.
//	SetupKey(ctx, householdID uuid.UUID, verifier string, keySalt []byte, keyVersion int) (*SecretKey, error)
//	    Inserts a secret_keys row with an argon2id verifier. Returns the created SecretKey.
//	GetKey(ctx, householdID uuid.UUID, keyVersion int) (*SecretKey, error)
//	    Returns the verifier (or legacy key_hash) + key_salt. Returns (nil, nil)
//	    if no key exists for the given household + version.
//	UpgradeLegacyKey / ReplaceVerifier
//	    Compare-and-swap writes used by /verify to upgrade or rehash a row.
package secret

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
	"home-os/api/internal/searchqueue"
	"home-os/api/pkg/apierr"
)

// searchIndexer is the minimal subset of search.Client that the secret handler
// needs for indexing. Defined as an interface so the handler can work without
// a search client (graceful degradation if Typesense is unavailable).
type searchIndexer interface {
	IndexDocument(ctx context.Context, doc search.SearchDocument) error
	DeleteDocument(ctx context.Context, id string) error
}

// Handler holds dependencies for secret HTTP handlers.
type Handler struct {
	repo     *Repo
	search   searchIndexer
	agentKey *AgentKey    // nil: "shared with agents" is off (#4446)
	limiter  *failLimiter // wrong /verify attempts per member and per household
}

// NewHandler creates a new secret handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo, limiter: newFailLimiter()}
}

// WithSearchClient sets the Typesense client for search indexing.
func (h *Handler) WithSearchClient(sc *search.Client) *Handler {
	h.search = sc
	return h
}

// indexSecret indexes a secret's plaintext metadata (name, secret_type) into
// Typesense so it appears in global search. The encrypted data is never indexed.
func (h *Handler) indexSecret(ctx context.Context, s *Secret) {
	// Delegates to the one document builder (#5314). searchqueue.Document
	// reads the row and produces the same document the index queue's drain
	// writes, so a write made here and the same write made through an MCP tool
	// cannot index differently — and there is no second copy of the document
	// rules to drift. A failure is only logged: the trigger already queued the
	// row, so the drain retries it.
	if h.search == nil {
		return
	}
	if err := searchqueue.IndexNow(ctx, h.repo.pool, h.search, "secret", s.ID.String()); err != nil {
		slog.Warn("secret: indexed later by the queue", "id", s.ID, "error", err)
	}
}

// deleteSecretIndex removes a secret from the Typesense index.
func (h *Handler) deleteSecretIndex(ctx context.Context, id string) {
	if h.search == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("secret: panic during search deletion", "id", id, "panic", r)
		}
	}()
	if err := h.search.DeleteDocument(context.Background(), "secret-"+id); err != nil {
		slog.Warn("secret: failed to delete from search index", "id", id, "error", err)
	}
}

// --- request types ---

// createSecretRequest is the JSON body for POST /api/v1/secrets. The client
// sends the encrypted blob (encrypted_data + iv) along with plaintext metadata
// (name, secret_type) and optional entity association.
type createSecretRequest struct {
	EncryptedData []byte `json:"encrypted_data"`
	IV            []byte `json:"iv"`
	KeyVersion    int    `json:"key_version"`
	Name          string `json:"name"`
	SecretType    string `json:"secret_type"`
	EntityType    string `json:"entity_type"`
	EntityID      string `json:"entity_id"`
}

// updateSecretRequest is the JSON body for PATCH /api/v1/secrets/:id. All four
// fields are required — the client sends the full set of editable fields
// (re-encrypted blob + updated metadata). A partial update that omits
// encrypted_data/iv would leave the stored blob stale, so the handler requires
// all four to be present.
//
// A new blob may mean a new value, so a credential shared with agents (#4446)
// loses its server copy on every PATCH unless the request also carries
// agent_value (the new plaintext object, owner/family_manager only), which
// re-seals it in the same UPDATE.
type updateSecretRequest struct {
	EncryptedData []byte          `json:"encrypted_data"`
	IV            []byte          `json:"iv"`
	Name          string          `json:"name"`
	SecretType    string          `json:"secret_type"`
	AgentValue    json.RawMessage `json:"agent_value,omitempty"`
}

// setupKeyRequest is the JSON body for POST /api/v1/secrets/setup. The client
// derives a key from the master password (PBKDF2 + salt), computes a verifier
// from it, and sends verifier + salt + version. The API stores only an
// argon2id hash of the verifier.
//
// Verifier (base64) is the preferred field. KeyHash is the pre-051 name for
// the same value, still accepted so the current UI keeps working; when both
// are sent, Verifier wins.
type setupKeyRequest struct {
	Verifier   []byte `json:"verifier"`
	KeyHash    []byte `json:"key_hash"` // deprecated alias of Verifier
	KeySalt    []byte `json:"key_salt"`
	KeyVersion int    `json:"key_version"`
}

// verifyKeyRequest is the JSON body for POST /api/v1/secrets/verify. The client
// re-derives the key from the entered master password, computes the same
// verifier, and sends it. Same Verifier / KeyHash aliasing as setup.
type verifyKeyRequest struct {
	Verifier   []byte `json:"verifier"`
	KeyHash    []byte `json:"key_hash"` // deprecated alias of Verifier
	KeyVersion int    `json:"key_version"`
}

// clientVerifier returns the verifier a request carries, preferring the new
// field name over the deprecated key_hash alias.
func clientVerifier(verifier, keyHash []byte) []byte {
	if len(verifier) > 0 {
		return verifier
	}
	return keyHash
}

// writeInvalidMasterKey writes the body /verify uses for "no match": 422
// INVALID_MASTER_KEY. Not 401, because the BFF reads any upstream 401 as an
// expired session and spends a Dex token refresh on it (#4445/#4446).
func writeInvalidMasterKey(w http.ResponseWriter) {
	apierr.JSON(w, http.StatusUnprocessableEntity, apierr.ErrorResponse{
		Error: apierr.ErrorDetail{Code: "INVALID_MASTER_KEY", Message: "invalid master key"},
	})
}

// writeTooManyAttempts writes the 429 /verify sends while a household is
// locked out after too many wrong master passwords.
func writeTooManyAttempts(w http.ResponseWriter, wait time.Duration) {
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	apierr.JSON(w, http.StatusTooManyRequests, apierr.ErrorResponse{
		Error: apierr.ErrorDetail{
			Code:    "TOO_MANY_ATTEMPTS",
			Message: "too many wrong master passwords; try again in " + strconv.Itoa((secs+59)/60) + " minute(s)",
		},
	})
}

// householdID extracts the household UUID from the JWT claims in the request
// context. RequireAuth must have run on the route; if claims are missing the
// request is rejected as forbidden.
func householdID(r *http.Request) (uuid.UUID, error) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		return uuid.Nil, apierr.ErrForbidden
	}
	return uuid.Parse(claims.HouseholdID)
}

// CreateSecret stores a new encrypted secret. The client encrypts the secret
// data client-side and sends the encrypted blob + IV + metadata. The API never
// sees plaintext.
//
// POST /api/v1/secrets
func (h *Handler) CreateSecret(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	var req createSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if len(req.EncryptedData) == 0 {
		apierr.BadRequest(w, "encrypted_data is required")
		return
	}
	if len(req.IV) == 0 {
		apierr.BadRequest(w, "iv is required")
		return
	}
	if req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}
	if req.SecretType == "" {
		apierr.BadRequest(w, "secret_type is required")
		return
	}
	if req.KeyVersion == 0 {
		apierr.BadRequest(w, "key_version is required")
		return
	}

	// Entity association is optional: a secret with no entity belongs to the
	// family, stored as ('household', hid). If another entity_type is given,
	// entity_id must also be given and be a valid UUID — a dangling
	// entity_type with no ID is a caller bug.
	if req.EntityType == "" && req.EntityID == "" {
		req.EntityType = EntityTypeHousehold
	}
	if req.EntityType == EntityTypeHousehold && req.EntityID == "" {
		req.EntityID = hid.String()
	}
	var entityID uuid.UUID
	if req.EntityType != "" {
		if req.EntityID == "" {
			apierr.BadRequest(w, "entity_id is required when entity_type is provided")
			return
		}
		entityID, err = uuid.Parse(req.EntityID)
		if err != nil {
			apierr.BadRequest(w, "invalid entity_id")
			return
		}
	}

	s := &Secret{
		HouseholdID:   hid,
		EntityType:    req.EntityType,
		EntityID:      entityID,
		EncryptedData: req.EncryptedData,
		IV:            req.IV,
		KeyVersion:    req.KeyVersion,
		Name:          req.Name,
		SecretType:    req.SecretType,
	}

	// The owner trigger rejects another family's entity (→ 404).
	created, err := h.repo.CreateSecret(r.Context(), hid, s)
	if err != nil {
		apierr.DB(w, err)
		return
	}

	// Index the secret's plaintext metadata in Typesense for search.
	h.indexSecret(r.Context(), created)

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": created,
	})
}

// ListSecrets returns secrets for the authenticated household. Optional
// entity_type + entity_id query params filter to a specific entity; without
// them every secret in the household is returned. The response contains
// metadata only — encrypted_data is never included in list results.
//
// GET /api/v1/secrets?entity_type=property&entity_id=<uuid>
func (h *Handler) ListSecrets(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	entityType := r.URL.Query().Get("entity_type")
	entityIDStr := r.URL.Query().Get("entity_id")
	var entityID uuid.UUID
	if entityType == EntityTypeHousehold && entityIDStr == "" {
		entityIDStr = hid.String()
	}
	if entityType != "" {
		if entityIDStr == "" {
			apierr.BadRequest(w, "entity_id is required when entity_type is provided")
			return
		}
		entityID, err = uuid.Parse(entityIDStr)
		if err != nil {
			apierr.BadRequest(w, "invalid entity_id")
			return
		}
	}
	// When entityType == "" the repo ignores the entity filter and returns all
	// household secrets (entityID is uuid.Nil / disregarded).

	secrets, err := h.repo.ListSecrets(r.Context(), hid, entityType, entityID)
	if err != nil {
		apierr.DB(w, err)
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": secrets,
	})
}

// GetSecret returns a single secret by ID, including the encrypted_data and iv
// so the browser can decrypt it client-side. The API cannot decrypt — it only
// retrieves the stored blob.
//
// GET /api/v1/secrets/:id
func (h *Handler) GetSecret(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid secret id")
		return
	}

	s, err := h.repo.GetSecret(r.Context(), hid, id)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if s == nil {
		apierr.NotFound(w, "secret not found")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": s,
	})
}

// UpdateSecret replaces the editable fields of an existing secret: the
// encrypted blob (encrypted_data + iv) and the plaintext metadata (name,
// secret_type). All four fields must be present in the request body — the
// client re-encrypts and sends the full set.
//
// PATCH /api/v1/secrets/:id
func (h *Handler) UpdateSecret(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid secret id")
		return
	}

	var req updateSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if len(req.EncryptedData) == 0 {
		apierr.BadRequest(w, "encrypted_data is required")
		return
	}
	if len(req.IV) == 0 {
		apierr.BadRequest(w, "iv is required")
		return
	}
	if req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}
	if req.SecretType == "" {
		apierr.BadRequest(w, "secret_type is required")
		return
	}

	s := &Secret{
		EncryptedData: req.EncryptedData,
		IV:            req.IV,
		Name:          req.Name,
		SecretType:    req.SecretType,
	}

	// Agent copy: re-seal the new value when given, otherwise drop it.
	var agentBlob []byte
	if len(req.AgentValue) > 0 && string(req.AgentValue) != "null" {
		if !canShareWithAgents(r) {
			apierr.Forbidden(w, "only an owner or family manager can share a credential with agents")
			return
		}
		if h.agentKey == nil {
			writeSharingDisabled(w)
			return
		}
		plain, msg := agentValue(req.AgentValue)
		if msg != "" {
			apierr.BadRequest(w, "agent_"+msg)
			return
		}
		agentBlob, err = h.agentKey.Seal(hid, id, plain)
		clear(plain)
		if err != nil {
			apierr.InternalError(w, err)
			return
		}
	}

	updated, err := h.repo.UpdateSecret(r.Context(), hid, id, s, agentBlob, actorUserID(r))
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if updated == nil {
		apierr.NotFound(w, "secret not found")
		return
	}

	// Re-index the updated secret metadata.
	h.indexSecret(r.Context(), updated)

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": updated,
	})
}

// DeleteSecret removes a secret. Returns 204 on success. A missing or
// cross-tenant secret returns 404 — the two cases are indistinguishable to
// avoid leaking cross-tenant existence.
//
// DELETE /api/v1/secrets/:id
func (h *Handler) DeleteSecret(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid secret id")
		return
	}

	if err := h.repo.DeleteSecret(r.Context(), hid, id); err != nil {
		// The repo contract is: returns pgx.ErrNoRows only when the secret
		// does not exist or belongs to a different household. Any other error
		// is a wrapped DB failure (pool closed, ctx deadline, connection
		// lost) and must surface as a 500 so it is logged and not silently
		// dropped.
		if errors.Is(err, pgx.ErrNoRows) {
			apierr.NotFound(w, "secret not found")
			return
		}
		apierr.InternalError(w, err)
		return
	}

	// Remove from search index.
	h.deleteSecretIndex(r.Context(), id.String())

	w.WriteHeader(http.StatusNoContent)
}

// SetupKey stores the master-password verifier + salt for first-time setup.
// This is only allowed if no key exists yet for the given household + key
// version — calling setup twice returns 409 Conflict. The client-sent verifier
// is derived from the PBKDF2 key (not the key itself); the API stores only
// argon2id(verifier) with a random salt, never the verifier as sent.
//
// The response carries id, household_id, key_salt, key_version and
// created_at; never the verifier or its hash.
//
// POST /api/v1/secrets/setup
func (h *Handler) SetupKey(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	var req setupKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	verifier := clientVerifier(req.Verifier, req.KeyHash)
	if len(verifier) == 0 {
		apierr.BadRequest(w, "verifier is required")
		return
	}
	if !validClientVerifier(verifier) {
		apierr.BadRequest(w, "verifier must be 16 to 128 bytes")
		return
	}
	if len(req.KeySalt) == 0 {
		apierr.BadRequest(w, "key_salt is required")
		return
	}
	if req.KeyVersion == 0 {
		apierr.BadRequest(w, "key_version is required")
		return
	}

	// Only allowed if no key exists yet for this household + version. This is
	// the first-time-setup guard: a second call to setup with the same version
	// is rejected with 409. Key rotation (setting up a new version while an old
	// one exists) is a future feature and would use a different endpoint.
	existing, err := h.repo.GetKey(r.Context(), hid, req.KeyVersion)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if existing != nil {
		apierr.Conflict(w, "master key already set up for this version")
		return
	}

	encoded, err := hashVerifier(r.Context(), verifier)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	created, err := h.repo.SetupKey(r.Context(), hid, encoded, req.KeySalt, req.KeyVersion)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Lost a race with a concurrent setup for the same version.
			apierr.Conflict(w, "master key already set up for this version")
			return
		}
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusCreated, map[string]any{
		"data": created,
	})
}

// GetKeyInfo returns the stored PBKDF2 salt + key version for the authenticated
// household. The browser needs the salt to re-derive the encryption key from
// the user's master password during the unlock flow — the salt is stored
// server-side because it is per-household and must be stable across sessions.
//
// This endpoint deliberately returns ONLY { key_salt, key_version } — never the
// stored verifier (or legacy key_hash). Those are used only by /verify;
// exposing them would let an attacker attempt offline guesses. Returns 404 if no master key has been set up yet for this
// household (the client should redirect to the setup flow).
//
// GET /api/v1/secrets/key
func (h *Handler) GetKeyInfo(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	// Default to version 1 — the only version currently supported. Future key
	// rotation would accept a ?key_version query param; for now the client
	// always asks for the current key.
	keyVersion := 1

	stored, err := h.repo.GetKey(r.Context(), hid, keyVersion)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if stored == nil {
		// No key set up for this household — 404 so the client can redirect
		// to the setup flow. /verify answers 422 for "does not match".
		apierr.NotFound(w, "master key not set up")
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"key_salt":    stored.KeySalt,
			"key_version": stored.KeyVersion,
		},
	})
}

// verifierEvaluated is called each time VerifyKey compares a client verifier
// with the stored one (argon2id or legacy). Tests replace it to count guesses.
var verifierEvaluated = func() {}

// VerifyKey checks the client-sent verifier against the household's stored
// verifier for the given key version. The client re-derives the key from the
// entered master password (PBKDF2 + stored salt) and sends the verifier
// computed from it.
//
//   - Upgraded / new rows: argon2id(verifier, stored salt) is compared with the
//     stored hash in constant time. A match on a row with outdated cost
//     parameters is rehashed (best effort).
//   - Legacy rows (set up before migration 051): the verifier is compared with
//     key_hash in constant time; on a match the row is upgraded to an argon2id
//     verifier and key_hash is cleared, in one compare-and-swap UPDATE. A failed
//     upgrade is logged (household id only) and the unlock still succeeds; the
//     next unlock retries it.
//
// Returns 200 on a match; 422 INVALID_MASTER_KEY on a mismatch (never 401:
// the BFF treats a 401 as an expired session); 404 if no key is set up; 429
// TOO_MANY_ATTEMPTS (with Retry-After) after verifyMaxFailures wrong attempts
// by the member, or verifyHouseholdMaxFailures by the whole household, within
// verifyFailWindow (see ratelimit.go). Attempts are reserved before argon2id
// runs, so concurrent requests cannot exceed those limits.
//
// POST /api/v1/secrets/verify
func (h *Handler) VerifyKey(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}

	var req verifyKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	verifier := clientVerifier(req.Verifier, req.KeyHash)
	if len(verifier) == 0 {
		apierr.BadRequest(w, "verifier is required")
		return
	}
	if req.KeyVersion == 0 {
		apierr.BadRequest(w, "key_version is required")
		return
	}

	stored, err := h.repo.GetKey(r.Context(), hid, req.KeyVersion)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if stored == nil {
		// No key set up for this household + version. The client should
		// go to the setup flow (GET /secrets/key answers the same 404).
		apierr.NotFound(w, "master key not set up")
		return
	}
	// Reserve the attempt before any comparison: the check and the
	// reservation are one atomic step, so concurrent guesses cannot all pass
	// the limit while argon2id is still running for the first ones.
	uid := uuid.Nil
	if id := actorUserID(r); id != nil {
		uid = *id
	}
	attempt, wait := h.limiter.begin(hid, uid)
	if attempt == nil {
		writeTooManyAttempts(w, wait)
		return
	}
	// Any exit that neither fails nor succeeds (500, cancelled request) gives
	// the reservation back uncounted.
	defer attempt.release()

	// A verifier outside the accepted length can never match a stored one;
	// reject it before spending an argon2id computation on it.
	if !validClientVerifier(verifier) {
		attempt.fail()
		writeInvalidMasterKey(w)
		return
	}

	switch {
	case stored.Verifier != nil:
		verifierEvaluated()
		ok, needsRehash, err := checkVerifier(r.Context(), *stored.Verifier, verifier)
		if err != nil {
			apierr.InternalError(w, err)
			return
		}
		if !ok {
			attempt.fail()
			writeInvalidMasterKey(w)
			return
		}
		if needsRehash {
			h.rehashVerifier(r.Context(), stored, verifier)
		}

	case len(stored.KeyHash) > 0:
		verifierEvaluated()
		if subtle.ConstantTimeCompare(verifier, stored.KeyHash) != 1 {
			attempt.fail()
			writeInvalidMasterKey(w)
			return
		}
		h.upgradeLegacyKey(r.Context(), stored, verifier)

	default:
		// A row with neither (only possible after a 051 down) never matches.
		attempt.fail()
		writeInvalidMasterKey(w)
		return
	}
	attempt.succeed()

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"verified": true,
		},
	})
}

// upgradeLegacyKey replaces a verified legacy key_hash with an argon2id
// verifier. Best effort: errors are logged without any key material.
func (h *Handler) upgradeLegacyKey(ctx context.Context, stored *SecretKey, verifier []byte) {
	encoded, err := hashVerifier(ctx, verifier)
	if err != nil {
		slog.Warn("secret: legacy key upgrade: hash failed", "household_id", stored.HouseholdID, "error", err)
		return
	}
	if _, err := h.repo.UpgradeLegacyKey(ctx, stored.HouseholdID, stored.ID, stored.KeyHash, encoded); err != nil {
		slog.Warn("secret: legacy key upgrade failed", "household_id", stored.HouseholdID, "error", err)
		return
	}
}

// rehashVerifier rewrites a verified argon2id verifier with the current cost
// parameters. Best effort, logged without key material.
func (h *Handler) rehashVerifier(ctx context.Context, stored *SecretKey, verifier []byte) {
	encoded, err := hashVerifier(ctx, verifier)
	if err != nil {
		slog.Warn("secret: verifier rehash: hash failed", "household_id", stored.HouseholdID, "error", err)
		return
	}
	if _, err := h.repo.ReplaceVerifier(ctx, stored.HouseholdID, stored.ID, *stored.Verifier, encoded); err != nil {
		slog.Warn("secret: verifier rehash failed", "household_id", stored.HouseholdID, "error", err)
	}
}
