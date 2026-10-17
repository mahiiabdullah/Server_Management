// Package httpapi is the REST API: router, middleware and handlers.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// APIError is the single error shape every endpoint returns:
// {"error": {"code": "...", "message": "...", "fields": {...}}}
type APIError struct {
	Status  int               `json:"-"`
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func errValidation(fields map[string]string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "validation_failed", Message: "Some fields are invalid.", Fields: fields}
}

var (
	errUnauthenticated = &APIError{Status: http.StatusUnauthorized, Code: "unauthenticated", Message: "Sign in to continue."}
	errForbidden       = &APIError{Status: http.StatusForbidden, Code: "forbidden", Message: "Your role does not allow this action."}
	errCSRF            = &APIError{Status: http.StatusForbidden, Code: "csrf_failed", Message: "Missing or invalid CSRF token. Reload the page and try again."}
	errNotFound        = &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Not found."}
	errInternal        = &APIError{Status: http.StatusInternalServerError, Code: "internal", Message: "Something went wrong. The error has been logged."}
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes err as JSON. Errors that are not *APIError are logged and
// reported as a generic 500 so internals never reach the client.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		loggerFrom(r.Context()).Error("request failed", "err", err)
		apiErr = errInternal
	}
	writeJSON(w, apiErr.Status, map[string]*APIError{"error": apiErr})
}

const maxBodyBytes = 1 << 20

// decodeJSON reads a JSON body of at most 1 MiB and rejects unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return &APIError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "Send the body as application/json."}
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return &APIError{Status: http.StatusRequestEntityTooLarge, Code: "body_too_large", Message: "Request body is too large."}
		}
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_json", Message: "Request body is not valid JSON: " + err.Error()}
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return &APIError{Status: http.StatusBadRequest, Code: "invalid_json", Message: "Request body must contain a single JSON object."}
	}
	return nil
}

type loggerKey struct{}

func loggerFrom(ctx interface{ Value(any) any }) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
