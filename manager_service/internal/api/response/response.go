package response

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
)

type Envelope struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

const (
	CodeOK           = 0
	CodeUnauthorized = 401
	CodeForbidden    = 403
	CodeBadRequest   = 400
	CodeInternal     = 500
)

func OK(c *gin.Context, requestID string, data any) {
	c.JSON(http.StatusOK, Envelope{Code: CodeOK, Message: "ok", Data: data, RequestID: requestID})
}

// OKRaw wraps a pre-encoded JSON value (json.RawMessage from a WS-RPC response)
// inside the standard envelope. This avoids double-encoding the payload.
func OKRaw(c *gin.Context, requestID string, raw json.RawMessage) {
	c.JSON(http.StatusOK, &envelopeRaw{Code: CodeOK, Message: "ok", Data: raw, RequestID: requestID})
}

// envelopeRaw embeds a json.RawMessage so the data field is not double-encoded.
type envelopeRaw struct {
	Code      int             `json:"code"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
}

func Unauthorized(c *gin.Context, requestID string) {
	c.JSON(http.StatusUnauthorized, Envelope{Code: CodeUnauthorized, Message: "unauthorized", RequestID: requestID})
}

func BadRequest(c *gin.Context, requestID, message string) {
	c.JSON(http.StatusBadRequest, Envelope{Code: CodeBadRequest, Message: message, RequestID: requestID})
}

func Internal(c *gin.Context, requestID, message string) {
	c.JSON(http.StatusInternalServerError, Envelope{Code: CodeInternal, Message: message, RequestID: requestID})
}

