package httpapi

import (
	"encoding/json/v2"
	"net/http"
	"strings"
)

// Error codes of the JSON error body (PRD §3.1).
const (
	codeInvalidJSON          = "invalid_json"
	codeValidationFailed     = "validation_failed"
	codeNotFound             = "not_found"
	codeMethodNotAllowed     = "method_not_allowed"
	codePayloadTooLarge      = "payload_too_large"
	codeUnsupportedMediaType = "unsupported_media_type"
	codeBacklogFull          = "backlog_full"
	codeRateLimited          = "rate_limited"
	codeTooManyStreams       = "too_many_streams"
	codeStreamsFull          = "streams_full"
	codeNotReady             = "not_ready"
	codeInternal             = "internal"
)

// errorBody is the one error shape: {"error": {"code": ..., "message": ...}}.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError writes the JSON error body. The message must never echo request
// input: it is fixed text or built from validation problems, which carry only
// field names and limits.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

// writeNotFound writes the single 404 used for an unknown route, an unknown
// or malformed ticket id, and a missing, malformed or wrong token alike.
func writeNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, codeNotFound, "not found")
}

func writeInternalError(w http.ResponseWriter) {
	writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
}

// methodNotAllowed answers a known path called with another method.
func methodNotAllowed(allowed ...string) http.Handler {
	allow := strings.Join(allowed, ", ")
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "method not allowed")
	})
}

// writeJSON writes v as the JSON response body. Every response may carry
// a ticket or its token, so none is cached.
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		// Only the fixed response types are marshaled here, so this is a bug.
		status = http.StatusInternalServerError
		b = []byte(`{"error":{"code":"internal","message":"internal error"}}`)
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	// A failed write means the client is gone; the access log has the status.
	_, _ = w.Write(append(b, '\n'))
}
