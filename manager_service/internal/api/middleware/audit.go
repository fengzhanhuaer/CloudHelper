package middleware

import (
	"fmt"
	"strings"
	"time"

	"github.com/cloudhelper/manager_service/internal/auth"
	"github.com/cloudhelper/manager_service/internal/logging"
	"github.com/gin-gonic/gin"
)

func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := fmt.Sprintf("req-%d", time.Now().UnixNano())
		c.Set("RequestID", rid)
		c.Header("X-Request-ID", rid)
		c.Next()
	}
}

func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		latency := time.Since(start)
		rid := c.GetString("RequestID")
		status := c.Writer.Status()
		logging.Infof("[API] rid=%s method=%s path=%s status=%d latency=%v clientIP=%s",
			rid, c.Request.Method, c.Request.URL.Path, status, latency, c.ClientIP())
	}
}

func Auth(svc *auth.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetString("RequestID")
		authHeader := c.GetHeader("Authorization")
		token := c.GetHeader("X-Session-Token")
		if token == "" && strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimPrefix(authHeader, "Bearer ")
		}
		if token == "" {
			c.JSON(401, gin.H{"code": 401, "message": "unauthorized", "request_id": rid})
			c.Abort()
			return
		}
		err := svc.ValidateToken(token)
		if err != nil {
			c.JSON(401, gin.H{"code": 401, "message": "invalid session", "request_id": rid})
			c.Abort()
			return
		}
		c.Next()
	}
}

func LocalhostOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetString("RequestID")
		ip := strings.Split(c.Request.RemoteAddr, ":")[0]
		if ip != "127.0.0.1" && ip != "::1" && ip != "localhost" {
			c.JSON(403, gin.H{"code": 403, "message": "forbidden: localhost only", "request_id": rid})
			c.Abort()
			return
		}
		c.Next()
	}
}
