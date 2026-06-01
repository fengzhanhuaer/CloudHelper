package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"runtime"
	"sync"
	"time"
)

var BuildVersion = "dev"

type probeRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Updated  string `json:"updated"`
	Platform string `json:"platform"`
	Version  string `json:"version"`
}

type serverState struct {
	mu     sync.RWMutex
	probes []probeRecord
}

func main() {
	listen := flag.String("listen", "127.0.0.1:15030", "dashboard listen address")
	flag.Parse()

	state := &serverState{probes: []probeRecord{{
		ID:       "local",
		Name:     "Local Status Probe",
		Status:   "observed",
		Updated:  time.Now().UTC().Format(time.RFC3339),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Version:  BuildVersion,
	}}}

	mux := http.NewServeMux()
	mux.HandleFunc("/", state.handleDashboard)
	mux.HandleFunc("/dashboard", state.handleDashboard)
	mux.HandleFunc("/dashboard/status", state.handleStatus)
	mux.HandleFunc("/dashboard/probes", state.handleProbes)

	fmt.Printf("probe status dashboard listening on http://%s/dashboard\n", *listen)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		panic(err)
	}
}

func (s *serverState) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"ok":         true,
		"version":    BuildVersion,
		"time":       time.Now().UTC().Format(time.RFC3339),
		"go_version": runtime.Version(),
	})
}

func (s *serverState) handleProbes(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, map[string]any{"ok": true, "probes": s.probes})
}

func (s *serverState) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dashboardTemplate.Execute(w, map[string]any{
		"Version": BuildVersion,
		"Probes":  s.probes,
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

var dashboardTemplate = template.Must(template.New("dashboard").Parse(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>CloudHelper Status</title>
  <style>
    body{margin:0;font-family:Arial,sans-serif;background:#f7f7f5;color:#1f2937}
    main{max-width:920px;margin:0 auto;padding:32px 18px}
    h1{font-size:24px;margin:0 0 6px}
    .sub{color:#6b7280;margin-bottom:22px}
    table{width:100%;border-collapse:collapse;background:#fff;border:1px solid #e5e7eb}
    th,td{text-align:left;padding:12px;border-bottom:1px solid #e5e7eb;font-size:14px}
    th{background:#f3f4f6}
    .ok{color:#047857;font-weight:700}
  </style>
</head>
<body>
<main>
  <h1>Probe Status</h1>
  <div class="sub">Version {{.Version}}</div>
  <table>
    <thead><tr><th>ID</th><th>Name</th><th>Status</th><th>Platform</th><th>Updated</th></tr></thead>
    <tbody>{{range .Probes}}<tr><td>{{.ID}}</td><td>{{.Name}}</td><td class="ok">{{.Status}}</td><td>{{.Platform}}</td><td>{{.Updated}}</td></tr>{{end}}</tbody>
  </table>
</main>
</body>
</html>`))
