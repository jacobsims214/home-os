// Package maintenance provides the MCP upkeep tools (catalog v2, KB
// architecture/mcp-tool-catalog.md "maintenance"): maintenance_find,
// maintenance_create, maintenance_update, maintenance_complete and
// maintenance_delete. Every result is built with internal/respond, and every
// tool declares all four annotation hints and an outputSchema. Dates are
// YYYY-MM-DD in the family's timezone. All operations are scoped to the
// authenticated household via JWT claims.
package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/mcp/internal/calendar"
	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

type handlerFunc = func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// Param descriptions shared by create and update (each at most 80 chars).
const (
	descName        = "Short name of the job, e.g. \"Replace furnace filter\""
	descDueDate     = "Due date, YYYY-MM-DD in the family's timezone"
	descDescription = "Details or steps"
	descPropertyID  = "The home it's for (from property_find); not with vehicle_id"
	descAssetID     = "The thing it's for (from asset_find); its home is filled in"
	descVehicleID   = "The car it's for (from vehicle_find); not with property/asset"
	descContactID   = "Who does it (from contact_find)"
	descTaskID      = "The task's id (from maintenance_find)"
)

// location is the family's timezone.
func location(ctx context.Context, pool *pgxpool.Pool, householdID string) *time.Location {
	return calendar.LoadHouseholdLocation(ctx, pool, householdID)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ─────────────────────────────────────────────
// maintenance_find
// ─────────────────────────────────────────────

// NewMaintenanceFindTool creates maintenance_find, which replaces
// list_maintenance_tasks and get_upcoming_maintenance.
func NewMaintenanceFindTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("maintenance_find", toolOptions(classR, "Find upkeep tasks",
		mcp.WithDescription("List upkeep tasks by status, due window, or what they're for (house, asset, car). Use due_within_days for what's coming up."),
		mcp.WithString("id", mcp.Description("A task_id to get exactly that task")),
		mcp.WithString("status", mcp.Description("pending (not done yet, the default), done or all"),
			mcp.Enum("pending", "done", "all")),
		mcp.WithNumber("due_within_days", mcp.Description("Due within this many days from today, overdue included, e.g. 30"),
			mcp.Min(0), mcp.Max(maxDueWithinDays)),
		mcp.WithString("property_id", mcp.Description("Only tasks for this home, its assets included")),
		mcp.WithString("asset_id", mcp.Description("Only tasks for this asset")),
		mcp.WithString("vehicle_id", mcp.Description("Only tasks for this vehicle")),
		mcp.WithString("query", mcp.Description("Words in the name, description or notes, e.g. filter")),
		mcp.WithNumber("limit", mcp.Description("Max tasks to return (default 25, max 100)"), mcp.Min(1), mcp.Max(maxLimit)),
		mcp.WithString("cursor", mcp.Description("next page cursor from the previous result")),
		respond.ListSchema[taskData](),
	)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return authRequired(), nil
		}
		args := req.GetArguments()
		loc := location(ctx, pool, claims.HouseholdID)
		today := todayIn(loc)
		ownJobs, fail := vendorScope(ctx, pool, claims)
		if fail != nil {
			return fail, nil
		}

		if raw := str(args, "id"); raw != "" {
			id, bad := parseID("id", raw, "maintenance_find")
			if bad != nil {
				return bad, nil
			}
			r, err := loadTask(ctx, pool, claims.HouseholdID, id, false)
			if err == nil && ownJobs != nil && (r.VendorID == nil || *r.VendorID != *ownJobs) {
				err = pgx.ErrNoRows // another vendor's job is not there for a linked vendor
			}
			if errors.Is(err, pgx.ErrNoRows) {
				return respond.NotFound("upkeep task", raw, "Call maintenance_find without id to list the family's tasks."), nil
			}
			if err != nil {
				return respond.Failed("load the task", err), nil
			}
			d := r.data(today, loc)
			next := taskHints(d)
			if h := guideHint(ctx, pool, claims, d); h != "" {
				next = append(next, h)
			}
			return respond.List(fmt.Sprintf("Found upkeep task %q%s.", r.Name, r.about()), []taskData{d}, respond.Page{}, next...), nil
		}

		status := strings.ToLower(str(args, "status"))
		if status == "" {
			status = statusPending
		}
		where := []string{"t.household_id = $1"}
		params := []any{claims.HouseholdID}
		add := func(cond string, v any) {
			params = append(params, v)
			where = append(where, fmt.Sprintf(cond, len(params)))
		}
		if ownJobs != nil {
			add("t.vendor_id = $%d", *ownJobs)
		}
		order := "t.due_date ASC NULLS LAST, t.name, t.id"
		switch status {
		case statusPending:
			where = append(where, "t.status IN ('pending', 'in_progress')")
		case statusDone:
			where = append(where, "t.status = 'done'")
			order = "t.completed_at DESC NULLS LAST, t.due_date DESC NULLS LAST, t.id"
		case "all":
		default:
			return respond.Invalid("status", fmt.Sprintf("status %q is not one of pending, done, all.", status),
				"Pass status pending, done or all, or leave it out for pending.", map[string]any{"status": "pending"}), nil
		}
		within, hasWithin, bad := wholeNumber(args, "due_within_days", 0, maxDueWithinDays, map[string]any{"due_within_days": 30})
		if bad != nil {
			return bad, nil
		}
		if hasWithin {
			add("t.due_date <= $%d", today.AddDate(0, 0, within))
		}
		for _, f := range []struct{ name, col, finder string }{
			{"property_id", "t.property_id", "property_find"},
			{"asset_id", "t.asset_id", "asset_find"},
			{"vehicle_id", "t.vehicle_id", "vehicle_find"},
		} {
			if raw := str(args, f.name); raw != "" {
				id, bad := parseID(f.name, raw, f.finder)
				if bad != nil {
					return bad, nil
				}
				add(f.col+" = $%d", id)
			}
		}
		if q := str(args, "query"); q != "" {
			add("(t.name ILIKE $%[1]d OR t.description ILIKE $%[1]d OR t.notes ILIKE $%[1]d)", "%"+likeEscape(q)+"%")
		}
		limit, offset, bad := pageArgs(args)
		if bad != nil {
			return bad, nil
		}

		sql := strings.Replace(taskSelect, "t.created_at, t.updated_at", "t.created_at, t.updated_at, COUNT(*) OVER ()", 1) +
			" WHERE " + strings.Join(where, " AND ") +
			fmt.Sprintf(" ORDER BY %s LIMIT %d OFFSET %d", order, limit, offset)
		rows, err := pool.Query(ctx, sql, params...)
		if err != nil {
			return respond.Failed("list upkeep tasks", err), nil
		}
		defer rows.Close()
		items := []taskData{}
		total, overdue := 0, 0
		for rows.Next() {
			r, err := scanTask(rows, &total)
			if err != nil {
				return respond.Failed("read upkeep tasks", err), nil
			}
			d := r.data(today, loc)
			if d.Overdue {
				overdue++
			}
			items = append(items, d)
		}
		if err := rows.Err(); err != nil {
			return respond.Failed("list upkeep tasks", err), nil
		}
		if len(items) == 0 && offset > 0 {
			// Past the end: report the real total so the model stops paging.
			_ = pool.QueryRow(ctx, "SELECT COUNT(*) FROM maintenance_tasks t WHERE "+strings.Join(where, " AND "), params...).Scan(&total)
		}

		label := map[string]string{statusPending: "pending upkeep", statusDone: "done upkeep", "all": "upkeep"}[status]
		scope := ""
		if hasWithin {
			scope = fmt.Sprintf(" due in the next %d %s or overdue", within, plural(within, "day", "days"))
		}
		if total == 0 {
			return respond.List(fmt.Sprintf("No %s tasks%s.", label, scope), items, respond.Page{},
				"Add one with maintenance_create name=… due_date=YYYY-MM-DD."), nil
		}
		summary := fmt.Sprintf("Found %d %s %s%s.", total, label, plural(total, "task", "tasks"), scope)
		if len(items) < total {
			summary = fmt.Sprintf("Showing %d of %d %s tasks%s.", len(items), total, label, scope)
		}
		if overdue > 0 {
			summary = strings.TrimSuffix(summary, ".") + fmt.Sprintf(" (%d overdue on this page).", overdue)
		}
		var next []string
		if len(items) > 0 {
			next = taskHints(items[0])
			if total == 1 && len(items) == 1 {
				if h := guideHint(ctx, pool, claims, items[0]); h != "" {
					next = append(next, h)
				}
			}
		}
		// respond marks a list truncated whenever Total exceeds the items, so
		// the last page passes no Total (the summary still gives it).
		page := respond.Page{}
		if offset+len(items) < total {
			page.Total = total
			page.NextCursor = respond.EncodeCursor(offset + len(items))
			page.Narrow = "Pass next_cursor as cursor for the next page, or add due_within_days, status or query to narrow."
		}
		return respond.List(summary, items, page, next...), nil
	}
	return "maintenance_find", tool, handler
}

// likeEscape escapes ILIKE wildcards in user text.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// taskHints are the next steps for one task.
func taskHints(d taskData) []string {
	if d.Status == statusDone || d.Status == statusSkipped {
		return []string{fmt.Sprintf("Change it with maintenance_update task_id=%s.", d.ID)}
	}
	return []string{
		fmt.Sprintf("Mark it done with maintenance_complete task_id=%s.", d.ID),
		fmt.Sprintf("Move or rename it with maintenance_update task_id=%s.", d.ID),
	}
}

// ─────────────────────────────────────────────
// maintenance_create
// ─────────────────────────────────────────────

// NewMaintenanceCreateTool creates maintenance_create (was
// create_maintenance_task). repeat_every_days / repeat_every_miles arrive
// with Stage 2 story 602.
func NewMaintenanceCreateTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("maintenance_create", toolOptions(classC, "Add upkeep task",
		mcp.WithDescription("Add an upkeep task for the house, a thing or a car. It appears on the Maintenance calendar. Mark it done later with maintenance_complete."),
		mcp.WithString("name", mcp.Required(), mcp.Description(descName)),
		mcp.WithString("due_date", mcp.Required(), mcp.Description(descDueDate)),
		mcp.WithString("description", mcp.Description(descDescription)),
		mcp.WithString("property_id", mcp.Description(descPropertyID)),
		mcp.WithString("asset_id", mcp.Description(descAssetID)),
		mcp.WithString("vehicle_id", mcp.Description(descVehicleID)),
		mcp.WithString("contact_id", mcp.Description(descContactID)),
		respond.Schema[taskData](),
	)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return authRequired(), nil
		}
		args := req.GetArguments()
		loc := location(ctx, pool, claims.HouseholdID)
		today := todayIn(loc)
		example := map[string]any{"name": "Replace furnace filter", "due_date": today.AddDate(0, 0, 7).Format(time.DateOnly)}

		name, bad := taskName(str(args, "name"), example)
		if bad != nil {
			return bad, nil
		}
		rawDue := str(args, "due_date")
		if rawDue == "" {
			return respond.Invalid("due_date", "due_date is required.",
				"Pass the day it is due as YYYY-MM-DD; use today for a job to do now.", example), nil
		}
		due, bad := parseDate("due_date", rawDue, loc)
		if bad != nil {
			return bad, nil
		}
		var description *string
		if s := str(args, "description"); s != "" {
			description = &s
		}
		subj, bad := readSubject(ctx, pool, claims.HouseholdID, args)
		if bad != nil {
			return bad, nil
		}
		contact, bad := readContact(ctx, pool, claims.HouseholdID, args)
		if bad != nil {
			return bad, nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("add the task", err), nil
		}
		defer tx.Rollback(ctx)

		// Loop guard (§1): the same job for the same thing and day, added
		// minutes ago, comes back flagged duplicate. maintenance_tasks has
		// no author column, so the check is per family. The advisory lock
		// makes two identical parallel calls produce one row.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			"maintenance_create:"+claims.HouseholdID+":"+strings.ToLower(name)); err != nil {
			return respond.Failed("add the task", err), nil
		}
		var dupID uuid.UUID
		err = tx.QueryRow(ctx,
			`SELECT id FROM maintenance_tasks
			  WHERE household_id = $1 AND lower(name) = lower($2) AND due_date = $3
			    AND asset_id IS NOT DISTINCT FROM $4 AND vehicle_id IS NOT DISTINCT FROM $5
			    AND ($4::uuid IS NOT NULL OR property_id IS NOT DISTINCT FROM $6)
			    AND created_at > NOW() - make_interval(secs => $7)
			  ORDER BY created_at DESC LIMIT 1`,
			claims.HouseholdID, name, due, subj.AssetID, subj.VehicleID, subj.PropertyID, duplicateWindow.Seconds(),
		).Scan(&dupID)
		if err == nil {
			r, err := loadTask(ctx, tx, claims.HouseholdID, dupID, false)
			if err != nil {
				return respond.Failed("load the task", err), nil
			}
			d := r.data(today, loc)
			d.Duplicate = true
			return respond.OK(fmt.Sprintf("%q due %s was already added minutes ago; nothing new was added.", r.Name, describeDate(due)), d,
				fmt.Sprintf("To change it use maintenance_update task_id=%s; don't add it again.", d.ID)), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return respond.Failed("add the task", err), nil
		}

		var id uuid.UUID
		if err := tx.QueryRow(ctx,
			`INSERT INTO maintenance_tasks (household_id, property_id, asset_id, vehicle_id, vendor_id, name, description, status, due_date)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8) RETURNING id`,
			claims.HouseholdID, subj.PropertyID, subj.AssetID, subj.VehicleID, contact, name, description, due,
		).Scan(&id); err != nil {
			return respond.Failed("add the task", err), nil
		}
		if _, err := syncEvent(ctx, tx, claims.HouseholdID, id, name, description, due); err != nil {
			return respond.Failed("add the task to the calendar", err), nil
		}
		r, err := loadTask(ctx, tx, claims.HouseholdID, id, false)
		if err != nil {
			return respond.Failed("read back the task", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("add the task", err), nil
		}
		slog.Info("maintenance_create: created", "task_id", id, "household_id", claims.HouseholdID)

		d := r.data(today, loc)
		summary := fmt.Sprintf("Added upkeep task %q%s, due %s.", r.Name, r.about(), describeDate(due))
		if d.Overdue {
			summary += " That date has already passed."
		}
		return respond.OK(summary, d,
			fmt.Sprintf("When it's done: maintenance_complete task_id=%s.", d.ID)), nil
	}
	return "maintenance_create", tool, handler
}

// ─────────────────────────────────────────────
// maintenance_update
// ─────────────────────────────────────────────

// NewMaintenanceUpdateTool creates maintenance_update. Only the given fields
// change. Passing any of property_id / asset_id / vehicle_id replaces the
// task's subject as a whole (a task is about one thing).
func NewMaintenanceUpdateTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("maintenance_update", toolOptions(classU, "Update upkeep task",
		mcp.WithDescription("Change an upkeep task; only the given fields change. Passing property_id, asset_id or vehicle_id replaces what it's for."),
		mcp.WithString("task_id", mcp.Required(), mcp.Description(descTaskID)),
		mcp.WithString("name", mcp.Description(descName)),
		mcp.WithString("due_date", mcp.Description(descDueDate)),
		mcp.WithString("description", mcp.Description(descDescription)),
		mcp.WithString("property_id", mcp.Description(descPropertyID)),
		mcp.WithString("asset_id", mcp.Description(descAssetID)),
		mcp.WithString("vehicle_id", mcp.Description(descVehicleID)),
		mcp.WithString("contact_id", mcp.Description(descContactID)),
		respond.Schema[taskData](),
	)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return authRequired(), nil
		}
		args := req.GetArguments()
		loc := location(ctx, pool, claims.HouseholdID)
		today := todayIn(loc)

		rawID := firstStr(args, "task_id", "id")
		if rawID == "" {
			return respond.Invalid("task_id", "task_id is required.", "Call maintenance_find to get the task_id.",
				map[string]any{"task_id": exampleTaskID, "due_date": today.AddDate(0, 0, 7).Format(time.DateOnly)}), nil
		}
		id, bad := parseID("task_id", rawID, "maintenance_find")
		if bad != nil {
			return bad, nil
		}
		example := map[string]any{"task_id": id.String(), "due_date": today.AddDate(0, 0, 7).Format(time.DateOnly)}

		var changed []string
		var name, description *string
		var due *time.Time
		if s := str(args, "name"); s != "" {
			n, bad := taskName(s, example)
			if bad != nil {
				return bad, nil
			}
			name, changed = &n, append(changed, "name")
		}
		if s := str(args, "due_date"); s != "" {
			d, bad := parseDate("due_date", s, loc)
			if bad != nil {
				return bad, nil
			}
			due, changed = &d, append(changed, "due_date")
		}
		if s := str(args, "description"); s != "" {
			description, changed = &s, append(changed, "description")
		}
		subj, bad := readSubject(ctx, pool, claims.HouseholdID, args)
		if bad != nil {
			return bad, nil
		}
		if subj.given() {
			changed = append(changed, "what it's for")
		}
		contact, bad := readContact(ctx, pool, claims.HouseholdID, args)
		if bad != nil {
			return bad, nil
		}
		if contact != nil {
			changed = append(changed, "contact")
		}
		if len(changed) == 0 {
			return respond.Invalid("", "Nothing to change: pass at least one field besides task_id.",
				"Add the fields to change, e.g. due_date or name.", example), nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("update the task", err), nil
		}
		defer tx.Rollback(ctx)
		cur, err := loadTask(ctx, tx, claims.HouseholdID, id, true)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("upkeep task", rawID, "Call maintenance_find to get a valid task_id."), nil
		}
		if err != nil {
			return respond.Failed("load the task", err), nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE maintenance_tasks SET
			        name = COALESCE($3, name),
			        due_date = COALESCE($4, due_date),
			        description = COALESCE($5, description),
			        property_id = CASE WHEN $6 THEN $7 ELSE property_id END,
			        asset_id = CASE WHEN $6 THEN $8 ELSE asset_id END,
			        vehicle_id = CASE WHEN $6 THEN $9 ELSE vehicle_id END,
			        vendor_id = COALESCE($10, vendor_id),
			        updated_at = NOW()
			  WHERE id = $1 AND household_id = $2`,
			id, claims.HouseholdID, name, due, description,
			subj.given(), subj.PropertyID, subj.AssetID, subj.VehicleID, contact,
		); err != nil {
			return respond.Failed("update the task", err), nil
		}
		r, err := loadTask(ctx, tx, claims.HouseholdID, id, false)
		if err != nil {
			return respond.Failed("read back the task", err), nil
		}
		if (name != nil || due != nil || description != nil) && r.DueDate != nil {
			if _, err := syncEvent(ctx, tx, claims.HouseholdID, id, r.Name, r.Description, *r.DueDate); err != nil {
				return respond.Failed("update the calendar entry", err), nil
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("update the task", err), nil
		}

		d := r.data(today, loc)
		summary := fmt.Sprintf("Updated upkeep task %q (%s).", r.Name, strings.Join(changed, ", "))
		if cur.Name != r.Name {
			summary = fmt.Sprintf("Updated upkeep task %q, was %q (%s).", r.Name, cur.Name, strings.Join(changed, ", "))
		}
		if d.DueDate != nil {
			summary = strings.TrimSuffix(summary, ".") + fmt.Sprintf(". Due %s %s.", *d.DueWeekday, *d.DueDate)
		}
		return respond.OK(summary, d, taskHints(d)...), nil
	}
	return "maintenance_update", tool, handler
}

// ─────────────────────────────────────────────
// maintenance_complete
// ─────────────────────────────────────────────

// NewMaintenanceCompleteTool creates maintenance_complete. A task that is
// already done comes back unchanged with already_done:true. A task linked
// to a repeating schedule adds the next task (once).
func NewMaintenanceCompleteTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("maintenance_complete", toolOptions(classU, "Complete upkeep task",
		mcp.WithDescription("Mark an upkeep task done. A recurring task schedules its next one. Calling it again for the same task changes nothing."),
		mcp.WithString("task_id", mcp.Required(), mcp.Description(descTaskID)),
		mcp.WithString("done_date", mcp.Description("Day it was done, YYYY-MM-DD (default today)")),
		mcp.WithNumber("cost", mcp.Description("What it cost, e.g. 89.50"), mcp.Min(0)),
		mcp.WithNumber("odometer", mcp.Description("Car jobs: odometer reading, e.g. 45210"), mcp.Min(0)),
		mcp.WithString("notes", mcp.Description("What was done, parts used, etc.")),
		respond.Schema[taskData](),
	)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return authRequired(), nil
		}
		args := req.GetArguments()
		loc := location(ctx, pool, claims.HouseholdID)
		today := todayIn(loc)

		rawID := firstStr(args, "task_id", "id")
		if rawID == "" {
			return respond.Invalid("task_id", "task_id is required.", "Call maintenance_find to get the task_id.",
				map[string]any{"task_id": exampleTaskID}), nil
		}
		id, bad := parseID("task_id", rawID, "maintenance_find")
		if bad != nil {
			return bad, nil
		}
		example := map[string]any{"task_id": id.String(), "done_date": today.Format(time.DateOnly)}
		done := today
		if s := str(args, "done_date"); s != "" {
			d, bad := parseDate("done_date", s, loc)
			if bad != nil {
				return bad, nil
			}
			if d.After(today) {
				return respond.Invalid("done_date", fmt.Sprintf("done_date %s is in the future.", s),
					"Pass the day it was actually done (today or earlier), or leave it out for today.", example), nil
			}
			done = d
		}
		cost, bad := money(args, "cost", map[string]any{"task_id": id.String(), "cost": 89.5})
		if bad != nil {
			return bad, nil
		}
		odometer, hasOdometer, bad := wholeNumber(args, "odometer", 0, 10_000_000, map[string]any{"task_id": id.String(), "odometer": 45210})
		if bad != nil {
			return bad, nil
		}
		var extra []string
		if s := str(args, "notes"); s != "" {
			extra = append(extra, s)
		}
		if hasOdometer {
			// maintenance_tasks has no odometer column yet (story 602).
			extra = append(extra, fmt.Sprintf("Odometer: %d", odometer))
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("complete the task", err), nil
		}
		defer tx.Rollback(ctx)
		cur, err := loadTask(ctx, tx, claims.HouseholdID, id, true)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("upkeep task", rawID, "Call maintenance_find to get a valid task_id."), nil
		}
		if err != nil {
			return respond.Failed("load the task", err), nil
		}
		if cur.Status == statusDone {
			d := cur.data(today, loc)
			d.AlreadyDone = ptr(true)
			when := ""
			if d.CompletedDate != nil {
				when = fmt.Sprintf(" on %s %s", *d.CompletedWeekday, *d.CompletedDate)
			}
			return respond.OK(fmt.Sprintf("%q was already marked done%s; nothing changed.", cur.Name, when), d,
				"See what's next with maintenance_find due_within_days=30."), nil
		}

		// Done at local noon, so the stored instant stays on done's date in
		// the family timezone whatever offsets apply.
		completedAt := time.Date(done.Year(), done.Month(), done.Day(), 12, 0, 0, 0, loc)
		if _, err := tx.Exec(ctx,
			`UPDATE maintenance_tasks SET status = 'done', completed_at = $3,
			        cost = COALESCE($4, cost),
			        notes = CASE WHEN $5 = '' THEN notes
			                     WHEN COALESCE(notes, '') = '' THEN $5
			                     ELSE notes || E'\n' || $5 END,
			        updated_at = NOW()
			  WHERE id = $1 AND household_id = $2`,
			id, claims.HouseholdID, completedAt, cost, strings.Join(extra, "\n"),
		); err != nil {
			return respond.Failed("complete the task", err), nil
		}

		var nextID *uuid.UUID
		var nextDate *time.Time
		var rrule *string
		if cur.ScheduleID != nil {
			if err := tx.QueryRow(ctx, `SELECT rrule FROM maintenance_schedules WHERE id = $1 AND household_id = $2`,
				*cur.ScheduleID, claims.HouseholdID).Scan(&rrule); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return respond.Failed("read the task's schedule", err), nil
			}
		}
		if rrule != nil {
			if nd, ok := nextDue(done, *rrule); ok {
				nextID, nextDate, err = scheduleNext(ctx, tx, claims.HouseholdID, cur, nd)
				if err != nil {
					return respond.Failed("schedule the next one", err), nil
				}
			}
		}
		r, err := loadTask(ctx, tx, claims.HouseholdID, id, false)
		if err != nil {
			return respond.Failed("read back the task", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("complete the task", err), nil
		}

		d := r.data(today, loc)
		d.AlreadyDone = ptr(false)
		summary := fmt.Sprintf("Marked %q%s done on %s.", r.Name, r.about(), describeDate(done))
		next := []string{"See what's next with maintenance_find due_within_days=30."}
		if nextID != nil {
			d.NextTaskID, d.NextDueDate = ptr(nextID.String()), ptr(nextDate.Format(time.DateOnly))
			summary += fmt.Sprintf(" Next due %s.", describeDate(*nextDate))
			next = []string{
				fmt.Sprintf("Next due %s (task_id=%s).", describeDate(*nextDate), nextID.String()),
				fmt.Sprintf("Change it with maintenance_update task_id=%s.", nextID.String()),
			}
		} else if rrule != nil {
			next = append(next, "Its schedule's repeat rule is not a simple interval; add the next one with maintenance_create.")
		}
		return respond.OK(summary, d, next...), nil
	}
	return "maintenance_complete", tool, handler
}

// scheduleNext adds the next task of cur's schedule due on due, unless the
// schedule already has a pending task (then that one is returned).
func scheduleNext(ctx context.Context, tx pgx.Tx, householdID string, cur taskRow, due time.Time) (*uuid.UUID, *time.Time, error) {
	var id uuid.UUID
	var existingDue *time.Time
	err := tx.QueryRow(ctx,
		`SELECT id, due_date FROM maintenance_tasks
		  WHERE household_id = $1 AND schedule_id = $2 AND id <> $3 AND status IN ('pending', 'in_progress')
		  ORDER BY due_date NULLS LAST LIMIT 1`,
		householdID, *cur.ScheduleID, cur.ID).Scan(&id, &existingDue)
	if err == nil {
		if existingDue == nil {
			return &id, &due, nil
		}
		d := civil(*existingDue)
		return &id, &d, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	// The asset's property is derived by trigger, so only one of
	// property/asset is written, exactly as on create.
	property := cur.PropertyID
	if cur.AssetID != nil {
		property = nil
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO maintenance_tasks (household_id, schedule_id, property_id, asset_id, vehicle_id, vendor_id, name, description, status, due_date)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending', $9) RETURNING id`,
		householdID, cur.ScheduleID, property, cur.AssetID, cur.VehicleID, cur.VendorID, cur.Name, cur.Description, due,
	).Scan(&id); err != nil {
		return nil, nil, err
	}
	if _, err := syncEvent(ctx, tx, householdID, id, cur.Name, cur.Description, due); err != nil {
		return nil, nil, err
	}
	return &id, &due, nil
}

// ─────────────────────────────────────────────
// maintenance_delete
// ─────────────────────────────────────────────

// NewMaintenanceDeleteTool creates maintenance_delete. The calendar entry
// goes with the task (038 rehome trigger); the change is recorded for
// CalDAV sync. A repeat call finds nothing and deletes nothing.
func NewMaintenanceDeleteTool(pool *pgxpool.Pool) (string, mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("maintenance_delete", toolOptions(classX, "Delete upkeep task",
		mcp.WithDescription("Delete an upkeep task (and its calendar entry)."),
		mcp.WithString("task_id", mcp.Required(), mcp.Description(descTaskID)),
		respond.Schema[taskData](),
	)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return authRequired(), nil
		}
		args := req.GetArguments()
		loc := location(ctx, pool, claims.HouseholdID)
		today := todayIn(loc)

		rawID := firstStr(args, "task_id", "id")
		if rawID == "" {
			return respond.Invalid("task_id", "task_id is required.", "Call maintenance_find to get the task_id.",
				map[string]any{"task_id": exampleTaskID}), nil
		}
		id, bad := parseID("task_id", rawID, "maintenance_find")
		if bad != nil {
			return bad, nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("delete the task", err), nil
		}
		defer tx.Rollback(ctx)
		r, err := loadTask(ctx, tx, claims.HouseholdID, id, true)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("upkeep task", rawID, "It may already be deleted. Call maintenance_find to check."), nil
		}
		if err != nil {
			return respond.Failed("load the task", err), nil
		}
		type event struct{ calendarID, uid string }
		var events []event
		rows, err := tx.Query(ctx,
			`SELECT co.calendar_id::text, co.uid FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id
			  WHERE c.household_id = $1 AND co.entity_id = $2 AND co.entity_type = 'maintenance_task'`,
			claims.HouseholdID, id)
		if err != nil {
			return respond.Failed("delete the task", err), nil
		}
		for rows.Next() {
			var e event
			if err := rows.Scan(&e.calendarID, &e.uid); err != nil {
				rows.Close()
				return respond.Failed("delete the task", err), nil
			}
			events = append(events, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return respond.Failed("delete the task", err), nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM maintenance_tasks WHERE id = $1 AND household_id = $2`, id, claims.HouseholdID); err != nil {
			return respond.Failed("delete the task", err), nil
		}
		for _, e := range events {
			if err := recordChange(ctx, tx, e.calendarID, e.uid, "delete"); err != nil {
				return respond.Failed("delete the task", err), nil
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("delete the task", err), nil
		}
		slog.Info("maintenance_delete: deleted", "task_id", id, "household_id", claims.HouseholdID, "calendar_events", len(events))

		summary := fmt.Sprintf("Deleted upkeep task %q%s.", r.Name, r.about())
		if len(events) > 0 {
			summary = strings.TrimSuffix(summary, ".") + " and its calendar entry."
		}
		return respond.OK(summary, r.data(today, loc), "Add a new one with maintenance_create, or see the rest with maintenance_find."), nil
	}
	return "maintenance_delete", tool, handler
}
