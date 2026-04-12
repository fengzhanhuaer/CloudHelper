// Package response defines the unified API response envelope.
// All handlers MUST use these helpers for consistent output.
// Envelope: { "code": int, "message": string, "data": any, "request_id": string }
package response

import (
	"encoding/json"
	"net/http"
)

// Envelope is the standard API response wrapper.
type Envelope struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// Error codes.
const (
	CodeOK           = 0
	CodeUnauthorized = 401
	CodeForbidden    = 403
	CodeBadRequest   = 400
	CodeInternal     = 500
)

func write(w http.ResponseWriter, statusHTTP int, env Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusHTTP)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(env)
}

// OK sends a successful response.
func OK(w http.ResponseWriter, requestID string, data any) {
	write(w, http.StatusOK, Envelope{
		Code:      CodeOK,
		Message:   "ok",
		Data:      data,
		RequestID: requestID,
	})
}

// Unauthorized sends a 401 response.
func Unauthorized(w http.ResponseWriter, requestID string) {
	write(w, http.StatusUnauthorized, Envelope{
		Code:      CodeUnauthorized,
		Message:   "unauthorized",
		RequestID: requestID,
	})
}

// BadRequest sends a 400 response.
func BadRequest(w http.ResponseWriter, requestID, message string) {
	write(w, http.StatusBadRequest, Envelope{
		Code:      CodeBadRequest,
		Message:   message,
		RequestID: requestID,
	})
}

// Internal sends a 500 response.
func Internal(w http.ResponseWriter, requestID, message string) {
	write(w, http.StatusInternalServerError, Envelope{
		Code:      CodeInternal,
		Message:   message,
		RequestID: requestID,
	})
}
