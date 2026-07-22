package caldav

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

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

	// Parse the stored JSON into a CalEvent.
	ev := &ical.CalEvent{}
	if err := json.Unmarshal([]byte(obj.ICALData), ev); err != nil {
		logging.Logger.Error("caldav: unmarshal event",
			slog.String("event_uid", eventUID),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	ev.UID = obj.UID
	// Source DTSTAMP/LAST-MODIFIED from the DB updated_at so the same stored
	// event produces identical values on every GET. Without this, ev.DTStamp
	// is zero and generateEventBody falls back to time.Now(), which changes
	// on every request and forces Apple Calendar to re-sync every event every
	// poll. ev.UpdatedAt is also typically zero for API-written events
	// because the API doesn't populate it before marshaling.
	ev.DTStamp = obj.UpdatedAt

	// Generate iCalendar text.
	icalText := ical.GenerateEvent(ev)

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

	// Fetch the existing object BEFORE upserting. We need:
	//   1. Its current ETag for If-Match comparison.
	//   2. Its mere existence for If-None-Match:* (create-only) — this must
	//      be checked BEFORE the write so a phantom event is not produced.
	//   3. The existence flag to pick 201 Created vs 200 OK after the upsert.
	//
	// Doing this check up front fixes two prior bugs:
	//   - If-None-Match:* used to be evaluated AFTER the upsert, so the
	//     phantom event was written and then exposed to subsequent reads.
	//   - The handler always returned 201, even for updates.
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

	// Evaluate preconditions against the current state, before reading the
	// body so a failing precondition is cheap and produces no side effects.
	currentETag := ""
	if existing != nil {
		currentETag = existing.ETag
	}
	switch checkPUTPreconditions(
		r.Header.Get("If-Match"),
		r.Header.Get("If-None-Match"),
		currentETag,
		existing != nil,
	) {
	case preconditionFailExists:
		http.Error(w, "Precondition Failed: If-None-Match: * but resource exists", http.StatusPreconditionFailed)
		return
	case preconditionFailMissing:
		http.Error(w, "Precondition Failed: If-Match on missing resource", http.StatusPreconditionFailed)
		return
	case preconditionFailMismatch:
		http.Error(w, "Precondition Failed: ETag mismatch", http.StatusPreconditionFailed)
		return
	case preconditionPass:
		// fall through to the write path
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

	// Override UID with the one from the URL path (CalDAV convention:
	// the URL resource name determines the event UID).
	ev.UID = eventUID

	// SEQUENCE revision tracking (RFC 5545 §3.8.7.4): a new event starts at
	// SEQUENCE 0 (the struct's zero value). Each subsequent PUT to the same
	// resource increments the existing sequence so CalDAV clients like Apple
	// Calendar can tell which version is newer. Without this, every PUT
	// re-stores SEQUENCE 0 and clients cannot detect revisions, forcing them
	// to re-sync every event on every poll. We read the prior sequence from
	// the existing stored JSON (fetched above as `existing`) before
	// re-marshaling, so the increment survives the round-trip.
	if existing != nil {
		var prev ical.CalEvent
		if err := json.Unmarshal([]byte(existing.ICALData), &prev); err == nil {
			ev.Sequence = prev.Sequence + 1
		} else {
			// Could not parse the prior payload — preserve any sequence the
			// client sent in the new body (or default to 1 to signal an
			// update happened). Logging only; we still proceed with the PUT
			// because failing a write over a corrupt prior row would be
			// worse than resetting the sequence.
			logging.Logger.Warn("caldav: parse prior event for sequence failed; starting from client-supplied sequence",
				slog.String("event_uid", eventUID),
				slog.String("error", err.Error()))
			if ev.Sequence == 0 {
				ev.Sequence = 1
			}
		}
	}

	// Marshal the event to JSON for storage.
	jsonData, err := json.Marshal(ev)
	if err != nil {
		logging.Logger.Error("caldav: marshal event",
			slog.String("event_uid", eventUID),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Generate an ETag for the new revision.
	etag := db.ETagGen()

	// Upsert the event AND bump the calendar CTag in a single pgx
	// transaction. This is critical: the CTag is what tells Apple Calendar
	// that something in the collection changed and it needs to re-sync. If
	// the write succeeded but the CTag bump failed (or vice versa), the
	// client would silently miss the change forever. Wrapping both in one tx
	// means either they both commit or neither does.
	//
	// If the calendar is not owned by the caller's household (defense-in-depth
	// check at the DB layer), upserted will be nil and we return 404 rather
	// than leaking that the calendar exists. The transaction is rolled back in
	// that case.
	upserted, err := repo.UpsertCalendarObjectWithCTag(ctx, householdID, cal.ID, eventUID, string(jsonData), etag)
	if err != nil {
		logging.Logger.Error("caldav: upsert object + ctag (tx)",
			slog.String("calendar_uid", calUID),
			slog.String("event_uid", eventUID),
			slog.String("household_id", householdID),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if upserted == nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// 201 Created for a new resource, 200 OK for an update. The existence
	// flag was captured BEFORE the upsert, so this correctly reflects whether
	// the PUT created something new versus updated an existing object.
	//
	// RFC 7232 requires ETags to be quoted strings. Quote consistently with
	// PROPFIND/REPORT so Apple Calendar's If-None-Match comparisons match.
	w.Header().Set("ETag", `"`+etag+`"`)
	if existing == nil {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "Event created: %s", eventUID)
	} else {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Event updated: %s", eventUID)
	}
}

// HandleDELETE handles DELETE /dav/{calendar-uid}/{event-uid}.ics
// Removes a calendar event and increments the calendar CTag.
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

	// Delete the event AND bump the calendar CTag in a single pgx
	// transaction. Same rationale as the PUT handler: the CTag bump is what
	// tells Apple Calendar that an event was removed. If the delete succeeded
	// but the CTag bump failed, the client would keep showing the deleted
	// event forever. Wrapping both in one tx means either they both commit or
	// neither does.
	//
	// DELETE is idempotent by CalDAV convention — deleting a missing event is
	// not an error — so a successful (nil) return covers both the "deleted a
	// row" and "row was already gone" cases. If the calendar is not owned by
	// the caller's household, the household guard inside the delete SQL
	// prevents any row from being touched (no CTag bump occurs either).
	if err := repo.DeleteCalendarObjectWithCTag(ctx, householdID, cal.ID, eventUID); err != nil {
		logging.Logger.Error("caldav: delete object + ctag (tx)",
			slog.String("calendar_uid", calUID),
			slog.String("event_uid", eventUID),
			slog.String("household_id", householdID),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
