// Package caldav implements the CalDAV protocol handlers for the calendar
// service. It handles PROPFIND, OPTIONS, MKCALENDAR, and other WebDAV/CalDAV
// methods as specified in RFC 4918 and RFC 4791.
package caldav

import (
	"context"
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

// XML namespace constants used in CalDAV responses.
const (
	nsDAV = "DAV:"
	nsC   = "urn:ietf:params:xml:ns:caldav"
	nsCS  = "http://calendarserver.org/ns/"
)

// ----- PROPFIND request parsing -----

// propfindRequest is the XML body of a PROPFIND request.
type propfindRequest struct {
	XMLName xml.Name       `xml:"DAV: propfind"`
	AllProp *allProp       `xml:"DAV: allprop"`
	Prop    *propList      `xml:"DAV: prop"`
	PropName *propName    `xml:"DAV: propname"`
}

type allProp struct{}
type propName struct{}

type propList struct {
	Props []xmlElement `xml:",any"`
}

// xmlElement captures any XML element with its namespace and local name.
type xmlElement struct {
	XMLName xml.Name
}

// ----- PROPFIND response building -----

// writeXMLHeader writes the XML declaration and opens the multistatus element.
func writeXMLHeader(w io.Writer) {
	fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>`+"\n")
	fmt.Fprint(w, `<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CS="http://calendarserver.org/ns/">`+"\n")
}

// writeXMLFooter closes the multistatus element.
func writeXMLFooter(w io.Writer) {
	fmt.Fprint(w, "</D:multistatus>\n")
}

// writeResponseForCalendar writes a PROPFIND response entry for a calendar collection.
func writeResponseForCalendar(w io.Writer, cal *db.Calendar, props []xmlElement) {
	fmt.Fprintf(w, "  <D:response>\n")
	fmt.Fprintf(w, "    <D:href>/dav/%s/</D:href>\n", cal.CalDAVUID)

	// Determine which properties to return.
	if len(props) == 0 {
		// No specific props requested — return all known props (like allprop).
		writePropStatOK(w, cal, true)
	} else {
		// Return requested properties, with 404 for unsupported ones.
		var known, unknown []xmlElement
		for _, p := range props {
			if isKnownCalendarProp(p.XMLName) {
				known = append(known, p)
			} else {
				unknown = append(unknown, p)
			}
		}
		if len(known) > 0 {
			writePropStatForProps(w, cal, known, "HTTP/1.1 200 OK")
		}
		if len(unknown) > 0 {
			writePropStatNotFound(w, unknown)
		}
	}

	fmt.Fprintf(w, "  </D:response>\n")
}

// writeResponseForEvent writes a PROPFIND response entry for a single calendar
// object (an .ics resource) inside a calendar collection. This is what Apple
// Calendar expects when it sends PROPFIND Depth:1 on a calendar to enumerate
// all events.
func writeResponseForEvent(w io.Writer, calUID string, obj *db.CalendarObject, props []xmlElement) {
	fmt.Fprintf(w, "  <D:response>\n")
	fmt.Fprintf(w, "    <D:href>/dav/%s/%s.ics</D:href>\n", calUID, obj.UID)

	if len(props) == 0 {
		// No specific props — return the common ones.
		fmt.Fprintf(w, "    <D:propstat>\n")
		fmt.Fprintf(w, "      <D:prop>\n")
		fmt.Fprintf(w, "        <D:getcontenttype>text/calendar; charset=utf-8</D:getcontenttype>\n")
		fmt.Fprintf(w, "        <D:getetag>\"%s\"</D:getetag>\n", xmlEscape(obj.ETag))
		fmt.Fprintf(w, "        <D:resourcetype/>\n")
		fmt.Fprintf(w, "        <D:displayname>%s.ics</D:displayname>\n", xmlEscape(obj.UID))
		fmt.Fprintf(w, "      </D:prop>\n")
		fmt.Fprintf(w, "      <D:status>HTTP/1.1 200 OK</D:status>\n")
		fmt.Fprintf(w, "    </D:propstat>\n")
	} else {
		// Return only requested properties.
		var known, unknown []xmlElement
		for _, p := range props {
			if isKnownEventProp(p.XMLName) {
				known = append(known, p)
			} else {
				unknown = append(unknown, p)
			}
		}
		if len(known) > 0 {
			fmt.Fprintf(w, "    <D:propstat>\n")
			fmt.Fprintf(w, "      <D:prop>\n")
			for _, p := range known {
				writeSingleEventProp(w, p.XMLName, obj)
			}
			fmt.Fprintf(w, "      </D:prop>\n")
			fmt.Fprintf(w, "      <D:status>HTTP/1.1 200 OK</D:status>\n")
			fmt.Fprintf(w, "    </D:propstat>\n")
		}
		if len(unknown) > 0 {
			writePropStatNotFound(w, unknown)
		}
	}

	fmt.Fprintf(w, "  </D:response>\n")
}

// isKnownEventProp returns true if the property is supported for individual
// calendar object resources (events).
func isKnownEventProp(name xml.Name) bool {
	switch name.Space {
	case nsDAV:
		switch name.Local {
		case "getcontenttype", "getetag", "resourcetype", "displayname", "getcontentlength", "getlastmodified", "creationdate":
			return true
		}
	case nsC:
		switch name.Local {
		case "calendar-data":
			return true
		}
	}
	return false
}

// writeSingleEventProp writes a single property for an event resource.
func writeSingleEventProp(w io.Writer, name xml.Name, obj *db.CalendarObject) {
	switch name.Space {
	case nsDAV:
		switch name.Local {
		case "getcontenttype":
			fmt.Fprintf(w, "        <D:getcontenttype>text/calendar; charset=utf-8</D:getcontenttype>\n")
		case "getetag":
			fmt.Fprintf(w, "        <D:getetag>\"%s\"</D:getetag>\n", xmlEscape(obj.ETag))
		case "resourcetype":
			fmt.Fprintf(w, "        <D:resourcetype/>\n")
		case "displayname":
			fmt.Fprintf(w, "        <D:displayname>%s.ics</D:displayname>\n", xmlEscape(obj.UID))
		case "getcontentlength":
			fmt.Fprintf(w, "        <D:getcontentlength>%d</D:getcontentlength>\n", len(obj.ICALData))
		case "getlastmodified":
			fmt.Fprintf(w, "        <D:getlastmodified>%s</D:getlastmodified>\n", obj.UpdatedAt.UTC().Format(http.TimeFormat))
		case "creationdate":
			fmt.Fprintf(w, "        <D:creationdate>%s</D:creationdate>\n", obj.CreatedAt.UTC().Format("20060102T150405Z"))
		}
	case nsC:
		switch name.Local {
		case "calendar-data":
			fmt.Fprintf(w, "        <C:calendar-data><![CDATA[%s]]></C:calendar-data>\n", obj.ICALData)
		}
	}
}

// isKnownCalendarProp returns true if the property is one the calendar service
// supports for calendar collections.
func isKnownCalendarProp(name xml.Name) bool {
	switch name.Space {
	case nsDAV:
		switch name.Local {
		case "resourcetype", "displayname", "owner", "supported-report-set", "current-user-principal", "principal-URL", "sync-token", "getcontenttype", "getetag", "current-user-privilege-set":
			return true
		}
	case nsC:
		switch name.Local {
		case "calendar-color", "supported-calendar-component-set", "calendar-description", "calendar-timezone", "calendar-home-set", "principal-email", "supported-calendar-data":
			return true
		}
	case nsCS:
		switch name.Local {
		case "getctag":
			return true
		}
	}
	return false
}

// writePropStatOK writes a propstat element with all known calendar properties.
func writePropStatOK(w io.Writer, cal *db.Calendar, includeHomeSet bool) {
	fmt.Fprint(w, "    <D:propstat>\n")
	fmt.Fprint(w, "      <D:prop>\n")
	fmt.Fprintf(w, "        <D:resourcetype><D:collection/><C:calendar/></D:resourcetype>\n")
	fmt.Fprintf(w, "        <D:displayname>%s</D:displayname>\n", xmlEscape(cal.Name))
	if cal.Color != nil && *cal.Color != "" {
		fmt.Fprintf(w, "        <C:calendar-color>%s</C:calendar-color>\n", xmlEscape(*cal.Color))
	} else {
		fmt.Fprint(w, "        <C:calendar-color>#6366f1</C:calendar-color>\n")
	}
	fmt.Fprintf(w, "        <CS:getctag>%s</CS:getctag>\n", xmlEscape(cal.CTag))
	fmt.Fprint(w, "        <C:supported-calendar-component-set><C:comp name=\"VEVENT\"/></C:supported-calendar-component-set>\n")
	fmt.Fprint(w, "        <D:owner><D:href>/dav/</D:href></D:owner>\n")
	fmt.Fprint(w, "        <D:supported-report-set>\n")
	fmt.Fprint(w, "          <D:supported-report><D:report><C:calendar-query/></D:report></D:supported-report>\n")
	fmt.Fprint(w, "          <D:supported-report><D:report><C:calendar-multiget/></D:report></D:supported-report>\n")
	fmt.Fprint(w, "          <D:supported-report><D:report><D:sync-collection/></D:report></D:supported-report>\n")
	fmt.Fprint(w, "        </D:supported-report-set>\n")
	fmt.Fprint(w, "        <C:supported-calendar-data><C:calendar-data content-type=\"text/calendar\" version=\"2.0\"/></C:supported-calendar-data>\n")
	fmt.Fprint(w, "        <D:current-user-privilege-set>\n")
	fmt.Fprint(w, "          <D:privilege><D:read/></D:privilege>\n")
	fmt.Fprint(w, "          <D:privilege><D:read-current-user-privilege-set/></D:privilege>\n")
	fmt.Fprint(w, "          <D:privilege><D:write/></D:privilege>\n")
	fmt.Fprint(w, "        </D:current-user-privilege-set>\n")
	if includeHomeSet {
		fmt.Fprint(w, "        <C:calendar-home-set><D:href>/dav/calendars/</D:href></C:calendar-home-set>\n")
	}
	fmt.Fprint(w, "      </D:prop>\n")
	fmt.Fprintf(w, "      <D:status>HTTP/1.1 200 OK</D:status>\n")
	fmt.Fprint(w, "    </D:propstat>\n")
}

// writePropStatForProps writes a propstat with only the requested properties.
func writePropStatForProps(w io.Writer, cal *db.Calendar, props []xmlElement, status string) {
	fmt.Fprint(w, "    <D:propstat>\n")
	fmt.Fprint(w, "      <D:prop>\n")
	for _, p := range props {
		writeSingleProp(w, cal, p.XMLName)
	}
	fmt.Fprint(w, "      </D:prop>\n")
	fmt.Fprintf(w, "      <D:status>%s</D:status>\n", status)
	fmt.Fprint(w, "    </D:propstat>\n")
}

// writeSingleProp writes a single property element with its value.
func writeSingleProp(w io.Writer, cal *db.Calendar, name xml.Name) {
	switch name.Space {
	case nsDAV:
		switch name.Local {
		case "resourcetype":
			fmt.Fprint(w, "        <D:resourcetype><D:collection/><C:calendar/></D:resourcetype>\n")
		case "displayname":
			fmt.Fprintf(w, "        <D:displayname>%s</D:displayname>\n", xmlEscape(cal.Name))
		case "owner":
			fmt.Fprint(w, "        <D:owner><D:href>/dav/</D:href></D:owner>\n")
		case "supported-report-set":
			fmt.Fprint(w, "        <D:supported-report-set>\n")
			fmt.Fprint(w, "          <D:supported-report><D:report><C:calendar-query/></D:report></D:supported-report>\n")
			fmt.Fprint(w, "          <D:supported-report><D:report><C:calendar-multiget/></D:report></D:supported-report>\n")
			fmt.Fprint(w, "          <D:supported-report><D:report><D:sync-collection/></D:report></D:supported-report>\n")
			fmt.Fprint(w, "        </D:supported-report-set>\n")
		case "current-user-principal":
			fmt.Fprint(w, "        <D:current-user-principal><D:href>/dav/</D:href></D:current-user-principal>\n")
		case "principal-URL":
			fmt.Fprint(w, "        <D:principal-URL><D:href>/dav/</D:href></D:principal-URL>\n")
		case "sync-token":
			fmt.Fprint(w, "        <D:sync-token>http://home-os.local/ns/sync/"+xmlEscape(cal.CTag)+"</D:sync-token>\n")
		case "getcontenttype":
			fmt.Fprint(w, "        <D:getcontenttype>httpd/unix-directory</D:getcontenttype>\n")
		case "getetag":
			fmt.Fprintf(w, "        <D:getetag>%s</D:getetag>\n", xmlEscape(cal.CTag))
		case "current-user-privilege-set":
			fmt.Fprint(w, "        <D:current-user-privilege-set>\n")
			fmt.Fprint(w, "          <D:privilege><D:read/></D:privilege>\n")
			fmt.Fprint(w, "          <D:privilege><D:read-current-user-privilege-set/></D:privilege>\n")
			fmt.Fprint(w, "          <D:privilege><D:write/></D:privilege>\n")
			fmt.Fprint(w, "        </D:current-user-privilege-set>\n")
		}
	case nsC:
		switch name.Local {
		case "calendar-color":
			color := "#6366f1"
			if cal.Color != nil && *cal.Color != "" {
				color = *cal.Color
			}
			fmt.Fprintf(w, "        <C:calendar-color>%s</C:calendar-color>\n", xmlEscape(color))
		case "supported-calendar-component-set":
			fmt.Fprint(w, "        <C:supported-calendar-component-set><C:comp name=\"VEVENT\"/></C:supported-calendar-component-set>\n")
		case "supported-calendar-data":
			fmt.Fprint(w, "        <C:supported-calendar-data><C:calendar-data content-type=\"text/calendar\" version=\"2.0\"/></C:supported-calendar-data>\n")
		case "calendar-home-set":
			fmt.Fprint(w, "        <C:calendar-home-set><D:href>/dav/calendars/</D:href></C:calendar-home-set>\n")
		case "calendar-description":
			fmt.Fprintf(w, "        <C:calendar-description>%s</C:calendar-description>\n", xmlEscape(cal.Name))
		case "calendar-timezone":
			// No timezone info stored yet; return empty.
			fmt.Fprint(w, "        <C:calendar-timezone/>\n")
		}
	case nsCS:
		switch name.Local {
		case "getctag":
			fmt.Fprintf(w, "        <CS:getctag>%s</CS:getctag>\n", xmlEscape(cal.CTag))
		}
	}
}

// writePropStatNotFound writes a propstat with 404 for unsupported properties.
func writePropStatNotFound(w io.Writer, props []xmlElement) {
	fmt.Fprint(w, "    <D:propstat>\n")
	fmt.Fprint(w, "      <D:prop>\n")
	for _, p := range props {
		fmt.Fprintf(w, "        <%s:%s xmlns:%s=\"%s\"/>\n",
			prefixForNamespace(p.XMLName.Space), p.XMLName.Local,
			prefixForNamespace(p.XMLName.Space), p.XMLName.Space)
	}
	fmt.Fprint(w, "      </D:prop>\n")
	fmt.Fprint(w, "      <D:status>HTTP/1.1 404 Not Found</D:status>\n")
	fmt.Fprint(w, "    </D:propstat>\n")
}

// writePrincipalResponse writes the response for a PROPFIND on the principal
// URL (/dav/) itself — returns properties of the principal resource.
// The email is used for calendar-user-address-set and email-address-set.
func writePrincipalResponse(w io.Writer, calendars []db.Calendar, props []xmlElement, includeCalendarSet bool, email string) {
	fmt.Fprint(w, "  <D:response>\n")
	fmt.Fprint(w, "    <D:href>/dav/</D:href>\n")

	// If no specific props requested, return all known principal props.
	if len(props) == 0 {
		fmt.Fprint(w, "    <D:propstat>\n")
		fmt.Fprint(w, "      <D:prop>\n")
		fmt.Fprint(w, "        <D:resourcetype><D:principal/></D:resourcetype>\n")
		fmt.Fprint(w, "        <D:current-user-principal><D:href>/dav/</D:href></D:current-user-principal>\n")
		fmt.Fprint(w, "        <D:principal-URL><D:href>/dav/</D:href></D:principal-URL>\n")
		fmt.Fprint(w, "        <C:calendar-home-set><D:href>/dav/calendars/</D:href></C:calendar-home-set>\n")
		fmt.Fprintf(w, "        <D:displayname>%s</D:displayname>\n", xmlEscape(email))
		fmt.Fprintf(w, "        <C:calendar-user-address-set><D:href>mailto:%s</D:href></C:calendar-user-address-set>\n", xmlEscape(email))
		fmt.Fprintf(w, "        <CS:email-address-set><CS:email-address>%s</CS:email-address></CS:email-address-set>\n", xmlEscape(email))
		fmt.Fprint(w, "      </D:prop>\n")
		fmt.Fprint(w, "      <D:status>HTTP/1.1 200 OK</D:status>\n")
		fmt.Fprint(w, "    </D:propstat>\n")
		fmt.Fprint(w, "  </D:response>\n")
	} else {
		// Return only requested properties.
		var known, unknown []xmlElement
		for _, p := range props {
			if isKnownPrincipalProp(p.XMLName) {
				known = append(known, p)
			} else {
				unknown = append(unknown, p)
			}
		}
		if len(known) > 0 {
			fmt.Fprint(w, "    <D:propstat>\n")
			fmt.Fprint(w, "      <D:prop>\n")
			for _, p := range known {
				writeSinglePrincipalProp(w, p.XMLName, email)
			}
			fmt.Fprint(w, "      </D:prop>\n")
			fmt.Fprint(w, "      <D:status>HTTP/1.1 200 OK</D:status>\n")
			fmt.Fprint(w, "    </D:propstat>\n")
		}
		if len(unknown) > 0 {
			writePropStatNotFound(w, unknown)
		}
		fmt.Fprint(w, "  </D:response>\n")
	}

	if includeCalendarSet {
		// Return calendar set (calendars embedded in the response).
		for i := range calendars {
			writeResponseForCalendar(w, &calendars[i], props)
		}
	}
}

// isKnownPrincipalProp returns true if the property is supported for the principal resource.
func isKnownPrincipalProp(name xml.Name) bool {
	switch name.Space {
	case nsDAV:
		switch name.Local {
		case "resourcetype", "current-user-principal", "principal-URL", "displayname",
			"calendar-home-set", "current-user-privilege-set", "supported-report-set",
			"owner", "group", "principal-collection-set":
			return true
		}
	case nsC:
		switch name.Local {
		case "calendar-home-set", "calendar-user-address-set", "schedule-inbox-URL",
			"schedule-outbox-URL", "schedule-default-calendar-URL":
			return true
		}
	case nsCS:
		switch name.Local {
		case "email-address-set", "email-address":
			return true
		}
	}
	return false
}

// writeSinglePrincipalProp writes a single property for the principal resource.
func writeSinglePrincipalProp(w io.Writer, name xml.Name, email string) {
	switch name.Space {
	case nsDAV:
		switch name.Local {
		case "resourcetype":
			fmt.Fprint(w, "        <D:resourcetype><D:principal/></D:resourcetype>\n")
		case "current-user-principal":
			fmt.Fprint(w, "        <D:current-user-principal><D:href>/dav/</D:href></D:current-user-principal>\n")
		case "principal-URL":
			fmt.Fprint(w, "        <D:principal-URL><D:href>/dav/</D:href></D:principal-URL>\n")
		case "displayname":
			fmt.Fprintf(w, "        <D:displayname>%s</D:displayname>\n", xmlEscape(email))
		case "calendar-home-set":
			fmt.Fprint(w, "        <C:calendar-home-set><D:href>/dav/calendars/</D:href></C:calendar-home-set>\n")
		case "current-user-privilege-set":
			fmt.Fprint(w, "        <D:current-user-privilege-set>\n")
			fmt.Fprint(w, "          <D:privilege><D:read/></D:privilege>\n")
			fmt.Fprint(w, "          <D:privilege><D:write/></D:privilege>\n")
			fmt.Fprint(w, "          <D:privilege><D:read-current-user-privilege-set/></D:privilege>\n")
			fmt.Fprint(w, "        </D:current-user-privilege-set>\n")
		case "supported-report-set":
			fmt.Fprint(w, "        <D:supported-report-set>\n")
			fmt.Fprint(w, "          <D:supported-report><D:report><C:calendar-query/></D:report></D:supported-report>\n")
			fmt.Fprint(w, "          <D:supported-report><D:report><C:calendar-multiget/></D:report></D:supported-report>\n")
			fmt.Fprint(w, "          <D:supported-report><D:report><D:sync-collection/></D:report></D:supported-report>\n")
			fmt.Fprint(w, "        </D:supported-report-set>\n")
		case "owner":
			fmt.Fprint(w, "        <D:owner><D:href>/dav/</D:href></D:owner>\n")
		case "group":
			fmt.Fprint(w, "        <D:group/>\n")
		case "principal-collection-set":
			fmt.Fprint(w, "        <D:principal-collection-set><D:href>/dav/</D:href></D:principal-collection-set>\n")
		}
	case nsC:
		switch name.Local {
		case "calendar-home-set":
			fmt.Fprint(w, "        <C:calendar-home-set><D:href>/dav/calendars/</D:href></C:calendar-home-set>\n")
		case "calendar-user-address-set":
			fmt.Fprintf(w, "        <C:calendar-user-address-set><D:href>mailto:%s</D:href></C:calendar-user-address-set>\n", xmlEscape(email))
		case "schedule-inbox-URL":
			fmt.Fprint(w, "        <C:schedule-inbox-URL><D:href>/dav/</D:href></C:schedule-inbox-URL>\n")
		case "schedule-outbox-URL":
			fmt.Fprint(w, "        <C:schedule-outbox-URL><D:href>/dav/</D:href></C:schedule-outbox-URL>\n")
		case "schedule-default-calendar-URL":
			fmt.Fprint(w, "        <C:schedule-default-calendar-URL><D:href>/dav/calendars/</D:href></C:schedule-default-calendar-URL>\n")
		}
	case nsCS:
		switch name.Local {
		case "email-address-set":
			fmt.Fprintf(w, "        <CS:email-address-set><CS:email-address>%s</CS:email-address></CS:email-address-set>\n", xmlEscape(email))
		case "email-address":
			fmt.Fprintf(w, "        <CS:email-address>%s</CS:email-address>\n", xmlEscape(email))
		}
	}
}

// prefixForNamespace returns a suitable XML prefix for a namespace URI.
func prefixForNamespace(ns string) string {
	switch ns {
	case nsDAV:
		return "D"
	case nsC:
		return "C"
	case nsCS:
		return "CS"
	default:
		return "NS"
	}
}

// xmlEscape escapes special XML characters in a string.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// ----- PROPFIND handler -----

// HandlePROPFIND handles CalDAV PROPFIND requests.
func HandlePROPFIND(w http.ResponseWriter, r *http.Request, repo *db.Repo, path string) {
	ctx := r.Context()

	// Parse Depth header (default: 1 for CalDAV).
	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "1"
	}

	// Parse request body to find requested properties.
	var req propfindRequest
	body, err := io.ReadAll(r.Body)
	if err == nil && len(body) > 0 {
		if unmarshalErr := xml.Unmarshal(body, &req); unmarshalErr != nil {
			// Log malformed PROPFIND XML. We deliberately keep the graceful
			// fallback (zero-value req → default props) so quirky clients keep
			// working, but the warning makes interop bugs visible during
			// debugging instead of being silently swallowed.
			logging.Logger.Warn("caldav: propfind parse xml",
				slog.String("error", unmarshalErr.Error()),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path))
		}
	}

	// Log the raw PROPFIND body ONLY at debug level. PROPFIND bodies can
	// contain calendar property values (display names, colors) and, in
	// edge cases, echoed event metadata — gating behind debug prevents
	// accidental PII leakage in production logs.
	if len(body) > 0 {
		logging.Logger.Debug("caldav: PROPFIND body",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("body_bytes", len(body)),
			slog.String("body", string(body)))
	}

	// Get property list from request.
	var requestedProps []xmlElement
	if req.Prop != nil {
		requestedProps = req.Prop.Props
	}

	// Handle based on path.
	switch {

	case path == "/dav/" || path == "":
		// PROPFIND on principal URL.
		householdID := auth.HouseholdIDFromContext(ctx)
		calendars, err := repo.ListCalendars(ctx, householdID)
		if err != nil {
			logging.Logger.Error("caldav: list calendars",
				slog.String("path", path),
				slog.String("household_id", householdID),
				slog.String("error", err.Error()))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("DAV", "1, 2, 3, calendar-access")
		w.WriteHeader(http.StatusMultiStatus)

		buf := &strings.Builder{}
		writeXMLHeader(buf)
		writePrincipalResponse(buf, calendars, requestedProps, depth != "0", auth.EmailFromContext(ctx))
		writeXMLFooter(buf)
		w.Write([]byte(buf.String()))

	case path == "/dav/calendars/" || path == "/dav/calendars":
		// PROPFIND on calendar home-set — return only calendars, no principal.
		householdID := auth.HouseholdIDFromContext(ctx)
		calendars, err := repo.ListCalendars(ctx, householdID)
		if err != nil {
			logging.Logger.Error("caldav: list calendars",
				slog.String("path", path),
				slog.String("household_id", householdID),
				slog.String("error", err.Error()))
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("DAV", "1, 2, 3, calendar-access")
		w.WriteHeader(http.StatusMultiStatus)

		buf := &strings.Builder{}
		writeXMLHeader(buf)
		for i := range calendars {
			writeResponseForCalendar(buf, &calendars[i], requestedProps)
		}
		writeXMLFooter(buf)
		w.Write([]byte(buf.String()))

	case strings.HasPrefix(path, "/dav/") && strings.Count(strings.TrimSuffix(path, "/"), "/") == 2:
		// PROPFIND on a specific calendar collection: /dav/{uid}/
		uid := extractCalendarUID(path)
		if uid == "" {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}

		householdID := auth.HouseholdIDFromContext(ctx)
		cal, err := repo.GetCalendarByCalDAVUID(ctx, householdID, uid)
		if err != nil {
			logging.Logger.Error("caldav: get calendar",
				slog.String("path", path),
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

		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("DAV", "1, 2, 3, access-control, calendar-access")
		w.WriteHeader(http.StatusMultiStatus)

		buf := &strings.Builder{}
		writeXMLHeader(buf)
		// Always include the calendar collection itself.
		writeResponseForCalendar(buf, cal, requestedProps)

		// If Depth: 1, also enumerate child event resources (the actual .ics files).
		// Apple Calendar uses PROPFIND Depth:1 to list all events in a calendar
		// before fetching their bodies via REPORT calendar-multiget.
		if depth != "0" {
			objects, err := repo.ListCalendarObjects(ctx, householdID, cal.ID)
			if err != nil {
				logging.Logger.Error("caldav: list calendar objects",
					slog.String("calendar_uid", uid),
					slog.String("household_id", householdID),
					slog.String("error", err.Error()))
			} else {
				for i := range objects {
					writeResponseForEvent(buf, cal.CalDAVUID, &objects[i], requestedProps)
				}
			}
		}
		writeXMLFooter(buf)
		w.Write([]byte(buf.String()))

	default:
		http.Error(w, "Not Found", http.StatusNotFound)
	}
}

// extractCalendarUID extracts the calendar UID from a path like /dav/{uid}/.
func extractCalendarUID(path string) string {
	// path is like /dav/{uid}/ or /dav/{uid}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// HandleRootPROPFIND handles PROPFIND on the root principal URL.
func HandleRootPROPFIND(w http.ResponseWriter, r *http.Request, repo *db.Repo) {
	HandlePROPFIND(w, r, repo, "/dav/")
}

// ----- Helpers for context keys -----

type contextKey string

const repoKey contextKey = "caldav-repo"

// NewContext embeds a Repo into context for handler access.
func NewContext(ctx context.Context, repo *db.Repo) context.Context {
	return context.WithValue(ctx, repoKey, repo)
}

// RepoFromContext extracts the Repo from context.
func RepoFromContext(ctx context.Context) *db.Repo {
	repo, _ := ctx.Value(repoKey).(*db.Repo)
	return repo
}