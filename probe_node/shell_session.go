package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	probeShellSessionIdleTTL            = 30 * time.Minute
	probeShellSessionOutputBufferBytes  = 512 * 1024
	probeShellSessionOutputPollingDelay = 25 * time.Millisecond
)

type probeShellSessionControlResultPayload struct {
	Type       string `json:"type"`
	RequestID  string `json:"request_id"`
	NodeID     string `json:"node_id"`
	SessionID  string `json:"session_id,omitempty"`
	Action     string `json:"action,omitempty"`
	OK         bool   `json:"ok"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	Error      string `json:"error,omitempty"`
	Message    string `json:"message,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Timestamp  string `json:"timestamp,omitempty"`
}

type probeShellSessionRuntime struct {
	sessionID string
	nodeID    string
	cmd       *exec.Cmd
	stdin     io.WriteCloser

	execMu   sync.Mutex
	outputMu sync.Mutex
	stdout   bytes.Buffer
	stderr   bytes.Buffer
	closed   bool
	doneErr  error
	doneCh   chan struct{}

	lastActive atomic.Int64
	closeOnce  sync.Once
}

var probeShellSessionState = struct {
	mu     sync.RWMutex
	byID   map[string]*probeShellSessionRuntime
	byNode map[string]string
}{
	byID:   make(map[string]*probeShellSessionRuntime),
	byNode: make(map[string]string),
}

var probeShellSessionCleanerOnce sync.Once

func runProbeShellSessionControl(cmd probeControlMessage, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	requestID := strings.TrimSpace(cmd.RequestID)
	if requestID == "" {
		return
	}
	nodeID := strings.TrimSpace(identity.NodeID)
	action := strings.ToLower(strings.TrimSpace(cmd.Action))
	result := probeShellSessionControlResultPayload{
		Type:      "shell_session_result",
		RequestID: requestID,
		NodeID:    nodeID,
		Action:    action,
		OK:        false,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	switch action {
	case "start":
		session, err := startProbeShellSession(nodeID)
		if err != nil {
			result.Error = err.Error()
			sendProbeShellSessionControlResult(stream, encoder, writeMu, result)
			return
		}
		result.OK = true
		result.SessionID = session.sessionID
		result.Message = "shell session started"
		sendProbeShellSessionControlResult(stream, encoder, writeMu, result)
		return
	case "exec":
		session, err := getProbeShellSessionForCommand(nodeID, cmd.SessionID)
		if err != nil {
			result.Error = err.Error()
			sendProbeShellSessionControlResult(stream, encoder, writeMu, result)
			return
		}
		result.SessionID = strings.TrimSpace(session.sessionID)
		startedAt := time.Now()
		result.StartedAt = startedAt.UTC().Format(time.RFC3339)
		stdout, stderr, execErr := session.exec(cmd.Command, cmd.TimeoutSec)
		result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		result.DurationMS = time.Since(startedAt).Milliseconds()
		result.Stdout = truncateProbeShellOutput(stdout, maxProbeShellOutputBytes)
		result.Stderr = truncateProbeShellOutput(stderr, maxProbeShellOutputBytes)
		if execErr != nil {
			result.Error = execErr.Error()
			sendProbeShellSessionControlResult(stream, encoder, writeMu, result)
			return
		}
		result.OK = true
		sendProbeShellSessionControlResult(stream, encoder, writeMu, result)
		return
	case "stop":
		stoppedSessionID, stopped, err := stopProbeShellSession(nodeID, cmd.SessionID, "requested by controller")
		if err != nil {
			result.Error = err.Error()
			sendProbeShellSessionControlResult(stream, encoder, writeMu, result)
			return
		}
		result.OK = true
		result.SessionID = strings.TrimSpace(stoppedSessionID)
		if stopped {
			result.Message = "shell session stopped"
		} else {
			result.Message = "shell session not found"
		}
		sendProbeShellSessionControlResult(stream, encoder, writeMu, result)
		return
	default:
		result.Error = "unsupported action"
		sendProbeShellSessionControlResult(stream, encoder, writeMu, result)
		return
	}
}

func sendProbeShellSessionControlResult(stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, payload probeShellSessionControlResultPayload) {
	if writeErr := writeProbeStreamJSON(stream, encoder, writeMu, payload); writeErr != nil {
		log.Printf("probe shell session result send failed: request_id=%s action=%s err=%v", strings.TrimSpace(payload.RequestID), strings.TrimSpace(payload.Action), writeErr)
	}
}

func startProbeShellSession(nodeID string) (*probeShellSessionRuntime, error) {
	normalizedNodeID := strings.TrimSpace(nodeID)
	if normalizedNodeID == "" {
		return nil, fmt.Errorf("node_id is required")
	}
	runtimeSession, err := newProbeShellSessionRuntime(normalizedNodeID)
	if err != nil {
		return nil, err
	}
	ensureProbeShellSessionCleaner()

	var replaced *probeShellSessionRuntime
	probeShellSessionState.mu.Lock()
	if existingID := strings.TrimSpace(probeShellSessionState.byNode[normalizedNodeID]); existingID != "" {
		if existing := probeShellSessionState.byID[existingID]; existing != nil {
			replaced = existing
		}
		delete(probeShellSessionState.byID, existingID)
		delete(probeShellSessionState.byNode, normalizedNodeID)
	}
	probeShellSessionState.byID[runtimeSession.sessionID] = runtimeSession
	probeShellSessionState.byNode[normalizedNodeID] = runtimeSession.sessionID
	probeShellSessionState.mu.Unlock()

	if replaced != nil {
		replaced.stop("replaced by new shell session")
	}
	go func(session *probeShellSessionRuntime) {
		<-session.doneCh
		unregisterProbeShellSession(session.sessionID, session)
	}(runtimeSession)

	return runtimeSession, nil
}

func stopProbeShellSession(nodeID string, sessionID string, reason string) (string, bool, error) {
	normalizedNodeID := strings.TrimSpace(nodeID)
	normalizedSessionID := strings.TrimSpace(sessionID)

	probeShellSessionState.mu.Lock()
	targetSessionID := normalizedSessionID
	if targetSessionID == "" && normalizedNodeID != "" {
		targetSessionID = strings.TrimSpace(probeShellSessionState.byNode[normalizedNodeID])
	}
	if targetSessionID == "" {
		probeShellSessionState.mu.Unlock()
		return normalizedSessionID, false, nil
	}
	target := probeShellSessionState.byID[targetSessionID]
	if target == nil {
		delete(probeShellSessionState.byID, targetSessionID)
		if normalizedNodeID != "" && probeShellSessionState.byNode[normalizedNodeID] == targetSessionID {
			delete(probeShellSessionState.byNode, normalizedNodeID)
		}
		probeShellSessionState.mu.Unlock()
		return targetSessionID, false, nil
	}
	if normalizedNodeID != "" && !strings.EqualFold(strings.TrimSpace(target.nodeID), normalizedNodeID) {
		probeShellSessionState.mu.Unlock()
		return "", false, fmt.Errorf("session does not belong to current node")
	}
	delete(probeShellSessionState.byID, targetSessionID)
	if nodeKey := strings.TrimSpace(target.nodeID); nodeKey != "" && probeShellSessionState.byNode[nodeKey] == targetSessionID {
		delete(probeShellSessionState.byNode, nodeKey)
	}
	probeShellSessionState.mu.Unlock()

	target.stop(reason)
	return targetSessionID, true, nil
}

func unregisterProbeShellSession(sessionID string, runtimeSession *probeShellSessionRuntime) {
	normalizedSessionID := strings.TrimSpace(sessionID)
	if normalizedSessionID == "" {
		return
	}
	probeShellSessionState.mu.Lock()
	current, ok := probeShellSessionState.byID[normalizedSessionID]
	if ok && (runtimeSession == nil || current == runtimeSession) {
		delete(probeShellSessionState.byID, normalizedSessionID)
		if current != nil {
			if nodeKey := strings.TrimSpace(current.nodeID); nodeKey != "" && probeShellSessionState.byNode[nodeKey] == normalizedSessionID {
				delete(probeShellSessionState.byNode, nodeKey)
			}
		}
	}
	probeShellSessionState.mu.Unlock()
}

func getProbeShellSessionForCommand(nodeID string, sessionID string) (*probeShellSessionRuntime, error) {
	normalizedNodeID := strings.TrimSpace(nodeID)
	normalizedSessionID := strings.TrimSpace(sessionID)

	probeShellSessionState.mu.RLock()
	targetSessionID := normalizedSessionID
	if targetSessionID == "" && normalizedNodeID != "" {
		targetSessionID = strings.TrimSpace(probeShellSessionState.byNode[normalizedNodeID])
	}
	session := probeShellSessionState.byID[targetSessionID]
	probeShellSessionState.mu.RUnlock()

	if targetSessionID == "" || session == nil {
		return nil, fmt.Errorf("shell session not found")
	}
	if normalizedNodeID != "" && !strings.EqualFold(strings.TrimSpace(session.nodeID), normalizedNodeID) {
		return nil, fmt.Errorf("shell session does not belong to current node")
	}
	return session, nil
}

func newProbeShellSessionRuntime(nodeID string) (*probeShellSessionRuntime, error) {
	shellCmd := buildProbeInteractiveShellCommand()
	stdinPipe, err := shellCmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open shell stdin failed: %w", err)
	}
	stdoutPipe, err := shellCmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return nil, fmt.Errorf("open shell stdout failed: %w", err)
	}
	stderrPipe, err := shellCmd.StderrPipe()
	if err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		return nil, fmt.Errorf("open shell stderr failed: %w", err)
	}
	if err := shellCmd.Start(); err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		_ = stderrPipe.Close()
		return nil, fmt.Errorf("start shell process failed: %w", err)
	}

	session := &probeShellSessionRuntime{
		sessionID: fmt.Sprintf("shell-%s-%d", randomHexToken(8), time.Now().UnixNano()),
		nodeID:    strings.TrimSpace(nodeID),
		cmd:       shellCmd,
		stdin:     stdinPipe,
		doneCh:    make(chan struct{}),
	}
	session.touch()

	go session.captureOutput(stdoutPipe, true)
	go session.captureOutput(stderrPipe, false)
	go func() {
		waitErr := shellCmd.Wait()
		session.outputMu.Lock()
		session.closed = true
		session.doneErr = waitErr
		session.outputMu.Unlock()
		close(session.doneCh)
	}()

	return session, nil
}

func buildProbeInteractiveShellCommand() *exec.Cmd {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("powershell", "-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "-")
		hideWindowSysProcAttr(cmd)
		return cmd
	}
	return exec.Command("sh")
}

func (session *probeShellSessionRuntime) exec(commandText string, timeoutSec int) (string, string, error) {
	if strings.TrimSpace(commandText) == "" {
		return "", "", fmt.Errorf("command is required")
	}
	safeTimeout := normalizeProbeShellTimeoutSec(timeoutSec)

	session.execMu.Lock()
	defer session.execMu.Unlock()

	if err := session.ensureAlive(); err != nil {
		return "", "", err
	}

	session.resetOutput()
	marker := newProbeShellSessionMarker()
	payload := buildProbeShellSessionExecPayload(commandText, marker)
	if _, err := io.WriteString(session.stdin, payload); err != nil {
		session.stop("shell stdin write failed")
		return "", "", fmt.Errorf("write shell command failed: %w", err)
	}
	session.touch()

	deadline := time.Now().Add(time.Duration(safeTimeout) * time.Second)
	for {
		stdout, stderr, closed, doneErr := session.snapshotOutput()
		if markerPos := strings.Index(stdout, marker); markerPos >= 0 {
			session.touch()
			return stdout[:markerPos], stderr, nil
		}
		if closed {
			if doneErr != nil {
				return stdout, stderr, fmt.Errorf("shell session closed: %v", doneErr)
			}
			return stdout, stderr, fmt.Errorf("shell session closed")
		}
		if time.Now().After(deadline) {
			session.stop("shell command timeout")
			return stdout, stderr, fmt.Errorf("shell command timeout after %ds; session closed", safeTimeout)
		}
		time.Sleep(probeShellSessionOutputPollingDelay)
	}
}

func (session *probeShellSessionRuntime) ensureAlive() error {
	_, _, closed, doneErr := session.snapshotOutput()
	if !closed {
		return nil
	}
	if doneErr != nil {
		return fmt.Errorf("shell session closed: %v", doneErr)
	}
	return fmt.Errorf("shell session closed")
}

func (session *probeShellSessionRuntime) resetOutput() {
	session.outputMu.Lock()
	session.stdout.Reset()
	session.stderr.Reset()
	session.outputMu.Unlock()
}

func (session *probeShellSessionRuntime) snapshotOutput() (stdout string, stderr string, closed bool, doneErr error) {
	session.outputMu.Lock()
	stdout = session.stdout.String()
	stderr = session.stderr.String()
	closed = session.closed
	doneErr = session.doneErr
	session.outputMu.Unlock()
	return
}

func (session *probeShellSessionRuntime) captureOutput(reader io.Reader, isStdout bool) {
	buf := make([]byte, 4096)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			session.outputMu.Lock()
			if isStdout {
				appendOutputBufferWithLimit(&session.stdout, buf[:n], probeShellSessionOutputBufferBytes)
			} else {
				appendOutputBufferWithLimit(&session.stderr, buf[:n], probeShellSessionOutputBufferBytes)
			}
			session.outputMu.Unlock()
		}
		if readErr != nil {
			return
		}
	}
}

func appendOutputBufferWithLimit(buffer *bytes.Buffer, chunk []byte, maxBytes int) {
	if buffer == nil || maxBytes <= 0 || len(chunk) == 0 {
		return
	}
	if len(chunk) >= maxBytes {
		buffer.Reset()
		_, _ = buffer.Write(chunk[len(chunk)-maxBytes:])
		return
	}
	if buffer.Len()+len(chunk) <= maxBytes {
		_, _ = buffer.Write(chunk)
		return
	}

	needDrop := buffer.Len() + len(chunk) - maxBytes
	current := buffer.Bytes()
	if needDrop >= len(current) {
		buffer.Reset()
		_, _ = buffer.Write(chunk)
		return
	}
	remain := append([]byte(nil), current[needDrop:]...)
	buffer.Reset()
	_, _ = buffer.Write(remain)
	_, _ = buffer.Write(chunk)
}

func (session *probeShellSessionRuntime) stop(reason string) {
	session.closeOnce.Do(func() {
		if strings.TrimSpace(reason) != "" {
			log.Printf("probe shell session stop: session_id=%s node_id=%s reason=%s", strings.TrimSpace(session.sessionID), strings.TrimSpace(session.nodeID), strings.TrimSpace(reason))
		}
		if session.stdin != nil {
			_ = session.stdin.Close()
		}
		if session.cmd != nil && session.cmd.Process != nil {
			_ = session.cmd.Process.Kill()
		}
	})
}

func (session *probeShellSessionRuntime) touch() {
	session.lastActive.Store(time.Now().UnixNano())
}

func (session *probeShellSessionRuntime) idleDuration(now time.Time) time.Duration {
	last := session.lastActive.Load()
	if last <= 0 {
		return 0
	}
	lastTime := time.Unix(0, last)
	if now.Before(lastTime) {
		return 0
	}
	return now.Sub(lastTime)
}

func newProbeShellSessionMarker() string {
	return "__CLOUDHELPER_SHELL_DONE_" + randomHexToken(8) + "_" + fmt.Sprintf("%d", time.Now().UnixNano()) + "__"
}

func buildProbeShellSessionExecPayload(commandText string, marker string) string {
	var builder strings.Builder
	builder.WriteString(commandText)
	if !strings.HasSuffix(commandText, "\n") {
		builder.WriteString("\n")
	}
	if runtime.GOOS == "windows" {
		builder.WriteString("Write-Output ")
		builder.WriteString(quotePowerShellSingle(marker))
		builder.WriteString("\n")
		return builder.String()
	}
	builder.WriteString("printf '%s\\n' ")
	builder.WriteString(quotePosixSingle(marker))
	builder.WriteString("\n")
	return builder.String()
}

func quotePowerShellSingle(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func quotePosixSingle(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func ensureProbeShellSessionCleaner() {
	probeShellSessionCleanerOnce.Do(func() {
		go probeShellSessionCleanupLoop()
	})
}

func probeShellSessionCleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for now := range ticker.C {
		probeShellSessionState.mu.RLock()
		sessions := make([]*probeShellSessionRuntime, 0, len(probeShellSessionState.byID))
		for _, session := range probeShellSessionState.byID {
			if session != nil {
				sessions = append(sessions, session)
			}
		}
		probeShellSessionState.mu.RUnlock()

		for _, session := range sessions {
			if session.idleDuration(now) <= probeShellSessionIdleTTL {
				continue
			}
			_, _, _ = stopProbeShellSession(session.nodeID, session.sessionID, "idle timeout")
		}
	}
}
