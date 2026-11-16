package notification

import (
	"encoding/json"
	"errors"
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
	ID          string  `json:"id"`
	HouseholdID string  `json:"household_id"`
	Type        string  `json:"type"`
	Title       string  `json:"title"`
	Body        string  `json:"body"`
	EntityType  *string `json:"entity_type,omitempty"`
	EntityID    *string `json:"entity_id,omitempty"`
	ReadAt      *string `json:"read_at,omitempty"`
	CreatedAt   string  `json:"created_at"`
}

// toNotificationResponse converts a Notification domain model to a response DTO.
func toNotificationResponse(n *Notification) notificationResponse {
	resp := notificationResponse{
		ID:          n.ID,
		HouseholdID: n.HouseholdID,
		Type:        n.Type,
		Title:       n.Title,
		Body:        n.Body,
		CreatedAt:   n.CreatedAt.Format(http.TimeFormat),
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

// List returns the caller's unread notifications: family-wide ones and their
// own personal ones, limited to what their current role may see.
// GET /api/v1/notifications
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "authentication required")
		return
	}

	notifications, err := h.repo.ListUnread(r.Context(), claims.HouseholdID, callerUser(claims.UserID), claims.Role)
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

	if req.EntityID != nil {
		if _, err := uuid.Parse(*req.EntityID); err != nil {
			apierr.BadRequest(w, "invalid entity_id")
			return
		}
	}

	n, err := h.repo.Create(r.Context(), claims.HouseholdID, req.Type, req.Title, req.Body, req.EntityType, req.EntityID)
	if err != nil {
		// notifications.entity_type is entity_kind with an owner check
		// (migration 070): an unknown kind is 400, a record of another
		// family (or a missing one) 404.
		apierr.DB(w, err)
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

	// Only a notification the caller may see is marked; any other id is 404,
	// whether it is missing, another family's or member's, or hidden from
	// their current role.
	if err := h.repo.MarkRead(r.Context(), id.String(), claims.HouseholdID, callerUser(claims.UserID), claims.Role); err != nil {
		if errors.Is(err, apierr.ErrNotFound) {
			apierr.NotFound(w, "notification not found")
			return
		}
		apierr.InternalError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
