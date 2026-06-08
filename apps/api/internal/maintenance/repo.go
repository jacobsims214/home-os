package maintenance

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo provides read/write access to the maintenance_schedules and
// maintenance_tasks tables using a pgx connection pool.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo creates a new maintenance repository backed by the given connection pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// ListTasks returns all maintenance tasks for a household, optionally filtered by
// status and/or property_id. Results are ordered by due_date ascending (nulls last)
// followed by created_at.
func (r *Repo) ListTasks(ctx context.Context, householdID uuid.UUID, status *TaskStatus, propertyID *uuid.UUID) ([]*Task, error) {
	var clauses []string
	args := []any{householdID}
	argIdx := 2

	if status != nil && *status != "" {
		clauses = append(clauses, fmt.Sprintf("AND status = $%d", argIdx))
		args = append(args, string(*status))
		argIdx++
	}

	if propertyID != nil {
		clauses = append(clauses, fmt.Sprintf("AND property_id = $%d", argIdx))
		args = append(args, *propertyID)
		argIdx++
	}

	query := fmt.Sprintf(
		`SELECT id, household_id, schedule_id, property_id, asset_id,
		        name, description, status, due_date, completed_at, cost,
		        vendor_id, notes, created_at, updated_at
		 FROM maintenance_tasks
		 WHERE household_id = $1 %s
		 ORDER BY due_date ASC NULLS LAST, created_at DESC`,
		strings.Join(clauses, " "),
	)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	tasks, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Task])
	if err != nil {
		return nil, fmt.Errorf("collect tasks: %w", err)
	}
	return tasks, nil
}

// GetTask returns a single maintenance task by ID. Returns nil if not found.
func (r *Repo) GetTask(ctx context.Context, id uuid.UUID) (*Task, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, schedule_id, property_id, asset_id,
		        name, description, status, due_date, completed_at, cost,
		        vendor_id, notes, created_at, updated_at
		 FROM maintenance_tasks WHERE id = $1`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	defer rows.Close()

	task, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Task])
	if err != nil {
		if pgx.ErrNoRows == err {
			return nil, nil
		}
		return nil, fmt.Errorf("collect task: %w", err)
	}
	return task, nil
}

// CreateTask inserts a new maintenance task and returns the created record.
func (r *Repo) CreateTask(ctx context.Context, t *Task) (*Task, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO maintenance_tasks (household_id, schedule_id, property_id, asset_id,
		        name, description, status, due_date, cost, vendor_id, notes)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING id, household_id, schedule_id, property_id, asset_id,
		           name, description, status, due_date, completed_at, cost,
		           vendor_id, notes, created_at, updated_at`,
		t.HouseholdID, t.ScheduleID, t.PropertyID, t.AssetID,
		t.Name, t.Description, t.Status, t.DueDate, t.Cost, t.VendorID, t.Notes,
	)
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	defer rows.Close()

	task, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Task])
	if err != nil {
		return nil, fmt.Errorf("collect created task: %w", err)
	}
	return task, nil
}

// UpdateTask updates fields on an existing maintenance task identified by id.
// Only non-nil fields in the provided task are applied. Returns the updated record.
func (r *Repo) UpdateTask(ctx context.Context, id uuid.UUID, updates *Task) (*Task, error) {
	var sets []string
	args := []any{id}
	argIdx := 2

	if updates.Name != "" {
		sets = append(sets, fmt.Sprintf("name = $%d", argIdx))
		args = append(args, updates.Name)
		argIdx++
	}
	if updates.Description != nil {
		sets = append(sets, fmt.Sprintf("description = $%d", argIdx))
		args = append(args, *updates.Description)
		argIdx++
	}
	if updates.Status != "" {
		sets = append(sets, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, string(updates.Status))
		argIdx++
	}
	if updates.DueDate != nil {
		sets = append(sets, fmt.Sprintf("due_date = $%d", argIdx))
		args = append(args, *updates.DueDate)
		argIdx++
	}
	if updates.CompletedAt != nil {
		sets = append(sets, fmt.Sprintf("completed_at = $%d", argIdx))
		args = append(args, *updates.CompletedAt)
		argIdx++
	}
	if updates.Cost != nil {
		sets = append(sets, fmt.Sprintf("cost = $%d", argIdx))
		args = append(args, *updates.Cost)
		argIdx++
	}
	if updates.VendorID != nil {
		sets = append(sets, fmt.Sprintf("vendor_id = $%d", argIdx))
		args = append(args, *updates.VendorID)
		argIdx++
	}
	if updates.Notes != nil {
		sets = append(sets, fmt.Sprintf("notes = $%d", argIdx))
		args = append(args, *updates.Notes)
		argIdx++
	}
	if updates.ScheduleID != nil {
		sets = append(sets, fmt.Sprintf("schedule_id = $%d", argIdx))
		args = append(args, *updates.ScheduleID)
		argIdx++
	}
	if updates.PropertyID != nil {
		sets = append(sets, fmt.Sprintf("property_id = $%d", argIdx))
		args = append(args, *updates.PropertyID)
		argIdx++
	}
	if updates.AssetID != nil {
		sets = append(sets, fmt.Sprintf("asset_id = $%d", argIdx))
		args = append(args, *updates.AssetID)
		argIdx++
	}

	if len(sets) == 0 {
		return nil, fmt.Errorf("update task: no fields to update")
	}

	sets = append(sets, "updated_at = NOW()")

	query := fmt.Sprintf(
		`UPDATE maintenance_tasks SET %s WHERE id = $1
		 RETURNING id, household_id, schedule_id, property_id, asset_id,
		           name, description, status, due_date, completed_at, cost,
		           vendor_id, notes, created_at, updated_at`,
		strings.Join(sets, ", "),
	)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("update task: %w", err)
	}
	defer rows.Close()

	task, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Task])
	if err != nil {
		if pgx.ErrNoRows == err {
			return nil, nil
		}
		return nil, fmt.Errorf("collect updated task: %w", err)
	}
	return task, nil
}

// ListSchedules returns all maintenance schedules for a household.
func (r *Repo) ListSchedules(ctx context.Context, householdID uuid.UUID) ([]*Schedule, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, household_id, property_id, asset_id,
		        name, description, rrule, estimated_cost, vendor_id,
		        created_at, updated_at
		 FROM maintenance_schedules
		 WHERE household_id = $1
		 ORDER BY name ASC`,
		householdID,
	)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close()

	schedules, err := pgx.CollectRows(rows, pgx.RowToAddrOfStructByPos[Schedule])
	if err != nil {
		return nil, fmt.Errorf("collect schedules: %w", err)
	}
	return schedules, nil
}

// CreateSchedule inserts a new maintenance schedule and returns the created record.
func (r *Repo) CreateSchedule(ctx context.Context, s *Schedule) (*Schedule, error) {
	rows, err := r.pool.Query(ctx,
		`INSERT INTO maintenance_schedules (household_id, property_id, asset_id,
		        name, description, rrule, estimated_cost, vendor_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, household_id, property_id, asset_id,
		           name, description, rrule, estimated_cost, vendor_id,
		           created_at, updated_at`,
		s.HouseholdID, s.PropertyID, s.AssetID,
		s.Name, s.Description, s.RRule, s.EstimatedCost, s.VendorID,
	)
	if err != nil {
		return nil, fmt.Errorf("create schedule: %w", err)
	}
	defer rows.Close()

	schedule, err := pgx.CollectOneRow(rows, pgx.RowToAddrOfStructByPos[Schedule])
	if err != nil {
		return nil, fmt.Errorf("collect created schedule: %w", err)
	}
	return schedule, nil
}
