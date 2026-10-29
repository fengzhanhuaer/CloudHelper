package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Separate from controller policy and reports: these choices belong to this probe.
var probeVirtualRouterExitSettingsState = struct {
	sync.Mutex
	path      string
	overrides map[string]string
}{}

func loadProbeVirtualRouterExitOverrides() map[string]string {
	dir, err := resolveDataDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(dir, "probe_virtual_router_exits.json")
	state := &probeVirtualRouterExitSettingsState
	state.Lock()
	defer state.Unlock()
	if state.path != path || state.overrides == nil {
		values := map[string]string{}
		if raw, err := os.ReadFile(path); err == nil {
			// A malformed file cannot partially change routing.
			if json.Unmarshal(raw, &values) != nil || values == nil {
				values = map[string]string{}
			}
		}
		state.path, state.overrides = path, values
	}
	result := make(map[string]string, len(state.overrides))
	for k, v := range state.overrides {
		result[k] = v
	}
	return result
}

func effectiveProbeVirtualRouterRouteRule(rule probeVirtualRouterRouteRule) probeVirtualRouterRouteRule {
	if sanitizeProbeVirtualRouterRouteRuleAction(rule.Action, rule.ExitNodeID) != "probe_exit" {
		return rule
	}
	// Load once; ordinary packet matching only reads one cached choice.
	state := &probeVirtualRouterExitSettingsState
	state.Lock()
	ready := state.overrides != nil
	state.Unlock()
	if !ready {
		loadProbeVirtualRouterExitOverrides()
	}
	state.Lock()
	node := state.overrides[strings.TrimSpace(rule.ID)]
	state.Unlock()
	if node != "" {
		rule.ExitNodeID = node
		rule.BackupExitNodeID = "" // An explicit local choice must not fall back to a controller-selected exit.
	}
	return rule
}

func saveProbeVirtualRouterExitOverrides(values map[string]string) error {
	config := currentProbeVirtualRouterConfig()
	previous := loadProbeVirtualRouterExitOverrides()
	rules := make(map[string]bool, len(config.RouteRules))
	nodes := make(map[string]bool, len(config.ProbeIPs))
	for _, r := range config.RouteRules {
		rules[r.ID] = sanitizeProbeVirtualRouterRouteRuleAction(r.Action, r.ExitNodeID) == "probe_exit"
	}
	for _, n := range config.ProbeIPs {
		nodes[n.NodeID] = true
	}
	clean := make(map[string]string, len(values))
	if len(values) > len(rules) {
		return errors.New("too many local exit choices")
	}
	for id, node := range values {
		if !rules[id] {
			return fmt.Errorf("rule %q is not a proxy rule", id)
		}
		if node == "" {
			continue
		}
		if normalizeProbeRouteNodeID(node) != node || (!nodes[node] && previous[id] != node) {
			return fmt.Errorf("unknown exit node %q", node)
		}
		clean[id] = node
	}
	dir, err := resolveDataDir()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, "probe_virtual_router_exits.json")
	raw, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	state := &probeVirtualRouterExitSettingsState
	state.Lock()
	defer state.Unlock()
	f, err := os.CreateTemp(dir, ".probe-exits-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp, path)
	}
	if err != nil {
		return err
	}
	state.path, state.overrides = path, clean
	// Carrier sessions and established flows are left intact; new decisions use the new exits.
	return nil
}

func probeLocalVirtualRouterExitsPayload() map[string]any {
	config := currentProbeVirtualRouterConfig()
	overrides := loadProbeVirtualRouterExitOverrides()
	rows := make([]map[string]any, 0, len(config.RouteRules))
	for _, r := range config.RouteRules {
		effective := effectiveProbeVirtualRouterRouteRule(r)
		rows = append(rows, map[string]any{"id": r.ID, "name": r.Name, "action": sanitizeProbeVirtualRouterRouteRuleAction(r.Action, r.ExitNodeID), "entries": r.Entries, "default_exit_node_id": r.ExitNodeID, "default_backup_exit_node_id": r.BackupExitNodeID, "local_exit_node_id": overrides[r.ID], "effective_exit_node_id": effective.ExitNodeID})
	}
	// Only public identity/name/IP fields; never topology secrets or authentication tickets.
	return map[string]any{"rules": rows, "nodes": config.ProbeIPs, "overrides": overrides}
}

func probeLocalVirtualRouterExitsHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, probeLocalVirtualRouterExitsPayload())
	case http.MethodPost:
		var req struct {
			Overrides map[string]string `json:"overrides"`
			Reset     bool              `json:"reset"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil || (req.Overrides == nil && !req.Reset) || (req.Reset && len(req.Overrides) > 0) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provide exit overrides or reset only"})
			return
		}
		if err := saveProbeVirtualRouterExitOverrides(req.Overrides); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, probeLocalVirtualRouterExitsPayload())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
