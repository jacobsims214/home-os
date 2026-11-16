package calendar

// event_create, event_update and event_delete. Every write stores ical_data,
// ical_raw and rrule together (see recurrence.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mark3labs/mcp-go/mcp"

	"home-os/icalx"
	"home-os/mcp/internal/respond"
	"home-os/mcp/internal/server"
)

// duplicateWindow is the loop guard: an identical create within this window
// returns the existing event instead of a second one.
const duplicateWindow = "10 minutes"

// recurringDeleteNote is added to event_delete's result for a whole series.
const recurringDeleteNote = "This was a repeating event: the whole series was deleted, not just one occurrence."

// lockedEdit is returned for a stored event the tools can't rewrite safely.
const lockedEdit = "This event came from a phone in a form that can't be edited here (it holds single changed occurrences without their series). Nothing was changed."

// eventEdits are the optional fields of a create or update.
type eventEdits struct {
	title, description, location *string
	start, end                   *string

	rep              *Repeat
	rawRule          string
	repSet, repClear bool

	exceptDates []string
	exceptSet   bool

	reminders    []int
	remindersSet bool

	people    []EventPerson
	peopleSet bool
}

// seriesOnly reports whether the edit touches only series-level fields, so
// it needs no occurrence (scope all is implied).
func (e eventEdits) seriesOnly() bool {
	return e.title == nil && e.description == nil && e.location == nil && e.start == nil && e.end == nil &&
		!e.remindersSet && !e.peopleSet
}

// parseRecurrenceArgs reads repeat, except_dates, reminder_minutes and
// person_ids into e.
func parseRecurrenceArgs(ctx context.Context, q querier, householdID string, args map[string]any, e *eventEdits) *mcp.CallToolResult {
	var bad *mcp.CallToolResult
	if e.rep, e.rawRule, e.repSet, e.repClear, bad = repeatArg(args); bad != nil {
		return bad
	}
	e.exceptDates, e.exceptSet = stringList(args, "except_dates")
	if e.reminders, e.remindersSet, bad = reminderArg(args); bad != nil {
		return bad
	}
	if refs, ok := stringList(args, "person_ids"); ok {
		if e.people, bad = resolvePeople(ctx, q, householdID, refs); bad != nil {
			return bad
		}
		e.peopleSet = true
	}
	return nil
}

// applyRule sets (or clears) m's rule from the edit. It returns the days the
// start moved to land on a listed weekday.
func applyRule(obj *icalx.Object, e eventEdits, hh *time.Location) (int, *mcp.CallToolResult) {
	m := obj.Master
	if !e.repSet {
		return 0, nil
	}
	if e.repClear {
		m.RRule, m.RDates, m.ExDates, obj.Overrides = "", nil, nil, nil
		return 0, nil
	}
	if !m.AllDay {
		toZone(m, hh)
	}
	rule := e.rawRule
	if e.rep != nil {
		var err error
		if rule, err = repeatRule(e.rep, m, hh); err != nil {
			return 0, respond.Invalid("repeat", err.Error()+".",
				"Pass freq (daily, weekly, monthly, yearly) and optionally interval, by_day, until (YYYY-MM-DD) or count.",
				map[string]any{"repeat": map[string]any{"freq": "weekly", "by_day": []string{"TU", "TH"}, "until": "2026-12-15"}})
		}
	}
	if rule != m.RRule {
		// A different pattern: per-occurrence changes and skips referred
		// to the old instances, so they go.
		m.ExDates, m.RDates, obj.Overrides = nil, nil, nil
	}
	m.RRule = rule
	return alignWeekly(m, hh), nil
}

// applyExtras applies except_dates, reminders and people to the master (and,
// for reminders and people, to every override: scope all means all).
func applyExtras(obj *icalx.Object, e eventEdits, hh *time.Location) *mcp.CallToolResult {
	m := obj.Master
	if e.exceptSet {
		if len(e.exceptDates) > 0 && !m.IsRecurring() {
			return respond.Invalid("except_dates", "except_dates only applies to a repeating event.",
				"Add repeat, or leave except_dates out.", nil)
		}
		rids, bad := exceptInstances(obj, e.exceptDates, hh)
		if bad != nil {
			return bad
		}
		m.ExDates = rids
		dropOverrides(obj, rids)
	}
	for _, ev := range append([]*icalx.Event{m}, obj.Overrides...) {
		if e.remindersSet {
			ev.Alarms = alarmsFor(e.reminders)
		}
		if e.peopleSet {
			setPeople(ev, e.people)
		}
	}
	return nil
}

// masterJSON writes the master's fields into the stored JSON (keeping keys
// the tools don't model and bumping `sequence` when present). people is
// stored when set.
func masterJSON(existing string, m *icalx.Event, people []EventPerson, peopleSet bool) (string, error) {
	if existing == "" {
		var err error
		if existing, err = marshalEventFields("", "", "", "", "", false); err != nil {
			return "", err
		}
	}
	s, e := jsonTimes(m)
	title, desc, where, allDay := m.Summary, m.Description, m.Location, m.AllDay
	out, err := mergeEventFields(existing, &title, &desc, &s, &e, &where, &allDay)
	if err != nil || !peopleSet {
		return out, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return "", err
	}
	if len(people) == 0 {
		delete(doc, "people")
	} else {
		b, _ := json.Marshal(people)
		doc["people"] = b
	}
	b, err := json.Marshal(doc)
	return string(b), err
}

// occurrenceEvent shows one occurrence (rid, override ov or nil) of series
// ev as an Event.
func occurrenceEvent(ev Event, m *icalx.Event, rid time.Time, ov *icalx.Event, hh *time.Location) Event {
	src := m
	start, end := realTime(m, rid, hh), realTime(m, instanceEnd(m, rid), hh)
	if ov != nil {
		src = ov
		start, end = realTime(ov, ov.Start, hh), realTime(ov, ov.End, hh)
		if !ov.End.After(ov.Start) {
			end = start.Add(time.Hour)
		}
	}
	out := ev
	out.Title, out.Description, out.Location = src.Summary, src.Description, src.Location
	out.Start, out.End = start.In(hh).Format(time.RFC3339), end.In(hh).Format(time.RFC3339)
	out.ReminderMinutes = remindersOf(src.Alarms)
	out.People = peopleFromAttendees(src)
	if m.AllDay {
		out.RecurrenceID = realTime(m, rid, hh).Format(dateLayout)
	} else {
		out.RecurrenceID = realTime(m, rid, hh).In(hh).Format(time.RFC3339)
	}
	setLocalDay(&out, hh)
	return out
}

// ─────────────────────────────────────────────
// event_create
// ─────────────────────────────────────────────

// NewEventCreateTool creates event_create (replaces create_event).
func NewEventCreateTool(pool *pgxpool.Pool) (mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("event_create", toolOptions(classC, "Create event",
		mcp.WithDescription("Add an event. Returns it plus any events it overlaps. Use repeat for recurring events, never one call per day. reminder_minutes makes phones alert. calendar_id defaults to Family."),
		mcp.WithString("title", mcp.Description("Event title, e.g. Dentist"), mcp.Required()),
		mcp.WithString("start", mcp.Description("ISO 8601 with offset, e.g. 2026-11-19T17:30:00-05:00; a date if all_day"), mcp.Required()),
		mcp.WithString("end", mcp.Description("End of the first occurrence; if all_day the last day, e.g. 2026-11-21"), mcp.Required()),
		mcp.WithString("calendar_id", mcp.Description("Calendar name or id (default Family)")),
		mcp.WithBoolean("all_day", mcp.Description("Whole days in the family timezone (default false)")),
		mcp.WithString("location", mcp.Description("Where, e.g. Lincoln Elementary")),
		mcp.WithString("description", mcp.Description("Notes for the event")),
		mcp.WithString("event_type", mcp.Description("Event type (default event)")),
		mcp.WithArray("person_ids", mcp.Description("Who it is for: person ids from person_find, or names"), mcp.WithStringItems()),
		mcp.WithObject("repeat", mcp.Description("Repeat rule, e.g. {freq:weekly, by_day:[TU,TH], until:2026-12-15}"), mcp.Properties(repeatSchema(false))),
		mcp.WithArray("except_dates", mcp.Description("Occurrence dates to skip (YYYY-MM-DD), with repeat"), mcp.WithStringItems()),
		mcp.WithArray("reminder_minutes", mcp.Description("Phone alerts, minutes before start, e.g. [30]"), mcp.WithNumberItems()),
		respond.Schema[eventWriteResult](),
	)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return authRequired(), nil
		}
		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)
		args := req.GetArguments()
		title := strings.TrimSpace(getStringArg(args, "title"))
		startArg := strings.TrimSpace(getStringArg(args, "start"))
		endArg := strings.TrimSpace(getStringArg(args, "end"))
		allDay := getBoolArg(args, "all_day", false)
		description := getStringArg(args, "description")
		location := getStringArg(args, "location")
		eventType := strings.TrimSpace(getStringArg(args, "event_type"))
		if eventType == "" {
			eventType = "event"
		}

		example := map[string]any{"title": "Dentist", "start": exampleTime(loc), "end": exampleTimePlus(loc, time.Hour)}
		for _, f := range []struct{ name, val string }{{"title", title}, {"start", startArg}, {"end", endArg}} {
			if f.val == "" {
				return respond.Invalid(f.name, f.name+" is required.",
					"Pass title, start and end (ISO 8601 with offset; plain dates with all_day:true).", example), nil
			}
		}

		cal, bad := resolveCalendar(ctx, pool, claims.HouseholdID, "calendar_id", getStringArg(args, "calendar_id"))
		if bad != nil {
			return bad, nil
		}
		var edits eventEdits
		if bad := parseRecurrenceArgs(ctx, pool, claims.HouseholdID, args, &edits); bad != nil {
			return bad, nil
		}

		eventID := newUID()
		m := &icalx.Event{UID: eventID, Summary: title, Description: description, Location: location, Status: icalx.StatusConfirmed}
		// All-day events are whole days (UTC-midnight dates, exclusive end;
		// see allDayRange). Timed events are instants; a repeating one
		// repeats in the family timezone so 17:30 stays 17:30 across DST.
		if allDay {
			first, endExcl, err := allDayRange(startArg, endArg)
			if err != nil {
				return respond.Invalid("end", err.Error(),
					"For all_day events pass start as the first day and end as the last day (YYYY-MM-DD).",
					map[string]any{"title": "Trip", "all_day": true, "start": exampleDate(loc), "end": exampleDate(loc)}), nil
			}
			m.AllDay, m.Start, m.End = true, first, endExcl
		} else {
			startT, bad := parseInstant("start", startArg, loc)
			if bad != nil {
				return bad, nil
			}
			endT, bad := parseInstant("end", endArg, loc)
			if bad != nil {
				return bad, nil
			}
			if !endT.After(startT) {
				return respond.Invalid("end", "end must be after start.",
					"Pass an end later than start, e.g. one hour after it (end is the first occurrence's end).",
					map[string]any{"title": title, "start": startT.In(loc).Format(time.RFC3339), "end": startT.Add(time.Hour).In(loc).Format(time.RFC3339)}), nil
			}
			m.Start, m.End = startT.UTC().Truncate(time.Second), endT.UTC().Truncate(time.Second)
		}
		obj := &icalx.Object{UID: eventID, Master: m}
		if edits.repSet && edits.repClear {
			edits.repSet = false
		}
		moved, bad := applyRule(obj, edits, loc)
		if bad != nil {
			return bad, nil
		}
		if bad := applyExtras(obj, edits, loc); bad != nil {
			return bad, nil
		}
		icalRaw, rrule, bad := encodeObject(obj)
		if bad != nil {
			return bad, nil
		}
		storedStart, storedEnd := jsonTimes(m)
		rangeStart, rangeEnd := realTime(m, m.Start, loc), realTime(m, m.End, loc)

		// Loop guard: the same event on the same calendar created moments
		// ago comes back as it is, flagged duplicate, instead of a copy.
		if row, ok := recentDuplicate(ctx, pool, cal.ID, title, storedStart, storedEnd, allDay); ok {
			ev, err := parseEventFromRow(row, loc)
			if err == nil {
				res := eventWriteResult{Event: *ev, Duplicate: true}
				res.Conflicts, res.ConflictsError = conflictsFor(ctx, pool, claims.HouseholdID, loc, rangeStart, rangeEnd, ev.ID)
				return respond.OK(fmt.Sprintf("%q on %s already exists (created minutes ago); nothing new was added.", ev.Title, describeWhen(*ev, loc)), res,
					fmt.Sprintf("To change it use event_update event_id=%s; don't create it again.", ev.ID)), nil
			}
		}

		icalData, err := masterJSON("", m, edits.people, edits.peopleSet && len(edits.people) > 0)
		if err != nil {
			return respond.Failed("encode the event", err), nil
		}
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("create the event", err), nil
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, etag, event_type, ical_raw, rrule)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			eventID, cal.ID, eventID, icalData, newUID(), eventType, icalRaw, rrule,
		); err != nil {
			return respond.Failed("create the event", err), nil
		}
		if err := recordChangeAndBumpCtag(ctx, tx, cal.ID, eventID, "create"); err != nil {
			return respond.Failed("create the event", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("create the event", err), nil
		}
		slog.Info("event_create: created", "event_id", eventID, "calendar_id", cal.ID, "recurring", rrule != nil)

		ev, err := parseEventFromRow(eventRow{ID: eventID, CalendarID: cal.ID, IcalData: icalData, EventType: eventType,
			CalendarName: cal.Name, Rrule: rrule, IcalRaw: &icalRaw}, loc)
		if err != nil {
			return respond.Failed("read back the new event", err), nil
		}
		// The event is committed, so a failed overlap lookup must not fail
		// the call: it returns an empty list plus conflicts_error instead.
		res := eventWriteResult{Event: *ev}
		if m.IsRecurring() {
			res.Conflicts, res.ConflictsError = seriesConflicts(ctx, pool, claims.HouseholdID, loc, obj, eventID)
		} else {
			res.Conflicts, res.ConflictsError = conflictsFor(ctx, pool, claims.HouseholdID, loc, rangeStart, rangeEnd, eventID)
		}

		summary := fmt.Sprintf("Created %q on %s in %s.", ev.Title, describeWhen(*ev, loc), cal.Name)
		if m.IsRecurring() {
			summary = fmt.Sprintf("Created %q in %s, %s, first on %s.", ev.Title, cal.Name, describeRepeat(ev.Repeat, ev.RRule), describeWhen(*ev, loc))
			if moved > 0 {
				res.Note = fmt.Sprintf("The first occurrence was moved %d %s later to fall on a repeat day.", moved, plural(moved, "day", "days"))
				summary += " " + res.Note
			}
		}
		if n := len(res.Conflicts); n > 0 {
			summary += fmt.Sprintf(" It overlaps %d other %s.", n, plural(n, "event", "events"))
		}
		return respond.OK(summary, res, conflictHints(*ev, res.Conflicts, loc)...), nil
	}
	return tool, handler
}

// ─────────────────────────────────────────────
// event_update
// ─────────────────────────────────────────────

// storedEvent is a calendar_objects row read for a write.
type storedEvent struct {
	calendarID, icalData, uid, eventType string
	rrule, icalRaw                       *string
	ej                                   eventJSON
}

func loadForWrite(ctx context.Context, tx pgx.Tx, eventID, householdID string) (storedEvent, error) {
	var s storedEvent
	err := tx.QueryRow(ctx,
		`SELECT co.calendar_id::text, co.ical_data, co.uid, co.event_type, co.rrule, co.ical_raw
		   FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id
		  WHERE co.id = $1 AND c.household_id = $2
		  FOR UPDATE OF co`,
		eventID, householdID,
	).Scan(&s.calendarID, &s.icalData, &s.uid, &s.eventType, &s.rrule, &s.icalRaw)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal([]byte(s.icalData), &s.ej); err != nil {
		return s, fmt.Errorf("parse stored event: %w", err)
	}
	return s, nil
}

func scopeArg(args map[string]any) (string, *mcp.CallToolResult) {
	scope := strings.ToLower(strings.TrimSpace(getStringArg(args, "scope")))
	if scope != "" && !slices.Contains(scopes, scope) {
		return "", respond.Invalid("scope", fmt.Sprintf("scope %q must be this, following or all.", scope),
			"this = one occurrence, following = it and later ones, all = every occurrence.", map[string]any{"scope": "all"})
	}
	return scope, nil
}

// NewEventUpdateTool creates event_update (replaces update_event).
func NewEventUpdateTool(pool *pgxpool.Pool) (mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("event_update", toolOptions(classU, "Update event",
		mcp.WithDescription("Change an event; only the given fields change. Changing start keeps the duration unless you also pass end. For repeating events pick scope and occurrence_date. Returns the event."),
		mcp.WithString("event_id", mcp.Description("The event's id from event_find"), mcp.Required()),
		mcp.WithString("title", mcp.Description("New title")),
		mcp.WithString("start", mcp.Description("New start with offset, e.g. 2026-11-19T17:30:00-05:00; a date if all_day")),
		mcp.WithString("end", mcp.Description("New end; omit to keep the duration. If all_day the last day")),
		mcp.WithBoolean("all_day", mcp.Description("true snaps to whole days; false needs start and end")),
		mcp.WithString("location", mcp.Description("New location (\"\" clears it)")),
		mcp.WithString("description", mcp.Description("New description (\"\" clears it)")),
		mcp.WithString("event_type", mcp.Description("New event type")),
		mcp.WithString("calendar_id", mcp.Description("Move to this calendar (name or id), e.g. Kids")),
		mcp.WithString("scope", mcp.Description("Repeating event: this occurrence, following ones, or all"), mcp.Enum(scopes...)),
		mcp.WithString("occurrence_date", mcp.Description("Original date of the occurrence (YYYY-MM-DD) for this/following")),
		mcp.WithArray("person_ids", mcp.Description("Who it is for (replaces the list): person ids or names"), mcp.WithStringItems()),
		mcp.WithObject("repeat", mcp.Description("New repeat rule; {freq:none} makes it a one-off"), mcp.Properties(repeatSchema(true))),
		mcp.WithArray("except_dates", mcp.Description("Dates to skip (YYYY-MM-DD); replaces the list"), mcp.WithStringItems()),
		mcp.WithArray("reminder_minutes", mcp.Description("Phone alerts, minutes before start; [] removes them"), mcp.WithNumberItems()),
		respond.Schema[eventWriteResult](),
	)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return authRequired(), nil
		}
		args := req.GetArguments()
		eventID := strings.TrimSpace(getStringArg(args, "event_id"))
		if eventID == "" {
			return respond.Invalid("event_id", "event_id is required.", "Pass the id of the event from event_find.",
				map[string]any{"event_id": "3f2b8c1e-7a4d-4e0b-9c2a-1d5e6f7a8b9c", "title": "New title"}), nil
		}
		if bad := checkUUID("event_id", eventID, "event_find"); bad != nil {
			return bad, nil
		}
		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)
		scope, bad := scopeArg(args)
		if bad != nil {
			return bad, nil
		}
		occArg := strings.TrimSpace(getStringArg(args, "occurrence_date"))

		var target *Calendar
		if ref := strings.TrimSpace(getStringArg(args, "calendar_id")); ref != "" {
			cal, bad := resolveCalendar(ctx, pool, claims.HouseholdID, "calendar_id", ref)
			if bad != nil {
				return bad, nil
			}
			target = &cal
		}

		// Optional fields. description and location may be cleared by
		// passing an empty string.
		var edits eventEdits
		if v := strings.TrimSpace(getStringArg(args, "title")); v != "" {
			edits.title = &v
		}
		if args["description"] != nil {
			v := getStringArg(args, "description")
			edits.description = &v
		}
		if v := strings.TrimSpace(getStringArg(args, "start")); v != "" {
			edits.start = &v
		}
		if v := strings.TrimSpace(getStringArg(args, "end")); v != "" {
			edits.end = &v
		}
		if args["location"] != nil {
			v := getStringArg(args, "location")
			edits.location = &v
		}
		var allDayPtr *bool
		if v, ok := args["all_day"].(bool); ok {
			allDayPtr = &v
		}
		var eventTypeArg *string
		if v := strings.TrimSpace(getStringArg(args, "event_type")); v != "" {
			eventTypeArg = &v
		}
		if bad := parseRecurrenceArgs(ctx, pool, claims.HouseholdID, args, &edits); bad != nil {
			return bad, nil
		}

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("update the event", err), nil
		}
		defer tx.Rollback(ctx)

		// Read and lock the row inside the transaction, so a concurrent
		// edit (or a CalDAV PUT) cannot land between the read and the write.
		// uid is what CalDAV clients know the event by and what
		// calendar_changes must record; it differs from id for events
		// created over CalDAV.
		st, err := loadForWrite(ctx, tx, eventID, claims.HouseholdID)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("event", eventID, "Call event_find to find the event_id."), nil
		}
		if err != nil {
			return respond.Failed("read the event", err), nil
		}
		series := loadSeries(st.ej, st.uid, st.rrule, st.icalRaw)
		if series.locked {
			return respond.Fail(respond.Error{Message: lockedEdit,
				Fix: "Ask the user to make this change in their phone's calendar app."}), nil
		}
		if series.obj == nil {
			return respond.Failed("read the stored event", fmt.Errorf("unreadable start/end in ical_data")), nil
		}
		obj := series.obj
		m := obj.Master

		w := updateWrite{pool: pool, tx: tx, loc: loc, eventID: eventID, st: st, obj: obj, edits: edits,
			target: target, eventType: eventTypeArg, householdID: claims.HouseholdID}
		if !series.recurring() {
			return w.single(ctx, allDayPtr)
		}

		// A repeating event: work out which occurrences change.
		if allDayPtr != nil && *allDayPtr != m.AllDay {
			return respond.Invalid("all_day", "all_day can't be switched on a repeating event.",
				"Delete the series (scope all) and create it again with the other all_day setting.", nil), nil
		}
		if scope == "" {
			switch {
			case occArg != "":
				scope = scopeThis
			case edits.seriesOnly():
				scope = scopeAll
			default:
				return respond.Invalid("scope", fmt.Sprintf("%q repeats; say which occurrences to change.", m.Summary),
					"Pass scope=all for every occurrence, or scope=this (or following) with occurrence_date=YYYY-MM-DD.",
					map[string]any{"event_id": eventID, "scope": scopeThis, "occurrence_date": nextOrToday(obj, loc)}), nil
			}
		}
		if (scope == scopeThis || scope == scopeFollowing) && occArg == "" {
			return respond.Invalid("occurrence_date", "occurrence_date is required with scope "+scope+".",
				"Pass the original date of the occurrence (YYYY-MM-DD), e.g. from event_find.",
				map[string]any{"event_id": eventID, "scope": scope, "occurrence_date": nextOrToday(obj, loc)}), nil
		}
		switch scope {
		case scopeThis:
			for _, f := range []struct {
				name string
				set  bool
			}{{"repeat", edits.repSet}, {"except_dates", edits.exceptSet}, {"calendar_id", target != nil}, {"event_type", eventTypeArg != nil}} {
				if f.set {
					return respond.Invalid(f.name, f.name+" applies to the whole series, not one occurrence.",
						"Use scope all (or following) for "+f.name+".", map[string]any{"event_id": eventID, "scope": scopeAll}), nil
				}
			}
			return w.this(ctx, occArg)
		case scopeFollowing:
			inst, bad := findInstance(obj, "occurrence_date", occArg, loc)
			if bad != nil {
				return bad, nil
			}
			if !isFirstInstance(obj, inst.RID) {
				return w.following(ctx, inst)
			}
			// From the first occurrence on is every occurrence.
			return w.all(ctx, inst.RID, "Changed from the first occurrence on, so every occurrence changed.")
		default:
			anchor := m.Start
			if occArg != "" {
				inst, bad := findInstance(obj, "occurrence_date", occArg, loc)
				if bad != nil {
					return bad, nil
				}
				anchor = inst.RID
			}
			return w.all(ctx, anchor, "")
		}
	}
	return tool, handler
}

// nextOrToday is the next occurrence date of obj, for error examples.
func nextOrToday(obj *icalx.Object, hh *time.Location) string {
	if obj == nil || obj.Master == nil {
		return todayIn(hh).Format(dateLayout)
	}
	if next := nextOccurrences(obj, todayIn(hh), hh, 1); len(next) > 0 {
		return next[0]
	}
	return todayIn(hh).Format(dateLayout)
}

// updateWrite carries one event_update call.
type updateWrite struct {
	pool        *pgxpool.Pool
	tx          pgx.Tx
	loc         *time.Location
	householdID string
	eventID     string
	st          storedEvent
	obj         *icalx.Object
	edits       eventEdits
	target      *Calendar
	eventType   *string
}

// single edits a one-off event (which may become a repeating one).
func (w updateWrite) single(ctx context.Context, allDayPtr *bool) (*mcp.CallToolResult, error) {
	m, loc, e := w.obj.Master, w.loc, w.edits
	existing := w.st.ej
	timesChanged := e.start != nil || e.end != nil || allDayPtr != nil

	// New start/end: an explicit end is stored as given; a start-only
	// move keeps the original duration. All-day events are always
	// re-normalised to whole days.
	allDay := existing.AllDay
	if allDayPtr != nil {
		allDay = *allDayPtr
	}
	switch {
	case allDay:
		s, en, err := resolveUpdatedAllDay(existing, loc, e.start, e.end)
		if err != nil {
			return respond.Invalid("start", err.Error(),
				"For all-day events pass start as the first day and end as the last day (YYYY-MM-DD).",
				map[string]any{"event_id": w.eventID, "start": exampleDate(loc), "end": exampleDate(loc)}), nil
		}
		first, endExcl, _ := allDayRange(s, en)
		m.AllDay, m.Floating, m.TZID, m.Start, m.End = true, false, "", first, endExcl
	case existing.AllDay && e.start == nil && e.end == nil:
		return respond.Invalid("start", "Pass start and end when turning off all_day.",
			"Give the new timed start and end, ISO 8601 with offsets.",
			map[string]any{"event_id": w.eventID, "all_day": false, "start": exampleTime(loc), "end": exampleTimePlus(loc, time.Hour)}), nil
	case e.start != nil || e.end != nil:
		// Parse up front for corrective errors (a naive time gets the
		// family's offset as its fix).
		for _, f := range []struct {
			name string
			val  *string
		}{{"start", e.start}, {"end", e.end}} {
			if f.val == nil {
				continue
			}
			if _, bad := parseInstant(f.name, *f.val, loc); bad != nil {
				return bad, nil
			}
		}
		newStart, newEnd, err := resolveUpdatedTimes(existing.Start, existing.End, e.start, e.end)
		if err != nil {
			return respond.Invalid("end", err.Error(),
				"Pass an end later than start, or omit end to keep the event's duration.",
				map[string]any{"event_id": w.eventID, "start": exampleTime(loc), "end": exampleTimePlus(loc, time.Hour)}), nil
		}
		zone := time.UTC
		if m.TZID != "" && !m.AllDay && !m.Floating {
			zone = m.Start.Location()
		} else {
			m.TZID = ""
		}
		m.AllDay, m.Floating = false, false
		m.Start, m.End = newStart.In(zone), newEnd.In(zone)
	}
	setText(m, e)
	moved, bad := applyRule(w.obj, e, loc)
	if bad != nil {
		return bad, nil
	}
	if bad := applyExtras(w.obj, e, loc); bad != nil {
		return bad, nil
	}
	m.Sequence++
	return w.save(ctx, timesChanged || e.repSet, "", "", moved)
}

// setText applies title, description and location to an event.
func setText(ev *icalx.Event, e eventEdits) {
	if e.title != nil {
		ev.Summary = *e.title
	}
	if e.description != nil {
		ev.Description = *e.description
	}
	if e.location != nil {
		ev.Location = *e.location
	}
}

// shiftSeries moves a series so the occurrence that started at anchor
// starts at the new start (a wall-clock move, so 17:30 stays 17:30 across
// DST) and applies a new length. EXDATEs, override RECURRENCE-IDs and the
// overrides that were not moved on their own move with it.
func shiftSeries(obj *icalx.Object, anchor time.Time, startArg, endArg *string, hh *time.Location) *mcp.CallToolResult {
	if startArg == nil && endArg == nil {
		return nil
	}
	m := obj.Master
	old := m.Clone()
	example := map[string]any{"start": exampleTime(hh)}
	var delta time.Duration
	dayDelta := 0
	if m.AllDay {
		example = map[string]any{"start": exampleDate(hh)}
		days := allDayDays(m)
		first := anchor
		if startArg != nil {
			d, _, err := civilDate(*startArg)
			if err != nil {
				return respond.Invalid("start", err.Error(), "Pass a date (YYYY-MM-DD) for an all-day event.", example)
			}
			first = d
		}
		if endArg != nil {
			_, endExcl, err := allDayRange(first.Format(dateLayout), *endArg)
			if err != nil {
				return respond.Invalid("end", err.Error(), "Pass the last day (YYYY-MM-DD).", example)
			}
			days = int(endExcl.Sub(first).Hours()+12) / 24
		}
		dayDelta = int(first.Sub(anchor).Hours()+12) / 24
		if first.Before(anchor) {
			dayDelta = -int(anchor.Sub(first).Hours()+12) / 24
		}
		delta = time.Duration(dayDelta) * 24 * time.Hour
		m.Start = m.Start.AddDate(0, 0, dayDelta)
		m.End = m.Start.AddDate(0, 0, days)
	} else {
		sl := seriesLoc(m, hh)
		anchorReal := realTime(m, anchor, hh).In(sl)
		newStart := anchorReal
		if startArg != nil {
			t, bad := parseInstant("start", *startArg, hh)
			if bad != nil {
				return bad
			}
			newStart = t.In(sl)
		}
		dur := m.Duration()
		if dur <= 0 {
			dur = time.Hour
		}
		if endArg != nil {
			t, bad := parseInstant("end", *endArg, hh)
			if bad != nil {
				return bad
			}
			dur = t.Sub(newStart)
			if dur <= 0 {
				return respond.Invalid("end", "end must be after start.", "Pass an end later than the occurrence's start, or omit end to keep the duration.", example)
			}
		}
		delta = wall(newStart).Sub(wall(anchorReal))
		nd, ad := wall(newStart), wall(anchorReal)
		dayDelta = int(time.Date(nd.Year(), nd.Month(), nd.Day(), 0, 0, 0, 0, time.UTC).Sub(time.Date(ad.Year(), ad.Month(), ad.Day(), 0, 0, 0, 0, time.UTC)).Hours() / 24)
		m.Start = shiftWall(m.Start, delta)
		m.End = m.Start.Add(dur)
	}
	if delta == 0 {
		return nil
	}
	move := func(t time.Time) time.Time {
		switch {
		case m.AllDay:
			return t.AddDate(0, 0, dayDelta)
		case m.Floating:
			return shiftWall(t, delta)
		}
		return shiftWall(t.In(seriesLoc(m, hh)), delta)
	}
	for i, x := range m.ExDates {
		m.ExDates[i] = move(x)
	}
	for i, x := range m.RDates {
		m.RDates[i] = move(x)
	}
	for _, ov := range obj.Overrides {
		if sameInstance(old, ov.Start, ov.RecurrenceID) { // not moved on its own
			d := ov.End.Sub(ov.Start)
			ov.Start = move(ov.Start)
			ov.End = ov.Start.Add(d)
		}
		ov.RecurrenceID = move(ov.RecurrenceID)
	}
	if dayDelta != 0 {
		m.RRule = shiftByDay(m.RRule, dayDelta)
	}
	if u := rulePart(m.RRule, "UNTIL"); u != "" && dayDelta > 0 {
		// Keep the last occurrence when the series moves to later days
		// (UNTIL is the end of a day, so a move within the day keeps it).
		if t, err := time.Parse("20060102", u); err == nil {
			m.RRule = withRulePart(m.RRule, "UNTIL", t.AddDate(0, 0, dayDelta).Format("20060102"))
		} else if t, err := time.Parse("20060102T150405Z", u); err == nil {
			m.RRule = withRulePart(m.RRule, "UNTIL", t.AddDate(0, 0, dayDelta).Format("20060102T150405Z"))
		}
	}
	return nil
}

// editSeries applies an edit to every occurrence of obj, measured from the
// occurrence at anchor. Overrides keep their own changes; a field they
// shared with the master follows the master.
func editSeries(obj *icalx.Object, anchor time.Time, e eventEdits, hh *time.Location) (int, *mcp.CallToolResult) {
	m := obj.Master
	if bad := shiftSeries(obj, anchor, e.start, e.end, hh); bad != nil {
		return 0, bad
	}
	for _, ov := range obj.Overrides {
		if e.title != nil && ov.Summary == m.Summary {
			ov.Summary = *e.title
		}
		if e.description != nil && ov.Description == m.Description {
			ov.Description = *e.description
		}
		if e.location != nil && ov.Location == m.Location {
			ov.Location = *e.location
		}
	}
	setText(m, e)
	moved, bad := applyRule(obj, e, hh)
	if bad != nil {
		return 0, bad
	}
	if bad := applyExtras(obj, e, hh); bad != nil {
		return 0, bad
	}
	return moved, nil
}

// all edits every occurrence.
func (w updateWrite) all(ctx context.Context, anchor time.Time, note string) (*mcp.CallToolResult, error) {
	moved, bad := editSeries(w.obj, anchor, w.edits, w.loc)
	if bad != nil {
		return bad, nil
	}
	w.obj.Master.Sequence++
	for _, ov := range w.obj.Overrides {
		ov.Sequence = w.obj.Master.Sequence
	}
	return w.save(ctx, w.edits.start != nil || w.edits.end != nil || w.edits.repSet, scopeAll, note, moved)
}

// this edits one occurrence through a RECURRENCE-ID override.
func (w updateWrite) this(ctx context.Context, occArg string) (*mcp.CallToolResult, error) {
	obj, m, loc, e := w.obj, w.obj.Master, w.loc, w.edits
	inst, bad := findInstance(obj, "occurrence_date", occArg, loc)
	if bad != nil {
		return bad, nil
	}
	ov := inst.Override
	if ov == nil {
		ov = m.Clone()
		ov.RRule, ov.RDates, ov.ExDates = "", nil, nil
		ov.RecurrenceID = inst.RID
		ov.Start, ov.End = inst.RID, instanceEnd(m, inst.RID)
		obj.Overrides = append(obj.Overrides, ov)
	}
	// Times of this occurrence only: start alone keeps its length, end
	// alone keeps its start.
	if e.start != nil || e.end != nil {
		if m.AllDay {
			startStr := ov.Start.Format(dateLayout)
			if e.start != nil {
				startStr = *e.start
			}
			first, _, err := civilDate(startStr)
			if err != nil {
				return respond.Invalid("start", err.Error(), "Pass a date (YYYY-MM-DD) for an all-day event.", nil), nil
			}
			days := allDayDays(ov)
			if e.end != nil {
				_, endExcl, err := allDayRange(startStr, *e.end)
				if err != nil {
					return respond.Invalid("end", err.Error(), "Pass the last day (YYYY-MM-DD).", nil), nil
				}
				days = int(endExcl.Sub(first).Hours()+12) / 24
			}
			ov.Start, ov.End = first, first.AddDate(0, 0, days)
		} else {
			start := realTime(ov, ov.Start, loc)
			dur := ov.End.Sub(ov.Start)
			if dur <= 0 {
				dur = time.Hour
			}
			if e.start != nil {
				t, bad := parseInstant("start", *e.start, loc)
				if bad != nil {
					return bad, nil
				}
				start = t
			}
			end := start.Add(dur)
			if e.end != nil {
				t, bad := parseInstant("end", *e.end, loc)
				if bad != nil {
					return bad, nil
				}
				end = t
			}
			if !end.After(start) {
				return respond.Invalid("end", "end must be after start.", "Pass an end later than start, or omit end to keep the length.", nil), nil
			}
			ov.Start, ov.End = seriesTime(ov, start.Truncate(time.Second), loc), seriesTime(ov, end.Truncate(time.Second), loc)
		}
	}
	setText(ov, e)
	if e.remindersSet {
		ov.Alarms = alarmsFor(e.reminders)
	}
	if e.peopleSet {
		setPeople(ov, e.people)
	}
	m.Sequence++
	ov.Sequence = m.Sequence

	icalRaw, rrule, bad := encodeObject(obj)
	if bad != nil {
		return bad, nil
	}
	icalData, err := masterJSON(w.st.icalData, m, nil, false)
	if err != nil {
		return respond.Failed("update the event", err), nil
	}
	row, err := w.writeRow(ctx, icalData, icalRaw, rrule, nil)
	if err != nil {
		return respond.Failed("update the event", err), nil
	}
	if err := recordChangeAndBumpCtag(ctx, w.tx, w.st.calendarID, w.st.uid, "update"); err != nil {
		return respond.Failed("update the event", err), nil
	}
	if err := w.tx.Commit(ctx); err != nil {
		return respond.Failed("update the event", err), nil
	}
	series, err := parseEventFromRow(row, loc)
	if err != nil {
		return respond.Failed("read back the updated event", err), nil
	}
	occ := occurrenceEvent(*series, m, inst.RID, ov, loc)
	res := eventWriteResult{Event: occ, Conflicts: []ConflictEvent{}, Scope: scopeThis}
	if e.start != nil || e.end != nil {
		if s, en, ok := eventRange(occ); ok {
			res.Conflicts, res.ConflictsError = conflictsFor(ctx, w.pool, w.householdID, loc, s, en, w.eventID)
		}
	}
	slog.Info("event_update: occurrence", "event_id", w.eventID, "recurrence_id", occ.RecurrenceID)
	summary := fmt.Sprintf("Changed only the %s occurrence of %q: %s.", occ.RecurrenceID[:min(10, len(occ.RecurrenceID))], series.Title, describeWhen(occ, loc))
	if n := len(res.Conflicts); n > 0 {
		summary += fmt.Sprintf(" It now overlaps %d other %s.", n, plural(n, "event", "events"))
	}
	return respond.OK(summary, res, conflictHints(occ, res.Conflicts, loc)...), nil
}

// following splits the series at inst: the original keeps the earlier
// occurrences, a new event (new UID) carries inst onward with the edit.
func (w updateWrite) following(ctx context.Context, inst instance) (*mcp.CallToolResult, error) {
	obj, m, loc := w.obj, w.obj.Master, w.loc
	newID := newUID()
	nm := m.Clone()
	nm.UID = newID
	nm.Sequence = 0
	nm.Start, nm.End = inst.RID, instanceEnd(m, inst.RID)
	nm.ExDates, nm.RDates = nil, nil
	for _, x := range m.ExDates {
		if !instanceBefore(m, x, inst.RID) {
			nm.ExDates = append(nm.ExDates, x)
		}
	}
	for _, x := range m.RDates {
		if !instanceBefore(m, x, inst.RID) {
			nm.RDates = append(nm.RDates, x)
		}
	}
	newObj := &icalx.Object{UID: newID, Master: nm}
	for _, ov := range obj.Overrides {
		if !instanceBefore(m, ov.RecurrenceID, inst.RID) {
			c := ov.Clone()
			c.UID = newID
			newObj.Overrides = append(newObj.Overrides, c)
		}
	}
	remaining, err := endSeriesBefore(obj, inst.RID, loc)
	if err != nil {
		return respond.Failed("split the series", err), nil
	}
	if remaining > 0 {
		nm.RRule = withRulePart(nm.RRule, "COUNT", fmt.Sprint(remaining))
	}
	moved, bad := editSeries(newObj, nm.Start, w.edits, loc)
	if bad != nil {
		return bad, nil
	}
	m.Sequence++

	oldRaw, oldRule, bad := encodeObject(obj)
	if bad != nil {
		return bad, nil
	}
	newRaw, newRule, bad := encodeObject(newObj)
	if bad != nil {
		return bad, nil
	}
	oldData, err := masterJSON(w.st.icalData, m, nil, false)
	if err != nil {
		return respond.Failed("update the event", err), nil
	}
	people := w.edits.people
	if !w.edits.peopleSet {
		people = peopleFromAttendees(nm)
	}
	base := ""
	if doc := map[string]json.RawMessage{}; json.Unmarshal([]byte(w.st.icalData), &doc) == nil {
		// Carry color/event_type-style keys over, but not sequence.
		delete(doc, "sequence")
		if b, err := json.Marshal(doc); err == nil {
			base = string(b)
		}
	}
	newData, err := masterJSON(base, nm, people, true)
	if err != nil {
		return respond.Failed("update the event", err), nil
	}
	if _, err := w.writeRow(ctx, oldData, oldRaw, oldRule, nil); err != nil {
		return respond.Failed("update the event", err), nil
	}
	if err := recordChangeAndBumpCtag(ctx, w.tx, w.st.calendarID, w.st.uid, "update"); err != nil {
		return respond.Failed("update the event", err), nil
	}
	calID := w.st.calendarID
	if w.target != nil {
		calID = w.target.ID
	}
	eventType := w.st.eventType
	if w.eventType != nil {
		eventType = *w.eventType
	}
	if _, err := w.tx.Exec(ctx,
		`INSERT INTO calendar_objects (id, calendar_id, uid, ical_data, etag, event_type, ical_raw, rrule)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		newID, calID, newID, newData, newUID(), eventType, newRaw, newRule); err != nil {
		return respond.Failed("create the new part of the series", err), nil
	}
	if err := recordChangeAndBumpCtag(ctx, w.tx, calID, newID, "create"); err != nil {
		return respond.Failed("update the event", err), nil
	}
	if err := w.tx.Commit(ctx); err != nil {
		return respond.Failed("update the event", err), nil
	}
	row, err := scanEventRow(w.pool.QueryRow(ctx,
		`SELECT `+eventColumns+` FROM calendar_objects co JOIN calendars c ON c.id = co.calendar_id WHERE co.id = $1`, newID))
	if err != nil {
		return respond.Failed("read back the new event", err), nil
	}
	ev, err := parseEventFromRow(row, loc)
	if err != nil {
		return respond.Failed("read back the new event", err), nil
	}
	day := realTime(m, inst.RID, loc).In(loc).Format(dateLayout)
	res := eventWriteResult{Event: *ev, Scope: scopeFollowing, OriginalEventID: w.eventID,
		Note: fmt.Sprintf("Occurrences before %s stay in event %s; from %s on they are event %s.", day, w.eventID, day, newID)}
	res.Conflicts, res.ConflictsError = seriesConflicts(ctx, w.pool, w.householdID, loc, newObj, newID, w.eventID)
	if moved > 0 {
		res.Note += fmt.Sprintf(" The first occurrence moved %d %s later to fall on a repeat day.", moved, plural(moved, "day", "days"))
	}
	slog.Info("event_update: split series", "event_id", w.eventID, "new_event_id", newID, "at", day)
	summary := fmt.Sprintf("Changed %q from %s on (%s). %s", ev.Title, day, describeRepeat(ev.Repeat, ev.RRule), res.Note)
	if n := len(res.Conflicts); n > 0 {
		summary += fmt.Sprintf(" It overlaps %d other %s.", n, plural(n, "event", "events"))
	}
	return respond.OK(summary, res, conflictHints(*ev, res.Conflicts, loc)...), nil
}

// writeRow updates the event row (moving it to moveTo when set) and returns
// it.
func (w updateWrite) writeRow(ctx context.Context, icalData, icalRaw string, rrule *string, moveTo *string) (eventRow, error) {
	return scanEventRow(w.tx.QueryRow(ctx,
		`UPDATE calendar_objects co
		    SET ical_data = $1, etag = $2, event_type = COALESCE($3, co.event_type),
		        calendar_id = COALESCE($5::uuid, co.calendar_id), ical_raw = $6, rrule = $7, updated_at = NOW()
		   FROM calendars c
		  WHERE co.id = $4 AND c.id = COALESCE($5::uuid, co.calendar_id)
		RETURNING `+eventColumns,
		icalData, newUID(), w.eventType, w.eventID, moveTo, icalRaw, rrule))
}

// save writes a single event or a whole-series edit: JSON from the master,
// ical_raw and rrule, a calendar move when asked, and the change rows.
func (w updateWrite) save(ctx context.Context, timesChanged bool, scope, note string, moved int) (*mcp.CallToolResult, error) {
	m, loc := w.obj.Master, w.loc
	icalRaw, rrule, bad := encodeObject(w.obj)
	if bad != nil {
		return bad, nil
	}
	icalData, err := masterJSON(w.st.icalData, m, w.edits.people, w.edits.peopleSet)
	if err != nil {
		return respond.Failed("update the event", err), nil
	}
	// A move to another calendar is a delete there and a create here
	// for CalDAV sync; moving to the same calendar is a plain update.
	var moveTo *string
	if w.target != nil && w.target.ID != w.st.calendarID {
		moveTo = &w.target.ID
	}
	row, err := w.writeRow(ctx, icalData, icalRaw, rrule, moveTo)
	if err != nil {
		return respond.Failed("update the event", err), nil
	}
	if moveTo != nil {
		if err := recordChangeAndBumpCtag(ctx, w.tx, w.st.calendarID, w.st.uid, "delete"); err != nil {
			return respond.Failed("update the event", err), nil
		}
		if err := recordChangeAndBumpCtag(ctx, w.tx, *moveTo, w.st.uid, "create"); err != nil {
			return respond.Failed("update the event", err), nil
		}
	} else if err := recordChangeAndBumpCtag(ctx, w.tx, w.st.calendarID, w.st.uid, "update"); err != nil {
		return respond.Failed("update the event", err), nil
	}
	if err := w.tx.Commit(ctx); err != nil {
		return respond.Failed("update the event", err), nil
	}
	slog.Info("event_update: updated", "event_id", w.eventID, "uid", w.st.uid, "scope", scope)

	updated, err := parseEventFromRow(row, loc)
	if err != nil {
		return respond.Failed("read back the updated event", err), nil
	}
	res := eventWriteResult{Event: *updated, Conflicts: []ConflictEvent{}, Scope: scope, Note: note}
	if timesChanged {
		if m.IsRecurring() {
			res.Conflicts, res.ConflictsError = seriesConflicts(ctx, w.pool, w.householdID, loc, w.obj, w.eventID)
		} else if s, e, ok := eventRange(*updated); ok {
			res.Conflicts, res.ConflictsError = conflictsFor(ctx, w.pool, w.householdID, loc, s, e, w.eventID)
		}
	}
	if moved > 0 {
		extra := fmt.Sprintf("The first occurrence moved %d %s later to fall on a repeat day.", moved, plural(moved, "day", "days"))
		res.Note = strings.TrimSpace(res.Note + " " + extra)
	}
	summary := fmt.Sprintf("Updated %q: %s", updated.Title, describeWhen(*updated, loc))
	if updated.Recurring {
		summary = fmt.Sprintf("Updated every occurrence of %q: %s, first on %s", updated.Title, describeRepeat(updated.Repeat, updated.RRule), describeWhen(*updated, loc))
	}
	if moveTo != nil {
		summary += ", now in " + updated.CalendarName
	}
	summary += "."
	if res.Note != "" {
		summary += " " + res.Note
	}
	if n := len(res.Conflicts); n > 0 {
		summary += fmt.Sprintf(" It now overlaps %d other %s.", n, plural(n, "event", "events"))
	}
	return respond.OK(summary, res, conflictHints(*updated, res.Conflicts, loc)...), nil
}

// ─────────────────────────────────────────────
// event_delete
// ─────────────────────────────────────────────

// NewEventDeleteTool creates event_delete (replaces delete_event).
func NewEventDeleteTool(pool *pgxpool.Pool) (mcp.Tool, handlerFunc) {
	tool := mcp.NewTool("event_delete", toolOptions(classX, "Delete event",
		mcp.WithDescription("Delete an event, or for a repeating event just this occurrence, this and following, or all (scope). Returns what was deleted."),
		mcp.WithString("event_id", mcp.Description("The event's id from event_find"), mcp.Required()),
		mcp.WithString("scope", mcp.Description("Repeating event: this occurrence, following ones, or all"), mcp.Enum(scopes...)),
		mcp.WithString("occurrence_date", mcp.Description("Original date of the occurrence (YYYY-MM-DD) for this/following")),
		respond.Schema[eventDeleteResult](),
	)...)

	handler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		claims := server.ClaimsFromContext(ctx)
		if claims == nil {
			return authRequired(), nil
		}
		args := req.GetArguments()
		eventID := strings.TrimSpace(getStringArg(args, "event_id"))
		if eventID == "" {
			return respond.Invalid("event_id", "event_id is required.", "Pass the id of the event from event_find.",
				map[string]any{"event_id": "3f2b8c1e-7a4d-4e0b-9c2a-1d5e6f7a8b9c"}), nil
		}
		if bad := checkUUID("event_id", eventID, "event_find"); bad != nil {
			return bad, nil
		}
		scope, bad := scopeArg(args)
		if bad != nil {
			return bad, nil
		}
		occArg := strings.TrimSpace(getStringArg(args, "occurrence_date"))
		loc := loadHouseholdLocation(ctx, pool, claims.HouseholdID)

		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return respond.Failed("delete the event", err), nil
		}
		defer tx.Rollback(ctx)

		st, err := loadForWrite(ctx, tx, eventID, claims.HouseholdID)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("event", eventID, "Call event_find to find the event_id; it may already be deleted."), nil
		}
		series := storedSeries{}
		if err == nil {
			series = loadSeries(st.ej, st.uid, st.rrule, st.icalRaw)
		} else if !strings.Contains(err.Error(), "parse stored event") {
			return respond.Failed("delete the event", err), nil
		}
		recurring := series.recurring() || (series.locked && isRecurringEvent(st.rrule, st.icalRaw))

		if recurring && scope == "" {
			if occArg == "" {
				return respond.Invalid("scope", "This event repeats; say which occurrences to delete. Nothing was deleted.",
					"Pass scope=this with occurrence_date to skip one occurrence, scope=following to end the series there, or scope=all to delete every occurrence.",
					map[string]any{"event_id": eventID, "scope": scopeThis, "occurrence_date": nextOrToday(series.obj, loc)}), nil
			}
			scope = scopeThis
		}
		if recurring && scope != scopeAll {
			if series.locked {
				return respond.Fail(respond.Error{Message: lockedEdit,
					Fix: "Delete the whole event with scope all, or ask the user to change it on their phone."}), nil
			}
			if occArg == "" {
				return respond.Invalid("occurrence_date", "occurrence_date is required with scope "+scope+".",
					"Pass the original date of the occurrence (YYYY-MM-DD).",
					map[string]any{"event_id": eventID, "scope": scope, "occurrence_date": nextOrToday(series.obj, loc)}), nil
			}
			inst, bad := findInstance(series.obj, "occurrence_date", occArg, loc)
			if bad != nil {
				return bad, nil
			}
			if !(scope == scopeFollowing && isFirstInstance(series.obj, inst.RID)) {
				return deleteOccurrences(ctx, pool, tx, loc, eventID, st, series.obj, inst, scope)
			}
			scope = scopeAll // from the first occurrence on is everything
		}

		var r eventRow
		err = tx.QueryRow(ctx,
			`DELETE FROM calendar_objects co USING calendars c
			  WHERE co.id = $1 AND c.id = co.calendar_id AND c.household_id = $2
			RETURNING `+eventColumns,
			eventID, claims.HouseholdID,
		).Scan(&r.ID, &r.CalendarID, &r.IcalData, &r.EventType, &r.EntityType, &r.CalendarName, &r.CreatedAt, &r.UpdatedAt,
			&r.Rrule, &r.IcalRaw, &r.CalendarType)
		if errors.Is(err, pgx.ErrNoRows) {
			return respond.NotFound("event", eventID, "Call event_find to find the event_id; it may already be deleted."), nil
		}
		if err != nil {
			return respond.Failed("delete the event", err), nil
		}
		// Record the delete change (DB constraint allows 'create', 'update', 'delete').
		if err := recordChangeAndBumpCtag(ctx, tx, r.CalendarID, st.uid, "delete"); err != nil {
			return respond.Failed("delete the event", err), nil
		}
		if err := tx.Commit(ctx); err != nil {
			return respond.Failed("delete the event", err), nil
		}

		ev, err := parseEventFromRow(r, loc)
		if err != nil { // deleted; the stored JSON was unreadable
			ev = &Event{ID: r.ID, CalendarID: r.CalendarID, CalendarName: r.CalendarName, EventType: r.EventType, Recurring: recurring}
		}
		ev.Recurring = recurring
		res := eventDeleteResult{Event: *ev, SeriesDeleted: recurring}
		summary := fmt.Sprintf("Deleted %q (%s) from %s.", ev.Title, describeWhen(*ev, loc), ev.CalendarName)
		if recurring {
			res.Note = recurringDeleteNote
			res.Scope = scopeAll
			summary = fmt.Sprintf("Deleted the whole repeating series %q from %s.", ev.Title, ev.CalendarName)
		}
		slog.Info("event_delete: deleted", "event_id", eventID, "series", recurring)
		return respond.OK(summary, res, "Add a replacement with event_create if needed."), nil
	}
	return tool, handler
}

// deleteOccurrences removes one occurrence (an EXDATE) or ends the series
// before it (scope following), keeping the row.
func deleteOccurrences(ctx context.Context, pool *pgxpool.Pool, tx pgx.Tx, loc *time.Location, eventID string, st storedEvent, obj *icalx.Object, inst instance, scope string) (*mcp.CallToolResult, error) {
	m := obj.Master
	if scope == scopeThis {
		m.ExDates = append(m.ExDates, inst.RID)
		dropOverrides(obj, []time.Time{inst.RID})
	} else if _, err := endSeriesBefore(obj, inst.RID, loc); err != nil {
		return respond.Failed("end the series", err), nil
	}
	m.Sequence++
	icalRaw, rrule, bad := encodeObject(obj)
	if bad != nil {
		return bad, nil
	}
	icalData, err := masterJSON(st.icalData, m, nil, false)
	if err != nil {
		return respond.Failed("delete the occurrence", err), nil
	}
	w := updateWrite{tx: tx, eventID: eventID}
	row, err := w.writeRow(ctx, icalData, icalRaw, rrule, nil)
	if err != nil {
		return respond.Failed("delete the occurrence", err), nil
	}
	if err := recordChangeAndBumpCtag(ctx, tx, st.calendarID, st.uid, "update"); err != nil {
		return respond.Failed("delete the occurrence", err), nil
	}
	if err := tx.Commit(ctx); err != nil {
		return respond.Failed("delete the occurrence", err), nil
	}
	ev, err := parseEventFromRow(row, loc)
	if err != nil {
		return respond.Failed("read back the event", err), nil
	}
	day := realTime(m, inst.RID, loc).In(loc).Format(dateLayout)
	res := eventDeleteResult{Event: *ev, Scope: scope, OccurrenceDate: day}
	summary := fmt.Sprintf("Removed the %s occurrence of %q; the rest of the series stays.", day, ev.Title)
	if scope == scopeFollowing {
		summary = fmt.Sprintf("Ended %q before %s; earlier occurrences stay.", ev.Title, day)
	}
	slog.Info("event_delete: occurrences", "event_id", eventID, "scope", scope, "at", day)
	return respond.OK(summary, res, eventHints(*ev)...), nil
}
