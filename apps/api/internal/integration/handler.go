package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/config"
	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// Handler holds the dependencies needed by integration HTTP handlers.
type Handler struct {
	repo *Repo
	cfg  *config.Config
}

// NewHandler creates a new integration handler.
func NewHandler(repo *Repo, cfg *config.Config) *Handler {
	return &Handler{repo: repo, cfg: cfg}
}

// --- request / response types ---

// connectRequest accepts the config JSON for a given integration type.
// The expected fields depend on the type (see config types in model.go).
type connectRequest struct {
	// Config is a raw JSON object — validated per type in Connect.
	Config json.RawMessage `json:"config"`
}

// integrationResponse is the public shape returned by List.
// Config values are NEVER included.
type integrationResponse struct {
	ID              string  `json:"id"`
	HouseholdID     string  `json:"household_id"`
	Type            string  `json:"type"`
	Status          string  `json:"status"`
	LastHealthCheck *string `json:"last_health_check,omitempty"`
	LastSync        *string `json:"last_sync,omitempty"`
	ErrorMessage    *string `json:"error_message,omitempty"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

// connectedResponse is returned on successful Connect.
type connectedResponse struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

// testResponse is returned by Test.
type testResponse struct {
	Type    string `json:"type"`
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// List returns all integrations for the authenticated household.
// Config values are NEVER exposed in the response.
// GET /api/v1/integrations
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "no claims in context")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	integrations, err := h.repo.GetAll(r.Context(), householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	resp := make([]integrationResponse, 0, len(integrations))
	for _, integ := range integrations {
		resp = append(resp, integrationToResponse(integ))
	}

	apierr.JSON(w, http.StatusOK, map[string]any{"data": resp})
}

// Connect accepts config JSON for the given integration type, encrypts it,
// and saves it. If the integration already exists, it is updated.
// POST /api/v1/integrations/{type}/connect
func (h *Handler) Connect(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "no claims in context")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	integrationType := chi.URLParam(r, "type")
	if !isValidType(integrationType) {
		apierr.BadRequest(w, "invalid integration type: must be one of homeassistant")
		return
	}

	var req connectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}
	if len(req.Config) == 0 {
		apierr.BadRequest(w, "config is required")
		return
	}

	// Validate config shape per type.
	parsed, err := parseAndValidateConfig(integrationType, req.Config)
	if err != nil {
		apierr.BadRequest(w, fmt.Sprintf("invalid config: %v", err))
		return
	}

	// Encrypt the config.
	encrypted, err := EncryptConfig(h.cfg.EncryptionKey, parsed)
	if err != nil {
		apierr.InternalError(w, fmt.Errorf("encrypt config: %w", err))
		return
	}

	created, err := h.repo.Upsert(r.Context(), householdID, integrationType, encrypted)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusOK, connectedResponse{
		ID:     created.ID.String(),
		Type:   created.Type,
		Status: created.Status,
	})
}

// Test performs a health check against the external service using the saved config.
// POST /api/v1/integrations/{type}/test
func (h *Handler) Test(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "no claims in context")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	integrationType := chi.URLParam(r, "type")
	if !isValidType(integrationType) {
		apierr.BadRequest(w, "invalid integration type")
		return
	}

	integ, err := h.repo.GetByType(r.Context(), householdID, integrationType)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if integ == nil || integ.Config == nil || len(integ.Config) == 0 {
		apierr.BadRequest(w, "integration not configured; connect first")
		return
	}

	// Decrypt config and run the type-specific health check.
	result := testIntegration(r.Context(), integrationType, h.cfg.EncryptionKey, integ.Config)

	// Update status based on result.
	status := StatusConnected
	var errorMsg *string
	if !result.Success {
		status = StatusError
		errorMsg = &result.Message
	}
	if err := h.repo.UpdateStatus(r.Context(), householdID, integrationType, status, errorMsg); err != nil {
		// Log but don't fail the request — the test result is still returned.
		log.Printf("integration test: update status for %s: %v", integrationType, err)
	}

	apierr.JSON(w, http.StatusOK, result)
}

// Disconnect clears the config and sets status to disconnected.
// DELETE /api/v1/integrations/{type}
func (h *Handler) Disconnect(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "no claims in context")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	integrationType := chi.URLParam(r, "type")
	if !isValidType(integrationType) {
		apierr.BadRequest(w, "invalid integration type")
		return
	}

	if err := h.repo.Delete(r.Context(), householdID, integrationType); err != nil {
		apierr.InternalError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

func integrationToResponse(i *Integration) integrationResponse {
	r := integrationResponse{
		ID:           i.ID.String(),
		HouseholdID:  i.HouseholdID.String(),
		Type:         i.Type,
		Status:       i.Status,
		ErrorMessage: i.ErrorMessage,
		CreatedAt:    i.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    i.UpdatedAt.Format(time.RFC3339),
	}
	if i.LastHealthCheck != nil {
		s := i.LastHealthCheck.Format(time.RFC3339)
		r.LastHealthCheck = &s
	}
	if i.LastSync != nil {
		s := i.LastSync.Format(time.RFC3339)
		r.LastSync = &s
	}
	return r
}

// isValidType checks that the integration type string matches one of the known types.
func isValidType(t string) bool {
	switch t {
	case TypeHomeAssistant:
		return true
	default:
		return false
	}
}

// parseAndValidateConfig unmarshals the raw JSON config into the correct struct
// for the given integration type and validates it has the required fields.
func parseAndValidateConfig(integrationType string, raw json.RawMessage) (any, error) {
	switch integrationType {
	case TypeHomeAssistant:
		var c HomeAssistantConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		c.BaseURL = strings.TrimRight(c.BaseURL, "/")
		if c.BaseURL == "" {
			return nil, fmt.Errorf("base_url is required")
		}
		if c.Token == "" {
			return nil, fmt.Errorf("token is required")
		}
		return c, nil

	default:
		return nil, fmt.Errorf("unknown integration type: %s", integrationType)
	}
}

// testIntegration performs a type-specific health check against the external service.
func testIntegration(ctx context.Context, integrationType, encryptionKey string, encryptedConfig []byte) testResponse {
	client := &http.Client{Timeout: 10 * time.Second}

	switch integrationType {
	case TypeHomeAssistant:
		var cfg HomeAssistantConfig
		if err := DecryptConfig(encryptionKey, encryptedConfig, &cfg); err != nil {
			return testResponse{Type: integrationType, Success: false, Message: "failed to decrypt config"}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.BaseURL+"/api/", nil)
		if err != nil {
			return testResponse{Type: integrationType, Success: false, Message: err.Error()}
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
		resp, err := client.Do(req)
		if err != nil {
			return testResponse{Type: integrationType, Success: false, Message: err.Error()}
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var haResp map[string]any
		if err := json.Unmarshal(body, &haResp); err != nil {
			return testResponse{Type: integrationType, Success: false, Message: fmt.Sprintf("unexpected response: %s", string(body))}
		}
		if msg, ok := haResp["message"]; ok && msg == "API running." {
			return testResponse{Type: integrationType, Success: true, Message: "Home Assistant is reachable"}
		}
		return testResponse{Type: integrationType, Success: false, Message: fmt.Sprintf("unexpected response: %s", string(body))}

	default:
		return testResponse{Type: integrationType, Success: false, Message: "unknown integration type"}
	}
}
