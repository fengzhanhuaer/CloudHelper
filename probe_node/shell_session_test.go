package main

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestProbeShellSessionReadOutputUsesIncrementalCursor(t *testing.T) {
	session := &probeShellSessionRuntime{}
	session.outputMu.Lock()
	session.appendStreamOutputLocked([]byte("abc"))
	session.outputMu.Unlock()

	output, cursor, truncated, closed, errText := session.readOutput(0)
	if output != "abc" || cursor != 3 || truncated || closed || errText != "" {
		t.Fatalf("first read = output=%q cursor=%d truncated=%t closed=%t err=%q", output, cursor, truncated, closed, errText)
	}
	session.outputMu.Lock()
	session.appendStreamOutputLocked([]byte("def"))
	session.outputMu.Unlock()
	output, cursor, truncated, _, _ = session.readOutput(cursor)
	if output != "def" || cursor != 6 || truncated {
		t.Fatalf("second read = output=%q cursor=%d truncated=%t", output, cursor, truncated)
	}

	large := []byte(strings.Repeat("x", probeShellSessionOutputBufferBytes+32))
	session.outputMu.Lock()
	session.appendStreamOutputLocked(large)
	session.outputMu.Unlock()
	output, _, truncated, _, _ = session.readOutput(0)
	if !truncated || len(output) != probeShellSessionOutputBufferBytes {
		t.Fatalf("truncated read = bytes=%d truncated=%t", len(output), truncated)
	}
}

func TestProbeShellSessionInputStreamsAndPersistsState(t *testing.T) {
	session, err := newProbeShellSessionRuntime("test-node")
	if err != nil {
		t.Skipf("interactive shell unavailable: %v", err)
	}
	t.Cleanup(func() { session.stop("test cleanup") })

	setCommand := "CLOUDHELPER_SESSION_VALUE=state-ok"
	readCommand := "printf '%s\\n' \"$CLOUDHELPER_SESSION_VALUE\""
	if runtime.GOOS == "windows" {
		setCommand = "$CloudHelperSessionValue = 'state-ok'"
		readCommand = "Write-Output $CloudHelperSessionValue"
	}
	if err := session.writeInput(setCommand); err != nil {
		t.Fatal(err)
	}
	if err := session.writeInput(readCommand); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output, _, _, closed, errText := session.readOutput(0)
		if strings.Contains(output, "state-ok") {
			return
		}
		if closed {
			t.Fatalf("shell closed before output: %s", errText)
		}
		time.Sleep(25 * time.Millisecond)
	}
	output, _, _, _, errText := session.readOutput(0)
	t.Fatalf("persistent shell output missing: output=%q err=%q", output, errText)
}

func TestClosedProbeShellSessionRemainsReadableDuringGracePeriod(t *testing.T) {
	session, err := startProbeShellSession("closed-test-node")
	if err != nil {
		t.Skipf("interactive shell unavailable: %v", err)
	}
	t.Cleanup(func() {
		_, _, _ = stopProbeShellSession("closed-test-node", session.sessionID, "test cleanup")
	})
	if err := session.writeInput("exit"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, _, _, closed, _ := session.readOutput(0)
		if closed {
			retained, err := getProbeShellSessionForCommand("closed-test-node", session.sessionID)
			if err != nil || retained != session {
				t.Fatalf("closed session was not retained for final read: session=%p err=%v", retained, err)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("shell did not close after exit")
}
