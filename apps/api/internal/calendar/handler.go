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

// parseRange reads the optional start/end (RFC 3339) query parameters. With
// both, recurring events are expanded into occurrences in [start, end), so
// the window is bounded. It returns a client error message, or "".
func parseRange(r *http.Request) (EventRange, string) {
	var rg EventRange
	for _, p := range []struct {
		name string
		dst  **time.Time
	}{{"start", &rg.Start}, {"end", &rg.End}} {
		v := r.URL.Query().Get(p.name)
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return rg, "invalid " + p.name + ": use RFC 3339, e.g. 2026-11-15T00:00:00Z"
		}
		*p.dst = &t
	}
	if rg.expands() {
		if !rg.End.After(*rg.Start) {
			return rg, "end must be after start"
		}
		if rg.End.Sub(*rg.Start) > maxExpandRange {
			return rg, "range too long: at most 3 years"
		}
	}
	return rg, ""
}

// writeEventError answers a create/update error: 400 with the message for
// invalid input (bad or unsupported RRULE, timezone, reminders), 500
// otherwise.
func writeEventError(w http.ResponseWriter, err error) {
	if msg, ok := IsInvalidEvent(err); ok {
		apierr.BadRequest(w, msg)
		return
	}
	apierr.InternalError(w, err)
}

// ListEvents GET /api/v1/calendars/:id/events[?start=&end=]
//
// With both start and end, recurring events are returned as their
// occurrences in [start, end) (overrides and EXDATEs applied), each with the
// series id and a recurrence_id, sorted by start.
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
	rg, msg := parseRange(r)
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	events, err := h.repo.ListEvents(r.Context(), calID, rg, h.repo.HouseholdLocation(r.Context(), hid))
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

	rg, msg := parseRange(r)
	if msg != "" {
		apierr.BadRequest(w, msg)
		return
	}
	var propertyID *uuid.UUID
	if pidStr := r.URL.Query().Get("property_id"); pidStr != "" {
		pid, err := uuid.Parse(pidStr)
		if err == nil {
			propertyID = &pid
		}
	}
	events, err := h.repo.ListAllEvents(r.Context(), hid, rg, propertyID, h.repo.HouseholdLocation(r.Context(), hid))
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
	if ev.EventType == "" {
		ev.EventType = "custom"
	}
	ev.remindersSet = true
	created, err := h.repo.CreateEvent(r.Context(), calID, &ev, h.repo.HouseholdLocation(r.Context(), hid))
	if err != nil {
		writeEventError(w, err)
		return
	}
	apierr.JSON(w, http.StatusCreated, map[string]any{"data": created})
}

// UpdateEvent PATCH /api/v1/calendars/:id/events/:eventId
//
// Partial update: omitted fields keep their value. "rrule": "" ends the
// recurrence, "reminders": [] removes the reminders. ical_data, ical_raw and
// rrule are rewritten together.
func (h *Handler) UpdateEvent(w http.ResponseWriter, r *http.Request) {
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
	eventID, err := uuid.Parse(chi.URLParam(r, "eventId"))
	if err != nil {
		apierr.BadRequest(w, "invalid event id")
		return
	}
	// 404 for a calendar of another household, and for an event that is
	// not in this calendar, so neither existence leaks.
	cal, err := h.repo.GetCalendarByIDForHousehold(r.Context(), calID, hid)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if cal == nil {
		apierr.NotFound(w, "event not found")
		return
	}
	var p EventPatch
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		apierr.BadRequest(w, "invalid body")
		return
	}
	updated, err := h.repo.UpdateEvent(r.Context(), calID, eventID, p, h.repo.HouseholdLocation(r.Context(), hid))
	if err != nil {
		writeEventError(w, err)
		return
	}
	if updated == nil {
		apierr.NotFound(w, "event not found")
		return
	}
	apierr.JSON(w, http.StatusOK, map[string]any{"data": updated})
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
