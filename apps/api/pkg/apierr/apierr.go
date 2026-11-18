// Package apierr provides standardized API error types and HTTP response helpers.
package apierr

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
)

// Sentinel errors that domain code can return and handlers can check with errors.Is.
var (
	ErrNotFound   = errors.New("not found")
	ErrForbidden  = errors.New("forbidden")
	ErrConflict   = errors.New("conflict")
	ErrBadRequest = errors.New("bad request")
)

// ErrorResponse is the JSON body written for all error responses.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail holds the machine-readable code and human-readable message.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NotFound writes a 404 JSON error response.
func NotFound(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusNotFound, "NOT_FOUND", msg)
}

// Forbidden writes a 403 JSON error response.
func Forbidden(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusForbidden, "FORBIDDEN", msg)
}

// Unauthorized writes a 401 JSON error response.
func Unauthorized(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", msg)
}

// BadRequest writes a 400 JSON error response.
func BadRequest(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusBadRequest, "BAD_REQUEST", msg)
}

// Conflict writes a 409 JSON error response.
func Conflict(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusConflict, "CONFLICT", msg)
}

// InternalError logs the error and writes a 500 JSON error response.
// The internal error message is not exposed to the client.
func InternalError(w http.ResponseWriter, err error) {
	slog.Error("internal server error", "error", err)
	writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
}

// DB writes the response for an error returned by a database write. The
// domain v2 ownership triggers (migration 038) raise foreign_key_violation
// when a request points at a row that is missing or belongs to another
// family; that is reported as 404 so a caller cannot probe other families.
// A check_violation is a 400. Anything else is a 500.
func DB(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			NotFound(w, "referenced record not found")
			return
		case "23514":
			if pgErr.DataTypeName == "entity_kind" {
				BadRequest(w, "invalid entity_type")
				return
			}
			BadRequest(w, "invalid combination of fields")
			return
		}
	}
	InternalError(w, err)
}

// JSON writes a JSON response with the given status code and body.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("failed to encode JSON response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: msg,
		},
	})
}
