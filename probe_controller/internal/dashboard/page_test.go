package dashboard

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardPageHandlerETagAndGzip(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	rr := httptest.NewRecorder()
	Handler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("unexpected content-type: %q", ct)
	}
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("expected non-empty ETag")
	}
	body := rr.Body.String()
	for _, want := range []string{
		"CloudHelper 探针看板",
		"/dashboard/status",
		"/dashboard/probes",
		"/dashboard/network",
		"networkHealthStatus",
		"drawLossBars",
		"computeTaskLatencyBaseline",
		"scale=hour24",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("dashboard HTML missing %q", want)
		}
	}

	// Conditional request with matching ETag returns 304.
	req304 := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req304.Header.Set("If-None-Match", etag)
	rr304 := httptest.NewRecorder()
	Handler(rr304, req304)
	if rr304.Code != http.StatusNotModified {
		t.Fatalf("expected 304 on matching ETag, got %d", rr304.Code)
	}
	if rr304.Body.Len() != 0 {
		t.Fatalf("expected empty body on 304, got %d bytes", rr304.Body.Len())
	}

	// Gzipped response decodes to the exact same HTML.
	reqGZ := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	reqGZ.Header.Set("Accept-Encoding", "gzip, deflate")
	rrGZ := httptest.NewRecorder()
	Handler(rrGZ, reqGZ)
	if rrGZ.Code != http.StatusOK {
		t.Fatalf("expected 200 on gzip request, got %d", rrGZ.Code)
	}
	if enc := rrGZ.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("expected Content-Encoding gzip, got %q", enc)
	}
	gr, err := gzip.NewReader(rrGZ.Body)
	if err != nil {
		t.Fatalf("gzip reader failed: %v", err)
	}
	defer gr.Close()
	decoded, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("gzip read failed: %v", err)
	}
	if string(decoded) != body {
		t.Fatalf("gzipped body mismatch: got %d bytes, want %d bytes", len(decoded), len(body))
	}

	// Non-GET/HEAD method rejected.
	reqPost := httptest.NewRequest(http.MethodPost, "/dashboard", nil)
	rrPost := httptest.NewRecorder()
	Handler(rrPost, reqPost)
	if rrPost.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for POST, got %d", rrPost.Code)
	}
}
