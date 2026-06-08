// Package maintenance manages maintenance schedules and tasks for home upkeep.
// It provides domain models and database repositories for the maintenance_schedules
// and maintenance_tasks tables.
package maintenance

import (
	"time"

	"github.com/google/uuid"
)

// TaskStatus represents the current state of a maintenance task.
// Maps to the task_status PostgreSQL enum: pending, in_progress, done, skipped.
type TaskStatus string

// Valid task status transitions: pending → in_progress → done.
const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusDone       TaskStatus = "done"
	TaskStatusSkipped    TaskStatus = "skipped"
)

// ValidTaskStatus returns true if the given status is one of the defined constants.
func ValidTaskStatus(s TaskStatus) bool {
	switch s {
	case TaskStatusPending, TaskStatusInProgress, TaskStatusDone, TaskStatusSkipped:
		return true
	default:
		return false
	}
}

// Schedule represents a recurring maintenance schedule from the maintenance_schedules table.
type Schedule struct {
	ID            uuid.UUID
	HouseholdID   uuid.UUID
	PropertyID    *uuid.UUID
	AssetID       *uuid.UUID
	Name          string
	Description   *string
	RRule         string
	EstimatedCost *string
	VendorID      *uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Task represents a single maintenance task from the maintenance_tasks table.
type Task struct {
	ID           uuid.UUID
	HouseholdID  uuid.UUID
	ScheduleID   *uuid.UUID
	PropertyID   *uuid.UUID
	AssetID      *uuid.UUID
	Name         string
	Description  *string
	Status       TaskStatus
	DueDate      *time.Time
	CompletedAt  *time.Time
	Cost         *string
	VendorID     *uuid.UUID
	Notes        *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
