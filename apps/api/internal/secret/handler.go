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
// password (PBKDF2 + per-household salt), hashes it, and sends the hash. The
// API stores key_hash (not the key) and compares hashes on verify — this lets
// the API confirm the user entered the correct master password without ever
// holding the encryption key itself.
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
//	SetupKey(ctx, householdID uuid.UUID, keyHash, keySalt []byte, keyVersion int) (*SecretKey, error)
//	    Inserts a secret_keys row. Returns the created SecretKey.
//	GetKey(ctx, householdID uuid.UUID, keyVersion int) (*SecretKey, error)
//	    Returns the key_hash + key_salt for verification. Returns (nil, nil) if
//	    no key exists for the given household + version.
package secret

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"home-os/api/internal/middleware"
	"home-os/api/internal/search"
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
	repo   *Repo
	search searchIndexer
}

// NewHandler creates a new secret handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

// WithSearchClient sets the Typesense client for search indexing.
func (h *Handler) WithSearchClient(sc *search.Client) *Handler {
	h.search = sc
	return h
}

// indexSecret indexes a secret's plaintext metadata (name, secret_type) into
// Typesense so it appears in global search. The encrypted data is never indexed.
func (h *Handler) indexSecret(ctx context.Context, s *Secret) {
	if h.search == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("secret: panic during search indexing", "id", s.ID, "panic", r)
		}
	}()
	tags := []string{s.SecretType}
	if s.EntityType != "" {
		tags = append(tags, s.EntityType)
	}

	// For search indexing, use the parent entity's type and ID so that
	// clicking a search result navigates to the entity detail page where
	// the secret can be viewed. If the secret has no parent entity, use
	// the secret's own ID with entity_type "secret" (non-clickable).
	indexPathType := "secret"
	indexPathID := s.ID.String()
	if s.EntityType != "" && s.EntityID != uuid.Nil {
		indexPathType = s.EntityType
		indexPathID = s.EntityID.String()
	}

	doc := search.SearchDocument{
		ID:          "secret-" + s.ID.String(),
		HouseholdID: s.HouseholdID.String(),
		EntityType:  indexPathType,
		EntityID:    indexPathID,
		Title:       s.Name,
		Body:        s.SecretType,
		Tags:        tags,
		CreatedAt:   s.CreatedAt.Unix(),
		UpdatedAt:   s.UpdatedAt.Unix(),
	}
	indexCtx := context.Background()
	if err := h.search.IndexDocument(indexCtx, doc); err != nil {
		slog.Warn("secret: failed to index in search", "id", s.ID, "error", err)
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
type updateSecretRequest struct {
	EncryptedData []byte `json:"encrypted_data"`
	IV            []byte `json:"iv"`
	Name          string `json:"name"`
	SecretType    string `json:"secret_type"`
}

// setupKeyRequest is the JSON body for POST /api/v1/secrets/setup. The client
// derives a key from the master password (PBKDF2 + salt), hashes it, and sends
// the hash + salt + version. The API stores these for later verification.
type setupKeyRequest struct {
	KeyHash    []byte `json:"key_hash"`
	KeySalt    []byte `json:"key_salt"`
	KeyVersion int    `json:"key_version"`
}

// verifyKeyRequest is the JSON body for POST /api/v1/secrets/verify. The client
// re-derives the key from the entered master password, hashes it, and sends the
// hash. The API compares it to the stored key_hash.
type verifyKeyRequest struct {
	KeyHash    []byte `json:"key_hash"`
	KeyVersion int    `json:"key_version"`
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

	// Entity association is optional (standalone secrets have no entity). But
	// if entity_type is provided, entity_id must also be provided and must be a
	// valid UUID — a dangling entity_type with no ID is a caller bug.
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

	created, err := h.repo.CreateSecret(r.Context(), hid, s)
	if err != nil {
		apierr.InternalError(w, err)
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
		apierr.InternalError(w, err)
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

	updated, err := h.repo.UpdateSecret(r.Context(), hid, id, s)
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

// SetupKey stores the master-password key hash + salt for first-time setup.
// This is only allowed if no key exists yet for the given household + key
// version — calling setup twice returns 409 Conflict. The key_hash is a hash
// of the PBKDF2-derived encryption key (not the key itself), used by VerifyKey
// to confirm the user entered the correct master password without the API ever
// holding the encryption key.
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

	if len(req.KeyHash) == 0 {
		apierr.BadRequest(w, "key_hash is required")
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

	created, err := h.repo.SetupKey(r.Context(), hid, req.KeyHash, req.KeySalt, req.KeyVersion)
	if err != nil {
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
// key_hash. The hash is a verifier used only by /verify for constant-time
// comparison; exposing it to the client would let an attacker attempt offline
// hash comparisons. Returns 404 if no master key has been set up yet for this
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
		// to the setup flow. Distinguish from /verify's 401: here the question
		// is "does a key exist?" (404 = no), not "does this hash match?"
		// (401 = no).
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

// VerifyKey checks whether the provided key_hash matches the stored key_hash
// for the given household + key version. The client re-derives the key from
// the entered master password (PBKDF2 + stored salt), hashes it, and sends the
// hash. The API compares the hashes — a match means the user entered the
// correct master password.
//
// Returns 200 if the hashes match, 401 if they don't (or if no key is set up
// yet). A constant-time comparison is used as defense-in-depth even though the
// compared values are hashes, not raw secrets.
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

	if len(req.KeyHash) == 0 {
		apierr.BadRequest(w, "key_hash is required")
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
		// No key set up for this household + version — not a match. Return
		// 401 per the spec ("Return 200 if match, 401 if not"). The client
		// should redirect to the setup flow.
		apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{
				Code:    "UNAUTHORIZED",
				Message: "master key not set up",
			},
		})
		return
	}

	// Constant-time comparison as defense-in-depth. The compared values are
	// hashes of derived keys, not raw keys, so timing leakage would only
	// reveal hash bytes — but ConstantTimeCompare is a one-liner with no
	// downside.
	if subtle.ConstantTimeCompare(req.KeyHash, stored.KeyHash) != 1 {
		apierr.JSON(w, http.StatusUnauthorized, apierr.ErrorResponse{
			Error: apierr.ErrorDetail{
				Code:    "UNAUTHORIZED",
				Message: "invalid master key",
			},
		})
		return
	}

	apierr.JSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"verified": true,
		},
	})
}
