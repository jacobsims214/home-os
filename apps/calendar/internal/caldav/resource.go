package caldav

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"home-os/calendar/internal/auth"
	"home-os/calendar/internal/db"
	"home-os/calendar/internal/ical"
	"home-os/calendar/internal/logging"
)

// extractEventUID extracts the event UID from a path like /dav/{cal-uid}/{event-uid}.ics.
// Returns empty string if the path doesn't match the expected pattern.
func extractEventUID(path string) string {
	// path looks like /dav/{cal-uid}/{event-uid}.ics
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 {
		return ""
	}
	// Last part is the event filename, e.g., "event-uid.ics"
	filename := parts[2]
	// Strip .ics suffix
	if strings.HasSuffix(filename, ".ics") {
		return strings.TrimSuffix(filename, ".ics")
	}
	return ""
}

// extractCalendarUIDFromResource extracts the calendar UID from a resource path
// like /dav/{cal-uid}/{event-uid}.ics.
func extractCalendarUIDFromResource(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// isResourcePath returns true if the path matches /dav/{cal-uid}/{event-uid}.ics.
func isResourcePath(path string) bool {
	trimmed := strings.Trim(path, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 3 {
		return false
	}
	return strings.HasSuffix(parts[2], ".ics")
}

// HandleGET handles GET /dav/{calendar-uid}/{event-uid}.ics
// Returns the iCalendar representation of a single event with its ETag.
func HandleGET(w http.ResponseWriter, r *http.Request, repo *db.Repo, path string) {
	if !isResourcePath(path) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	calUID := extractCalendarUIDFromResource(path)
	eventUID := extractEventUID(path)
	if calUID == "" || eventUID == "" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	ctx := r.Context()
	householdID := auth.HouseholdIDFromContext(ctx)

	// Resolve calendar UID to calendar ID.
	cal, err := repo.GetCalendarByCalDAVUID(ctx, householdID, calUID)
	if err != nil {
		logging.Logger.Error("caldav: get calendar",
			slog.String("calendar_uid", calUID),
			slog.String("household_id", householdID),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if cal == nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// Look up the event.
	obj, err := repo.GetCalendarObjectByUID(ctx, householdID, cal.ID, eventUID)
	if err != nil {
		logging.Logger.Error("caldav: get object",
			slog.String("calendar_uid", calUID),
			slog.String("event_uid", eventUID),
			slog.String("household_id", householdID),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if obj == nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// Serve the client's own VCALENDAR when we have it, otherwise generate
	// one from the stored JSON. See renderObjectICal.
	icalText, err := renderObjectICal(obj)
	if err != nil {
		logging.Logger.Error("caldav: render event",
			slog.String("event_uid", eventUID),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	// RFC 7232 requires ETags to be quoted strings. Quote consistently with
	// PROPFIND/REPORT so Apple Calendar's If-None-Match comparisons match.
	w.Header().Set("ETag", `"`+obj.ETag+`"`)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(icalText))
}

// normalizeETag strips the surrounding double quotes and the weak "W/" prefix
// from an ETag value so two ETags can be compared by their opaque-tag core.
//
// RFC 7232 §2.3 defines an entity-tag as either:
//
//	W/"<opaque-tag>"   (weak)
//	"<opaque-tag>"     (strong)
//
// The calendar service stores ETags as a bare UUID string (see db.ETagGen) and
// emits them via GET/PUT as a strong-quoted value (`"<uuid>"`). CalDAV clients
// send If-Match with a quoted strong ETag (e.g. `If-Match: "550e8400-..."`).
// To compare a client-supplied value against the stored value we strip quotes
// and any `W/` prefix from both sides, which keeps the comparison correct
// regardless of which side quoted the value.
func normalizeETag(etag string) string {
	etag = strings.TrimSpace(etag)
	// Strip a leading weak marker. RFC 7232 writes it as `W/` immediately
	// before the opening quote, with no whitespace.
	etag = strings.TrimPrefix(etag, "W/")
	// Strip at most one pair of surrounding double quotes. strings.Trim would
	// also remove quotes that legitimately appear inside the opaque tag, so
	// we do this explicitly.
	if len(etag) >= 2 && etag[0] == '"' && etag[len(etag)-1] == '"' {
		etag = etag[1 : len(etag)-1]
	}
	return etag
}

// putPreconditionResult is the outcome of checking If-Match / If-None-Match
// headers against the existing state of the resource being PUT.
type putPreconditionResult int

const (
	// preconditionPass means the PUT may proceed.
	preconditionPass putPreconditionResult = iota
	// preconditionFailMissing means the request referenced the current state
	// of a resource that does not exist (If-Match on a missing object, or
	// If-Match: * on a missing object). RFC 4918 §10.4.6 / RFC 7232 §3.2: the
	// server must return 412 Precondition Failed.
	preconditionFailMissing
	// preconditionFailMismatch means If-Match supplied an ETag that does not
	// equal the resource's current ETag. Return 412 Precondition Failed.
	preconditionFailMismatch
	// preconditionFailExists means If-None-Match: * was sent but the resource
	// already exists (the client wanted create-only). RFC 7232 §3.2: return
	// 412 Precondition Failed.
	preconditionFailExists
)

// checkPUTPreconditions is a pure function that decides whether a PUT may
// proceed given the request headers and the resource's current state. It is
// pulled out of HandlePUT so it can be unit-tested without a database.
//
// Header semantics (RFC 7232 §3.2 + RFC 4918 §10.4.6):
//
//   - If-Match: a comma-separated list of ETags, or `*`. The request succeeds
//     only if the resource's current ETag is in the list (strong comparison).
//     `*` matches any existing resource — it fails if the resource does not
//     exist. An empty If-Match header is treated as absent.
//   - If-None-Match: `*` means "do not write if the resource exists". A
//     non-wildcard If-None-Match on a PUT is not meaningful for CalDAV
//     optimistic concurrency and is ignored here.
//
// If-None-Match:* is evaluated first so the create-only guard fails fast
// before any If-Match comparison; a 412 from either check short-circuits the
// request.
func checkPUTPreconditions(ifMatch, ifNoneMatch, currentETag string, exists bool) putPreconditionResult {
	ifMatch = strings.TrimSpace(ifMatch)
	ifNoneMatch = strings.TrimSpace(ifNoneMatch)

	// If-None-Match: * — create-only guard. Must be evaluated BEFORE the
	// write so that a phantom event is never produced.
	if ifNoneMatch == "*" && exists {
		return preconditionFailExists
	}

	// If-Match — strong comparison against the current ETag.
	if ifMatch != "" {
		if !exists {
			// Any If-Match (including `*`) fails when the resource does not
			// exist. RFC 7232 §3.2: `*` "is a special value, representing any
			// entity-tag" — it succeeds only if the resource is present.
			return preconditionFailMissing
		}
		if ifMatch == "*" {
			// Wildcard If-Match matches any existing resource.
			return preconditionPass
		}
		// Comma-separated list of ETags. Compare each (after normalization)
		// against the current ETag (also normalized — our stored ETag is a
		// bare hex string but we normalize defensively in case a caller
		// already wrapped it in quotes).
		currentNorm := normalizeETag(currentETag)
		for _, candidate := range strings.Split(ifMatch, ",") {
			if normalizeETag(candidate) == currentNorm {
				return preconditionPass
			}
		}
		return preconditionFailMismatch
	}

	return preconditionPass
}

// HandlePUT handles PUT /dav/{calendar-uid}/{event-uid}.ics
// Creates or updates a calendar event from the iCalendar body.
//
// RFC 4918 §10.4.6 / RFC 7232 §3.2 require the server to honor If-Match and
// If-None-Match headers for optimistic concurrency. Apple Calendar sends
// If-Match on every update; without it, concurrent edits from multiple
// devices silently overwrite each other.
//
// Response codes:
//   - 201 Created              — a new resource was created
//   - 200 OK                   — an existing resource was updated
//   - 400 Bad Request          — invalid iCalendar data or empty body
//   - 404 Not Found            — calendar doesn't exist
//   - 403 Forbidden            — RRULE icalx refuses to expand, or the body
//     holds a character XML 1.0 forbids (CALDAV:valid-calendar-data)
//   - 412 Precondition Failed  — If-Match or If-None-Match:* precondition failed
//   - 500 Internal Server Error — database or marshal error
func HandlePUT(w http.ResponseWriter, r *http.Request, repo *db.Repo, path string) {
	if !isResourcePath(path) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	calUID := extractCalendarUIDFromResource(path)
	eventUID := extractEventUID(path)
	if calUID == "" || eventUID == "" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	ctx := r.Context()
	householdID := auth.HouseholdIDFromContext(ctx)

	// Resolve calendar UID to calendar ID.
	cal, err := repo.GetCalendarByCalDAVUID(ctx, householdID, calUID)
	if err != nil {
		logging.Logger.Error("caldav: get calendar",
			slog.String("calendar_uid", calUID),
			slog.String("household_id", householdID),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if cal == nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// Fast path: read the object outside any transaction and evaluate the
	// preconditions, so an obviously stale If-Match (or If-None-Match: * on
	// an existing event) is refused before the body is read or parsed. This
	// check is advisory only: another device can write between it and our
	// transaction. The authoritative check runs again inside the write
	// transaction against the row locked FOR UPDATE (see decide below and
	// db.PutCalendarObject), which is what prevents lost updates.
	existing, err := repo.GetCalendarObjectByUID(ctx, householdID, cal.ID, eventUID)
	if err != nil {
		logging.Logger.Error("caldav: get existing object",
			slog.String("calendar_uid", calUID),
			slog.String("event_uid", eventUID),
			slog.String("household_id", householdID),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	ifMatch := r.Header.Get("If-Match")
	ifNoneMatch := r.Header.Get("If-None-Match")
	if res := evalPUTPreconditions(ifMatch, ifNoneMatch, existing); res != preconditionPass {
		writePreconditionFailed(w, res)
		return
	}

	// Read iCalendar body.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logging.Logger.Error("caldav: read body",
			slog.String("event_uid", eventUID),
			slog.String("error", err.Error()))
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Parse the iCalendar body.
	ev, err := ical.ParseEvent(string(body))
	if err != nil {
		logging.Logger.Error("caldav: parse ical",
			slog.String("event_uid", eventUID),
			slog.String("error", err.Error()))
		http.Error(w, "Bad Request: invalid iCalendar data", http.StatusBadRequest)
		return
	}
	if rejectNonXMLChars(w, body, eventUID) {
		return
	}
	if rejectUnsupportedRecurrence(w, body, eventUID) {
		return
	}

	// Override UID with the one from the URL path (CalDAV convention:
	// the URL resource name determines the event UID).
	ev.UID = eventUID
	clientSequence := ev.Sequence

	// Generate an ETag for the new revision.
	etag := db.ETagGen()

	// The body is also kept verbatim in ical_raw (with its RRULE in rrule) so
	// GET/REPORT can hand it back losslessly: the JSON keeps only the fields
	// the API and MCP tools use and drops RRULE, EXDATE, VALARM and the rest.
	icalRaw, rrule := rawForStorage(body)

	// decide runs inside the write transaction with the current row locked
	// FOR UPDATE (nil when the event does not exist yet). It re-evaluates
	// If-Match / If-None-Match against that row — this is the authoritative
	// check — and derives SEQUENCE from it. It may run more than once (after
	// a lost insert race), so it only reads its inputs.
	decide := func(cur *db.CalendarObject) (db.ObjectWrite, error) {
		if res := evalPUTPreconditions(ifMatch, ifNoneMatch, cur); res != preconditionPass {
			return db.ObjectWrite{}, &preconditionError{result: res}
		}
		next := *ev
		next.Sequence = nextSequence(cur, clientSequence, eventUID)
		jsonData, err := json.Marshal(&next)
		if err != nil {
			return db.ObjectWrite{}, fmt.Errorf("marshal event: %w", err)
		}
		return db.ObjectWrite{ICALData: string(jsonData), ETag: etag, ICALRaw: icalRaw, RRule: rrule}, nil
	}

	// Write the event, its calendar_changes row and the CTag bump in one
	// transaction (see db.PutCalendarObject). A stale CTag would make Apple
	// Calendar miss the change forever, and a missing change row would hide
	// it from sync-collection clients. If the calendar is not owned by the
	// caller's household (defense-in-depth check at the DB layer), stored is
	// nil and we return 404 rather than leaking that the calendar exists.
	stored, created, err := repo.PutCalendarObject(ctx, householdID, cal.ID, eventUID, decide)
	if err != nil {
		var pe *preconditionError
		if errors.As(err, &pe) {
			writePreconditionFailed(w, pe.result)
			return
		}
		logging.Logger.Error("caldav: put object + ctag (tx)",
			slog.String("calendar_uid", calUID),
			slog.String("event_uid", eventUID),
			slog.String("household_id", householdID),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if stored == nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// 201 Created for a new resource, 200 OK for an update, as decided by the
	// write itself (not the fast-path read, which a concurrent writer can
	// make stale).
	//
	// RFC 7232 requires ETags to be quoted strings. Quote consistently with
	// PROPFIND/REPORT so Apple Calendar's If-None-Match comparisons match.
	w.Header().Set("ETag", `"`+etag+`"`)
	if created {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "Event created: %s", eventUID)
	} else {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Event updated: %s", eventUID)
	}
}

// preconditionError carries a failed PUT precondition out of the write
// transaction. It wraps db.ErrPreconditionFailed so callers that only care
// about "412 or not" can use errors.Is.
type preconditionError struct {
	result putPreconditionResult
}

func (e *preconditionError) Error() string {
	return fmt.Sprintf("precondition failed (%d)", e.result)
}

func (e *preconditionError) Unwrap() error { return db.ErrPreconditionFailed }

// evalPUTPreconditions applies checkPUTPreconditions to a stored object
// (nil when the resource does not exist).
func evalPUTPreconditions(ifMatch, ifNoneMatch string, cur *db.CalendarObject) putPreconditionResult {
	if cur == nil {
		return checkPUTPreconditions(ifMatch, ifNoneMatch, "", false)
	}
	return checkPUTPreconditions(ifMatch, ifNoneMatch, cur.ETag, true)
}

// writePreconditionFailed answers 412 for a failed PUT precondition.
func writePreconditionFailed(w http.ResponseWriter, res putPreconditionResult) {
	msg := "Precondition Failed"
	switch res {
	case preconditionFailExists:
		msg = "Precondition Failed: If-None-Match: * but resource exists"
	case preconditionFailMissing:
		msg = "Precondition Failed: If-Match on missing resource"
	case preconditionFailMismatch:
		msg = "Precondition Failed: ETag mismatch"
	}
	http.Error(w, msg, http.StatusPreconditionFailed)
}

// nextSequence returns the SEQUENCE (RFC 5545 §3.8.7.4) to store for a PUT.
// A new event keeps the client's value (0 when absent). Each PUT to an
// existing resource increments the stored sequence so CalDAV clients like
// Apple Calendar can tell which version is newer; without it every PUT would
// re-store the same SEQUENCE and clients would re-sync every event on every
// poll. cur is the row locked inside the write transaction, so two concurrent
// updates cannot both derive their increment from the same prior value.
func nextSequence(cur *db.CalendarObject, clientSequence int, eventUID string) int {
	if cur == nil {
		return clientSequence
	}
	var prev ical.CalEvent
	if err := json.Unmarshal([]byte(cur.ICALData), &prev); err != nil {
		// Could not parse the prior payload: keep the client's sequence (or 1
		// to signal an update happened). Failing a write over a corrupt prior
		// row would be worse than resetting the sequence.
		logging.Logger.Warn("caldav: parse prior event for sequence failed; starting from client-supplied sequence",
			slog.String("event_uid", eventUID),
			slog.String("error", err.Error()))
		if clientSequence == 0 {
			return 1
		}
		return clientSequence
	}
	return prev.Sequence + 1
}

// HandleDELETE handles DELETE /dav/{calendar-uid}/{event-uid}.ics
// Removes a calendar event and increments the calendar CTag.
//
// If-Match (RFC 7232 §3.1) is honoured: Apple Calendar sends the ETag it last
// saw, and a device that has not synced yet must not delete an event another
// member just edited. The check runs against the row locked inside the delete
// transaction (db.DeleteCalendarObjectIf), so a concurrent PUT cannot slip in
// between the check and the delete.
//
// Response codes:
//   - 204 No Content           — deleted, or (without If-Match) already gone
//   - 404 Not Found            — calendar doesn't exist / other household
//   - 412 Precondition Failed  — If-Match does not match the current ETag, or
//     If-Match was sent for an event that does not exist
//   - 500 Internal Server Error — database error
func HandleDELETE(w http.ResponseWriter, r *http.Request, repo *db.Repo, path string) {
	if !isResourcePath(path) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	calUID := extractCalendarUIDFromResource(path)
	eventUID := extractEventUID(path)
	if calUID == "" || eventUID == "" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	ctx := r.Context()
	householdID := auth.HouseholdIDFromContext(ctx)

	// Resolve calendar UID to calendar ID.
	cal, err := repo.GetCalendarByCalDAVUID(ctx, householdID, calUID)
	if err != nil {
		logging.Logger.Error("caldav: get calendar",
			slog.String("calendar_uid", calUID),
			slog.String("household_id", householdID),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if cal == nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// The delete, its calendar_changes tombstone and the CTag bump commit
	// together (a delete without the CTag bump would leave the event on Apple
	// Calendar forever). An empty If-Match is treated as absent, exactly as
	// for PUT; If-None-Match has no meaning for a CalDAV DELETE and is
	// ignored.
	ifMatch := r.Header.Get("If-Match")
	check := func(cur *db.CalendarObject) error {
		if res := evalPUTPreconditions(ifMatch, "", cur); res != preconditionPass {
			return &preconditionError{result: res}
		}
		return nil
	}
	owned, _, err := repo.DeleteCalendarObjectIf(ctx, householdID, cal.ID, eventUID, check)
	if err != nil {
		var pe *preconditionError
		if errors.As(err, &pe) {
			writePreconditionFailed(w, pe.result)
			return
		}
		logging.Logger.Error("caldav: delete object + ctag (tx)",
			slog.String("calendar_uid", calUID),
			slog.String("event_uid", eventUID),
			slog.String("household_id", householdID),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if !owned {
		// The calendar left the household between the lookup and the tx.
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// renderObjectICal returns the iCalendar text CalDAV serves for a stored
// object, for GET and every REPORT.
//
// When the object carries the VCALENDAR a client PUT (ical_raw) and that text
// still describes the event in ical_data, it is returned byte for byte, so
// RRULE, EXDATE, VALARM, attendees and any X- properties survive the round
// trip. Otherwise the text is generated from the JSON, as it was before
// ical_raw existed. That covers:
//   - rows with NULL ical_raw (written before migration 036, or by the API
//     and MCP tools, which only write ical_data);
//   - rows whose ical_raw is stale: the API and MCP tools update ical_data
//     without touching ical_raw, so after such an edit the raw text no longer
//     matches and serving it would undo the edit on every CalDAV client.
//
// Staleness is detected by parsing ical_raw with the same parser PUT used to
// build ical_data and comparing the fields ical_data holds (see
// rawMatchesEvent). A fresh PUT therefore always matches.
func renderObjectICal(obj *db.CalendarObject) (string, error) {
	ev := &ical.CalEvent{}
	if err := json.Unmarshal([]byte(obj.ICALData), ev); err != nil {
		return "", fmt.Errorf("unmarshal event: %w", err)
	}
	if obj.ICALRaw != nil && *obj.ICALRaw != "" && rawMatchesEvent(*obj.ICALRaw, ev) {
		return *obj.ICALRaw, nil
	}
	ev.UID = obj.UID
	// Source DTSTAMP/LAST-MODIFIED from the DB updated_at so the same stored
	// event produces identical values on every GET. Without this, ev.DTStamp
	// is zero and generateEventBody falls back to time.Now(), which changes
	// on every request and forces Apple Calendar to re-sync every event every
	// poll. ev.UpdatedAt is also typically zero for API-written events
	// because the API doesn't populate it before marshaling.
	ev.DTStamp = obj.UpdatedAt
	return ical.GenerateEvent(ev), nil
}

// rawMatchesEvent reports whether raw, parsed the way PUT parses a body,
// yields the same event fields as ev (the stored JSON). Only the fields
// ical_data carries and the generated iCalendar emits are compared; SEQUENCE
// is left out because PUT bumps it in the JSON while the raw text keeps the
// client's own value.
func rawMatchesEvent(raw string, ev *ical.CalEvent) bool {
	parsed, err := ical.ParseEvent(raw)
	if err != nil {
		return false
	}
	return parsed.Title == ev.Title &&
		parsed.Description == ev.Description &&
		parsed.Location == ev.Location &&
		parsed.AllDay == ev.AllDay &&
		parsed.Start.Equal(ev.Start) &&
		parsed.End.Equal(ev.End)
}

// rejectNonXMLChars answers a PUT with 403 Forbidden and the
// CALDAV:valid-calendar-data precondition when a valid-UTF-8 body contains a
// rune XML 1.0 forbids (C0 controls other than HTAB/LF/CR such as \x0B or
// \x1B, U+FFFE, U+FFFF). RFC 5545 §3.1 already excludes CONTROL characters
// other than HTAB from property values, and the body is served verbatim from
// ical_raw inside REPORT/PROPFIND XML, where one such rune would make the
// whole multistatus unparseable (#4659). The body is refused rather than
// silently stripped so ical_raw stays byte-exact for everything we accept.
// Bodies that are not valid UTF-8 are left to the existing handling
// (rawForStorage does not keep them). It reports whether it wrote the response.
func rejectNonXMLChars(w http.ResponseWriter, body []byte, eventUID string) bool {
	if !utf8.Valid(body) {
		return false
	}
	i := firstNonXMLChar(string(body))
	if i < 0 {
		return false
	}
	logging.Logger.Warn("caldav: PUT rejected: character not allowed in XML",
		slog.String("event_uid", eventUID),
		slog.Int("offset", i))
	writeValidCalendarDataError(w)
	return true
}

// writeValidCalendarDataError writes 403 Forbidden with the
// CALDAV:valid-calendar-data precondition element (RFC 4791 §5.3.2.1).
func writeValidCalendarDataError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>`+"\n"+
		`<D:error xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><C:valid-calendar-data/></D:error>`+"\n")
}

// rawForStorage returns the PUT body to keep in ical_raw and the RRULE value
// to keep in rrule. Either is nil when there is nothing to store. A body that
// is not valid UTF-8 or contains a NUL byte is not kept (Postgres TEXT
// rejects both, and failing the PUT would be worse than falling back to the
// generated iCalendar for that event).
func rawForStorage(body []byte) (icalRaw *string, rrule *string) {
	if len(body) == 0 || !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		return nil, nil
	}
	raw := string(body)
	if r := extractRRULE(raw); r != "" {
		rrule = &r
	}
	return &raw, rrule
}

// extractRRULE returns the value of the first RRULE property inside a VEVENT
// of an iCalendar text, after unfolding continuation lines (RFC 5545 §3.1)
// with ical.UnfoldLines, which is linear in the input size.
// Property names are case-insensitive. Returns "" when there is none.
func extractRRULE(icalText string) string {
	unfolded := ical.UnfoldLines(icalText)

	inEvent := false
	for _, line := range unfolded {
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "BEGIN:VEVENT"):
			inEvent = true
			continue
		case strings.HasPrefix(upper, "END:VEVENT"):
			inEvent = false
			continue
		}
		if !inEvent {
			continue
		}
		if !strings.HasPrefix(upper, "RRULE:") && !strings.HasPrefix(upper, "RRULE;") {
			continue
		}
		// RRULE takes no quoted parameters, so the first colon ends the
		// name/params part.
		if i := strings.IndexByte(line, ':'); i >= 0 {
			return strings.TrimSpace(line[i+1:])
		}
	}
	return ""
}
