package response

import (
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

func Unauthorized(c *gin.Context, requestID string) {
	c.JSON(http.StatusUnauthorized, Envelope{Code: CodeUnauthorized, Message: "unauthorized", RequestID: requestID})
}

func BadRequest(c *gin.Context, requestID, message string) {
	c.JSON(http.StatusBadRequest, Envelope{Code: CodeBadRequest, Message: message, RequestID: requestID})
}

func Internal(c *gin.Context, requestID, message string) {
	c.JSON(http.StatusInternalServerError, Envelope{Code: CodeInternal, Message: message, RequestID: requestID})
}
