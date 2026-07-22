package calendar

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

type Handler struct {
	repo *Repo
}

func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

// ListCalendars GET /api/v1/calendars
func (h *Handler) ListCalendars(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	cals, err := h.repo.ListCalendars(r.Context(), hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": cals})
}

// CreateCalendar POST /api/v1/calendars
func (h *Handler) CreateCalendar(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	hid, _ := uuid.Parse(claims.HouseholdID)

	var req struct {
		Name       string `json:"name"`
		Type       string `json:"type"`
		Color      string `json:"color"`
		PropertyID string `json:"property_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid body")
		return
	}
	if req.Name == "" {
		apierr.BadRequest(w, "name required")
		return
	}
	if req.Type == "" {
		req.Type = "custom"
	}
	if req.Color == "" {
		req.Color = "#6366f1"
	}
	var propertyID *uuid.UUID
	if req.PropertyID != "" {
		pid, err := uuid.Parse(req.PropertyID)
		if err != nil {
			apierr.BadRequest(w, "invalid property_id")
			return
		}
		propertyID = &pid
	}
	cal, err := h.repo.CreateCalendar(r.Context(), hid, req.Name, req.Type, req.Color, propertyID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusCreated, map[string]any{"data": cal})
}

// ListEvents GET /api/v1/calendars/:id/events
func (h *Handler) ListEvents(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	calID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid calendar id")
		return
	}
	// Verify the target calendar belongs to the caller's household before
	// reading events. 404 (not 403) on mismatch so cross-tenant calendar
	// existence is not leaked.
	cal, err := h.repo.GetCalendarByIDForHousehold(r.Context(), calID, hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if cal == nil {
		apierr.NotFound(w, "calendar not found")
		return
	}
	var start, end *time.Time
	if s := r.URL.Query().Get("start"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err == nil {
			start = &t
		}
	}
	if e := r.URL.Query().Get("end"); e != "" {
		t, err := time.Parse(time.RFC3339, e)
		if err == nil {
			end = &t
		}
	}
	events, err := h.repo.ListEvents(r.Context(), calID, start, end)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": events})
}

// ListAllEvents GET /api/v1/calendars/events — all events for household
func (h *Handler) ListAllEvents(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	hid, _ := uuid.Parse(claims.HouseholdID)

	var start, end *time.Time
	if s := r.URL.Query().Get("start"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err == nil {
			start = &t
		}
	}
	if e := r.URL.Query().Get("end"); e != "" {
		t, err := time.Parse(time.RFC3339, e)
		if err == nil {
			end = &t
		}
	}
	var propertyID *uuid.UUID
	if pidStr := r.URL.Query().Get("property_id"); pidStr != "" {
		pid, err := uuid.Parse(pidStr)
		if err == nil {
			propertyID = &pid
		}
	}
	events, err := h.repo.ListAllEvents(r.Context(), hid, start, end, propertyID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": events})
}

// CreateEvent POST /api/v1/calendars/:id/events
func (h *Handler) CreateEvent(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	calID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid calendar id")
		return
	}
	// Verify the target calendar belongs to the caller's household before
	// writing into it. 404 (not 403) on mismatch so cross-tenant calendar
	// existence is not leaked.
	cal, err := h.repo.GetCalendarByIDForHousehold(r.Context(), calID, hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if cal == nil {
		apierr.NotFound(w, "calendar not found")
		return
	}
	var ev Event
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		apierr.BadRequest(w, "invalid body")
		return
	}
	if ev.Title == "" {
		apierr.BadRequest(w, "title required")
		return
	}
	if ev.End.IsZero() {
		ev.End = ev.Start.Add(time.Hour)
	}
	if ev.EventType == "" {
		ev.EventType = "custom"
	}
	created, err := h.repo.CreateEvent(r.Context(), calID, &ev)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	apierr.JSON(w, http.StatusCreated, map[string]any{"data": created})
}

// DeleteEvent DELETE /api/v1/calendars/:id/events/:eventId
func (h *Handler) DeleteEvent(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "missing auth")
		return
	}
	hid, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	eventID, err := uuid.Parse(chi.URLParam(r, "eventId"))
	if err != nil {
		apierr.BadRequest(w, "invalid event id")
		return
	}
	// Resolve the event's owning calendar, then verify that calendar belongs
	// to the caller's household before deleting. The event ID alone is not
	// enough to scope the check — an attacker could pass any event ID.
	// 404 (not 403) on mismatch so cross-tenant event/calendar existence is
	// not leaked. A genuinely missing event is also 404 here for consistency
	// with the not-owned path (both look identical to the caller).
	calID, err := h.repo.GetEventCalendarID(r.Context(), eventID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if calID == uuid.Nil {
		apierr.NotFound(w, "event not found")
		return
	}
	cal, err := h.repo.GetCalendarByIDForHousehold(r.Context(), calID, hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if cal == nil {
		apierr.NotFound(w, "event not found")
		return
	}
	if err := h.repo.DeleteEvent(r.Context(), eventID); err != nil {
		apierr.InternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
