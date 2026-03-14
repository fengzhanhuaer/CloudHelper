package core

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

var statusWSUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Desktop client may connect via custom host/proxy origin.
		return true
	},
}

func AdminStatusWSHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{
			"error": "https is required",
		})
		return
	}

	token, err := extractSessionTokenForWebSocket(r)
	if err != nil || !IsTokenValid(token) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "invalid or expired session token",
		})
		return
	}

	conn, err := statusWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	send := func() error {
		payload := statusPayload()
		payload["type"] = "status"
		payload["server_time"] = time.Now().UTC().Format(time.RFC3339)
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteJSON(payload)
	}

	if err := send(); err != nil {
		return
	}

	for range ticker.C {
		if err := send(); err != nil {
			return
		}
	}
}

func extractSessionTokenForWebSocket(r *http.Request) (string, error) {
	if t := strings.TrimSpace(r.URL.Query().Get("token")); t != "" {
		return t, nil
	}
	if t, err := extractBearerToken(r); err == nil {
		return t, nil
	}
	return "", errors.New("missing websocket session token")
}
