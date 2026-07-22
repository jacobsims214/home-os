package caldav

import (
	"net/http"
)

// HandleOPTIONS handles CalDAV OPTIONS requests.
// It advertises the DAV capabilities and allowed HTTP methods.
func HandleOPTIONS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("DAV", "1, 2, 3, access-control, calendar-access")
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, REPORT, MKCALENDAR")
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}