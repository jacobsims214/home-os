package caldav

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"home-os/calendar/internal/auth"
	"home-os/calendar/internal/db"
	"home-os/calendar/internal/ical"
	"home-os/calendar/internal/logging"
)

// ----- XML types for REPORT request parsing -----

// reportRequest is the top-level XML element for REPORT requests.
// Tags use local names only (no namespace) so any prefix works (C:, D:, or none).
type reportRequest struct {
	XMLName          xml.Name
	CalendarQuery    *calendarQuery    `xml:"calendar-query"`
	CalendarMultiget *calendarMultiget `xml:"calendar-multiget"`
}

type calendarQuery struct {
	Prop   *reportProp     `xml:"prop"`
	Filter *calendarFilter `xml:"filter"`
}

type calendarMultiget struct {
	Prop *reportProp `xml:"prop"`
	Href []string    `xml:"href"`
}

type reportProp struct {
	GetETag      *struct{} `xml:"getetag"`
	CalendarData *struct{} `xml:"calendar-data"`
}

type calendarFilter struct {
	CompFilter *compFilter `xml:"comp-filter"`
}

type compFilter struct {
	Name       string      `xml:"name,attr"`
	CompFilter *compFilter `xml:"comp-filter"`
	TimeRange  *timeRange  `xml:"time-range"`
}

type timeRange struct {
	Start string `xml:"start,attr,omitempty"`
	End   string `xml:"end,attr,omitempty"`
}

// ----- REPORT handler -----

// reportHandler is the internal entry point for REPORT requests.
// It is called from HandleREPORT in handler.go.
func reportHandler(w http.ResponseWriter, r *http.Request, repo *db.Repo, path string) {
	// Read and parse the XML body.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logging.Logger.Error("caldav: report read body",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("error", err.Error()))
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	var req reportRequest
	if err := xml.Unmarshal(body, &req); err != nil {
		logging.Logger.Warn("caldav: report parse xml",
			slog.String("path", r.URL.Path),
			slog.String("error", err.Error()))
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Determine report type by checking the raw XML body. Log the body only
	// at debug — REPORT bodies contain calendar event data (titles, times,
	// locations, attendees) which is PII. Gating behind debug prevents
	// accidental exposure in production logs while keeping the field
	// available for interop debugging.
	logging.Logger.Debug("caldav: REPORT body",
		slog.String("path", r.URL.Path),
		slog.Int("body_bytes", len(body)),
		slog.String("body", string(body)))
	switch {
	case strings.Contains(string(body), "calendar-query"):
		handleCalendarQuery(w, r, repo, path, body)
	case strings.Contains(string(body), "calendar-multiget"):
		handleCalendarMultiget(w, r, repo, path, body)
	case strings.Contains(string(body), "sync-collection"):
		handleSyncCollection(w, r, repo, path, body)
	default:
		logging.Logger.Warn("caldav: unknown report type",
			slog.String("path", r.URL.Path),
			slog.String("report_type", req.XMLName.Local))
		http.Error(w, "Bad Request: unknown report type", http.StatusBadRequest)
	}
}

// syncTokenPrefix is the opaque URI prefix used for every sync-token we emit.
// The numeric revision follows the final slash, e.g.
// "http://home-os.local/ns/sync/rev/42". Clients treat the token as opaque;
// we parse it back to a revision on the next sync-collection request. The
// prefix is namespaced under home-os.local so it cannot collide with another
// server's tokens if a calendar is ever moved between deployments.
const syncTokenPrefix = "http://home-os.local/ns/sync/rev/"

// handleSyncCollection processes a sync-collection REPORT (RFC 6578).
// Apple Calendar uses this for incremental sync: after an initial full fetch
// it sends only the sync-token we last returned and expects back the set of
// resources that changed since then — created/updated resources with their
// data and deleted resources as 404 tombstones — plus a new sync-token
// pointing at the latest revision.
//
// Token semantics:
//   - No <sync-token> element (or empty): initial sync. Return ALL current
//     events with their data and a new token encoding the calendar's latest
//     change revision.
//   - A token we previously emitted (rev/{N}): incremental sync. Return
//     changes with revision > N and a new token for the current latest
//     revision.
//   - A non-empty token we cannot parse (e.g. an old CTag-based token from
//     before change-tracking shipped, or a token from a different calendar):
//     fall back to a full initial sync so the client converges instead of
//     erroring out. This is logged so operators can spot stale clients.
func handleSyncCollection(w http.ResponseWriter, r *http.Request, repo *db.Repo, path string, body []byte) {
	ctx := r.Context()
	householdID := auth.HouseholdIDFromContext(ctx)

	calUID := extractCalendarUID(path)
	if calUID == "" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	cal, err := repo.GetCalendarByCalDAVUID(ctx, householdID, calUID)
	if err != nil {
		logging.Logger.Error("caldav: sync-collection: get calendar",
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

	token := extractSyncToken(string(body))
	logging.Logger.Info("caldav: sync-collection",
		slog.String("calendar", cal.Name),
		slog.String("calendar_uid", calUID),
		slog.String("sync_token", token),
		slog.String("household_id", householdID))

	// The new sync-token always reflects the latest revision at the time of
	// this response, so the client's next sync returns only changes that land
	// after this point. Computed up front so a failure here aborts before we
	// write any response bytes.
	latestRev, err := repo.LatestChangeRevision(ctx, householdID, cal.ID)
	if err != nil {
		logging.Logger.Error("caldav: sync-collection: latest revision",
			slog.String("calendar", cal.Name),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	buf := &strings.Builder{}
	writeXMLHeader(buf)

	switch sinceRev, parsed := parseSyncTokenRevision(token); {
	case token == "":
		// Initial sync: emit every current event.
		if err := writeSyncInitialResponses(ctx, buf, repo, householdID, cal); err != nil {
			logging.Logger.Error("caldav: sync-collection: initial sync",
				slog.String("calendar", cal.Name),
				slog.String("error", err.Error()))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	case !parsed:
		// Unparseable token — fall back to a full initial sync so the client
		// converges rather than receiving a 403/desync it cannot recover from.
		logging.Logger.Warn("caldav: sync-collection: unparseable token, falling back to initial sync",
			slog.String("calendar", cal.Name),
			slog.String("sync_token", token))
		if err := writeSyncInitialResponses(ctx, buf, repo, householdID, cal); err != nil {
			logging.Logger.Error("caldav: sync-collection: initial sync (fallback)",
				slog.String("calendar", cal.Name),
				slog.String("error", err.Error()))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	default:
		// Incremental sync: emit only changes since sinceRev.
		if err := writeSyncIncrementalResponses(ctx, buf, repo, householdID, cal, sinceRev); err != nil {
			logging.Logger.Error("caldav: sync-collection: incremental sync",
				slog.String("calendar", cal.Name),
				slog.Int64("since_rev", sinceRev),
				slog.String("error", err.Error()))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}

	// The new sync-token maps to the latest revision. Even on initial sync
	// with zero changes (latestRev=0) we emit rev/0, which the client stores
	// and sends back; the next sync then returns changes with revision > 0.
	fmt.Fprintf(buf, "  <D:sync-token>%s</D:sync-token>\n", xmlEscape(formatSyncToken(latestRev)))
	writeXMLFooter(buf)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("DAV", "1, 2, 3, access-control, calendar-access")
	w.WriteHeader(http.StatusMultiStatus)
	w.Write([]byte(buf.String()))
}

// writeSyncInitialResponses emits a D:response entry (with data) for every
// current event in the calendar. Used for the no-token initial sync and for
// the unparseable-token fallback.
func writeSyncInitialResponses(ctx context.Context, buf *strings.Builder, repo *db.Repo, householdID string, cal *db.Calendar) error {
	objects, err := repo.ListCalendarObjects(ctx, householdID, cal.ID)
	if err != nil {
		return fmt.Errorf("list objects: %w", err)
	}
	for i := range objects {
		obj := &objects[i]
		icalText, err := generateICalText(obj)
		if err != nil {
			logging.Logger.Warn("caldav: sync-collection: skip malformed event",
				slog.String("event_uid", obj.UID),
				slog.String("error", err.Error()))
			continue
		}
		writeReportResponse(buf, cal.CalDAVUID, obj.UID, obj.ETag, icalText)
	}
	logging.Logger.Info("caldav: sync-collection: initial sync",
		slog.String("calendar", cal.Name),
		slog.Int("events", len(objects)))
	return nil
}

// writeSyncIncrementalResponses emits one D:response per event_uid whose
// latest change in the (sinceRev, +inf) window is a create/update (with the
// current event data), or a 404 tombstone if the latest change is a delete.
//
// Multiple changes to the same event within the window are collapsed to the
// final one: e.g. create-then-delete yields only a tombstone (the client
// never sees the intermediate create), and delete-then-recreate yields only
// the current data (no spurious tombstone). This matches what the client
// needs to converge to the present state.
func writeSyncIncrementalResponses(ctx context.Context, buf *strings.Builder, repo *db.Repo, householdID string, cal *db.Calendar, sinceRev int64) error {
	changes, err := repo.ListChangesSince(ctx, householdID, cal.ID, sinceRev)
	if err != nil {
		return fmt.Errorf("list changes: %w", err)
	}
	if len(changes) == 0 {
		logging.Logger.Info("caldav: sync-collection: incremental sync returned 0 changes",
			slog.String("calendar", cal.Name),
			slog.Int64("since_rev", sinceRev))
		return nil
	}

	// Fetch all current objects once and index by UID so create/update
	// responses can include the latest data without a per-change lookup.
	objects, err := repo.ListCalendarObjects(ctx, householdID, cal.ID)
	if err != nil {
		return fmt.Errorf("list objects: %w", err)
	}
	objByUID := make(map[string]db.CalendarObject, len(objects))
	for i := range objects {
		objByUID[objects[i].UID] = objects[i]
	}

	// finalAction[uid] = the change with the highest revision for that uid.
	// Because changes is ordered by revision ASC, a later iteration overwrites
	// the earlier entry, leaving the final state.
	finalAction := make(map[string]db.CalendarChange, len(changes))
	for _, ch := range changes {
		finalAction[ch.EventUID] = ch
	}

	// Emit in revision order, but only for the change that is final for its
	// uid. This guarantees exactly one response per affected uid and a stable
	// ordering in the output.
	emitted := make(map[string]bool, len(finalAction))
	var emittedCount int
	for _, ch := range changes {
		if emitted[ch.EventUID] {
			continue
		}
		if final := finalAction[ch.EventUID]; final.Revision != ch.Revision {
			// A later change supersedes this one for the same uid; defer the
			// emit to that later iteration.
			continue
		}
		emitted[ch.EventUID] = true

		switch ch.Action {
		case db.ChangeCreate, db.ChangeUpdate:
			obj, ok := objByUID[ch.EventUID]
			if !ok {
			// The final action is create/update but the object is gone.
			// This should not happen (a delete would have superseded it
			// with a higher revision); skip defensively rather than emit
			// a response with no data.
			logging.Logger.Warn("caldav: sync-collection: change recorded but object missing, skipping",
				slog.String("event_uid", ch.EventUID),
				slog.String("action", string(ch.Action)))
			continue
		}
		icalText, err := generateICalText(&obj)
		if err != nil {
			logging.Logger.Warn("caldav: sync-collection: skip malformed event",
				slog.String("event_uid", obj.UID),
				slog.String("error", err.Error()))
			continue
		}
		writeReportResponse(buf, cal.CalDAVUID, obj.UID, obj.ETag, icalText)
		emittedCount++
	case db.ChangeDelete:
		writeSyncTombstone(buf, cal.CalDAVUID, ch.EventUID)
		emittedCount++
	default:
		logging.Logger.Warn("caldav: sync-collection: unknown action, skipping",
			slog.String("event_uid", ch.EventUID),
			slog.String("action", string(ch.Action)))
	}
	}
	logging.Logger.Info("caldav: sync-collection: incremental sync",
		slog.String("calendar", cal.Name),
		slog.Int64("since_rev", sinceRev),
		slog.Int("responses", emittedCount))
	return nil
}

// generateICalText unmarshals a stored calendar object's JSON ical_data into
// a CalEvent and regenerates the iCalendar text representation. The UID is
// set from the DB column because it is not stored inside the JSON.
// DTStamp/LAST-MODIFIED are sourced from the DB updated_at so re-generation
// is deterministic across REPORT responses (see HandleGET for the full
// rationale).
func generateICalText(obj *db.CalendarObject) (string, error) {
	ev := &ical.CalEvent{}
	if err := json.Unmarshal([]byte(obj.ICALData), ev); err != nil {
		return "", fmt.Errorf("unmarshal event: %w", err)
	}
	ev.UID = obj.UID
	ev.DTStamp = obj.UpdatedAt
	return ical.GenerateEvent(ev), nil
}

// writeSyncTombstone writes a D:response entry for a deleted resource per
// RFC 6578 §3.5: a href and a 404 status, and NO propstat element. This is
// what tells incremental-sync clients to remove the resource locally.
func writeSyncTombstone(w io.Writer, calUID, eventUID string) {
	fmt.Fprintf(w, "  <D:response>\n")
	fmt.Fprintf(w, "    <D:href>/dav/%s/%s.ics</D:href>\n", xmlEscape(calUID), xmlEscape(eventUID))
	fmt.Fprintf(w, "    <D:status>HTTP/1.1 404 Not Found</D:status>\n")
	fmt.Fprintf(w, "  </D:response>\n")
}

// extractSyncToken pulls the text content of the first <sync-token> element
// from a sync-collection request body. Returns "" when the element is absent
// or empty — both mean "initial sync" per RFC 6578.
func extractSyncToken(xmlBody string) string {
	decoder := xml.NewDecoder(strings.NewReader(xmlBody))
	for {
		tok, err := decoder.Token()
		if err != nil {
			return ""
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local == "sync-token" {
			var content string
			if err := decoder.DecodeElement(&content, &start); err == nil {
				return strings.TrimSpace(content)
			}
			return ""
		}
	}
}

// parseSyncTokenRevision extracts the numeric revision from a sync-token we
// previously emitted (syncTokenPrefix + revision). Returns (0, false) if the
// token does not match our format — callers should treat this as "fall back
// to initial sync" rather than erroring, so stale tokens from older server
// versions converge gracefully.
func parseSyncTokenRevision(token string) (int64, bool) {
	s := strings.TrimPrefix(token, syncTokenPrefix)
	if s == token {
		// TrimPrefix returned the original string unchanged: prefix did not
		// match, so this is not one of our revision tokens.
		return 0, false
	}
	rev, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return rev, true
}

// formatSyncToken builds the opaque sync-token string for a given revision.
func formatSyncToken(rev int64) string {
	return syncTokenPrefix + strconv.FormatInt(rev, 10)
}

// handleCalendarQuery processes a calendar-query REPORT.
// It queries events by time range and returns them as multistatus XML.
func handleCalendarQuery(w http.ResponseWriter, r *http.Request, repo *db.Repo, path string, body []byte) {
	ctx := r.Context()
	householdID := auth.HouseholdIDFromContext(ctx)

	// Extract the calendar UID from the request path.
	calUID := extractCalendarUID(path)
	if calUID == "" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

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

	// Extract time range from the raw XML body.
	timeStart, timeEnd, hasTimeRange := extractTimeRangeFromXML(string(body))

	// Fetch all objects for this calendar.
	objects, err := repo.ListCalendarObjectsInRange(ctx, householdID, cal.ID)
	if err != nil {
		logging.Logger.Error("caldav: list objects",
			slog.String("calendar", cal.Name),
			slog.String("household_id", householdID),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Filter by time range (in-app since events are stored as JSON).
	var filtered []db.CalendarObject
	if hasTimeRange {
		for _, obj := range objects {
			if eventInTimeRange(obj.ICALData, timeStart, timeEnd) {
				filtered = append(filtered, obj)
			}
		}
	} else {
		filtered = objects
	}

	// Build multistatus XML response.
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("DAV", "1, 2, 3, calendar-access")
	w.WriteHeader(http.StatusMultiStatus)

	buf := &strings.Builder{}
	writeXMLHeader(buf)

	for _, obj := range filtered {
		// Unmarshal the stored JSON to a CalEvent so we can generate iCalendar text.
		ev := &ical.CalEvent{}
		if err := json.Unmarshal([]byte(obj.ICALData), ev); err != nil {
			logging.Logger.Warn("caldav: unmarshal event for report",
				slog.String("event_uid", obj.UID),
				slog.String("error", err.Error()))
			continue
		}
		// Set UID from the DB column (not stored in JSON)
		ev.UID = obj.UID
		// Source DTSTAMP/LAST-MODIFIED from the DB updated_at for stable
		// re-generation. See HandleGET for the full rationale.
		ev.DTStamp = obj.UpdatedAt
		icalText := ical.GenerateEvent(ev)

		writeReportResponse(buf, cal.CalDAVUID, obj.UID, obj.ETag, icalText)
	}

	writeXMLFooter(buf)
	logging.Logger.Info("caldav: calendar-query",
		slog.String("calendar", cal.Name),
		slog.Int("events", len(filtered)),
		slog.Bool("time_range", hasTimeRange))
	w.Write([]byte(buf.String()))
}

// handleCalendarMultiget processes a calendar-multiget REPORT.
// It fetches specific events by UID and returns them as multistatus XML.
func handleCalendarMultiget(w http.ResponseWriter, r *http.Request, repo *db.Repo, path string, body []byte) {
	ctx := r.Context()
	householdID := auth.HouseholdIDFromContext(ctx)

	calUID := extractCalendarUID(path)
	if calUID == "" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

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

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("DAV", "1, 2, 3, calendar-access")

	buf := &strings.Builder{}
	writeXMLHeader(buf)

	// Extract href elements from XML body and fetch each event.
	// Note: WriteHeader(207) is deferred until ALL lookups succeed so a real DB
	// error can still abort the request with 500. Writing 207 prematurely would
	// commit the response and prevent surfacing the failure.
	bodyStr := string(body)
	for _, href := range extractHrefs(bodyStr) {
		eventUID := extractEventUIDFromHref(href)
		if eventUID == "" {
			continue
		}

		obj, err := repo.GetCalendarObjectByUID(ctx, householdID, cal.ID, eventUID)
		if err != nil {
			// A real DB error (connection failure, query error, etc.) MUST NOT be
			// conflated with "not found". Logging it leaves a server-side trace and
			// returning 500 tells the client to retry instead of treating the event
			// as permanently deleted.
			logging.Logger.Error("caldav: multiget: get object",
				slog.String("calendar", cal.Name),
				slog.String("event_uid", eventUID),
				slog.String("error", err.Error()))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if obj == nil {
			writeReportNotFoundResponse(buf, href)
			continue
		}

		ev := &ical.CalEvent{}
		if err := json.Unmarshal([]byte(obj.ICALData), ev); err != nil {
			continue
		}
		ev.UID = obj.UID
		// Source DTSTAMP/LAST-MODIFIED from the DB updated_at for stable
		// re-generation. See HandleGET for the full rationale.
		ev.DTStamp = obj.UpdatedAt
		icalText := ical.GenerateEvent(ev)
		writeReportResponse(buf, cal.CalDAVUID, obj.UID, obj.ETag, icalText)
	}

	writeXMLFooter(buf)
	w.WriteHeader(http.StatusMultiStatus)
	w.Write([]byte(buf.String()))
}

// ----- XML response builders for REPORT -----

// writeReportResponse writes a single D:response element for a calendar object
// in a REPORT multistatus response.
func writeReportResponse(w io.Writer, calUID, eventUID, etag, icalText string) {
	href := fmt.Sprintf("/dav/%s/%s.ics", calUID, eventUID)

	fmt.Fprintf(w, "  <D:response>\n")
	fmt.Fprintf(w, "    <D:href>%s</D:href>\n", href)
	fmt.Fprint(w, "    <D:propstat>\n")
	fmt.Fprint(w, "      <D:prop>\n")
	fmt.Fprintf(w, "        <D:getetag>\"%s\"</D:getetag>\n", xmlEscape(etag))
	fmt.Fprint(w, "        <C:calendar-data>")
	fmt.Fprint(w, "<![CDATA[")
	fmt.Fprint(w, icalText)
	fmt.Fprint(w, "]]>")
	fmt.Fprint(w, "</C:calendar-data>\n")
	fmt.Fprint(w, "      </D:prop>\n")
	fmt.Fprintf(w, "      <D:status>HTTP/1.1 200 OK</D:status>\n")
	fmt.Fprint(w, "    </D:propstat>\n")
	fmt.Fprintf(w, "  </D:response>\n")
}

// writeReportNotFoundResponse writes a 404 D:response for a calendar object
// that was not found during multiget.
func writeReportNotFoundResponse(w io.Writer, href string) {
	fmt.Fprintf(w, "  <D:response>\n")
	fmt.Fprintf(w, "    <D:href>%s</D:href>\n", xmlEscape(href))
	fmt.Fprint(w, "    <D:propstat>\n")
	fmt.Fprint(w, "      <D:prop/>\n")
	fmt.Fprintf(w, "      <D:status>HTTP/1.1 404 Not Found</D:status>\n")
	fmt.Fprint(w, "    </D:propstat>\n")
	fmt.Fprintf(w, "  </D:response>\n")
}

// ----- Helpers -----

// extractTimeRange walks the comp-filter tree to find a C:time-range element.
func extractTimeRange(cf *compFilter) (time.Time, time.Time, bool) {
	if cf == nil {
		return time.Time{}, time.Time{}, false
	}
	if cf.TimeRange != nil {
		start, _ := parseCalDAVDateTime(cf.TimeRange.Start)
		end, _ := parseCalDAVDateTime(cf.TimeRange.End)
		return start, end, true
	}
	if cf.CompFilter != nil {
		return extractTimeRange(cf.CompFilter)
	}
	return time.Time{}, time.Time{}, false
}

// parseCalDAVDateTime parses a CalDAV datetime string in UTC format:
// YYYYMMDDTHHMMSSZ. Returns zero time if parsing fails.
func parseCalDAVDateTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse("20060102T150405Z", s)
}

// eventInTimeRange checks if a stored event (JSON serialized CalEvent in icalData)
// falls within the given time range [start, end).
func eventInTimeRange(icalData string, rangeStart, rangeEnd time.Time) bool {
	ev := &ical.CalEvent{}
	if err := json.Unmarshal([]byte(icalData), ev); err != nil {
		return false
	}

	// If no range end is specified, only filter by start.
	if rangeEnd.IsZero() {
		return !ev.End.IsZero() && !ev.End.Before(rangeStart)
	}

	// Event overlaps range if event.Start < rangeEnd AND event.End > rangeStart.
	// Standard CalDAV time-range filtering: include if event starts before range end
	// AND event ends after range start (or has no end).
	evEnd := ev.End
	if evEnd.IsZero() {
		evEnd = ev.Start.Add(time.Hour)
	}
	return !ev.Start.After(rangeEnd) && !evEnd.Before(rangeStart)
}

// extractEventUIDFromHref extracts the event UID from a CalDAV href
// like "/dav/{cal-uid}/{event-uid}.ics". Handles URL-encoded characters.
func extractEventUIDFromHref(href string) string {
	// URL-decode the href first (e.g. %40 -> @)
	decoded, err := url.QueryUnescape(href)
	if err != nil {
		decoded = href
	}
	decoded = strings.TrimSuffix(decoded, "/")
	parts := strings.Split(strings.Trim(decoded, "/"), "/")
	if len(parts) < 3 {
		return ""
	}
	last := parts[len(parts)-1]
	if strings.HasSuffix(last, ".ics") {
		return strings.TrimSuffix(last, ".ics")
	}
	return last
}

// extractTimeRangeFromXML parses time-range start/end from raw CalDAV XML.
func extractTimeRangeFromXML(xmlBody string) (time.Time, time.Time, bool) {
	var start, end time.Time
	hasStart := false
	// Look for time-range element and extract start/end attributes
	trStart := extractAttr(xmlBody, "time-range", "start")
	trEnd := extractAttr(xmlBody, "time-range", "end")
	if trStart != "" {
		if t, err := parseCalDAVDateTime(trStart); err == nil {
			start = t
			hasStart = true
		}
	}
	if trEnd != "" {
		if t, err := parseCalDAVDateTime(trEnd); err == nil {
			end = t
		}
	}
	return start, end, hasStart
}

// extractAttr finds an attribute value from an XML element in the body.
func extractAttr(xmlBody, element, attr string) string {
	idx := strings.Index(xmlBody, "<"+element)
	if idx < 0 {
		idx = strings.Index(xmlBody, element+">")
	}
	if idx < 0 {
		return ""
	}
	// Find attribute in the opening tag
	tagEnd := strings.Index(xmlBody[idx:], ">")
	if tagEnd < 0 {
		return ""
	}
	tag := xmlBody[idx : idx+tagEnd]
	// Look for attr="value" or attr='value'
	for _, quote := range []string{"\"", "'"} {
		attrIdx := strings.Index(tag, attr+"="+quote)
		if attrIdx < 0 {
			continue
		}
		valStart := attrIdx + len(attr) + 2 // skip attr="
		valEnd := strings.Index(tag[valStart:], quote)
		if valEnd < 0 {
			continue
		}
		return tag[valStart : valStart+valEnd]
	}
	return ""
}

// extractHrefs extracts all href values from a CalDAV multiget XML body.
// Uses proper XML decoding to handle any namespace prefix (D:, A:, etc.).
func extractHrefs(xmlBody string) []string {
	decoder := xml.NewDecoder(strings.NewReader(xmlBody))
	var hrefs []string
	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		// Match any element with local name "href" in the DAV: namespace.
		if start.Name.Local == "href" && (start.Name.Space == "DAV:" || start.Name.Space == "") {
			var content string
			if err := decoder.DecodeElement(&content, &start); err == nil {
				content = strings.TrimSpace(content)
				if content != "" {
					hrefs = append(hrefs, content)
				}
			}
		}
	}
	return hrefs
}
