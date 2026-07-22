// Package caldav implements the CalDAV protocol handlers for the calendar
// service. This file implements PROPPATCH (RFC 4918 §9.2) — the WebDAV method
// a client uses to update properties on a resource. Apple Calendar sends
// PROPPATCH to rename a calendar or change its color.
package caldav

import (
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"home-os/calendar/internal/auth"
	"home-os/calendar/internal/db"
	"home-os/calendar/internal/logging"
)

// nsIcal is Apple's iCal namespace. Apple Calendar sends calendar-color
// updates using this namespace (e.g. <I:calendar-color>#abcdef</I:calendar-color>
// where I maps to http://apple.com/ns/ical/). RFC 6264 §3 likewise permits the
// CalDAV namespace (urn:ietf:params:xml:ns:caldav) for calendar-color, and we
// accept both — they target the same calendars.color column.
const nsIcal = "http://apple.com/ns/ical/"

// ----- Request parsing (RFC 4918 §9.2) -----
//
// A PROPPATCH body is a propertyupdate element containing one or more set and
// remove instructions. Each instruction wraps a prop element that lists the
// properties to modify. Example (Apple Calendar renaming + recoloring):
//
//   <D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"
//                      xmlns:I="http://apple.com/ns/ical/">
//     <D:set>
//       <D:prop>
//         <D:displayname>Johnson Family</D:displayname>
//         <I:calendar-color>#1BADF8</I:calendar-color>
//       </D:prop>
//     </D:set>
//   </D:propertyupdate>
//
// We use encoding/xml with namespace-aware structs so we can dispatch on
// (namespace, local-name) pairs rather than on the client's chosen prefix —
// different CalDAV clients use different prefixes for the same namespace.

// propertyupdate is the XML root of a PROPPATCH request body.
type propertyupdate struct {
	XMLName xml.Name    `xml:"DAV: propertyupdate"`
	Sets    []propAction `xml:"DAV: set"`
	Removes []propAction `xml:"DAV: remove"`
}

// propAction wraps a prop element inside a set or remove. We capture the
// containing element name (set/remove) implicitly via the slice the parent
// struct unmarshals us into.
type propAction struct {
	Prop rawPropList `xml:"DAV: prop"`
}

// rawPropList captures the list of property elements inside a <D:prop>...</D:prop>
// block. We use xml:",any" so every child element is unmarshaled into Props
// regardless of its namespace, then dispatch on the XMLName in code.
type rawPropList struct {
	Props []rawProp `xml:",any"`
}

// rawProp captures a single property element with its namespace, local name,
// and the text content of the element. For PROPPATCH set, the text content is
// the new value (e.g. "Johnson Family" inside <D:displayname>...</D:displayname>).
// For PROPPATCH remove, only the element name matters; the text is ignored.
//
// We also capture inner XML (InnerXML) so a future extension can parse
// structured property values; for the currently-supported properties
// (displayname, calendar-color) the chardata is sufficient.
type rawProp struct {
	XMLName  xml.Name
	Value    string `xml:",chardata"`
	InnerXML string `xml:",innerxml"`
}

// ----- Property classification -----
//
// classifyPROPPATCHProp is a pure function that maps a rawProp to one of three
// actions. It is pulled out of the handler so it can be unit-tested without
// XML/HTTP plumbing.
//
// The action set drives the DB update:
//   - actionSetName:    the new name is in the Value field
//   - actionSetColor:   the new color is in the Value field
//   - actionUnsupported: anything else (including all remove operations)
//
// The handler aggregates all actionSetColor and actionSetName updates into a
// single repo call — the last value wins for each, which matches RFC 4918
// §9.2.2 "the server MUST apply the changes in the order they appear in the
// request". In practice Apple Calendar sends at most one value per property
// per PROPPATCH.

type proppatchAction int

const (
	actionUnsupported proppatchAction = iota
	actionSetName
	actionSetColor
)

type classifiedProp struct {
	name   xml.Name
	action proppatchAction
	value  string
}

// classifySetProp maps a SET prop element to its action. Only displayname
// (DAV:) and calendar-color (urn:ietf:params:xml:ns:caldav OR
// http://apple.com/ns/ical/) are supported. Anything else is unsupported and
// will be reported 403 in the response.
//
// Value is whitespace-trimmed because Apple Calendar sometimes wraps the
// element body in stray newlines/indentation that would otherwise become part
// of the stored name (e.g. a calendar literally named "\n Johnson Family\n").
func classifySetProp(p rawProp) classifiedProp {
	switch p.XMLName.Space {
	case nsDAV:
		if p.XMLName.Local == "displayname" {
			return classifiedProp{name: p.XMLName, action: actionSetName, value: strings.TrimSpace(p.Value)}
		}
	case nsC:
		if p.XMLName.Local == "calendar-color" {
			return classifiedProp{name: p.XMLName, action: actionSetColor, value: strings.TrimSpace(p.Value)}
		}
	case nsIcal:
		if p.XMLName.Local == "calendar-color" {
			return classifiedProp{name: p.XMLName, action: actionSetColor, value: strings.TrimSpace(p.Value)}
		}
	}
	return classifiedProp{name: p.XMLName, action: actionUnsupported}
}

// classifyRemoveProp maps a REMOVE prop element to its action. We do not
// support removing any calendar properties in this implementation:
//   - displayname is NOT NULL on the calendars table — it cannot be removed.
//   - calendar-color COULD be cleared (set to NULL) but Apple Calendar never
//     sends a remove for it; it sends set with an empty value, which the SET
//     path already handles as NULL via UpdateCalendarProps.
//
// Therefore every remove is classified as unsupported and reported 403 in the
// response. This keeps PROPPATCH semantics simple and predictable while
// satisfying RFC 4918 §9.2.2's requirement that the server report an explicit
// status for every prop named in the request.
func classifyRemoveProp(p rawProp) classifiedProp {
	return classifiedProp{name: p.XMLName, action: actionUnsupported}
}

// ----- Response building (RFC 4918 §9.2) -----
//
// The response is a multistatus with one response element per resource
// patched. Inside the response, each propstat groups props by their per-prop
// status. We emit at most two propstats:
//
//   - 200 OK for successfully updated props (the supported set ones)
//   - 403 Forbidden for unsupported props (anything in the unsupported list,
//     and ALL removes)
//
// We deliberately DO NOT emit a 403 propstat for a supported prop whose DB
// update failed; that case is handled by a 500 inner server error at the HTTP
// layer — the multistatus contract assumes the server could attempt all
// supported updates. A 500 is the honest answer when the server itself is
// broken.
//
// Namespace declarations: the multistatus root declares D, C, CS (matching
// PROPFIND responses for consistency) and I (Apple iCal) so that we can echo
// back <I:calendar-color/> verbatim when Apple Calendar set the color through
// that namespace. The client matches by namespace URI, not prefix, so any
// prefix choice is safe.

// writePROPPATCHHeader writes the XML declaration and opens the multistatus
// element with the four namespace declarations PROPPATCH responses need. We
// use a separate header from PROPFIND because PROPFIND never needs the Apple
// iCal namespace and adding an unused xmlns would just bloat every PROPFIND
// response.
func writePROPPATCHHeader(w io.Writer) {
	fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>`+"\n")
	fmt.Fprint(w, `<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CS="http://calendarserver.org/ns/" xmlns:I="http://apple.com/ns/ical/">`+"\n")
}

// writePROPPATCHFooter closes the multistatus element.
func writePROPPATCHFooter(w io.Writer) {
	fmt.Fprint(w, "</D:multistatus>\n")
}

// writePROPPATCHResponse writes one D:response entry for the patched calendar.
// okProps are the (namespace, local-name) pairs of props we successfully
// updated (status 200). failedProps are the pairs we refused to update
// (status 403 Forbidden — unsupported property or remove attempt on a
// supported-but-unremovable property).
//
// The prop element inside each propstat is empty (no value) — RFC 4918 §9.2
// shows the propstat prop as a name-only element. The status text is the
// standard RFC 4918 line ("HTTP/1.1 200 OK" / "HTTP/1.1 403 Forbidden").
func writePROPPATCHResponse(w io.Writer, calUID string, okProps, failedProps []xml.Name) {
	fmt.Fprintf(w, "  <D:response>\n")
	fmt.Fprintf(w, "    <D:href>/dav/%s/</D:href>\n", xmlEscape(calUID))

	if len(okProps) > 0 {
		fmt.Fprint(w, "    <D:propstat>\n")
		fmt.Fprint(w, "      <D:prop>\n")
		for _, name := range okProps {
			writePropNameElement(w, name)
		}
		fmt.Fprint(w, "      </D:prop>\n")
		fmt.Fprint(w, "      <D:status>HTTP/1.1 200 OK</D:status>\n")
		fmt.Fprint(w, "    </D:propstat>\n")
	}

	if len(failedProps) > 0 {
		fmt.Fprint(w, "    <D:propstat>\n")
		fmt.Fprint(w, "      <D:prop>\n")
		for _, name := range failedProps {
			writePropNameElement(w, name)
		}
		fmt.Fprint(w, "      </D:prop>\n")
		fmt.Fprint(w, "      <D:status>HTTP/1.1 403 Forbidden</D:status>\n")
		fmt.Fprint(w, "    </D:propstat>\n")
	}

	fmt.Fprintf(w, "  </D:response>\n")
}

// writePropNameElement writes a single empty property element using the
// namespace prefix appropriate for its namespace. The four supported prefixes
// (D, C, CS, I) are declared on the multistatus root, so we can use them
// directly without redeclaring the namespace inline. For an unknown
// namespace (e.g. a client that sent a prop we don't recognize under a custom
// namespace), we declare the namespace inline on the element so the response
// is still well-formed XML — the client can still match the failed prop by
// (namespace, local-name).
func writePropNameElement(w io.Writer, name xml.Name) {
	pfx := prefixForNamespacePROPPATCH(name.Space)
	if pfx == "" {
		// Unknown namespace: declare it inline with a generic prefix so the
		// element is namespace-well-formed.
		fmt.Fprintf(w, "        <NS:%s xmlns:NS=\"%s\"/>\n", name.Local, xmlEscape(name.Space))
		return
	}
	fmt.Fprintf(w, "        <%s:%s/>\n", pfx, name.Local)
}

// prefixForNamespacePROPPATCH returns the XML prefix the PROPPATCH response
// builder will use for the given namespace URI, or "" if the namespace is not
// one of the four declared on the multistatus root (D, C, CS, I).
func prefixForNamespacePROPPATCH(ns string) string {
	switch ns {
	case nsDAV:
		return "D"
	case nsC:
		return "C"
	case nsCS:
		return "CS"
	case nsIcal:
		return "I"
	default:
		return ""
	}
}

// ----- Handler -----

// HandlePROPPATCH handles PROPPATCH /dav/{calendar-uid}/ requests.
//
// It parses the RFC 4918 §9.2 propertyupdate body, persists supported
// property changes (displayname → calendars.name, calendar-color →
// calendars.color) via repo.UpdateCalendarProps, and returns a multistatus
// response with one propstat (200 OK) for the successfully updated props and
// one propstat (403 Forbidden) for the unsupported ones.
//
// We accept calendar-color in EITHER the CalDAV namespace
// (urn:ietf:params:xml:ns:caldav) OR the Apple iCal namespace
// (http://apple.com/ns/ical/). Apple Calendar uses the Apple namespace; RFC
// 6264 §3 uses the CalDAV namespace. Both target the same DB column.
//
// We do NOT bump the calendar CTag on a property update. The CTag tracks
// changes to the event collection (the objects inside the calendar), not
// changes to the collection's own metadata. Bumping it on a property change
// would force Apple Calendar to re-fetch every event for no reason. The
// updated_at column on calendars IS bumped, which is enough for any future
// cache-invalidating read of collection metadata.
//
// Response codes:
//   - 207 Multi-Status: PROPPATCH was processed. Per-prop status is in the body.
//   - 400 Bad Request: body is empty or unparseable XML.
//   - 404 Not Found: calendar doesn't exist or isn't owned by the caller's
//     household (identical response to avoid cross-tenant information leak).
//   - 500 Internal Server Error: database error during the update.
func HandlePROPPATCH(w http.ResponseWriter, r *http.Request, repo *db.Repo, path string) {
	// PROPPATCH only targets calendar collections, not the principal URL or
	// individual events. The path must be /dav/{calendar-uid}/.
	uid := extractCalendarUID(path)
	if uid == "" {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	ctx := r.Context()
	householdID := auth.HouseholdIDFromContext(ctx)

	// Resolve the calendar by CalDAV UID, scoped to the caller's household.
	// A 404 here is identical regardless of whether the calendar is missing or
	// owned by another household — this prevents a cross-tenant calendar from
	// being patched (and prevents information leak about its existence).
	cal, err := repo.GetCalendarByCalDAVUID(ctx, householdID, uid)
	if err != nil {
		logging.Logger.Error("caldav: proppatch: get calendar",
			slog.String("calendar_uid", uid),
			slog.String("household_id", householdID),
			slog.String("error", err.Error()))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if cal == nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// Read and parse the request body.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logging.Logger.Error("caldav: proppatch: read body",
			slog.String("calendar_uid", uid),
			slog.String("error", err.Error()))
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		// Empty body PROPPATCH is malformed — RFC 4918 §9.2 requires a
		// propertyupdate element. We do not silently 207 it because that
		// would make Apple Calendar think its (nonexistent) update succeeded.
		http.Error(w, "Bad Request: empty PROPPATCH body", http.StatusBadRequest)
		return
	}

	var req propertyupdate
	if unmarshalErr := xml.Unmarshal(body, &req); unmarshalErr != nil {
		// Body is included at debug level only — PROPPATCH bodies contain
		// calendar display names and colors, which are user data.
		logging.Logger.Warn("caldav: proppatch: parse xml",
			slog.String("calendar_uid", uid),
			slog.String("error", unmarshalErr.Error()))
		logging.Logger.Debug("caldav: proppatch: raw body",
			slog.String("calendar_uid", uid),
			slog.String("body", string(body)))
		http.Error(w, "Bad Request: invalid PROPPATCH body", http.StatusBadRequest)
		return
	}

	// Classify every prop in every set and every remove. We process sets
	// first (so a set+remove on the same prop keeps the set value, matching
	// RFC 4918 §9.2.2 "MUST apply the changes in order"). Removes are all
	// classified as unsupported.
	var (
		okProps     []xml.Name
		failedProps []xml.Name
		newName     *string
		newColor    *string
	)

	for _, set := range req.Sets {
		for _, p := range set.Prop.Props {
			c := classifySetProp(p)
			switch c.action {
			case actionSetName:
				if c.value != "" {
					// RFC 4918 §9.2.2: last set wins. Apple Calendar only
					// sends one value per prop per PROPPATCH, so this loop
					// normally runs once. We still write the loop so a
					// duplicate set on the same prop has deterministic
					// behavior (the second one wins).
					v := c.value
					newName = &v
				}
				// Even an empty displayname set is reported 200 in the
				// response: the prop is supported, the server "accepted"
				// the request, it just had no effect (empty name is
				// illegal because the column is NOT NULL). This matches
				// Apple Calendar's expectation that a supported prop is
				// acknowledged.
				//
				// Note: this is a deliberate choice not to 403 empty
				// displayname sets. A 403 would make Apple Calendar retry
				// the PROPPATCH forever, growing its error log. Apple
				// Calendar never sends an empty displayname in practice.
				okProps = append(okProps, c.name)
			case actionSetColor:
				// calendar-color: any value (including empty) is a
				// legitimate set — empty means "clear the color" (the DB
				// method stores NULL for ""). Last set wins.
				v := c.value
				newColor = &v
				okProps = append(okProps, c.name)
			case actionUnsupported:
				failedProps = append(failedProps, c.name)
			}
		}
	}

	for _, rm := range req.Removes {
		for _, p := range rm.Prop.Props {
			c := classifyRemoveProp(p)
			failedProps = append(failedProps, c.name)
		}
	}

	// Persist the changes (if any). We make a single repo call so the update
	// is one DB round-trip and one transaction-wide updated_at bump.
	//
	// If both newName and newColor are nil, the request contained only
	// unsupported props (or removes). We skip the DB call entirely — there
	// is nothing to update. The response will be a multistatus with only the
	// 403 propstat. This is the correct RFC 4918 §9.2 answer for "I refused
	// every change you asked for".
	if newName != nil || newColor != nil {
		updated, err := repo.UpdateCalendarProps(ctx, householdID, cal.ID, newName, newColor)
		if err != nil {
			logging.Logger.Error("caldav: proppatch: update calendar props",
				slog.String("calendar", cal.Name),
				slog.String("calendar_id", cal.ID),
				slog.String("household_id", householdID),
				slog.String("error", err.Error()))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if !updated {
			// Race: the calendar existed when we resolved it via
			// GetCalendarByCalDAVUID, but the UPDATE matched no rows. This
			// means the calendar (or its household ownership) changed
			// between the two calls. Treat as 404 — the resource the
			// client targeted no longer exists in this household.
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
	}

	// Build and write the multistatus response. We always emit a 207 with the
	// per-prop status, even when there were zero supported props. RFC 4918
	// §9.2 expects a multistatus for every PROPPATCH that completes.
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("DAV", "1, 2, 3, calendar-access")
	w.WriteHeader(http.StatusMultiStatus)

	buf := &strings.Builder{}
	writePROPPATCHHeader(buf)
	writePROPPATCHResponse(buf, cal.CalDAVUID, okProps, failedProps)
	writePROPPATCHFooter(buf)
	if _, werr := w.Write([]byte(buf.String())); werr != nil {
		logging.Logger.Warn("caldav: proppatch: write response",
			slog.String("calendar_uid", cal.CalDAVUID),
			slog.String("error", werr.Error()))
	}
}
