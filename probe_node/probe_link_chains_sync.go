package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// probeLinkChainsSyncAPIPath is the controller endpoint that returns all chains
// where this probe node appears (entry / cascade / exit).
const (
	probeLinkChainsSyncAPIPath             = "/api/probe/link/chains"
	probeLinkChainsSyncPollInterval        = 60 * time.Minute
	probeLinkChainsSyncFetchTimeout        = 15 * time.Second
	probeChainTopologyCacheFileName        = "probe_link_chain_config.json"
	probeProxyChainsCacheFileName          = "proxy_chain.json"
	probeAdminWSPath                       = "/api/admin/ws"
	probeControllerSessionTokenEnv         = "PROBE_CONTROLLER_SESSION_TOKEN"
	probeControllerSessionTokenEnvAlt      = "CLOUDHELPER_CONTROLLER_SESSION_TOKEN"
	probeControllerAuthNonceAPIPath        = "/api/auth/nonce"
	probeControllerAuthLoginAPIPath        = "/api/auth/login"
	probeControllerAdminPrivateKeyFileName = "admin_private_key.pem"
	probeControllerAdminPrivateKeyPathEnv  = "CLOUDHELPER_ADMIN_PRIVATE_KEY_PATH"
)

// probeLinkChainsResponse mirrors the JSON returned by ProbeLinkChainsHandler.
type probeLinkChainsResponse struct {
	Chains []probeLinkChainServerItem `json:"chains"`
}

// probeChainTopologyCacheFile stores full chain topology fetched from controller.
type probeChainTopologyCacheFile struct {
	UpdatedAt string                     `json:"updated_at"`
	Items     []probeLinkChainServerItem `json:"items"`
}

// probeLinkChainServerItem is a single chain record returned by the controller.
// Fields map 1-to-1 with probeLinkChainRecord / probeChainRuntimeCacheItem.
type probeLinkChainServerItem struct {
	ChainID        string                            `json:"chain_id"`
	ChainType      string                            `json:"chain_type"`
	Name           string                            `json:"name"`
	UserID         string                            `json:"user_id"`
	UserPublicKey  string                            `json:"user_public_key"`
	Secret         string                            `json:"secret"`
	EntryNodeID    string                            `json:"entry_node_id"`
	ExitNodeID     string                            `json:"exit_node_id"`
	CascadeNodeIDs []string                          `json:"cascade_node_ids"`
	LinkLayer      string                            `json:"link_layer"`
	HopConfigs     []probeLinkChainHopServerItem     `json:"hop_configs"`
	PortForwards   []probeChainPortForwardServerItem `json:"port_forwards"`
	EgressHost     string                            `json:"egress_host"`
	EgressPort     int                               `json:"egress_port"`
}

// probeLinkChainHopServerItem maps one entry in hop_configs.
// relay_host is the selected domain for this hop node.
type probeLinkChainHopServerItem struct {
	NodeNo       int    `json:"node_no"`
	ListenHost   string `json:"listen_host"`
	ListenPort   int    `json:"listen_port"`
	ExternalPort int    `json:"external_port"`
	LinkLayer    string `json:"link_layer"`
	DialMode     string `json:"dial_mode"`
	RelayHost    string `json:"relay_host"`
}

type probeChainPortForwardServerItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	EntrySide  string `json:"entry_side"`
	ListenHost string `json:"listen_host"`
	ListenPort int    `json:"listen_port"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Network    string `json:"network"`
	Enabled    bool   `json:"enabled"`
}

// startProbeLinkChainsSyncLoop pulls chain configs from the controller and
// reconciles running runtimes. Falls back to the existing cache if controller
// is unconfigured or unreachable.
func startProbeLinkChainsSyncLoop(identity nodeIdentity, controllerBaseURL string) {
	go func() {
		// If controller is not configured, there is nothing to pull.
		// Cache restore (restoreProbeChainRuntimesFromTopologyCache) already handles
		// the offline case, so we simply skip polling.
		base := strings.TrimSpace(controllerBaseURL)
		if base == "" {
			log.Printf("probe chain sync disabled: controller base url not configured")
			return
		}

		// Initial sync immediately on startup.
		syncProbeChainRuntimes(identity, base)

		ticker := time.NewTicker(probeLinkChainsSyncPollInterval)
		defer ticker.Stop()
		for range ticker.C {
			syncProbeChainRuntimes(identity, base)
		}
	}()
}

// syncProbeChainRuntimes fetches the latest chains from the controller and
// diffing them against currently running runtimes:
//   - New / changed chains → apply (start / restart).
//   - Chains that were removed from the server → stop.
//
// On fetch failure the running runtimes are left untouched (best-effort).
func syncProbeChainRuntimes(identity nodeIdentity, controllerBaseURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), probeLinkChainsSyncFetchTimeout)
	items, err := fetchProbeLinkChains(ctx, controllerBaseURL, identity)
	cancel()
	if err != nil {
		log.Printf("warning: probe chain sync fetch failed: %v (using local topology cache when available)", err)
		restoreProbeChainRuntimesFromTopologyCache(identity, controllerBaseURL)
		return
	}

	if err := persistProbeChainTopologyCache(items); err != nil {
		log.Printf("warning: persist probe chain topology cache failed: %v", err)
	}
	if err := persistProbeProxyChainCache(items); err != nil {
		log.Printf("warning: persist probe proxy chain cache failed: %v", err)
	}

	applyProbeLinkChainServerItems(identity, controllerBaseURL, items)
}

func restoreProbeChainRuntimesFromTopologyCache(identity nodeIdentity, controllerBaseURL string) {
	items, err := loadProbeChainTopologyCacheItems()
	if err != nil {
		log.Printf("warning: load probe chain topology cache failed: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}
	for _, item := range items {
		applyProbeLinkChainServerItem(identity, controllerBaseURL, item)
	}
	log.Printf("restored probe chain runtimes from topology cache: count=%d", len(items))
}

func loadProbeChainTopologyCacheItems() ([]probeLinkChainServerItem, error) {
	cachePath, err := resolveProbeChainTopologyCachePath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []probeLinkChainServerItem{}, nil
		}
		return nil, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return []probeLinkChainServerItem{}, nil
	}
	var payload probeChainTopologyCacheFile
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, err
	}
	return sanitizeProbeChainServerItemsForCache(payload.Items), nil
}

// fetchProbeLinkChains uses the dedicated manager-aligned admin ws action only.
func fetchProbeLinkChains(ctx context.Context, controllerBaseURL string, identity nodeIdentity) ([]probeLinkChainServerItem, error) {
	_ = identity
	token := resolveProbeControllerSessionToken()
	if token == "" {
		var err error
		token, err = loginProbeControllerSessionToken(ctx, controllerBaseURL)
		if err != nil {
			return nil, err
		}
	}
	return fetchProbeLinkChainsViaAdminWS(ctx, controllerBaseURL, token)
}

func refreshProbeProxyChainCacheFromController(ctx context.Context, identity nodeIdentity, controllerBaseURL string) ([]probeLinkChainServerItem, error) {
	base := strings.TrimSpace(controllerBaseURL)
	if base == "" {
		return nil, errors.New("controller base url is empty")
	}
	items, err := fetchProbeLinkChains(ctx, base, identity)
	if err != nil {
		return nil, err
	}
	if err := persistProbeProxyChainCache(items); err != nil {
		return nil, err
	}
	return loadProbeLocalProxyChainItems()
}

func resolveProbeControllerSessionToken() string {
	return firstNonEmpty(
		strings.TrimSpace(os.Getenv(probeControllerSessionTokenEnv)),
		strings.TrimSpace(os.Getenv(probeControllerSessionTokenEnvAlt)),
	)
}

func loginProbeControllerSessionToken(ctx context.Context, controllerBaseURL string) (string, error) {
	nonce, err := requestProbeControllerAuthNonce(ctx, controllerBaseURL)
	if err != nil {
		return "", err
	}
	signature, err := signProbeControllerNonceWithLocalKey(nonce)
	if err != nil {
		return "", err
	}
	return requestProbeControllerSessionToken(ctx, controllerBaseURL, nonce, signature)
}

func requestProbeControllerAuthNonce(ctx context.Context, controllerBaseURL string) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(controllerBaseURL), "/")
	if baseURL == "" {
		return "", errors.New("controller base url is required")
	}
	nonceURL := baseURL + probeControllerAuthNonceAPIPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nonceURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("request nonce failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Nonce string `json:"nonce"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&payload); err != nil {
		return "", err
	}
	nonce := strings.TrimSpace(payload.Nonce)
	if nonce == "" {
		return "", errors.New("nonce is empty")
	}
	return nonce, nil
}

func requestProbeControllerSessionToken(ctx context.Context, controllerBaseURL string, nonce string, signature string) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(controllerBaseURL), "/")
	if baseURL == "" {
		return "", errors.New("controller base url is required")
	}
	loginURL := baseURL + probeControllerAuthLoginAPIPath
	body, err := json.Marshal(map[string]string{
		"nonce":     strings.TrimSpace(nonce),
		"signature": strings.TrimSpace(signature),
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("login failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var payload struct {
		SessionToken string `json:"session_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&payload); err != nil {
		return "", err
	}
	token := strings.TrimSpace(payload.SessionToken)
	if token == "" {
		return "", errors.New("session token is empty")
	}
	return token, nil
}

func signProbeControllerNonceWithLocalKey(nonce string) (string, error) {
	nonce = strings.TrimSpace(nonce)
	if nonce == "" {
		return "", errors.New("nonce is required")
	}
	keyPath, err := resolveProbeControllerAdminPrivateKeyPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("failed to read private key %s: %w", keyPath, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", fmt.Errorf("failed to decode pem private key: %s", keyPath)
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse private key: %w", err)
	}
	priv, ok := keyAny.(ed25519.PrivateKey)
	if !ok {
		return "", errors.New("private key is not ed25519")
	}
	signature := ed25519.Sign(priv, []byte(nonce))
	return base64.StdEncoding.EncodeToString(signature), nil
}

func resolveProbeControllerAdminPrivateKeyPath() (string, error) {
	candidates := make([]string, 0, 3)
	if envPath := strings.TrimSpace(os.Getenv(probeControllerAdminPrivateKeyPathEnv)); envPath != "" {
		candidates = append(candidates, envPath)
	}
	if dataDir, err := resolveDataDir(); err == nil && strings.TrimSpace(dataDir) != "" {
		candidates = append(candidates, filepath.Join(dataDir, probeControllerAdminPrivateKeyFileName))
	}
	candidates = append(candidates, filepath.Join(".", "data", probeControllerAdminPrivateKeyFileName))

	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		value := strings.TrimSpace(candidate)
		if value == "" {
			continue
		}
		if abs, err := filepath.Abs(value); err == nil {
			value = abs
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		if info, err := os.Stat(value); err == nil && info != nil && !info.IsDir() {
			return value, nil
		}
	}

	return "", fmt.Errorf("local admin private key not found: expected %s or set %s", filepath.Join("data", probeControllerAdminPrivateKeyFileName), probeControllerAdminPrivateKeyPathEnv)
}

type probeAdminWSRequest struct {
	ID      string      `json:"id"`
	Action  string      `json:"action"`
	Payload interface{} `json:"payload,omitempty"`
}

type probeAdminWSResponse struct {
	ID    string          `json:"id"`
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error"`
	Type  string          `json:"type"`
}

type probeAdminWSChainsPayload struct {
	Items []probeLinkChainServerItem `json:"items"`
}

func fetchProbeLinkChainsViaAdminWS(ctx context.Context, controllerBaseURL string, sessionToken string) ([]probeLinkChainServerItem, error) {
	wsURL, err := buildProbeAdminWSURL(controllerBaseURL)
	if err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	headers := http.Header{}
	headers.Set("X-Forwarded-Proto", "https")
	conn, resp, err := dialer.Dial(wsURL, headers)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			return nil, fmt.Errorf("admin ws handshake failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, err
	}
	defer conn.Close()

	deadline := time.Now().Add(probeLinkChainsSyncFetchTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return nil, err
	}

	authID := fmt.Sprintf("pn-chain-auth-%d", time.Now().UnixNano())
	authReq := probeAdminWSRequest{ID: authID, Action: "auth.session", Payload: map[string]string{"token": strings.TrimSpace(sessionToken)}}
	if err := conn.WriteJSON(authReq); err != nil {
		return nil, err
	}
	authResp, err := readProbeAdminWSResponseByID(conn, authID)
	if err != nil {
		return nil, err
	}
	if !authResp.OK {
		return nil, fmt.Errorf("admin ws auth failed: %s", strings.TrimSpace(authResp.Error))
	}

	chainsID := fmt.Sprintf("pn-chain-items-%d", time.Now().UnixNano())
	if err := conn.WriteJSON(probeAdminWSRequest{ID: chainsID, Action: "admin.probe.link.chains.get"}); err != nil {
		return nil, err
	}
	chainsResp, err := readProbeAdminWSResponseByID(conn, chainsID)
	if err != nil {
		return nil, err
	}
	if !chainsResp.OK {
		return nil, fmt.Errorf("fetch chain list failed: %s", strings.TrimSpace(chainsResp.Error))
	}

	payload := probeAdminWSChainsPayload{}
	if len(chainsResp.Data) > 0 {
		if err := json.Unmarshal(chainsResp.Data, &payload); err != nil {
			return nil, err
		}
	}
	return sanitizeProbeChainServerItemsForCache(payload.Items), nil
}

func readProbeAdminWSResponseByID(conn *websocket.Conn, requestID string) (probeAdminWSResponse, error) {
	for {
		var resp probeAdminWSResponse
		if err := conn.ReadJSON(&resp); err != nil {
			return probeAdminWSResponse{}, err
		}
		if strings.TrimSpace(resp.Type) != "" {
			continue
		}
		if strings.TrimSpace(resp.ID) != strings.TrimSpace(requestID) {
			continue
		}
		return resp, nil
	}
}

func buildProbeAdminWSURL(controllerBaseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(controllerBaseURL))
	if err != nil || parsed == nil || strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("invalid controller url")
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	switch scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	case "wss", "ws":
		// keep
	default:
		return "", fmt.Errorf("unsupported controller url scheme")
	}
	parsed.Path = probeAdminWSPath
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimSpace(parsed.String()), nil
}

// applyProbeLinkChainServerItems diffs server items against running runtimes.
func applyProbeLinkChainServerItems(identity nodeIdentity, controllerBaseURL string, items []probeLinkChainServerItem) {
	// Build a set of chain IDs from the server response.
	serverChainIDs := make(map[string]struct{}, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ChainID)
		if id != "" {
			serverChainIDs[id] = struct{}{}
		}
	}

	// Stop runtimes that are no longer present on the server.
	probeChainRuntimeState.mu.Lock()
	var toStop []string
	for id := range probeChainRuntimeState.runtimes {
		if _, ok := serverChainIDs[id]; !ok {
			toStop = append(toStop, id)
		}
	}
	probeChainRuntimeState.mu.Unlock()

	for _, id := range toStop {
		stopProbeChainRuntime(id, "chain removed from server")
	}

	// Apply / update chains from the server.
	for _, item := range items {
		applyProbeLinkChainServerItem(identity, controllerBaseURL, item)
	}
}

// applyProbeLinkChainServerItem converts one server chain record into a
// probeControlMessage and delegates to the existing start logic.
// It figures out this node's role and hop config from the chain topology.
func applyProbeLinkChainServerItem(identity nodeIdentity, controllerBaseURL string, item probeLinkChainServerItem) {
	chainID := strings.TrimSpace(item.ChainID)
	if chainID == "" {
		return
	}

	nodeID := strings.TrimSpace(identity.NodeID)
	role := resolveProbeNodeChainRole(item, nodeID)
	if role == "" {
		// This node is not in the chain's route – skip.
		return
	}

	// Locate this node's hop config to get listen_port, link_layer, listen_host.
	hop := findHopConfigForNode(item, nodeID)
	if hop.ListenPort <= 0 {
		log.Printf("warning: probe chain sync skip chain=%s role=%s: hop listen_port not configured", chainID, role)
		return
	}

	// Determine the next hop (relay_host:external_port) based on role.
	nextHost, nextPort, nextLinkLayer, nextDialMode, nextAuthMode := resolveProbeChainNextHopFromItem(item, nodeID, role)
	prevHost, prevPort, prevLinkLayer, prevDialMode := resolveProbeChainPrevHopFromItem(item, nodeID, role)

	// Require next_host+port unless this is the exit node (next_auth_mode=proxy).
	if nextAuthMode != "proxy" && (strings.TrimSpace(nextHost) == "" || nextPort <= 0) {
		log.Printf("warning: probe chain sync skip chain=%s role=%s: next hop not resolved", chainID, role)
		return
	}
	if strings.EqualFold(strings.TrimSpace(prevDialMode), "reverse") && (strings.TrimSpace(prevHost) == "" || prevPort <= 0) {
		log.Printf("warning: probe chain sync skip chain=%s role=%s: prev reverse hop not resolved", chainID, role)
		return
	}

	listenHost := strings.TrimSpace(hop.ListenHost)
	if listenHost == "" {
		listenHost = "0.0.0.0"
	}

	msg := probeControlMessage{
		ChainID:         chainID,
		ChainType:       strings.TrimSpace(item.ChainType),
		Name:            strings.TrimSpace(item.Name),
		UserID:          strings.TrimSpace(item.UserID),
		UserPublicKey:   strings.TrimSpace(item.UserPublicKey),
		LinkSecret:      strings.TrimSpace(item.Secret),
		Role:            role,
		ListenHost:      listenHost,
		ListenPort:      hop.ListenPort,
		LinkLayer:       firstNonEmpty(strings.TrimSpace(hop.LinkLayer), strings.TrimSpace(item.LinkLayer), "http"),
		NextLinkLayer:   strings.TrimSpace(nextLinkLayer),
		NextDialMode:    strings.TrimSpace(nextDialMode),
		NextHost:        nextHost,
		NextPort:        nextPort,
		PrevHost:        prevHost,
		PrevPort:        prevPort,
		PrevLinkLayer:   strings.TrimSpace(prevLinkLayer),
		PrevDialMode:    strings.TrimSpace(prevDialMode),
		PortForwards:    buildProbeChainPortForwardMessages(item.PortForwards),
		RequireUserAuth: strings.TrimSpace(item.UserPublicKey) != "",
		NextAuthMode:    nextAuthMode,
	}

	cfg, err := buildProbeChainRuntimeConfigFromControl(msg)
	if err != nil {
		log.Printf("warning: probe chain sync build config failed: chain=%s err=%v", chainID, err)
		return
	}
	cfg.identity = identity
	cfg.controllerURL = resolveProbeControllerBaseURL(strings.TrimSpace(controllerBaseURL), "")

	// Skip restart if config has not changed (compare fields that affect behaviour).
	if isSameProbeChainRuntimeConfig(chainID, cfg) {
		return
	}

	if _, err := startProbeChainRuntime(cfg); err != nil {
		log.Printf("warning: probe chain sync start failed: chain=%s err=%v", chainID, err)
	}
}

func normalizeProbeChainNodeID(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "node-") || strings.HasPrefix(lower, "node_") {
		suffix := strings.TrimPrefix(strings.TrimPrefix(lower, "node-"), "node_")
		suffix = strings.TrimSpace(suffix)
		if suffix != "" {
			if n, err := strconv.Atoi(suffix); err == nil && n > 0 {
				return strconv.Itoa(n)
			}
			return suffix
		}
	}
	if n, err := strconv.Atoi(value); err == nil && n > 0 {
		return strconv.Itoa(n)
	}
	return value
}

func findHopConfigForNodeID(item probeLinkChainServerItem, nodeID string) (probeLinkChainHopServerItem, bool) {
	targetNodeID := normalizeProbeChainNodeID(nodeID)
	if targetNodeID == "" {
		return probeLinkChainHopServerItem{}, false
	}
	for _, hop := range item.HopConfigs {
		if hop.NodeNo <= 0 {
			continue
		}
		hopNodeID := normalizeProbeChainNodeID(strconv.Itoa(hop.NodeNo))
		if hopNodeID == "" || hopNodeID != targetNodeID {
			continue
		}
		return hop, true
	}
	return probeLinkChainHopServerItem{}, false
}

// resolveProbeNodeChainRole returns the role of nodeID in the chain.
func resolveProbeNodeChainRole(item probeLinkChainServerItem, nodeID string) string {
	targetNodeID := normalizeProbeChainNodeID(nodeID)
	if targetNodeID == "" {
		return ""
	}
	entryNodeID := normalizeProbeChainNodeID(item.EntryNodeID)
	exitNodeID := normalizeProbeChainNodeID(item.ExitNodeID)
	isEntry := entryNodeID != "" && targetNodeID == entryNodeID
	isExit := exitNodeID != "" && targetNodeID == exitNodeID
	if isEntry && isExit {
		return "entry_exit"
	}
	if isEntry {
		return "entry"
	}
	if isExit {
		return "exit"
	}

	// Topology fallback: when entry/exit fields are partially missing,
	// infer head/tail roles from computed route [entry, cascade..., exit].
	// This keeps single-cascade chains (e.g. entry missing, cascade has one node)
	// correctly treated as entry instead of relay.
	route := buildChainRoute(item)
	if len(route) > 0 {
		inferredEntry := normalizeProbeChainNodeID(route[0])
		inferredExit := normalizeProbeChainNodeID(route[len(route)-1])
		inferredIsEntry := inferredEntry != "" && targetNodeID == inferredEntry
		inferredIsExit := inferredExit != "" && targetNodeID == inferredExit
		if inferredIsEntry && inferredIsExit {
			return "entry_exit"
		}
		if inferredIsEntry {
			return "entry"
		}
		if inferredIsExit {
			return "exit"
		}
	}

	for _, id := range item.CascadeNodeIDs {
		if normalizeProbeChainNodeID(id) == targetNodeID {
			return "relay"
		}
	}
	return ""
}

// findHopConfigForNode returns the hop_config for nodeID. It first matches hop.node_no
// as node identity (current format), then falls back to legacy positional numbering.
func findHopConfigForNode(item probeLinkChainServerItem, nodeID string) probeLinkChainHopServerItem {
	if hop, ok := findHopConfigForNodeID(item, nodeID); ok {
		return hop
	}

	// Legacy fallback: node_no was stored as route position (1..N).
	targetNodeID := normalizeProbeChainNodeID(nodeID)
	route := buildChainRoute(item)
	for no, id := range route {
		if normalizeProbeChainNodeID(id) != targetNodeID {
			continue
		}
		legacyNodeNo := no + 1 // 1-indexed
		for _, hop := range item.HopConfigs {
			if hop.NodeNo == legacyNodeNo {
				return hop
			}
		}
		break
	}
	return probeLinkChainHopServerItem{}
}

// resolveProbeChainNextHopFromItem determines next_host, next_port, next_auth_mode
// based on the current node's position in the chain.
//   - Entry/Relay:  next hop = following node in route (use relay_host + external_port)
//   - Exit:         next_auth_mode = "proxy" (connects to actual destination)
func resolveProbeChainNextHopFromItem(item probeLinkChainServerItem, nodeID, role string) (host string, port int, nextLayer string, nextDialMode string, authMode string) {
	if role == "exit" || role == "entry_exit" {
		// Exit node proxies to the end target, no next relay needed.
		return "", 0, "", probeChainDialModeNone, "proxy"
	}

	route := buildChainRoute(item)
	targetNodeID := normalizeProbeChainNodeID(nodeID)
	for i, id := range route {
		if normalizeProbeChainNodeID(id) != targetNodeID {
			continue
		}
		if i+1 >= len(route) {
			break
		}
		nextNodeID := route[i+1]
		dialMode := probeChainDialModeForward
		if currentHop, ok := findHopConfigForNodeID(item, id); ok {
			dialMode = normalizeProbeChainDialMode(strings.TrimSpace(currentHop.DialMode))
		}
		nextHop := findHopConfigForNode(item, nextNodeID)
		relayHost := strings.TrimSpace(nextHop.RelayHost)
		externalPort := nextHop.ExternalPort
		if externalPort <= 0 {
			externalPort = nextHop.ListenPort
		}
		return relayHost, externalPort, firstNonEmpty(strings.TrimSpace(nextHop.LinkLayer), strings.TrimSpace(item.LinkLayer), "http"), dialMode, "secret"
	}
	return "", 0, "", probeChainDialModeNone, "none"
}

func resolveProbeChainPrevHopFromItem(item probeLinkChainServerItem, nodeID, role string) (host string, port int, prevLayer string, prevDialMode string) {
	if role == "entry" {
		return "", 0, "", probeChainDialModeNone
	}
	route := buildChainRoute(item)
	targetNodeID := normalizeProbeChainNodeID(nodeID)
	for i, id := range route {
		if normalizeProbeChainNodeID(id) != targetNodeID {
			continue
		}
		if i <= 0 {
			return "", 0, "", probeChainDialModeNone
		}
		prevNodeID := route[i-1]
		prevHop := findHopConfigForNode(item, prevNodeID)
		externalPort := prevHop.ExternalPort
		if externalPort <= 0 {
			externalPort = prevHop.ListenPort
		}
		return strings.TrimSpace(prevHop.RelayHost), externalPort, firstNonEmpty(strings.TrimSpace(prevHop.LinkLayer), strings.TrimSpace(item.LinkLayer), "http"), normalizeProbeChainDialMode(strings.TrimSpace(prevHop.DialMode))
	}
	return "", 0, "", probeChainDialModeNone
}

// buildChainRoute returns the ordered node ID list: [entry, cascade..., exit].
func buildChainRoute(item probeLinkChainServerItem) []string {
	route := make([]string, 0, 2+len(item.CascadeNodeIDs))
	seen := make(map[string]struct{}, 2+len(item.CascadeNodeIDs))
	push := func(raw string) {
		nodeID := normalizeProbeChainNodeID(raw)
		if nodeID == "" {
			return
		}
		if _, exists := seen[nodeID]; exists {
			return
		}
		seen[nodeID] = struct{}{}
		route = append(route, nodeID)
	}
	push(item.EntryNodeID)
	for _, id := range item.CascadeNodeIDs {
		push(id)
	}
	push(item.ExitNodeID)
	return route
}

func buildProbeChainPortForwardMessages(values []probeChainPortForwardServerItem) []probeChainPortForwardMessage {
	if len(values) == 0 {
		return []probeChainPortForwardMessage{}
	}
	out := make([]probeChainPortForwardMessage, 0, len(values))
	for _, item := range values {
		out = append(out, probeChainPortForwardMessage{
			ID:         strings.TrimSpace(item.ID),
			Name:       strings.TrimSpace(item.Name),
			EntrySide:  strings.TrimSpace(item.EntrySide),
			ListenHost: strings.TrimSpace(item.ListenHost),
			ListenPort: item.ListenPort,
			TargetHost: strings.TrimSpace(item.TargetHost),
			TargetPort: item.TargetPort,
			Network:    strings.TrimSpace(item.Network),
			Enabled:    item.Enabled,
		})
	}
	return out
}

// isSameProbeChainRuntimeConfig returns true if the currently running runtime
// for chainID has the same effective config as cfg (no restart needed).
func isSameProbeChainRuntimeConfig(chainID string, cfg probeChainRuntimeConfig) bool {
	probeChainRuntimeState.mu.Lock()
	rt, ok := probeChainRuntimeState.runtimes[chainID]
	probeChainRuntimeState.mu.Unlock()
	if !ok || rt == nil {
		return false
	}
	c := rt.cfg
	return c.chainType == cfg.chainType &&
		c.role == cfg.role &&
		c.listenHost == cfg.listenHost &&
		c.listenPort == cfg.listenPort &&
		c.linkLayer == cfg.linkLayer &&
		c.nextLinkLayer == cfg.nextLinkLayer &&
		c.nextDialMode == cfg.nextDialMode &&
		c.nextHost == cfg.nextHost &&
		c.nextPort == cfg.nextPort &&
		c.prevHost == cfg.prevHost &&
		c.prevPort == cfg.prevPort &&
		c.prevLinkLayer == cfg.prevLinkLayer &&
		c.prevDialMode == cfg.prevDialMode &&
		c.nextAuthMode == cfg.nextAuthMode &&
		isSameProbeChainPortForwards(c.portForwards, cfg.portForwards) &&
		c.secret == cfg.secret &&
		c.rawPublicKey == cfg.rawPublicKey
}

func isSameProbeChainPortForwards(left []probeChainRuntimePortForward, right []probeChainRuntimePortForward) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if strings.TrimSpace(left[i].ID) != strings.TrimSpace(right[i].ID) {
			return false
		}
		if strings.TrimSpace(left[i].Name) != strings.TrimSpace(right[i].Name) {
			return false
		}
		if strings.TrimSpace(left[i].EntrySide) != strings.TrimSpace(right[i].EntrySide) {
			return false
		}
		if strings.TrimSpace(left[i].ListenHost) != strings.TrimSpace(right[i].ListenHost) {
			return false
		}
		if left[i].ListenPort != right[i].ListenPort {
			return false
		}
		if strings.TrimSpace(left[i].TargetHost) != strings.TrimSpace(right[i].TargetHost) {
			return false
		}
		if left[i].TargetPort != right[i].TargetPort {
			return false
		}
		if strings.TrimSpace(left[i].Network) != strings.TrimSpace(right[i].Network) {
			return false
		}
		if left[i].Enabled != right[i].Enabled {
			return false
		}
	}
	return true
}

func persistProbeChainTopologyCache(items []probeLinkChainServerItem) error {
	cachePath, err := resolveProbeChainTopologyCachePath()
	if err != nil {
		return err
	}
	payload := probeChainTopologyCacheFile{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Items:     sanitizeProbeChainServerItemsForCache(items),
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(cachePath, append(encoded, '\n'), 0o644)
}

func resolveProbeChainTopologyCachePath() (string, error) {
	dataPath, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataPath, probeChainTopologyCacheFileName), nil
}

func resolveProbeProxyChainsCachePath() (string, error) {
	dataPath, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataPath, probeProxyChainsCacheFileName), nil
}

func sanitizeProbeChainServerItemsForCache(items []probeLinkChainServerItem) []probeLinkChainServerItem {
	if len(items) == 0 {
		return []probeLinkChainServerItem{}
	}
	out := make([]probeLinkChainServerItem, 0, len(items))
	for _, item := range items {
		next := item
		next.ChainID = strings.TrimSpace(item.ChainID)
		next.ChainType = strings.TrimSpace(item.ChainType)
		next.Name = strings.TrimSpace(item.Name)
		next.UserID = strings.TrimSpace(item.UserID)
		next.UserPublicKey = strings.TrimSpace(item.UserPublicKey)
		next.Secret = strings.TrimSpace(item.Secret)
		next.EntryNodeID = strings.TrimSpace(item.EntryNodeID)
		next.ExitNodeID = strings.TrimSpace(item.ExitNodeID)
		next.LinkLayer = strings.TrimSpace(item.LinkLayer)
		next.EgressHost = strings.TrimSpace(item.EgressHost)
		next.CascadeNodeIDs = append([]string{}, item.CascadeNodeIDs...)
		for i := range next.CascadeNodeIDs {
			next.CascadeNodeIDs[i] = strings.TrimSpace(next.CascadeNodeIDs[i])
		}
		next.HopConfigs = append([]probeLinkChainHopServerItem{}, item.HopConfigs...)
		for i := range next.HopConfigs {
			next.HopConfigs[i].ListenHost = strings.TrimSpace(next.HopConfigs[i].ListenHost)
			next.HopConfigs[i].LinkLayer = strings.TrimSpace(next.HopConfigs[i].LinkLayer)
			next.HopConfigs[i].DialMode = strings.TrimSpace(next.HopConfigs[i].DialMode)
			next.HopConfigs[i].RelayHost = strings.TrimSpace(next.HopConfigs[i].RelayHost)
		}
		next.PortForwards = append([]probeChainPortForwardServerItem{}, item.PortForwards...)
		for i := range next.PortForwards {
			next.PortForwards[i].ID = strings.TrimSpace(next.PortForwards[i].ID)
			next.PortForwards[i].Name = strings.TrimSpace(next.PortForwards[i].Name)
			next.PortForwards[i].ListenHost = strings.TrimSpace(next.PortForwards[i].ListenHost)
			next.PortForwards[i].TargetHost = strings.TrimSpace(next.PortForwards[i].TargetHost)
			next.PortForwards[i].Network = strings.TrimSpace(next.PortForwards[i].Network)
		}
		out = append(out, next)
	}
	return out
}

func persistProbeProxyChainCache(items []probeLinkChainServerItem) error {
	cachePath, err := resolveProbeProxyChainsCachePath()
	if err != nil {
		return err
	}
	all := sanitizeProbeChainServerItemsForCache(items)
	proxyOnly := make([]probeLinkChainServerItem, 0, len(all))
	for _, item := range all {
		if !strings.EqualFold(strings.TrimSpace(item.ChainType), "proxy_chain") {
			continue
		}
		next := item
		next.PortForwards = []probeChainPortForwardServerItem{}
		proxyOnly = append(proxyOnly, next)
	}
	payload := struct {
		UpdatedAt string                     `json:"updated_at"`
		Items     []probeLinkChainServerItem `json:"items"`
	}{
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Items:     proxyOnly,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(cachePath, append(encoded, '\n'), 0o644)
}
