package caldav

import (
	"log/slog"
	"net/http"
	"strings"

	"home-os/calendar/internal/auth"
	"home-os/calendar/internal/db"
	"home-os/calendar/internal/logging"
)

// Handler is the main CalDAV HTTP handler. It dispatches requests to the
// appropriate method handler based on the HTTP method and request path.
type Handler struct {
	repo *db.Repo
}

// NewHandler creates a new CalDAV handler with the given database repository.
func NewHandler(repo *db.Repo) *Handler {
	return &Handler{repo: repo}
}

// ServeHTTP implements http.Handler. It routes CalDAV requests by method.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Clean the path by stripping /dav/ prefix and re-adding it cleanly
	path := strings.TrimPrefix(r.URL.Path, "/dav")
	path = "/dav/" + strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/")

	logging.Logger.Info("caldav request",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("resolved_path", path),
		slog.String("user_id", auth.UserIDFromContext(r.Context())),
		slog.String("household_id", auth.HouseholdIDFromContext(r.Context())),
	)

	// If path is just /dav/ with no calendar UID, this is a PROPFIND on root
	// or a request that needs root handling.

	switch r.Method {
	case http.MethodOptions:
		HandleOPTIONS(w, r)

	case "PROPFIND":
		HandlePROPFIND(w, r, h.repo, path)

	case "PROPPATCH":
		// PROPPATCH persists calendar property changes (displayname,
		// calendar-color). Implemented in proppatch.go.
		HandlePROPPATCH(w, r, h.repo, path)

	case "MKCALENDAR":
		HandleMKCALENDAR(w, r, h.repo)

	case "REPORT":
		HandleREPORT(w, r, h.repo, path)

	case http.MethodGet:
		HandleGET(w, r, h.repo, path)

	case http.MethodPut:
		HandlePUT(w, r, h.repo, path)

	case http.MethodDelete:
		HandleDELETE(w, r, h.repo, path)

	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// HandleMKCALENDAR handles MKCALENDAR requests by proxying to the main API.
func HandleMKCALENDAR(w http.ResponseWriter, r *http.Request, repo *db.Repo) {
	// MKCALENDAR creates a new calendar. For now, it requires the request to
	// be proxied to the main API. Since this is a complex interaction, we
	// return 501 Not Implemented until the full flow is designed.
	http.Error(w, "Not Implemented", http.StatusNotImplemented)
}

// HandleREPORT handles CalDAV REPORT requests (calendar-query, calendar-multiget).
// Implemented in report.go.
func HandleREPORT(w http.ResponseWriter, r *http.Request, repo *db.Repo, path string) {
	reportHandler(w, r, repo, path)
}

// RedirectToWellKnown handles the GET /.well-known/caldav redirect.
func RedirectToWellKnown(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/dav/", http.StatusMovedPermanently)
}