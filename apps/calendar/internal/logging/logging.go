// Package logging provides a single shared *slog.Logger for the calendar
// service. It is initialized once at startup from the LOG_LEVEL env var
// (see config.Config.LogLevel) and used by every internal package via
// logging.Logger.
//
// We use a package-level var (not context propagation) because:
//   - Every calendar request is already routed through middleware that
//     injects household_id/user_id into context. Re-propagating a logger
//     through context alongside those would force every handler to extract
//     both before logging — pure overhead for no gain at this service's
//     request volume.
//   - slog's structured fields let us attach request-scoped values
//     (method, path, user_id, household_id) per-call without needing a
//     per-request logger.
//
// The default logger writes JSON to stdout at Info level so logs are
// structured and grep-able in production (Kubernetes/Docker) and readable
// during local development.
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// Logger is the package-level logger used throughout the calendar service.
// It is safe to call before Init (a no-op default logger discards output);
// packages should call logging.Init at startup to configure it.
var Logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

// Init configures the package-level Logger from a level string. Supported
// values (case-insensitive): debug, info, warn, error. An empty or unknown
// value defaults to info. The handler emits JSON to stdout — this matches
// what the Kubernetes/Docker log collectors expect.
//
// Call this exactly once from main() after loading config. Subsequent calls
// reconfigure the logger (useful in tests).
func Init(level string) {
	var lvl slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = slog.LevelDebug
	case "info", "":
		lvl = slog.LevelInfo
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		// Unknown level — fall back to info but surface the misconfiguration
		// so an operator notices their typo instead of silently losing logs.
		lvl = slog.LevelInfo
		Logger.Warn("logging: unknown LOG_LEVEL, defaulting to info",
			slog.String("log_level", level))
	}
	Logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(Logger)
}
