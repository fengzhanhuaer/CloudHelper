package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMngControllerLogsHandlerReturnsRuntimeMemoryLogs(t *testing.T) {
	resetControllerRuntimeLogsForTest()
	t.Cleanup(resetControllerRuntimeLogsForTest)

	_, _ = controllerRuntimeLogs.Write([]byte(time.Now().Format(logLineTimeLayout) + " [warning] runtime memory warning\n"))
	_, _ = controllerRuntimeLogs.Write([]byte(time.Now().Format(logLineTimeLayout) + " [normal] runtime memory normal\n"))

	req := httptest.NewRequest(http.MethodGet, "/mng/api/controller/logs?lines=20&min_level=warning", nil)
	rr := httptest.NewRecorder()

	mngControllerLogsHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var payload adminLogsResponse
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Source != "controller_runtime_memory" {
		t.Fatalf("expected memory log source, got %q", payload.Source)
	}
	if strings.TrimSpace(payload.FilePath) != "" {
		t.Fatalf("expected no file path for runtime memory logs, got %q", payload.FilePath)
	}
	if !strings.Contains(payload.Content, "runtime memory warning") {
		t.Fatalf("expected warning log in content, got %q", payload.Content)
	}
	if strings.Contains(payload.Content, "runtime memory normal") {
		t.Fatalf("expected min_level=warning to filter normal logs, got %q", payload.Content)
	}
}
