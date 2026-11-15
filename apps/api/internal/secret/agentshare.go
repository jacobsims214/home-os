package secret

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/auth"
	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// maxAgentValueBytes bounds the plaintext a member can share with agents.
// Real credentials (login, card, api_key, short note) are far smaller.
const maxAgentValueBytes = 8 << 10

// WithAgentKey enables "shared with agents" (#4446). Without it the share
// endpoints answer 503 and a PATCH can't carry agent_value; unsharing always
// works.
func (h *Handler) WithAgentKey(k *AgentKey) *Handler {
	h.agentKey = k
	return h
}

// canShareWithAgents: only an owner or family_manager may share a credential
// with agents, or turn sharing off (decision #4439).
func canShareWithAgents(r *http.Request) bool {
	c := middleware.ClaimsFromContext(r.Context())
	return c != nil && (c.Role == auth.RoleOwner || c.Role == auth.RoleFamilyManager)
}

// actorUserID is the caller's users.id, or nil for an identity that isn't one
// (the auth-disabled local-dev user).
func actorUserID(r *http.Request) *uuid.UUID {
	c := middleware.ClaimsFromContext(r.Context())
	if c == nil {
		return nil
	}
	id, err := uuid.Parse(c.UserID)
	if err != nil {
		return nil
	}
	return &id
}

// agentValue validates the plaintext a client shares: a non-empty JSON object
// (the same object the browser encrypts, e.g. {"username","password","url"})
// of at most maxAgentValueBytes. It is returned compacted. Error messages
// never echo the value.
func agentValue(raw json.RawMessage) ([]byte, string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, "value is required"
	}
	if len(raw) > maxAgentValueBytes {
		return nil, "value is too large (max 8 KiB)"
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, "value must be a JSON object, e.g. {\"username\": \"...\", \"password\": \"...\"}"
	}
	if len(obj) == 0 {
		return nil, "value must not be empty"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, "value must be a JSON object"
	}
	return buf.Bytes(), ""
}

func writeSharingDisabled(w http.ResponseWriter) {
	apierr.JSON(w, http.StatusServiceUnavailable, apierr.ErrorResponse{
		Error: apierr.ErrorDetail{
			Code:    "AGENT_SHARING_DISABLED",
			Message: "sharing credentials with agents is not configured on this server",
		},
	})
}

// shareRequest is the body of PUT /api/v1/secrets/{id}/agent-share. value is
// the credential's decrypted payload, sent once over TLS. It is never logged,
// indexed, echoed back or stored unencrypted.
type shareRequest struct {
	Value json.RawMessage `json:"value"`
}

// ShareWithAgents turns on "shared with agents" for one credential: the API
// seals value with the household key and stores it in agent_ciphertext.
// Sharing again replaces the stored copy (e.g. after a password change).
//
//   - 200 {data: secret metadata, shared_with_agents: true}
//   - 400 bad value, 403 not owner/family_manager, 404 unknown secret,
//     503 AGENT_SHARING_DISABLED (no AGENT_CREDENTIALS_KEY on the server)
//
// PUT /api/v1/secrets/{id}/agent-share
func (h *Handler) ShareWithAgents(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}
	if !canShareWithAgents(r) {
		apierr.Forbidden(w, "only an owner or family manager can share a credential with agents")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid secret id")
		return
	}
	if h.agentKey == nil {
		writeSharingDisabled(w)
		return
	}
	var req shareRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4*maxAgentValueBytes)).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	plain, msg := agentValue(req.Value)
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	blob, err := h.agentKey.Seal(hid, id, plain)
	clear(plain)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	item, err := h.repo.ShareWithAgents(r.Context(), hid, id, blob, actorUserID(r))
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if item == nil {
		apierr.NotFound(w, "secret not found")
		return
	}
	slog.Info("secret: shared with agents", "household_id", hid, "secret_id", id)
	apierr.JSON(w, http.StatusOK, map[string]any{"data": item})
}

// UnshareWithAgents turns sharing off: the server-held copy is deleted in the
// same request. Idempotent; it works even when the server has no agent key.
//
// DELETE /api/v1/secrets/{id}/agent-share
func (h *Handler) UnshareWithAgents(w http.ResponseWriter, r *http.Request) {
	hid, err := householdID(r)
	if err != nil {
		apierr.Forbidden(w, "missing household context")
		return
	}
	if !canShareWithAgents(r) {
		apierr.Forbidden(w, "only an owner or family manager can change agent sharing")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid secret id")
		return
	}
	item, err := h.repo.UnshareWithAgents(r.Context(), hid, id)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if item == nil {
		apierr.NotFound(w, "secret not found")
		return
	}
	slog.Info("secret: unshared with agents", "household_id", hid, "secret_id", id)
	apierr.JSON(w, http.StatusOK, map[string]any{"data": item})
}
