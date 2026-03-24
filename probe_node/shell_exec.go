package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	defaultProbeShellTimeoutSec = 30
	maxProbeShellTimeoutSec     = 300
	maxProbeShellOutputBytes    = 256 * 1024
)

type probeShellExecResultPayload struct {
	Type       string `json:"type"`
	RequestID  string `json:"request_id"`
	NodeID     string `json:"node_id"`
	OK         bool   `json:"ok"`
	Command    string `json:"command,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Timestamp  string `json:"timestamp,omitempty"`
}

func runProbeShellExec(cmd probeControlMessage, identity nodeIdentity, stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex) {
	requestID := strings.TrimSpace(cmd.RequestID)
	if requestID == "" {
		return
	}

	commandText := strings.TrimSpace(cmd.Command)
	result := probeShellExecResultPayload{
		Type:      "shell_exec_result",
		RequestID: requestID,
		NodeID:    strings.TrimSpace(identity.NodeID),
		Command:   commandText,
		OK:        false,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	if commandText == "" {
		result.Error = "command is required"
		sendProbeShellExecResult(stream, encoder, writeMu, result)
		return
	}

	timeoutSec := normalizeProbeShellTimeoutSec(cmd.TimeoutSec)
	startedAt := time.Now()
	result.StartedAt = startedAt.UTC().Format(time.RFC3339)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	command := buildProbeShellCommand(ctx, commandText)
	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	command.Stdout = &stdoutBuf
	command.Stderr = &stderrBuf

	runErr := command.Run()
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	result.DurationMS = time.Since(startedAt).Milliseconds()
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	result.Stdout = truncateProbeShellOutput(stdoutBuf.String(), maxProbeShellOutputBytes)
	result.Stderr = truncateProbeShellOutput(stderrBuf.String(), maxProbeShellOutputBytes)

	if runErr == nil {
		result.OK = true
		sendProbeShellExecResult(stream, encoder, writeMu, result)
		return
	}

	if ctx.Err() == context.DeadlineExceeded {
		result.Error = fmt.Sprintf("command timeout after %ds", timeoutSec)
	} else {
		result.Error = runErr.Error()
	}
	sendProbeShellExecResult(stream, encoder, writeMu, result)
}

func sendProbeShellExecResult(stream net.Conn, encoder *json.Encoder, writeMu *sync.Mutex, payload probeShellExecResultPayload) {
	if writeErr := writeProbeStreamJSON(stream, encoder, writeMu, payload); writeErr != nil {
		log.Printf("probe shell exec result send failed: request_id=%s err=%v", strings.TrimSpace(payload.RequestID), writeErr)
	}
}

func buildProbeShellCommand(ctx context.Context, commandText string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "powershell", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", commandText)
	}
	return exec.CommandContext(ctx, "sh", "-lc", commandText)
}

func normalizeProbeShellTimeoutSec(raw int) int {
	if raw <= 0 {
		return defaultProbeShellTimeoutSec
	}
	if raw > maxProbeShellTimeoutSec {
		return maxProbeShellTimeoutSec
	}
	return raw
}

func truncateProbeShellOutput(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	raw := []byte(text)
	if len(raw) <= maxBytes {
		return text
	}
	suffix := []byte("\n...[output truncated]")
	if len(suffix) >= maxBytes {
		return string(raw[:maxBytes])
	}
	head := raw[:maxBytes-len(suffix)]
	return string(head) + string(suffix)
}
