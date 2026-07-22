package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"home-os/api/internal/calendar"
	"home-os/api/internal/middleware"
	"home-os/api/pkg/apierr"
)

// calendarSyncer is the subset of calendar.Repo needed for maintenance sync.
type calendarSyncer interface {
	UpsertMaintenanceEvent(ctx context.Context, householdID uuid.UUID, taskID uuid.UUID, propertyID *uuid.UUID, title, description string, dueDate time.Time) error
	DeleteMaintenanceEvent(ctx context.Context, taskID uuid.UUID) error
}

// Handler holds the dependencies needed by maintenance HTTP handlers.
type Handler struct {
	repo    *Repo
	calSync calendarSyncer
}

// NewHandler creates a new maintenance handler.
func NewHandler(repo *Repo) *Handler {
	return &Handler{repo: repo}
}

// WithCalendarRepo sets the calendar repo for bidirectional sync.
func (h *Handler) WithCalendarRepo(cr *calendar.Repo) *Handler {
	h.calSync = cr
	return h
}

// --- task request / response types ---

type createTaskRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	DueDate     *string `json:"due_date,omitempty"` // RFC3339 date
	ScheduleID  *string `json:"schedule_id,omitempty"`
	PropertyID  *string `json:"property_id,omitempty"`
	AssetID     *string `json:"asset_id,omitempty"`
	VehicleID    *string `json:"vehicle_id,omitempty"`
	Cost        *string `json:"cost,omitempty"`
	VendorID    *string `json:"vendor_id,omitempty"`
	Notes       *string `json:"notes,omitempty"`
}

type updateTaskRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
	DueDate     *string `json:"due_date,omitempty"`
	CompletedAt *string `json:"completed_at,omitempty"`
	ScheduleID  *string `json:"schedule_id,omitempty"`
	PropertyID  *string `json:"property_id,omitempty"`
	AssetID     *string `json:"asset_id,omitempty"`
	VehicleID    *string `json:"vehicle_id,omitempty"`
	Cost        *string `json:"cost,omitempty"`
	VendorID    *string `json:"vendor_id,omitempty"`
	Notes       *string `json:"notes,omitempty"`
}

type taskResponse struct {
	ID          string  `json:"id"`
	HouseholdID string  `json:"household_id"`
	ScheduleID  *string `json:"schedule_id,omitempty"`
	PropertyID  *string `json:"property_id,omitempty"`
	AssetID     *string `json:"asset_id,omitempty"`
	VehicleID    *string `json:"vehicle_id,omitempty"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Status      string  `json:"status"`
	DueDate     *string `json:"due_date,omitempty"`
	CompletedAt *string `json:"completed_at,omitempty"`
	Cost        *string `json:"cost,omitempty"`
	VendorID    *string `json:"vendor_id,omitempty"`
	Notes       *string `json:"notes,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// --- schedule request / response types ---

type createScheduleRequest struct {
	Name          string  `json:"name"`
	Description   *string `json:"description,omitempty"`
	RRule         string  `json:"rrule"`
	EstimatedCost *string `json:"estimated_cost,omitempty"`
	PropertyID    *string `json:"property_id,omitempty"`
	AssetID       *string `json:"asset_id,omitempty"`
		VehicleID      *string `json:"vehicle_id,omitempty"`
	VendorID      *string `json:"vendor_id,omitempty"`
}

type scheduleResponse struct {
	ID            string  `json:"id"`
	HouseholdID   string  `json:"household_id"`
	PropertyID    *string `json:"property_id,omitempty"`
	AssetID       *string `json:"asset_id,omitempty"`
		VehicleID      *string `json:"vehicle_id,omitempty"`
	Name          string  `json:"name"`
	Description   *string `json:"description,omitempty"`
	RRule         string  `json:"rrule"`
	EstimatedCost *string `json:"estimated_cost,omitempty"`
	VendorID      *string `json:"vendor_id,omitempty"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

// --- helpers ---

func parseUUID(s *string) (*uuid.UUID, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func uuidStr(id uuid.UUID) string {
	return id.String()
}

func parseDate(s *string) (*time.Time, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	// Accept both full RFC3339 timestamps and date-only strings.
	t, err := time.Parse(time.RFC3339, *s)
	if err == nil {
		return &t, nil
	}
	t, err = time.Parse("2006-01-02", *s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func timePtr(t time.Time) string {
	return t.Format(time.RFC3339)
}

func toTaskResponse(t *Task) taskResponse {
	resp := taskResponse{
		ID:          t.ID.String(),
		HouseholdID: t.HouseholdID.String(),
		Name:        t.Name,
		Description: t.Description,
		Status:      string(t.Status),
		Cost:        t.Cost,
		Notes:       t.Notes,
		CreatedAt:   timePtr(t.CreatedAt),
		UpdatedAt:   timePtr(t.UpdatedAt),
	}
	if t.ScheduleID != nil {
		s := t.ScheduleID.String()
		resp.ScheduleID = &s
	}
	if t.PropertyID != nil {
		s := t.PropertyID.String()
		resp.PropertyID = &s
	}
	if t.AssetID != nil {
		s := t.AssetID.String()
		resp.AssetID = &s
	}
	if t.VehicleID != nil {
		s := t.VehicleID.String()
		resp.VehicleID = &s
	}
	if t.DueDate != nil {
		s := timePtr(*t.DueDate)
		resp.DueDate = &s
	}
	if t.CompletedAt != nil {
		s := timePtr(*t.CompletedAt)
		resp.CompletedAt = &s
	}
	if t.VendorID != nil {
		s := t.VendorID.String()
		resp.VendorID = &s
	}
	return resp
}

func toScheduleResponse(s *Schedule) scheduleResponse {
	resp := scheduleResponse{
		ID:          s.ID.String(),
		HouseholdID: s.HouseholdID.String(),
		Name:        s.Name,
		Description: s.Description,
		RRule:       s.RRule,
		EstimatedCost: s.EstimatedCost,
		CreatedAt:   timePtr(s.CreatedAt),
		UpdatedAt:   timePtr(s.UpdatedAt),
	}
	if s.PropertyID != nil {
		v := s.PropertyID.String()
		resp.PropertyID = &v
	}
	if s.AssetID != nil {
		v := s.AssetID.String()
		resp.AssetID = &v
	}
	if s.VendorID != nil {
		v := s.VendorID.String()
		resp.VendorID = &v
	}
	return resp
}

// ListTasks returns all maintenance tasks for the authenticated household.
// Supports optional query params: status and property_id.
// GET /api/v1/maintenance/tasks.
func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "authentication required")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	var status *TaskStatus
	if s := r.URL.Query().Get("status"); s != "" {
		ts := TaskStatus(s)
		if !ValidTaskStatus(ts) {
			apierr.BadRequest(w, "invalid status filter")
			return
		}
		status = &ts
	}

	var propertyID *uuid.UUID
	if pid := r.URL.Query().Get("property_id"); pid != "" {
		id, err := uuid.Parse(pid)
		if err != nil {
			apierr.BadRequest(w, "invalid property_id")
			return
		}
		propertyID = &id
	}

	tasks, err := h.repo.ListTasks(r.Context(), householdID, status, propertyID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	resp := make([]taskResponse, 0, len(tasks))
	for _, t := range tasks {
		resp = append(resp, toTaskResponse(t))
	}

	apierr.JSON(w, http.StatusOK, map[string]any{"data": resp})
}

// CreateTask creates a new maintenance task for the authenticated household.
// POST /api/v1/maintenance/tasks.
func (h *Handler) CreateTask(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "authentication required")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}

	dueDate, err := parseDate(req.DueDate)
	if err != nil {
		apierr.BadRequest(w, "invalid due_date format; use RFC3339 or YYYY-MM-DD")
		return
	}

	scheduleID, err := parseUUID(req.ScheduleID)
	if err != nil {
		apierr.BadRequest(w, "invalid schedule_id")
		return
	}
	propertyID, err := parseUUID(req.PropertyID)
	if err != nil {
		apierr.BadRequest(w, "invalid property_id")
		return
	}
	assetID, err := parseUUID(req.AssetID)
	if err != nil {
		apierr.BadRequest(w, "invalid asset_id")
		return
	}
	vehicleID, err := parseUUID(req.VehicleID)
	if err != nil {
		apierr.BadRequest(w, "invalid vehicle_id")
		return
	}
	vendorID, err := parseUUID(req.VendorID)
	if err != nil {
		apierr.BadRequest(w, "invalid vendor_id")
		return
	}

	task := &Task{
		HouseholdID: householdID,
		Name:        req.Name,
		Description: req.Description,
		Status:      TaskStatusPending,
		DueDate:     dueDate,
		ScheduleID:  scheduleID,
		PropertyID:  propertyID,
		AssetID:     assetID,
		VehicleID:   vehicleID,
		Cost:        req.Cost,
		VendorID:    vendorID,
		Notes:       req.Notes,
	}

	created, err := h.repo.CreateTask(r.Context(), task)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Sync to calendar if the task has a due date.
	h.syncToCalendar(r.Context(), householdID, created)

	apierr.JSON(w, http.StatusCreated, map[string]any{"data": toTaskResponse(created)})
}

// UpdateTask updates an existing maintenance task.
// Only the fields present in the JSON body are applied.
// PATCH /api/v1/maintenance/tasks/:id.
func (h *Handler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "authentication required")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	taskID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid task id")
		return
	}

	// Fetch existing task to verify it belongs to the user's household.
	existing, err := h.repo.GetTask(r.Context(), taskID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if existing == nil {
		apierr.NotFound(w, "task not found")
		return
	}
	if existing.HouseholdID.String() != claims.HouseholdID {
		apierr.Forbidden(w, "task does not belong to your household")
		return
	}

	var req updateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	updates := &Task{}

	if req.Name != nil {
		updates.Name = *req.Name
	}
	if req.Description != nil {
		updates.Description = req.Description
	}
	if req.Status != nil {
		ts := TaskStatus(*req.Status)
		if !ValidTaskStatus(ts) {
			apierr.BadRequest(w, "invalid status; must be one of: pending, in_progress, done, skipped")
			return
		}
		updates.Status = ts

		// Auto-set completed_at when transitioning to done.
		if ts == TaskStatusDone {
			now := time.Now()
			updates.CompletedAt = &now
		}
	}
	if req.DueDate != nil {
		dt, err := parseDate(req.DueDate)
		if err != nil {
			apierr.BadRequest(w, "invalid due_date format; use RFC3339 or YYYY-MM-DD")
			return
		}
		updates.DueDate = dt
	}
	if req.CompletedAt != nil {
		ct, err := parseDate(req.CompletedAt)
		if err != nil {
			apierr.BadRequest(w, "invalid completed_at format")
			return
		}
		updates.CompletedAt = ct
	}
	if req.Cost != nil {
		updates.Cost = req.Cost
	}
	if req.VendorID != nil {
		id, err := parseUUID(req.VendorID)
		if err != nil {
			apierr.BadRequest(w, "invalid vendor_id")
			return
		}
		updates.VendorID = id
	}
	if req.Notes != nil {
		updates.Notes = req.Notes
	}
	if req.ScheduleID != nil {
		id, err := parseUUID(req.ScheduleID)
		if err != nil {
			apierr.BadRequest(w, "invalid schedule_id")
			return
		}
		updates.ScheduleID = id
	}
	if req.PropertyID != nil {
		id, err := parseUUID(req.PropertyID)
		if err != nil {
			apierr.BadRequest(w, "invalid property_id")
			return
		}
		updates.PropertyID = id
	}
	if req.AssetID != nil {
		id, err := parseUUID(req.AssetID)
		if err != nil {
			apierr.BadRequest(w, "invalid asset_id")
			return
		}
		updates.AssetID = id
	}
	if req.VehicleID != nil {
		id, err := parseUUID(req.VehicleID)
		if err != nil {
			apierr.BadRequest(w, "invalid vehicle_id")
			return
		}
		updates.VehicleID = id
	}

	updated, err := h.repo.UpdateTask(r.Context(), taskID, updates)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}
	if updated == nil {
		apierr.NotFound(w, "task not found")
		return
	}

	// Sync to calendar if the task has a due date.
	h.syncToCalendar(r.Context(), householdID, updated)

	apierr.JSON(w, http.StatusOK, map[string]any{"data": toTaskResponse(updated)})
}

// DeleteTask removes a maintenance task.
// DELETE /api/v1/maintenance/tasks/:id
func (h *Handler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "authentication required")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	taskID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		apierr.BadRequest(w, "invalid task id")
		return
	}

	if err := h.repo.DeleteTask(r.Context(), taskID, householdID); err != nil {
		apierr.InternalError(w, err)
		return
	}

	// Delete the linked calendar event (if any).
	if h.calSync != nil {
		if err := h.calSync.DeleteMaintenanceEvent(r.Context(), taskID); err != nil {
			slog.Warn("maintenance: failed to delete calendar event", "task_id", taskID, "error", err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// syncToCalendar creates or updates a calendar event for a maintenance task
// that has a due date. If the task has no due date, any existing calendar
// event is deleted. Errors are logged but never fail the request — calendar
// sync is best-effort.
func (h *Handler) syncToCalendar(ctx context.Context, householdID uuid.UUID, task *Task) {
	if h.calSync == nil {
		return
	}
	if task.DueDate == nil {
		// Task has no due date — remove any existing calendar event.
		if err := h.calSync.DeleteMaintenanceEvent(ctx, task.ID); err != nil {
			slog.Warn("maintenance: failed to delete calendar event for task without due date", "task_id", task.ID, "error", err)
		}
		return
	}
	desc := ""
	if task.Description != nil {
		desc = *task.Description
	}
	if err := h.calSync.UpsertMaintenanceEvent(ctx, householdID, task.ID, task.PropertyID, task.Name, desc, *task.DueDate); err != nil {
		slog.Warn("maintenance: failed to sync calendar event", "task_id", task.ID, "error", err)
	}
}

// ListSchedules returns all maintenance schedules for the authenticated household.
// GET /api/v1/maintenance/schedules.
func (h *Handler) ListSchedules(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "authentication required")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	schedules, err := h.repo.ListSchedules(r.Context(), householdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	resp := make([]scheduleResponse, 0, len(schedules))
	for _, s := range schedules {
		resp = append(resp, toScheduleResponse(s))
	}

	apierr.JSON(w, http.StatusOK, map[string]any{"data": resp})
}

// CreateSchedule creates a new maintenance schedule for the authenticated household.
// POST /api/v1/maintenance/schedules.
func (h *Handler) CreateSchedule(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		apierr.Forbidden(w, "authentication required")
		return
	}

	householdID, err := uuid.Parse(claims.HouseholdID)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	var req createScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.BadRequest(w, "invalid request body")
		return
	}

	if req.Name == "" {
		apierr.BadRequest(w, "name is required")
		return
	}
	if req.RRule == "" {
		apierr.BadRequest(w, "rrule is required")
		return
	}

	propertyID, err := parseUUID(req.PropertyID)
	if err != nil {
		apierr.BadRequest(w, "invalid property_id")
		return
	}
	assetID, err := parseUUID(req.AssetID)
	if err != nil {
		apierr.BadRequest(w, "invalid asset_id")
		return
	}
	vendorID, err := parseUUID(req.VendorID)
	if err != nil {
		apierr.BadRequest(w, "invalid vendor_id")
		return
	}

	schedule := &Schedule{
		HouseholdID:   householdID,
		Name:          req.Name,
		Description:   req.Description,
		RRule:         req.RRule,
		EstimatedCost: req.EstimatedCost,
		PropertyID:    propertyID,
		AssetID:       assetID,
		VendorID:      vendorID,
	}

	created, err := h.repo.CreateSchedule(r.Context(), schedule)
	if err != nil {
		apierr.InternalError(w, err)
		return
	}

	apierr.JSON(w, http.StatusCreated, map[string]any{"data": toScheduleResponse(created)})
}
