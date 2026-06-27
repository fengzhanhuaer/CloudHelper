package core

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const (
	mngTGSessionWSHeartbeatInterval = 30 * time.Second
)

var mngTGSessionWSUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type mngTGSessionWSPayload struct {
	Type      string                      `json:"type"`
	AccountID string                      `json:"account_id,omitempty"`
	Target    string                      `json:"target,omitempty"`
	Messages  []tgAssistantSessionMessage `json:"messages,omitempty"`
	Error     string                      `json:"error,omitempty"`
	UpdatedAt string                      `json:"updated_at,omitempty"`
}

func mngTGSessionWSHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	if accountID == "" || target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "account_id and target are required"})
		return
	}
	if _, _, err := currentMngSessionFromRequest(r); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired mng session"})
		return
	}

	conn, err := mngTGSessionWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	send := func(payload mngTGSessionWSPayload) error {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteJSON(payload)
	}
	sendError := func(format string, args ...any) {
		_ = send(mngTGSessionWSPayload{
			Type:      "session.error",
			AccountID: accountID,
			Target:    target,
			Error:     fmt.Sprintf(format, args...),
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}

	req := tgAssistantSessionMessagesRequest{
		AccountID: accountID,
		Target:    target,
		Limit:     50,
	}
	messages, err := listTGAssistantSessionMessages(req)
	if err != nil {
		sendError("%v", err)
		return
	}
	lastFingerprint := sessionMessagesFingerprint(messages)
	ensureTGAssistantSessionPushRunner(accountID)
	pushEvents, unsubscribe := subscribeTGAssistantSessionPush(accountID, target)
	defer unsubscribe()
	if err := send(mngTGSessionWSPayload{
		Type:      "session.snapshot",
		AccountID: accountID,
		Target:    target,
		Messages:  messages,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return
	}

	heartbeat := time.NewTicker(mngTGSessionWSHeartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-done:
			return
		case event := <-pushEvents:
			fingerprint := sessionMessagesFingerprint(event.Messages)
			if fingerprint == lastFingerprint {
				continue
			}
			lastFingerprint = fingerprint
			if err := send(mngTGSessionWSPayload{
				Type:      "session.snapshot",
				AccountID: accountID,
				Target:    target,
				Messages:  event.Messages,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			}); err != nil {
				return
			}
		case <-heartbeat.C:
			if err := send(mngTGSessionWSPayload{
				Type:      "session.heartbeat",
				AccountID: accountID,
				Target:    target,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			}); err != nil {
				return
			}
		}
	}
}

func sessionMessagesFingerprint(messages []tgAssistantSessionMessage) string {
	var b strings.Builder
	for idx, msg := range messages {
		if idx > 0 {
			b.WriteByte('|')
		}
		fmt.Fprintf(&b, "%d:%s:%t:%t:%s:%s:%s", msg.ID, msg.Date, msg.Out, msg.Service, msg.Text, msg.MediaType, msg.MediaPath)
	}
	return b.String()
}
