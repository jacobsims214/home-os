package notification

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
	"home-os/api/pkg/smtp"
)

// Handler holds the dependencies needed by notification HTTP handlers.
type Handler struct {
	repo       *Repo
	smtpClient *smtp.Client
}

// NewHandler creates a new notification handler.
func NewHandler(repo *Repo, smtpClient *smtp.Client) *Handler {
	return &Handler{repo: repo, smtpClient: smtpClient}
}

// --- request / response types ---

type createNotificationRequest struct {
	Type       string  `json:"type"`
	Title      string  `json:"title"`
	Body       string  `json:"body"`
	EntityType *string `json:"entity_type,omitempty"`
	EntityID   *string `json:"entity_id,omitempty"`
}

type notificationResponse struct {
	ID           string     `json:"id"`
	HouseholdID  string     `json:"household_id"`
	Type         string     `json:"type"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	EntityType   *string    `json:"entity_type,omitempty"`
	EntityID     *string    `json:"entity_id,omitempty"`
	ReadAt       *string    `json:"read_at,omitempty"`
	CreatedAt    string     `json:"created_at"`
}

// toNotificationResponse converts a Notification domain model to a response DTO.
func toNotificationResponse(n *Notification) notificationResponse {
	resp := notificationResponse{
		ID:           n.ID,
		HouseholdID:  n.HouseholdID,
		Type:         n.Type,
		Title:        n.Title,
		Body:         n.Body,
		CreatedAt:    n.CreatedAt.Format(http.TimeFormat),
	}
	if n.EntityType != nil {
		resp.EntityType = n.EntityType
	}
	if n.EntityID != nil {
		resp.EntityID = n.EntityID
	}
	if n.ReadAt != nil {
		s := n.ReadAt.Format(http.TimeFormat)
		resp.ReadAt = &s
	}
	return resp
}

// List returns all unread notifications for the authenticated household.
// GET /api/v1/notifications
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "authentication required")
		return
	}

	notifications, err := h.repo.ListUnread(r.Context(), claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	resp := make([]notificationResponse, 0, len(notifications))
	for _, n := range notifications {
		resp = append(resp, toNotificationResponse(n))
	}

	apierr.JSON(w, http.StatusOK, map[string]any{"data": resp})
}

// Create creates a new notification and optionally sends an email.
// POST /api/v1/notifications
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "authentication required")
		return
	}

	var req createNotificationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Type == "" || req.Title == "" {
		apierr.BadRequest(w, "type and title are required")
		return
	}

	n, err := h.repo.Create(r.Context(), claims.HouseholdID, req.Type, req.Title, req.Body, req.EntityType, req.EntityID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Send email for bill_due and maintenance_due notifications (best-effort).
	if claims.Email != "" {
		SendNotificationEmail(h.smtpClient, claims.Email, n.Type, n.Title, n.Body)
	}

	apierr.JSON(w, http.StatusCreated, map[string]any{"data": toNotificationResponse(n)})
}

// MarkRead marks a notification as read.
// PATCH /api/v1/notifications/{id}/read
func (h *Handler) MarkRead(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "authentication required")
		return
	}

	idStr := chi.URLParam(r, "id")
	if idStr == "" {
		apierr.BadRequest(w, "notification id is required")
		return
	}

	id, err := uuid.Parse(idStr)
	if err != nil {
		apierr.BadRequest(w, "invalid notification id format")
		return
	}

	// Verify the notification belongs to the authenticated household
	// by checking if it exists and updating it
	if err := h.repo.MarkRead(r.Context(), id.String(), claims.HouseholdID); err != nil {
		apierr.InternalError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
