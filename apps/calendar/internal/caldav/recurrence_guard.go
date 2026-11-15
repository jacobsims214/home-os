package caldav

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"home-os/calendar/internal/logging"
	"home-os/icalx"
)

// rejectUnsupportedRecurrence answers a PUT with 403 Forbidden and the
// CALDAV:valid-calendar-data precondition (RFC 4791 §5.3.2.1) when the body
// carries an RRULE icalx refuses to expand (icalx.ErrUnsupportedRule:
// FREQ=SECONDLY, BYSETPOS on HOURLY/MINUTELY, BYHOUR/BYMINUTE the INTERVAL
// never reaches, a rule with no instances). Storing one would make every
// REPORT pay for it (#4656). It reports whether it wrote the response. Bodies
// icalx can't parse are left to the existing handling.
func rejectUnsupportedRecurrence(w http.ResponseWriter, body []byte, eventUID string) bool {
	if !isRecurringICal(string(body)) {
		return false
	}
	obj, err := icalx.Parse(body)
	if err != nil {
		return false
	}
	err = obj.Validate()
	if !errors.Is(err, icalx.ErrUnsupportedRule) {
		return false
	}
	logging.Logger.Warn("caldav: PUT rejected: unsupported recurrence rule",
		slog.String("event_uid", eventUID),
		slog.String("error", err.Error()))
	w.Header().Set("Content-Type", `application/xml; charset="utf-8"`)
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprint(w, `<?xml version="1.0" encoding="utf-8"?>`+"\n"+
		`<D:error xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><C:valid-calendar-data/></D:error>`+"\n")
	return true
}
