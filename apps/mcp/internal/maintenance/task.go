package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Task status values as stored (task_status enum) and as filtered by
// maintenance_find. "pending" in the catalog means not finished yet
// (pending or in_progress); "done" means status done.
const (
	statusPending    = "pending"
	statusInProgress = "in_progress"
	statusDone       = "done"
	statusSkipped    = "skipped"
)

// taskSelect loads a task with the names of what it is about, in the column
// order scanTask reads.
const taskSelect = `SELECT t.id, t.name, t.description, t.status::text, t.due_date,
	t.property_id, p.name, t.asset_id, a.name, t.vehicle_id,
	NULLIF(CONCAT_WS(' ', v.year::text, v.make, v.model), ''),
	t.vendor_id, ve.name, t.cost::float8, t.notes, t.completed_at, t.schedule_id,
	t.created_at, t.updated_at
	FROM maintenance_tasks t
	LEFT JOIN properties p ON p.id = t.property_id
	LEFT JOIN assets a ON a.id = t.asset_id
	LEFT JOIN vehicles v ON v.id = t.vehicle_id
	LEFT JOIN vendors ve ON ve.id = t.vendor_id`

// taskRow is a maintenance_tasks row as the tools load it.
type taskRow struct {
	ID           uuid.UUID
	Name         string
	Description  *string
	Status       string
	DueDate      *time.Time
	PropertyID   *uuid.UUID
	PropertyName *string
	AssetID      *uuid.UUID
	AssetName    *string
	VehicleID    *uuid.UUID
	VehicleName  *string
	VendorID     *uuid.UUID
	VendorName   *string
	Cost         *float64
	Notes        *string
	CompletedAt  *time.Time
	ScheduleID   *uuid.UUID
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// scanTask reads one row of taskSelect (plus any trailing dest).
func scanTask(row pgx.Row, extra ...any) (taskRow, error) {
	var r taskRow
	dest := []any{&r.ID, &r.Name, &r.Description, &r.Status, &r.DueDate,
		&r.PropertyID, &r.PropertyName, &r.AssetID, &r.AssetName, &r.VehicleID, &r.VehicleName,
		&r.VendorID, &r.VendorName, &r.Cost, &r.Notes, &r.CompletedAt, &r.ScheduleID,
		&r.CreatedAt, &r.UpdatedAt}
	err := row.Scan(append(dest, extra...)...)
	return r, err
}

// loadTask reads one task of the household; FOR UPDATE locks it in a tx.
func loadTask(ctx context.Context, q querier, householdID string, id uuid.UUID, forUpdate bool) (taskRow, error) {
	sql := taskSelect + ` WHERE t.id = $1 AND t.household_id = $2`
	if forUpdate {
		sql += ` FOR UPDATE OF t`
	}
	return scanTask(q.QueryRow(ctx, sql, id, householdID))
}

// taskData is one upkeep task in every maintenance tool's output. IDs are
// strings (a uuid.UUID would be advertised as a byte array).
type taskData struct {
	ID               string    `json:"id" jsonschema_description:"The task_id for maintenance_update, maintenance_complete or maintenance_delete."`
	Name             string    `json:"name"`
	Description      *string   `json:"description"`
	Status           string    `json:"status" jsonschema_description:"pending, in_progress, done or skipped."`
	DueDate          *string   `json:"due_date" jsonschema_description:"Due date, YYYY-MM-DD in the family's timezone."`
	DueWeekday       *string   `json:"due_weekday" jsonschema_description:"Weekday of due_date, e.g. Thu."`
	DaysUntilDue     *int      `json:"days_until_due" jsonschema_description:"Whole days from today to due_date; negative when overdue."`
	Overdue          bool      `json:"overdue" jsonschema_description:"true when not done and due_date is before today."`
	PropertyID       *string   `json:"property_id" jsonschema_description:"The home it is for (filled in from the asset when there is one)."`
	PropertyName     *string   `json:"property_name"`
	AssetID          *string   `json:"asset_id"`
	AssetName        *string   `json:"asset_name"`
	VehicleID        *string   `json:"vehicle_id"`
	VehicleName      *string   `json:"vehicle_name"`
	ContactID        *string   `json:"contact_id" jsonschema_description:"The contact (vendor) who does the job."`
	ContactName      *string   `json:"contact_name"`
	Cost             *float64  `json:"cost" jsonschema_description:"What the job cost, in the family's currency."`
	Notes            *string   `json:"notes"`
	CompletedDate    *string   `json:"completed_date" jsonschema_description:"Day it was done, YYYY-MM-DD in the family's timezone."`
	CompletedWeekday *string   `json:"completed_weekday"`
	Recurring        bool      `json:"recurring" jsonschema_description:"true when it belongs to a repeating schedule."`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Duplicate        bool      `json:"duplicate,omitempty" jsonschema_description:"maintenance_create only: the same task was added minutes ago and is returned instead."`
	AlreadyDone      *bool     `json:"already_done,omitempty" jsonschema_description:"maintenance_complete only: true when it was already done and nothing changed."`
	NextTaskID       *string   `json:"next_task_id,omitempty" jsonschema_description:"maintenance_complete only: the next task a repeating job scheduled."`
	NextDueDate      *string   `json:"next_due_date,omitempty" jsonschema_description:"maintenance_complete only: when the next one is due (YYYY-MM-DD)."`
}

func idString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	return ptr(id.String())
}

// finished reports whether the task needs no more work.
func (r taskRow) finished() bool { return r.Status == statusDone || r.Status == statusSkipped }

// data builds the output for r; today is the family's today and loc its
// timezone.
func (r taskRow) data(today time.Time, loc *time.Location) taskData {
	d := taskData{
		ID: r.ID.String(), Name: r.Name, Description: r.Description, Status: r.Status,
		PropertyID: idString(r.PropertyID), PropertyName: r.PropertyName,
		AssetID: idString(r.AssetID), AssetName: r.AssetName,
		VehicleID: idString(r.VehicleID), VehicleName: r.VehicleName,
		ContactID: idString(r.VendorID), ContactName: r.VendorName,
		Cost: r.Cost, Notes: r.Notes, Recurring: r.ScheduleID != nil,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.DueDate != nil {
		due := civil(*r.DueDate)
		days := daysBetween(today, due)
		d.DueDate, d.DueWeekday, d.DaysUntilDue = ptr(due.Format(time.DateOnly)), ptr(shortWeekday(due)), &days
		d.Overdue = !r.finished() && days < 0
	}
	if r.CompletedAt != nil {
		done := civil(r.CompletedAt.In(loc))
		d.CompletedDate, d.CompletedWeekday = ptr(done.Format(time.DateOnly)), ptr(shortWeekday(done))
	}
	return d
}

// about names what the task is for, e.g. " for Furnace", or "".
func (r taskRow) about() string {
	for _, n := range []*string{r.AssetName, r.VehicleName, r.PropertyName} {
		if n != nil && *n != "" {
			return " for " + *n
		}
	}
	return ""
}

// ─────────────────────────────────────────────
// Calendar sync. A task with a due date is an all-day event on the family's
// Maintenance calendar (calendar_objects entity_type 'maintenance_task',
// entity_id = task id), as the api's UpsertMaintenanceEvent does. Writes
// record calendar_changes and bump the calendar ctag so phones sync.
// Deleting the task deletes the event through the 038 rehome trigger.
// ─────────────────────────────────────────────

// syncEvent creates or updates the task's calendar event inside tx and
// returns its id ("" when the family has no calendar at all).
func syncEvent(ctx context.Context, tx pgx.Tx, householdID string, taskID uuid.UUID, name string, description *string, due time.Time) (string, error) {
	start, end := civil(due), civil(due).AddDate(0, 0, 1)
	fields := map[string]any{
		"title":       name,
		"description": "",
		"start":       start.Format(time.RFC3339),
		"end":         end.Format(time.RFC3339),
		"all_day":     true,
		"event_type":  "maintenance",
	}
	if description != nil {
		fields["description"] = *description
	}

	var eventID, calendarID, uid, icalData string
	err := tx.QueryRow(ctx,
		`SELECT co.id::text, co.calendar_id::text, co.uid, co.ical_data
		   FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id
		  WHERE c.household_id = $1 AND co.entity_id = $2
		    AND co.entity_type IN ('maintenance', 'maintenance_task')
		  ORDER BY co.created_at LIMIT 1
		  FOR UPDATE OF co`, householdID, taskID,
	).Scan(&eventID, &calendarID, &uid, &icalData)
	switch {
	case err == nil:
		// Merge into the stored JSON so keys this package doesn't model
		// (the CalDAV service keeps `sequence` there) survive.
		merged := map[string]any{}
		if jerr := json.Unmarshal([]byte(icalData), &merged); jerr != nil {
			merged = map[string]any{}
		}
		if seq, ok := merged["sequence"].(float64); ok {
			merged["sequence"] = seq + 1
		}
		for k, v := range fields {
			merged[k] = v
		}
		b, _ := json.Marshal(merged)
		if _, err := tx.Exec(ctx,
			`UPDATE calendar_objects SET ical_data = $1, etag = $2, event_type = 'maintenance',
			        entity_type = 'maintenance_task', updated_at = NOW()
			  WHERE id = $3`, string(b), uuid.NewString(), eventID); err != nil {
			return "", fmt.Errorf("update calendar event: %w", err)
		}
		return eventID, recordChange(ctx, tx, calendarID, uid, "update")
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return "", fmt.Errorf("find calendar event: %w", err)
	}

	err = tx.QueryRow(ctx,
		`SELECT id::text FROM calendars WHERE household_id = $1
		  ORDER BY (type = 'maintenance') DESC, created_at, id LIMIT 1`, householdID,
	).Scan(&calendarID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find maintenance calendar: %w", err)
	}
	eventID = uuid.NewString()
	b, _ := json.Marshal(fields)
	if _, err := tx.Exec(ctx,
		`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, etag, event_type, entity_type, entity_id)
		 VALUES ($1, $2, $3, $4, $5, 'maintenance', 'maintenance_task', $6)`,
		eventID, calendarID, eventID, string(b), uuid.NewString(), taskID); err != nil {
		return "", fmt.Errorf("create calendar event: %w", err)
	}
	return eventID, recordChange(ctx, tx, calendarID, eventID, "create")
}

// recordChange adds a calendar_changes row and bumps the calendar's ctag.
func recordChange(ctx context.Context, tx pgx.Tx, calendarID, uid, action string) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO calendar_changes (calendar_id, event_uid, action) VALUES ($1, $2, $3)`,
		calendarID, uid, action); err != nil {
		return fmt.Errorf("record calendar change: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE calendars SET ctag = gen_random_uuid()::text, updated_at = NOW() WHERE id = $1`,
		calendarID); err != nil {
		return fmt.Errorf("bump calendar ctag: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────
// Repeating jobs. A task linked to a maintenance_schedules row repeats by
// that schedule's RRULE. nextDue understands the simple rules upkeep uses
// (FREQ=DAILY|WEEKLY|MONTHLY|YEARLY with INTERVAL) and counts from the day
// the job was done; anything else returns ok=false and no next task is made.
// ─────────────────────────────────────────────

func nextDue(done time.Time, rrule string) (time.Time, bool) {
	freq, interval := "", 1
	for _, part := range strings.Split(strings.TrimPrefix(strings.TrimSpace(rrule), "RRULE:"), ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch strings.ToUpper(k) {
		case "FREQ":
			freq = strings.ToUpper(v)
		case "INTERVAL":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 1000 {
				return time.Time{}, false
			}
			interval = n
		case "", "WKST":
		default:
			// BYDAY, COUNT, UNTIL...: not something a simple
			// "every N units" step can honour.
			return time.Time{}, false
		}
	}
	done = civil(done)
	switch freq {
	case "DAILY":
		return done.AddDate(0, 0, interval), true
	case "WEEKLY":
		return done.AddDate(0, 0, 7*interval), true
	case "MONTHLY":
		return addMonths(done, interval), true
	case "YEARLY":
		return addMonths(done, 12*interval), true
	}
	return time.Time{}, false
}

// addMonths adds n months, clamping the day to the target month's length
// (Jan 31 + 1 month is Feb 28/29, not Mar 3).
func addMonths(d time.Time, n int) time.Time {
	first := time.Date(d.Year(), d.Month()+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	day := d.Day()
	if day > last {
		day = last
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
}
