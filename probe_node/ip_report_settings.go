package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const probeIPReportSettingsFileName = "probe_ip_report_settings.json"

type probeIPReportSettings struct {
	Version                   int      `json:"version"`
	OnlySelectedLANInterfaces bool     `json:"only_selected_lan_interfaces"`
	SelectedInterfaceIDs      []string `json:"selected_interface_ids,omitempty"`
	UpdatedAt                 string   `json:"updated_at,omitempty"`
}

type probeIPReportInterfaceView struct {
	ID           string   `json:"id"`
	Index        int      `json:"index"`
	Name         string   `json:"name,omitempty"`
	HardwareAddr string   `json:"hardware_addr,omitempty"`
	Flags        string   `json:"flags,omitempty"`
	IPs          []string `json:"ips,omitempty"`
	LANIPs       []string `json:"lan_ips,omitempty"`
	Selected     bool     `json:"selected"`
}

type probeIPReportSettingsRequest struct {
	OnlySelectedLANInterfaces bool     `json:"only_selected_lan_interfaces"`
	SelectedInterfaceIDs      []string `json:"selected_interface_ids"`
}

func resolveProbeIPReportSettingsPath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeIPReportSettingsFileName), nil
}

func defaultProbeIPReportSettings() probeIPReportSettings {
	return probeIPReportSettings{
		Version:   1,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func normalizeProbeIPReportSettings(settings probeIPReportSettings) probeIPReportSettings {
	if settings.Version <= 0 {
		settings.Version = 1
	}
	settings.SelectedInterfaceIDs = normalizeProbeIPReportInterfaceIDs(settings.SelectedInterfaceIDs)
	if strings.TrimSpace(settings.UpdatedAt) == "" {
		settings.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return settings
}

func normalizeProbeIPReportInterfaceIDs(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		id := normalizeProbeIPReportInterfaceID(item)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func normalizeProbeIPReportInterfaceID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

func loadProbeIPReportSettings() (probeIPReportSettings, error) {
	path, err := resolveProbeIPReportSettingsPath()
	if err != nil {
		return probeIPReportSettings{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultProbeIPReportSettings(), nil
		}
		return probeIPReportSettings{}, err
	}
	settings := probeIPReportSettings{}
	if err := decodeProbeLocalJSONStrict(raw, &settings); err != nil {
		return probeIPReportSettings{}, err
	}
	return normalizeProbeIPReportSettings(settings), nil
}

func loadProbeIPReportSettingsBestEffort() probeIPReportSettings {
	settings, err := loadProbeIPReportSettings()
	if err != nil {
		logProbeWarnf("probe ip report settings load failed: %v", err)
		settings = defaultProbeIPReportSettings()
	}
	return effectiveProbeIPReportSettings(settings)
}

func effectiveProbeIPReportSettings(settings probeIPReportSettings) probeIPReportSettings {
	settings = normalizeProbeIPReportSettings(settings)
	settings.OnlySelectedLANInterfaces = true
	if currentProbeBuildKind() != probeBuildKindLinuxRouter {
		return settings
	}

	// The router's selected uplink is authoritative for LAN address reporting.
	// Public addresses are collected separately and are not affected by this filter.
	interfaceName := strings.TrimSpace(probeProductLinuxRouterReport().Interface)
	if interfaceName != "" {
		settings.SelectedInterfaceIDs = []string{"name:" + interfaceName}
	}
	return normalizeProbeIPReportSettings(settings)
}

func persistProbeIPReportSettings(settings probeIPReportSettings) (probeIPReportSettings, error) {
	settings = normalizeProbeIPReportSettings(settings)
	settings.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	path, err := resolveProbeIPReportSettingsPath()
	if err != nil {
		return probeIPReportSettings{}, err
	}
	if err := persistProbeLocalJSONFile(path, settings); err != nil {
		return probeIPReportSettings{}, err
	}
	return settings, nil
}

func selectedProbeIPReportInterfaceIDSet(settings probeIPReportSettings) map[string]struct{} {
	items := normalizeProbeIPReportInterfaceIDs(settings.SelectedInterfaceIDs)
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		out[item] = struct{}{}
	}
	return out
}

func probeReportIPIsLAN(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19) {
		return true
	}
	return ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func shouldIncludeProbeReportInterfaceIP(ip net.IP, interfaceID string, settings probeIPReportSettings) bool {
	return shouldIncludeProbeReportInterfaceIPByIDs(ip, []string{interfaceID}, settings)
}

func shouldIncludeProbeReportInterfaceIPByIDs(ip net.IP, interfaceIDs []string, settings probeIPReportSettings) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return false
	}
	if !settings.OnlySelectedLANInterfaces || !probeReportIPIsLAN(ip) {
		return true
	}
	selected := selectedProbeIPReportInterfaceIDSet(settings)
	for _, interfaceID := range interfaceIDs {
		if _, ok := selected[normalizeProbeIPReportInterfaceID(interfaceID)]; ok {
			return true
		}
	}
	return false
}

func listProbeIPReportInterfaces(settings probeIPReportSettings) []probeIPReportInterfaceView {
	ifaces, err := net.Interfaces()
	if err != nil {
		return []probeIPReportInterfaceView{}
	}
	selected := selectedProbeIPReportInterfaceIDSet(settings)
	items := make([]probeIPReportInterfaceView, 0, len(ifaces))
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		identity := resolveProbeIPReportInterfaceIdentity(iface)
		if identity.ID == "" {
			continue
		}
		view := probeIPReportInterfaceView{
			ID:           identity.ID,
			Index:        iface.Index,
			Name:         strings.TrimSpace(iface.Name),
			HardwareAddr: strings.TrimSpace(iface.HardwareAddr.String()),
			Flags:        strings.TrimSpace(iface.Flags.String()),
		}
		for _, id := range probeIPReportInterfaceIdentityIDs(identity) {
			if _, ok := selected[id]; ok {
				view.Selected = true
				break
			}
		}
		addrs, err := iface.Addrs()
		if err == nil {
			for _, addr := range addrs {
				ip := probeReportIPFromAddr(addr)
				if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
					continue
				}
				value := ip.String()
				view.IPs = append(view.IPs, value)
				if probeReportIPIsLAN(ip) {
					view.LANIPs = append(view.LANIPs, value)
				}
			}
		}
		sortProbeIPReportStringsIPv4First(view.IPs)
		sortProbeIPReportStringsIPv4First(view.LANIPs)
		items = append(items, view)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Selected != items[j].Selected {
			return items[i].Selected
		}
		leftHasLAN := len(items[i].LANIPs) > 0
		rightHasLAN := len(items[j].LANIPs) > 0
		if leftHasLAN != rightHasLAN {
			return leftHasLAN
		}
		leftName := strings.ToLower(items[i].Name)
		rightName := strings.ToLower(items[j].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return items[i].Index < items[j].Index
	})
	return items
}

func sortProbeIPReportStringsIPv4First(items []string) {
	sort.SliceStable(items, func(i, j int) bool {
		left := net.ParseIP(strings.TrimSpace(items[i]))
		right := net.ParseIP(strings.TrimSpace(items[j]))
		leftIs4 := left != nil && left.To4() != nil
		rightIs4 := right != nil && right.To4() != nil
		if leftIs4 != rightIs4 {
			return leftIs4
		}
		return strings.TrimSpace(items[i]) < strings.TrimSpace(items[j])
	})
}

func probeReportIPFromAddr(addr net.Addr) net.IP {
	switch v := addr.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	default:
		return nil
	}
}

func probeIPReportSettingsPayload(settings probeIPReportSettings) map[string]any {
	settings = effectiveProbeIPReportSettings(settings)
	return map[string]any{
		"ok":         true,
		"settings":   normalizeProbeIPReportSettings(settings),
		"interfaces": listProbeIPReportInterfaces(settings),
	}
}

func probeLocalSystemIPReportSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		settings, err := loadProbeIPReportSettings()
		if err != nil {
			writeProbeLocalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, probeIPReportSettingsPayload(settings))
	case http.MethodPost:
		body := http.MaxBytesReader(w, r.Body, probeLocalRouteReadBodyMaxLen)
		defer body.Close()
		decoder := json.NewDecoder(body)
		decoder.DisallowUnknownFields()
		var req probeIPReportSettingsRequest
		if err := decoder.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		settings, err := persistProbeIPReportSettings(probeIPReportSettings{
			Version:                   1,
			OnlySelectedLANInterfaces: req.OnlySelectedLANInterfaces,
			SelectedInterfaceIDs:      req.SelectedInterfaceIDs,
		})
		if err != nil {
			writeProbeLocalError(w, err)
			return
		}
		if triggered, err := triggerProbeImmediateReport(); err != nil {
			logProbeWarnf("probe ip report immediate upload failed: %v", err)
		} else if triggered {
			logProbeInfof("probe ip report immediate upload triggered after settings save")
		}
		writeJSON(w, http.StatusOK, probeIPReportSettingsPayload(settings))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
