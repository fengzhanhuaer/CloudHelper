package main

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"sync"
)

//go:embed local_pages/gateway.html
var probeGatewayPageHTML string

// Coalesce saves while an upload is in flight. The reporter reads the latest
// persisted configuration; neither the HTTP request nor an offline peer waits.
var probeGatewayReport = struct {
	sync.Mutex
	running bool
	pending bool
}{}

func scheduleProbeGatewayReport() {
	probeGatewayReport.Lock()
	probeGatewayReport.pending = true
	if probeGatewayReport.running {
		probeGatewayReport.Unlock()
		return
	}
	probeGatewayReport.running = true
	probeGatewayReport.Unlock()
	go func() {
		for {
			probeGatewayReport.Lock()
			probeGatewayReport.pending = false
			probeGatewayReport.Unlock()
			if _, err := triggerProbeImmediateReport(); err != nil {
				logProbeWarnf("gateway configuration report failed; periodic/reconnect report will retry: %v", err)
			}
			probeGatewayReport.Lock()
			if !probeGatewayReport.pending {
				probeGatewayReport.running = false
				probeGatewayReport.Unlock()
				return
			}
			probeGatewayReport.Unlock()
		}
	}()
}

func probeGatewayPageHandler(w http.ResponseWriter, r *http.Request) {
	serveProbeLocalHTMLPage(w, r, "/local/virtual-router/gateway", probeGatewayPageHTML)
}
func probeGatewayConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if r.Method == http.MethodPost {
		defer r.Body.Close()
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
		d.DisallowUnknownFields()
		var c probeGatewayProxyConfig
		err := d.Decode(&c)
		if err == nil {
			var extra any
			if d.Decode(&extra) != io.EOF {
				err = io.ErrUnexpectedEOF
			}
		}
		if err == nil {
			err = saveProbeGatewayConfig(c)
		}
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		scheduleProbeGatewayReport()
	}
	c := currentProbeGatewayConfig()
	if c == nil {
		if err := loadProbeGatewayConfig(); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		c = currentProbeGatewayConfig()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"ok": true, "config": c, "nodes": currentProbeVirtualRouterConfig().ProbeIPs, "node_id": currentProbeVirtualRouterLocalNodeID()})
}
