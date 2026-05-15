package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	probeLinkConfigPollInterval  = 20 * time.Second
	probeLinkConfigFetchTimeout  = 15 * time.Second
	probeLinkConfigAPIPath       = "/api/probe/link/config"
	probeLinkConfigCacheFileName = "probe_link_config.json"
)

type probeLinkConfig struct {
	NodeID        string `json:"node_id"`
	Enabled       bool   `json:"enabled"`
	ServiceType   string `json:"service_type,omitempty"`
	ServiceScheme string `json:"service_scheme"`
	ServiceHost   string `json:"service_host"`
	ServicePort   int    `json:"service_port"`
	ListenAddr    string `json:"listen_addr"`
	UpdatedAt     string `json:"updated_at"`
	SavedAt       string `json:"saved_at,omitempty"`
}

var probeHTTPSServiceState = struct {
	mu          sync.Mutex
	server      *http.Server
	listenAddr  string
	serviceType string
}{}

var (
	probeFetchLinkConfig = fetchProbeLinkConfig
	probeApplyLinkConfig = applyProbeLinkConfig
)

func startProbeServiceRuntimeLoop(handler http.Handler, identity nodeIdentity, controllerBaseURL string) {
	go func() {
		restoreProbeServiceFromLinkConfigCache(handler, identity, controllerBaseURL)
		syncProbeServiceFromLinkConfig(handler, identity, controllerBaseURL)
		ticker := time.NewTicker(probeLinkConfigPollInterval)
		defer ticker.Stop()

		for range ticker.C {
			syncProbeServiceFromLinkConfig(handler, identity, controllerBaseURL)
		}
	}()
}

func syncProbeServiceFromLinkConfig(handler http.Handler, identity nodeIdentity, controllerBaseURL string) {
	if strings.TrimSpace(controllerBaseURL) == "" {
		restoreProbeServiceFromLinkConfigCache(handler, identity, controllerBaseURL)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeLinkConfigFetchTimeout)
	config, err := probeFetchLinkConfig(ctx, controllerBaseURL, identity)
	cancel()
	if err != nil {
		log.Printf("warning: failed to fetch probe link config: %v", err)
		restoreProbeServiceFromLinkConfigCache(handler, identity, controllerBaseURL)
		return
	}
	normalized := normalizeProbeLinkConfig(config)
	if err := persistProbeLinkConfigCache(normalized); err != nil {
		log.Printf("warning: persist probe link config cache failed: %v", err)
	}
	probeApplyLinkConfig(handler, identity, controllerBaseURL, normalized, "controller")
}

func applyProbeLinkConfig(handler http.Handler, identity nodeIdentity, controllerBaseURL string, config probeLinkConfig, source string) {
	normalized := normalizeProbeLinkConfig(config)
	serviceType := resolveProbeServiceType(normalized)

	if !shouldEnableProbeHTTPServiceForScheme(serviceType) {
		stopProbeHTTPSService("service type does not require http service: " + serviceType + " source=" + strings.TrimSpace(source))
		return
	}

	listenAddr := normalizeProbeListenAddr(normalized.ListenAddr)
	if listenAddr == "" {
		stopProbeHTTPSService("link config has empty or invalid listen address, source=" + strings.TrimSpace(source))
		return
	}

	ensureProbeHTTPSService(handler, identity, controllerBaseURL, listenAddr, serviceType)
}

func fetchProbeLinkConfig(ctx context.Context, controllerBaseURL string, identity nodeIdentity) (probeLinkConfig, error) {
	requestURL := strings.TrimRight(strings.TrimSpace(controllerBaseURL), "/") + probeLinkConfigAPIPath
	body, err := probeAuthedGet(ctx, requestURL, identity)
	if err != nil {
		return probeLinkConfig{}, err
	}
	var config probeLinkConfig
	if err := json.Unmarshal(body, &config); err != nil {
		return probeLinkConfig{}, err
	}
	return normalizeProbeLinkConfig(config), nil
}

func restoreProbeServiceFromLinkConfigCache(handler http.Handler, identity nodeIdentity, controllerBaseURL string) {
	config, ok, err := loadProbeLinkConfigCache()
	if err != nil {
		log.Printf("warning: load probe link config cache failed: %v", err)
		return
	}
	if !ok {
		return
	}
	probeApplyLinkConfig(handler, identity, controllerBaseURL, config, "cache")
}

func ensureProbeHTTPSService(handler http.Handler, identity nodeIdentity, controllerBaseURL string, listenAddr string, serviceType string) {
	probeHTTPSServiceState.mu.Lock()
	currentServer := probeHTTPSServiceState.server
	currentAddr := probeHTTPSServiceState.listenAddr
	currentType := probeHTTPSServiceState.serviceType
	probeHTTPSServiceState.mu.Unlock()

	if currentServer != nil && currentAddr == listenAddr && currentType == serviceType {
		return
	}

	if currentServer != nil && (currentAddr != listenAddr || currentType != serviceType) {
		stopProbeHTTPSService("service config changed")
	}

	tlsCert, err := prepareProbeServerCertificate(identity, controllerBaseURL)
	if err != nil {
		log.Printf("warning: failed to prepare tls certificate for probe service: %v", err)
		return
	}

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	probeHTTPSServiceState.mu.Lock()
	if probeHTTPSServiceState.server != nil {
		probeHTTPSServiceState.mu.Unlock()
		return
	}
	probeHTTPSServiceState.server = server
	probeHTTPSServiceState.listenAddr = listenAddr
	probeHTTPSServiceState.serviceType = serviceType
	probeHTTPSServiceState.mu.Unlock()

	go func(s *http.Server, addr string, cert probeServerCertificate) {
		log.Printf("probe service enabled: type=%s listen=https://%s cert_domain=%s cert_expires=%s", serviceType, addr, cert.Domain, cert.NotAfter.Format(time.RFC3339))
		err := s.ListenAndServeTLS(cert.CertPath, cert.KeyPath)
		if err != nil && err != http.ErrServerClosed {
			log.Printf("probe https service exited: listen=%s err=%v", addr, err)
		}
		probeHTTPSServiceState.mu.Lock()
		if probeHTTPSServiceState.server == s {
			probeHTTPSServiceState.server = nil
			probeHTTPSServiceState.listenAddr = ""
			probeHTTPSServiceState.serviceType = ""
		}
		probeHTTPSServiceState.mu.Unlock()
	}(server, listenAddr, tlsCert)
}

func stopProbeHTTPSService(reason string) {
	probeHTTPSServiceState.mu.Lock()
	server := probeHTTPSServiceState.server
	addr := probeHTTPSServiceState.listenAddr
	probeHTTPSServiceState.server = nil
	probeHTTPSServiceState.listenAddr = ""
	probeHTTPSServiceState.serviceType = ""
	probeHTTPSServiceState.mu.Unlock()

	if server == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	_ = server.Shutdown(ctx)
	cancel()
	log.Printf("probe https service disabled: listen=%s reason=%s", addr, strings.TrimSpace(reason))
}

func normalizeProbeListenAddr(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if host, port, err := net.SplitHostPort(value); err == nil {
		host = strings.TrimSpace(strings.Trim(host, "[]"))
		port = strings.TrimSpace(port)
		if host == "" {
			return ""
		}
		portNum, pErr := strconv.Atoi(port)
		if pErr != nil || portNum <= 0 || portNum > 65535 {
			return ""
		}
		return net.JoinHostPort(host, strconv.Itoa(portNum))
	}
	return ""
}

func shouldEnableProbeHTTPServiceForScheme(serviceScheme string) bool {
	switch normalizeProbeServiceScheme(serviceScheme) {
	case "https", "http3", "websocket":
		return true
	default:
		return false
	}
}

func resolveProbeServiceType(config probeLinkConfig) string {
	return normalizeProbeServiceScheme(firstNonEmpty(config.ServiceType, config.ServiceScheme))
}

func normalizeProbeServiceScheme(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "https":
		return "https"
	case "http3", "h3":
		return "http3"
	case "websocket", "ws", "wss":
		return "websocket"
	case "tcp":
		return "tcp"
	case "http":
		return "http"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func normalizeProbeServiceHost(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	return strings.TrimSpace(strings.Trim(value, "[]"))
}

func normalizeProbeServicePort(port int) int {
	if port <= 0 || port > 65535 {
		return 0
	}
	return port
}

func buildProbeListenAddr(host string, port int) string {
	host = normalizeProbeServiceHost(host)
	port = normalizeProbeServicePort(port)
	if host == "" || port == 0 {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func normalizeProbeLinkConfig(config probeLinkConfig) probeLinkConfig {
	config.NodeID = strings.TrimSpace(config.NodeID)
	config.ServiceType = resolveProbeServiceType(config)
	config.ServiceScheme = config.ServiceType
	config.ServiceHost = normalizeProbeServiceHost(config.ServiceHost)
	config.ServicePort = normalizeProbeServicePort(config.ServicePort)
	config.ListenAddr = normalizeProbeListenAddr(config.ListenAddr)
	if config.ListenAddr == "" {
		config.ListenAddr = buildProbeListenAddr(config.ServiceHost, config.ServicePort)
	}
	config.UpdatedAt = strings.TrimSpace(config.UpdatedAt)
	config.SavedAt = strings.TrimSpace(config.SavedAt)
	return config
}

func persistProbeLinkConfigCache(config probeLinkConfig) error {
	cachePath, err := resolveProbeLinkConfigCachePath()
	if err != nil {
		return err
	}
	normalized := normalizeProbeLinkConfig(config)
	normalized.SavedAt = time.Now().UTC().Format(time.RFC3339Nano)
	encoded, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(cachePath, append(encoded, '\n'), 0o644)
}

func loadProbeLinkConfigCache() (probeLinkConfig, bool, error) {
	cachePath, err := resolveProbeLinkConfigCachePath()
	if err != nil {
		return probeLinkConfig{}, false, err
	}
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return probeLinkConfig{}, false, nil
		}
		return probeLinkConfig{}, false, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return probeLinkConfig{}, false, nil
	}
	var config probeLinkConfig
	if err := json.Unmarshal([]byte(trimmed), &config); err != nil {
		return probeLinkConfig{}, false, err
	}
	normalized := normalizeProbeLinkConfig(config)
	if normalized.ListenAddr == "" && !shouldEnableProbeHTTPServiceForScheme(normalized.ServiceType) {
		return normalized, true, nil
	}
	if normalized.ListenAddr == "" {
		return probeLinkConfig{}, false, errors.New("cached probe link config listen address is empty")
	}
	return normalized, true, nil
}

func resolveProbeLinkConfigCachePath() (string, error) {
	dataPath, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataPath, probeLinkConfigCacheFileName), nil
}
