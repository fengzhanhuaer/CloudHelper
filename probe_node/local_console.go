package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	probeLocalListenAddrDefault = "127.0.0.1:16032"

	probeLocalAuthStoreFile      = "probe_local_auth.json"
	probeLocalSessionCookieName  = "probe_local_session"
	probeLocalSessionTTL         = 30 * 24 * time.Hour
	probeLocalMinPasswordLength  = 8
	probeLocalMaxPasswordLength  = 128
	probeLocalMaxUsernameLength  = 64
	probeLocalAuthReadBodyMaxLen = 64 * 1024

	probeLocalProxyModeDirect = "direct"
	probeLocalProxyModeTUN    = "tunnel"

	probeLocalProxyGroupFileName  = "proxy_group.json"
	probeLocalProxyStateFileName  = "proxy_state.json"
	probeLocalProxyHostFileName   = "proxy_host.txt"
	probeLocalProxyChainFileName  = "proxy_chain.json"
	probeLocalProxyBackupAPIPath  = "/api/probe/proxy_group/backup"
	probeLocalProxyReadBodyMaxLen = 512 * 1024
)

type probeLocalAuthState struct {
	Registered   bool   `json:"registered"`
	Username     string `json:"username,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
	PasswordSalt string `json:"password_salt,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

type probeLocalSessionState struct {
	Username  string
	ExpiresAt time.Time
}

type probeLocalAuthManager struct {
	mu sync.RWMutex

	state    probeLocalAuthState
	sessions map[string]probeLocalSessionState
}

type probeLocalHTTPError struct {
	Status  int
	Message string
}

func (e *probeLocalHTTPError) Error() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.Message)
}

type probeLocalTunRuntimeState struct {
	Platform  string `json:"platform"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	LastError string `json:"last_error,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type probeLocalProxyRuntimeState struct {
	Enabled   bool   `json:"enabled"`
	Mode      string `json:"mode"`
	LastError string `json:"last_error,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type probeLocalControlManager struct {
	mu    sync.RWMutex
	tun   probeLocalTunRuntimeState
	proxy probeLocalProxyRuntimeState
}

type probeLocalProxyGroupEntry struct {
	Group     string `json:"group"`
	RulesText string `json:"rules_text"`
}

type probeLocalProxyGroupFile struct {
	Version int                         `json:"version"`
	Groups  []probeLocalProxyGroupEntry `json:"groups"`
	Note    string                      `json:"note,omitempty"`
}

type probeLocalProxyStateGroupEntry struct {
	Group         string `json:"group"`
	Action        string `json:"action,omitempty"`
	TunnelNodeID  string `json:"tunnel_node_id,omitempty"`
	RuntimeStatus string `json:"runtime_status,omitempty"`
}

type probeLocalProxyBackupState struct {
	LastUploadedAt   string `json:"last_uploaded_at,omitempty"`
	LastUploadStatus string `json:"last_upload_status,omitempty"`
	LastUploadError  string `json:"last_upload_error,omitempty"`
}

type probeLocalProxyStateFile struct {
	Version   int                              `json:"version"`
	UpdatedAt string                           `json:"updated_at"`
	Groups    []probeLocalProxyStateGroupEntry `json:"groups"`
	Backup    probeLocalProxyBackupState       `json:"backup"`
}

type probeLocalHostMapping struct {
	DNS string `json:"dns"`
	IP  string `json:"ip"`
}

type probeLocalProxyRuntimeContext struct {
	Identity          nodeIdentity
	ControllerBaseURL string
}

var (
	errProbeLocalProxyUnsupported = errors.New("probe local proxy takeover is not supported on this platform")
	errProbeLocalTUNUnsupported   = errors.New("probe local tun install is not supported on this platform")
	probeLocalInstallTUNDriver    = installProbeLocalTUNDriver
	probeLocalApplyProxyTakeover  = applyProbeLocalProxyTakeover
	probeLocalRestoreProxyDirect  = restoreProbeLocalProxyDirect
)

func newProbeLocalControlManager() *probeLocalControlManager {
	now := time.Now().UTC().Format(time.RFC3339)
	return &probeLocalControlManager{
		tun: probeLocalTunRuntimeState{
			Platform:  runtime.GOOS,
			Installed: false,
			Enabled:   false,
			UpdatedAt: now,
		},
		proxy: probeLocalProxyRuntimeState{
			Enabled:   false,
			Mode:      probeLocalProxyModeDirect,
			UpdatedAt: now,
		},
	}
}

func (m *probeLocalControlManager) tunStatus() probeLocalTunRuntimeState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tun
}

func (m *probeLocalControlManager) proxyStatus() probeLocalProxyRuntimeState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.proxy
}

func (m *probeLocalControlManager) installTUN() (probeLocalTunRuntimeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := probeLocalInstallTUNDriver(); err != nil {
		m.tun.LastError = strings.TrimSpace(err.Error())
		m.tun.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		status := http.StatusInternalServerError
		if errors.Is(err, errProbeLocalTUNUnsupported) {
			status = http.StatusNotImplemented
		}
		return m.tun, &probeLocalHTTPError{Status: status, Message: m.tun.LastError}
	}

	m.tun.Installed = true
	m.tun.LastError = ""
	m.tun.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return m.tun, nil
}

func (m *probeLocalControlManager) enableProxy() (probeLocalTunRuntimeState, probeLocalProxyRuntimeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.tun.Installed {
		m.proxy.LastError = "tun driver is not installed"
		m.proxy.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return m.tun, m.proxy, &probeLocalHTTPError{Status: http.StatusConflict, Message: m.proxy.LastError}
	}

	if err := probeLocalApplyProxyTakeover(); err != nil {
		m.proxy.LastError = strings.TrimSpace(err.Error())
		m.proxy.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		status := http.StatusInternalServerError
		if errors.Is(err, errProbeLocalProxyUnsupported) {
			status = http.StatusNotImplemented
		}
		return m.tun, m.proxy, &probeLocalHTTPError{Status: status, Message: m.proxy.LastError}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	m.tun.Enabled = true
	m.tun.LastError = ""
	m.tun.UpdatedAt = now
	m.proxy.Enabled = true
	m.proxy.Mode = probeLocalProxyModeTUN
	m.proxy.LastError = ""
	m.proxy.UpdatedAt = now
	return m.tun, m.proxy, nil
}

func (m *probeLocalControlManager) directProxy() (probeLocalTunRuntimeState, probeLocalProxyRuntimeState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := probeLocalRestoreProxyDirect(); err != nil {
		m.proxy.LastError = strings.TrimSpace(err.Error())
		m.proxy.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		status := http.StatusInternalServerError
		if errors.Is(err, errProbeLocalProxyUnsupported) {
			status = http.StatusNotImplemented
		}
		return m.tun, m.proxy, &probeLocalHTTPError{Status: status, Message: m.proxy.LastError}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	m.tun.Enabled = false
	m.tun.UpdatedAt = now
	m.proxy.Enabled = false
	m.proxy.Mode = probeLocalProxyModeDirect
	m.proxy.LastError = ""
	m.proxy.UpdatedAt = now
	return m.tun, m.proxy, nil
}

var (
	probeLocalAuthInitMu   sync.Mutex
	probeLocalAuthInstance *probeLocalAuthManager
	probeLocalControl      = newProbeLocalControlManager()
)

var probeLocalRuntimeState = struct {
	mu      sync.RWMutex
	context probeLocalProxyRuntimeContext
}{}

var probeLocalConsoleState = struct {
	mu         sync.Mutex
	server     *http.Server
	listenAddr string
}{}

func ensureProbeLocalAuthManager() (*probeLocalAuthManager, error) {
	probeLocalAuthInitMu.Lock()
	defer probeLocalAuthInitMu.Unlock()

	if probeLocalAuthInstance != nil {
		return probeLocalAuthInstance, nil
	}

	state, err := loadProbeLocalAuthState()
	if err != nil {
		return nil, err
	}

	probeLocalAuthInstance = &probeLocalAuthManager{
		state:    state,
		sessions: make(map[string]probeLocalSessionState),
	}
	return probeLocalAuthInstance, nil
}

func resolveProbeLocalAuthStorePath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeLocalAuthStoreFile), nil
}

func loadProbeLocalAuthState() (probeLocalAuthState, error) {
	path, err := resolveProbeLocalAuthStorePath()
	if err != nil {
		return probeLocalAuthState{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return probeLocalAuthState{}, nil
		}
		return probeLocalAuthState{}, err
	}
	state := probeLocalAuthState{}
	if err := json.Unmarshal(raw, &state); err != nil {
		return probeLocalAuthState{}, err
	}
	state.Username = strings.TrimSpace(state.Username)
	state.PasswordHash = strings.TrimSpace(state.PasswordHash)
	state.PasswordSalt = strings.TrimSpace(state.PasswordSalt)
	state.UpdatedAt = strings.TrimSpace(state.UpdatedAt)
	if !state.Registered {
		return probeLocalAuthState{}, nil
	}
	if state.Username == "" || state.PasswordHash == "" || state.PasswordSalt == "" {
		return probeLocalAuthState{}, errors.New("invalid probe local auth data")
	}
	return state, nil
}

func persistProbeLocalAuthState(state probeLocalAuthState) error {
	path, err := resolveProbeLocalAuthStorePath()
	if err != nil {
		return err
	}
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(path, payload, 0o600)
}

func normalizeProbeLocalUsername(raw string) string {
	return strings.TrimSpace(raw)
}

func hashProbeLocalPassword(password, salt string) string {
	material := strings.TrimSpace(salt) + "\n" + password
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}

func (m *probeLocalAuthManager) bootstrap() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]any{
		"registered": m.state.Registered,
	}
}

func (m *probeLocalAuthManager) register(username, password, confirmPassword string) error {
	username = normalizeProbeLocalUsername(username)
	if username == "" {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "username is required"}
	}
	if len([]rune(username)) > probeLocalMaxUsernameLength {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "username is too long"}
	}
	if strings.TrimSpace(password) == "" {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "password is required"}
	}
	if len(password) < probeLocalMinPasswordLength {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "password is too short"}
	}
	if len(password) > probeLocalMaxPasswordLength {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "password is too long"}
	}
	if password != confirmPassword {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "password confirmation does not match"}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state.Registered {
		return &probeLocalHTTPError{Status: http.StatusForbidden, Message: "registration is closed"}
	}

	salt := randomHexToken(16)
	next := probeLocalAuthState{
		Registered:   true,
		Username:     username,
		PasswordSalt: salt,
		PasswordHash: hashProbeLocalPassword(password, salt),
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if err := persistProbeLocalAuthState(next); err != nil {
		return err
	}
	m.state = next
	m.sessions = make(map[string]probeLocalSessionState)
	return nil
}

func (m *probeLocalAuthManager) login(username, password string) (string, probeLocalSessionState, error) {
	username = normalizeProbeLocalUsername(username)
	if username == "" || strings.TrimSpace(password) == "" {
		return "", probeLocalSessionState{}, &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "username and password are required"}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.state.Registered {
		return "", probeLocalSessionState{}, &probeLocalHTTPError{Status: http.StatusForbidden, Message: "account is not registered"}
	}

	if !strings.EqualFold(username, m.state.Username) {
		return "", probeLocalSessionState{}, &probeLocalHTTPError{Status: http.StatusUnauthorized, Message: "invalid username or password"}
	}
	givenHash := hashProbeLocalPassword(password, m.state.PasswordSalt)
	if !hmac.Equal([]byte(strings.ToLower(givenHash)), []byte(strings.ToLower(m.state.PasswordHash))) {
		return "", probeLocalSessionState{}, &probeLocalHTTPError{Status: http.StatusUnauthorized, Message: "invalid username or password"}
	}

	token := randomHexToken(32)
	session := probeLocalSessionState{
		Username:  m.state.Username,
		ExpiresAt: time.Now().Add(probeLocalSessionTTL),
	}
	m.sessions[token] = session
	m.cleanupExpiredLocked(time.Now())
	return token, session, nil
}

func (m *probeLocalAuthManager) sessionByToken(token string) (probeLocalSessionState, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return probeLocalSessionState{}, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	session, ok := m.sessions[token]
	if !ok {
		return probeLocalSessionState{}, false
	}
	if time.Now().After(session.ExpiresAt) {
		delete(m.sessions, token)
		return probeLocalSessionState{}, false
	}
	return session, true
}

func (m *probeLocalAuthManager) logoutToken(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

func (m *probeLocalAuthManager) cleanupExpiredLocked(now time.Time) {
	for token, session := range m.sessions {
		if now.After(session.ExpiresAt) {
			delete(m.sessions, token)
		}
	}
}

func extractProbeLocalSessionToken(r *http.Request) (string, error) {
	cookie, err := r.Cookie(probeLocalSessionCookieName)
	if err != nil {
		return "", errors.New("missing local session")
	}
	token := strings.TrimSpace(cookie.Value)
	if token == "" {
		return "", errors.New("missing local session")
	}
	return token, nil
}

func currentProbeLocalSessionFromRequest(r *http.Request) (probeLocalSessionState, string, error) {
	mgr, err := ensureProbeLocalAuthManager()
	if err != nil {
		return probeLocalSessionState{}, "", err
	}
	token, err := extractProbeLocalSessionToken(r)
	if err != nil {
		return probeLocalSessionState{}, "", err
	}
	session, ok := mgr.sessionByToken(token)
	if !ok {
		return probeLocalSessionState{}, "", errors.New("invalid or expired local session")
	}
	return session, token, nil
}

func requireProbeLocalSession(w http.ResponseWriter, r *http.Request) (probeLocalSessionState, bool) {
	session, _, err := currentProbeLocalSessionFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return probeLocalSessionState{}, false
	}
	return session, true
}

func setProbeLocalSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     probeLocalSessionCookieName,
		Value:    strings.TrimSpace(token),
		Path:     "/local",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
	})
}

func clearProbeLocalSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     probeLocalSessionCookieName,
		Value:    "",
		Path:     "/local",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0).UTC(),
		MaxAge:   -1,
	})
}

func writeProbeLocalError(w http.ResponseWriter, err error) {
	if httpErr, ok := err.(*probeLocalHTTPError); ok {
		writeJSON(w, httpErr.Status, map[string]string{"error": httpErr.Message})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": strings.TrimSpace(err.Error())})
}

func defaultProbeLocalProxyGroupFile() probeLocalProxyGroupFile {
	return probeLocalProxyGroupFile{
		Version: 1,
		Groups: []probeLocalProxyGroupEntry{
			{Group: "default", RulesText: ""},
		},
		Note: "fallback is built in",
	}
}

func defaultProbeLocalProxyStateFile() probeLocalProxyStateFile {
	return probeLocalProxyStateFile{
		Version:   1,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Groups:    []probeLocalProxyStateGroupEntry{},
		Backup: probeLocalProxyBackupState{
			LastUploadedAt:   "",
			LastUploadStatus: "idle",
			LastUploadError:  "",
		},
	}
}

func defaultProbeLocalProxyHostContent() string {
	return "# dns,ip\n"
}

func resolveProbeLocalProxyGroupPath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeLocalProxyGroupFileName), nil
}

func resolveProbeLocalProxyStatePath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeLocalProxyStateFileName), nil
}

func resolveProbeLocalProxyHostPath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeLocalProxyHostFileName), nil
}

func resolveProbeLocalProxyChainPath() (string, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, probeLocalProxyChainFileName), nil
}

func decodeProbeLocalJSONStrict(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("unexpected extra data")
		}
		return err
	}
	return nil
}

func validateProbeLocalProxyGroupFile(payload probeLocalProxyGroupFile) error {
	if len(payload.Groups) == 0 {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "groups is required"}
	}
	seen := make(map[string]struct{}, len(payload.Groups))
	for i, group := range payload.Groups {
		name := strings.TrimSpace(group.Group)
		if name == "" {
			return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("groups[%d].group is required", i)}
		}
		if strings.EqualFold(name, "fallback") {
			return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "fallback is built in and must not be configured explicitly"}
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("duplicate group: %s", name)}
		}
		seen[key] = struct{}{}
		lines := strings.Split(strings.ReplaceAll(group.RulesText, "\r\n", "\n"), "\n")
		for lineNo, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if !strings.Contains(trimmed, ":") {
				return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("groups[%d].rules_text line %d must contain ':'", i, lineNo+1)}
			}
		}
	}
	return nil
}

func persistProbeLocalJSONFile(path string, payload any) error {
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}

func loadProbeLocalProxyGroupFile() (probeLocalProxyGroupFile, error) {
	path, err := resolveProbeLocalProxyGroupPath()
	if err != nil {
		return probeLocalProxyGroupFile{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			def := defaultProbeLocalProxyGroupFile()
			if writeErr := persistProbeLocalProxyGroupFile(def); writeErr != nil {
				return probeLocalProxyGroupFile{}, writeErr
			}
			return def, nil
		}
		return probeLocalProxyGroupFile{}, err
	}
	payload := probeLocalProxyGroupFile{}
	if err := decodeProbeLocalJSONStrict(raw, &payload); err != nil {
		return probeLocalProxyGroupFile{}, err
	}
	if payload.Version <= 0 {
		payload.Version = 1
	}
	for i := range payload.Groups {
		payload.Groups[i].Group = strings.TrimSpace(payload.Groups[i].Group)
	}
	payload.Note = firstNonEmpty(strings.TrimSpace(payload.Note), "fallback is built in")
	if err := validateProbeLocalProxyGroupFile(payload); err != nil {
		return probeLocalProxyGroupFile{}, err
	}
	return payload, nil
}

func persistProbeLocalProxyGroupFile(payload probeLocalProxyGroupFile) error {
	if payload.Version <= 0 {
		payload.Version = 1
	}
	payload.Note = firstNonEmpty(strings.TrimSpace(payload.Note), "fallback is built in")
	if err := validateProbeLocalProxyGroupFile(payload); err != nil {
		return err
	}
	path, err := resolveProbeLocalProxyGroupPath()
	if err != nil {
		return err
	}
	return persistProbeLocalJSONFile(path, payload)
}

func loadProbeLocalProxyStateFile() (probeLocalProxyStateFile, error) {
	path, err := resolveProbeLocalProxyStatePath()
	if err != nil {
		return probeLocalProxyStateFile{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			def := defaultProbeLocalProxyStateFile()
			if writeErr := persistProbeLocalProxyStateFile(def); writeErr != nil {
				return probeLocalProxyStateFile{}, writeErr
			}
			return def, nil
		}
		return probeLocalProxyStateFile{}, err
	}
	payload := probeLocalProxyStateFile{}
	if err := decodeProbeLocalJSONStrict(raw, &payload); err != nil {
		return probeLocalProxyStateFile{}, err
	}
	if payload.Version <= 0 {
		payload.Version = 1
	}
	if strings.TrimSpace(payload.UpdatedAt) == "" {
		payload.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if payload.Groups == nil {
		payload.Groups = []probeLocalProxyStateGroupEntry{}
	}
	if strings.TrimSpace(payload.Backup.LastUploadStatus) == "" {
		payload.Backup.LastUploadStatus = "idle"
	}
	return payload, nil
}

func persistProbeLocalProxyStateFile(payload probeLocalProxyStateFile) error {
	if payload.Version <= 0 {
		payload.Version = 1
	}
	payload.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if payload.Groups == nil {
		payload.Groups = []probeLocalProxyStateGroupEntry{}
	}
	if strings.TrimSpace(payload.Backup.LastUploadStatus) == "" {
		payload.Backup.LastUploadStatus = "idle"
	}
	path, err := resolveProbeLocalProxyStatePath()
	if err != nil {
		return err
	}
	return persistProbeLocalJSONFile(path, payload)
}

func validateProbeLocalRuntimeAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "direct", "reject", "tunnel":
		return true
	default:
		return false
	}
}

func upsertProbeLocalRuntimeStateGroup(group, action, tunnelNodeID, runtimeStatus string) error {
	group = strings.TrimSpace(group)
	action = strings.ToLower(strings.TrimSpace(action))
	tunnelNodeID = strings.TrimSpace(tunnelNodeID)
	runtimeStatus = strings.TrimSpace(runtimeStatus)
	if group == "" {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "group is required"}
	}
	if !validateProbeLocalRuntimeAction(action) {
		return &probeLocalHTTPError{Status: http.StatusBadRequest, Message: "invalid runtime action"}
	}
	if action != "tunnel" {
		tunnelNodeID = ""
	}
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return err
	}
	matched := false
	for i := range state.Groups {
		if strings.EqualFold(strings.TrimSpace(state.Groups[i].Group), group) {
			state.Groups[i].Group = group
			state.Groups[i].Action = action
			state.Groups[i].TunnelNodeID = tunnelNodeID
			state.Groups[i].RuntimeStatus = runtimeStatus
			matched = true
			break
		}
	}
	if !matched {
		state.Groups = append(state.Groups, probeLocalProxyStateGroupEntry{
			Group:         group,
			Action:        action,
			TunnelNodeID:  tunnelNodeID,
			RuntimeStatus: runtimeStatus,
		})
	}
	return persistProbeLocalProxyStateFile(state)
}

func setProbeLocalBackupStatus(status, lastError, uploadedAt string) error {
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		return err
	}
	state.Backup.LastUploadStatus = firstNonEmpty(strings.TrimSpace(status), "idle")
	state.Backup.LastUploadError = strings.TrimSpace(lastError)
	state.Backup.LastUploadedAt = strings.TrimSpace(uploadedAt)
	return persistProbeLocalProxyStateFile(state)
}

func parseProbeLocalHostMappings(content string) ([]probeLocalHostMapping, error) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	indexByDNS := map[string]int{}
	out := make([]probeLocalHostMapping, 0, len(lines))
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(trimmed, ",", 2)
		if len(parts) != 2 {
			return nil, &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("proxy_host.txt line %d must be dns,ip", i+1)}
		}
		dns := strings.ToLower(strings.TrimSpace(parts[0]))
		ipText := strings.TrimSpace(parts[1])
		if dns == "" {
			return nil, &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("proxy_host.txt line %d dns is empty", i+1)}
		}
		if net.ParseIP(ipText) == nil {
			return nil, &probeLocalHTTPError{Status: http.StatusBadRequest, Message: fmt.Sprintf("proxy_host.txt line %d ip is invalid", i+1)}
		}
		entry := probeLocalHostMapping{DNS: dns, IP: ipText}
		if idx, exists := indexByDNS[dns]; exists {
			out[idx] = entry
			logProbeWarnf("probe local proxy host duplicate dns replaced: %s", dns)
			continue
		}
		indexByDNS[dns] = len(out)
		out = append(out, entry)
	}
	return out, nil
}

func encodeProbeLocalHostMappingsContent(hosts []probeLocalHostMapping) string {
	if len(hosts) == 0 {
		return defaultProbeLocalProxyHostContent()
	}
	lines := make([]string, 0, len(hosts))
	for _, host := range hosts {
		dns := strings.ToLower(strings.TrimSpace(host.DNS))
		ipText := strings.TrimSpace(host.IP)
		if dns == "" || ipText == "" {
			continue
		}
		lines = append(lines, dns+","+ipText)
	}
	if len(lines) == 0 {
		return defaultProbeLocalProxyHostContent()
	}
	return strings.Join(lines, "\n") + "\n"
}

func loadProbeLocalHostMappingsWithContent() (string, []probeLocalHostMapping, error) {
	path, err := resolveProbeLocalProxyHostPath()
	if err != nil {
		return "", nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			content := defaultProbeLocalProxyHostContent()
			hosts, parseErr := parseProbeLocalHostMappings(content)
			if parseErr != nil {
				return "", nil, parseErr
			}
			if writeErr := persistProbeLocalHostMappings(hosts); writeErr != nil {
				return "", nil, writeErr
			}
			return content, hosts, nil
		}
		return "", nil, err
	}
	content := string(raw)
	hosts, err := parseProbeLocalHostMappings(content)
	if err != nil {
		return "", nil, err
	}
	return content, hosts, nil
}

func persistProbeLocalHostMappings(hosts []probeLocalHostMapping) error {
	path, err := resolveProbeLocalProxyHostPath()
	if err != nil {
		return err
	}
	content := encodeProbeLocalHostMappingsContent(hosts)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

type probeLocalProxyChainsFile struct {
	UpdatedAt string                     `json:"updated_at"`
	Items     []probeLinkChainServerItem `json:"items"`
	Chains    []probeLinkChainServerItem `json:"chains"`
}

func loadProbeLocalProxyChainItems() ([]probeLinkChainServerItem, error) {
	path, err := resolveProbeLocalProxyChainPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []probeLinkChainServerItem{}, nil
		}
		return nil, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return []probeLinkChainServerItem{}, nil
	}
	payload := probeLocalProxyChainsFile{}
	if err := decodeProbeLocalJSONStrict([]byte(trimmed), &payload); err != nil {
		var items []probeLinkChainServerItem
		if err2 := decodeProbeLocalJSONStrict([]byte(trimmed), &items); err2 != nil {
			return nil, err
		}
		payload.Items = items
	}
	items := payload.Items
	if len(items) == 0 && len(payload.Chains) > 0 {
		items = payload.Chains
	}
	items = sanitizeProbeChainServerItemsForCache(items)
	out := make([]probeLinkChainServerItem, 0, len(items))
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.ChainType), "proxy_chain") {
			item.PortForwards = []probeChainPortForwardServerItem{}
			out = append(out, item)
		}
	}
	return out, nil
}

func backupProbeLocalProxyGroupToController(ctx context.Context) error {
	runtimeContext := currentProbeLocalProxyRuntimeContext()
	baseURL := strings.TrimSpace(runtimeContext.ControllerBaseURL)
	if baseURL == "" {
		return &probeLocalHTTPError{Status: http.StatusConflict, Message: "controller base url is empty"}
	}
	path, err := resolveProbeLocalProxyGroupPath()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"file_name":      probeLocalProxyGroupFileName,
		"node_id":        strings.TrimSpace(runtimeContext.Identity.NodeID),
		"content_base64": base64.StdEncoding.EncodeToString(raw),
	})
	if err != nil {
		return err
	}
	requestURL := strings.TrimRight(baseURL, "/") + probeLocalProxyBackupAPIPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range buildProbeAuthHeaders(runtimeContext.Identity) {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		message := strings.TrimSpace(string(responseBody))
		if message == "" {
			message = "controller backup upload failed"
		}
		return &probeLocalHTTPError{Status: http.StatusBadGateway, Message: fmt.Sprintf("controller backup upload failed: %d %s", resp.StatusCode, message)}
	}
	return nil
}

func normalizeProbeLocalListenAddr(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return ""
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "" {
		host = "127.0.0.1"
	}
	portNum, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil || portNum <= 0 || portNum > 65535 {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(portNum))
}

func resolveProbeLocalListenAddr(explicit string) string {
	candidate := firstNonEmpty(strings.TrimSpace(explicit), strings.TrimSpace(os.Getenv("PROBE_LOCAL_LISTEN")), probeLocalListenAddrDefault)
	normalized := normalizeProbeLocalListenAddr(candidate)
	if normalized != "" {
		return normalized
	}
	return probeLocalListenAddrDefault
}

func startProbeLocalConsoleServer(handler http.Handler, explicitListen string) error {
	if handler == nil {
		return errors.New("nil local console handler")
	}
	addr := resolveProbeLocalListenAddr(explicitListen)

	probeLocalConsoleState.mu.Lock()
	if probeLocalConsoleState.server != nil {
		probeLocalConsoleState.mu.Unlock()
		return nil
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		probeLocalConsoleState.mu.Unlock()
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	probeLocalConsoleState.server = server
	probeLocalConsoleState.listenAddr = addr
	probeLocalConsoleState.mu.Unlock()

	logProbeInfof("probe local console listening on http://%s", addr)
	go func(s *http.Server, ln net.Listener, listenAddr string) {
		err := s.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			logProbeErrorf("probe local console exited: listen=%s err=%v", listenAddr, err)
		}
		probeLocalConsoleState.mu.Lock()
		if probeLocalConsoleState.server == s {
			probeLocalConsoleState.server = nil
			probeLocalConsoleState.listenAddr = ""
		}
		probeLocalConsoleState.mu.Unlock()
	}(server, listener, addr)

	return nil
}

func buildProbeLocalConsoleMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", probeLocalRootHandler)
	registerProbeLocalConsoleRoutes(mux)
	return mux
}

func registerProbeLocalConsoleRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("/local/login", probeLocalLoginPageHandler)
	mux.HandleFunc("/local/panel", probeLocalPanelPageHandler)
	mux.HandleFunc("/local/api/auth/bootstrap", probeLocalAuthBootstrapHandler)
	mux.HandleFunc("/local/api/auth/register", probeLocalAuthRegisterHandler)
	mux.HandleFunc("/local/api/auth/login", probeLocalAuthLoginHandler)
	mux.HandleFunc("/local/api/auth/logout", probeLocalAuthLogoutHandler)
	mux.HandleFunc("/local/api/auth/session", probeLocalAuthSessionHandler)

	mux.HandleFunc("/local/api/tun/status", probeLocalTUNStatusHandler)
	mux.HandleFunc("/local/api/tun/install", probeLocalTUNInstallHandler)
	mux.HandleFunc("/local/api/proxy/enable", probeLocalProxyEnableHandler)
	mux.HandleFunc("/local/api/proxy/direct", probeLocalProxyDirectHandler)
	mux.HandleFunc("/local/api/proxy/status", probeLocalProxyStatusHandler)
	mux.HandleFunc("/local/api/proxy/chains", probeLocalProxyChainsHandler)
	mux.HandleFunc("/local/api/proxy/groups", probeLocalProxyGroupsHandler)
	mux.HandleFunc("/local/api/proxy/groups/save", probeLocalProxyGroupsSaveHandler)
	mux.HandleFunc("/local/api/proxy/state", probeLocalProxyStateHandler)
	mux.HandleFunc("/local/api/proxy/hosts", probeLocalProxyHostsHandler)
	mux.HandleFunc("/local/api/proxy/hosts/save", probeLocalProxyHostsSaveHandler)
	mux.HandleFunc("/local/api/proxy/groups/backup", probeLocalProxyGroupsBackupHandler)
}

type probeLocalRegisterRequest struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
}

type probeLocalLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type probeLocalProxyHostsSaveRequest struct {
	Content string `json:"content"`
}

func probeLocalRootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, _, err := currentProbeLocalSessionFromRequest(r); err == nil {
		http.Redirect(w, r, "/local/panel", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/local/login", http.StatusFound)
}

func probeLocalLoginPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/local/login" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(probeLocalLoginPageHTML))
}

func probeLocalPanelPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/local/panel" {
		http.NotFound(w, r)
		return
	}
	if _, _, err := currentProbeLocalSessionFromRequest(r); err != nil {
		http.Redirect(w, r, "/local/login", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(probeLocalPanelPageHTML))
}

func probeLocalAuthBootstrapHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mgr, err := ensureProbeLocalAuthManager()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, mgr.bootstrap())
}

func probeLocalAuthRegisterHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mgr, err := ensureProbeLocalAuthManager()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalAuthReadBodyMaxLen)
	defer body.Close()
	var req probeLocalRegisterRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := mgr.register(req.Username, req.Password, req.ConfirmPassword); err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "registered": true})
}

func probeLocalAuthLoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mgr, err := ensureProbeLocalAuthManager()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalAuthReadBodyMaxLen)
	defer body.Close()
	var req probeLocalLoginRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	token, session, err := mgr.login(req.Username, req.Password)
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	setProbeLocalSessionCookie(w, token, session.ExpiresAt)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"username":   session.Username,
		"expires_at": session.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func probeLocalAuthLogoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mgr, err := ensureProbeLocalAuthManager()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	if token, tokenErr := extractProbeLocalSessionToken(r); tokenErr == nil {
		mgr.logoutToken(token)
	}
	clearProbeLocalSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func probeLocalAuthSessionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	session, _, err := currentProbeLocalSessionFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"username":      session.Username,
		"expires_at":    session.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func probeLocalTUNStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, probeLocalControl.tunStatus())
}

func probeLocalTUNInstallHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	state, err := probeLocalControl.installTUN()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tun": state})
}

func probeLocalProxyEnableHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	tunState, proxyState, err := probeLocalControl.enableProxy()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	if updateErr := upsertProbeLocalRuntimeStateGroup("fallback", "tunnel", "", "online"); updateErr != nil {
		logProbeWarnf("probe local runtime state update failed: %v", updateErr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tun": tunState, "proxy": proxyState})
}

func probeLocalProxyDirectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	tunState, proxyState, err := probeLocalControl.directProxy()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	if updateErr := upsertProbeLocalRuntimeStateGroup("fallback", "direct", "", "online"); updateErr != nil {
		logProbeWarnf("probe local runtime state update failed: %v", updateErr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tun": tunState, "proxy": proxyState})
}

func probeLocalProxyStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, probeLocalControl.proxyStatus())
}

func probeLocalProxyChainsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	items, err := loadProbeLocalProxyChainItems()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func probeLocalProxyGroupsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	groups, err := loadProbeLocalProxyGroupFile()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func probeLocalProxyGroupsSaveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalProxyReadBodyMaxLen)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var payload probeLocalProxyGroupFile
	if err := decoder.Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := validateProbeLocalProxyGroupFile(payload); err != nil {
		writeProbeLocalError(w, err)
		return
	}
	payload.Note = firstNonEmpty(strings.TrimSpace(payload.Note), "fallback is built in")
	if payload.Version <= 0 {
		payload.Version = 1
	}
	if err := persistProbeLocalProxyGroupFile(payload); err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "groups": payload})
}

func probeLocalProxyStateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	state, err := loadProbeLocalProxyStateFile()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func probeLocalProxyHostsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	content, hosts, err := loadProbeLocalHostMappingsWithContent()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"content": content, "hosts": hosts})
}

func probeLocalProxyHostsSaveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	body := http.MaxBytesReader(w, r.Body, probeLocalProxyReadBodyMaxLen)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var req probeLocalProxyHostsSaveRequest
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	hosts, err := parseProbeLocalHostMappings(req.Content)
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	if err := persistProbeLocalHostMappings(hosts); err != nil {
		writeProbeLocalError(w, err)
		return
	}
	content, normalizedHosts, err := loadProbeLocalHostMappingsWithContent()
	if err != nil {
		writeProbeLocalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "content": content, "hosts": normalizedHosts})
}

func probeLocalProxyGroupsBackupHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := requireProbeLocalSession(w, r); !ok {
		return
	}
	if err := backupProbeLocalProxyGroupToController(r.Context()); err != nil {
		_ = setProbeLocalBackupStatus("failed", strings.TrimSpace(err.Error()), "")
		writeProbeLocalError(w, err)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_ = setProbeLocalBackupStatus("ok", "", now)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "uploaded_at": now})
}

func probeLocalAuthDataFilePath() (string, error) {
	path, err := resolveProbeLocalAuthStorePath()
	if err != nil {
		return "", err
	}
	return path, nil
}

func resetProbeLocalAuthManagerForTest() {
	probeLocalAuthInitMu.Lock()
	probeLocalAuthInstance = nil
	probeLocalAuthInitMu.Unlock()
}

func resetProbeLocalControlStateForTest() {
	probeLocalControl = newProbeLocalControlManager()
}

func resetProbeLocalProxyHooksForTest() {
	probeLocalApplyProxyTakeover = applyProbeLocalProxyTakeover
	probeLocalRestoreProxyDirect = restoreProbeLocalProxyDirect
}

func resetProbeLocalTUNHooksForTest() {
	probeLocalInstallTUNDriver = installProbeLocalTUNDriver
}

func setProbeLocalProxyRuntimeContext(identity nodeIdentity, controllerBaseURL string) {
	probeLocalRuntimeState.mu.Lock()
	probeLocalRuntimeState.context = probeLocalProxyRuntimeContext{
		Identity:          identity,
		ControllerBaseURL: strings.TrimSpace(controllerBaseURL),
	}
	probeLocalRuntimeState.mu.Unlock()
}

func currentProbeLocalProxyRuntimeContext() probeLocalProxyRuntimeContext {
	probeLocalRuntimeState.mu.RLock()
	defer probeLocalRuntimeState.mu.RUnlock()
	return probeLocalRuntimeState.context
}

func currentProbeLocalConsoleListen() string {
	probeLocalConsoleState.mu.Lock()
	defer probeLocalConsoleState.mu.Unlock()
	return strings.TrimSpace(probeLocalConsoleState.listenAddr)
}

func resolveProbeLocalConsoleURL() string {
	addr := strings.TrimSpace(currentProbeLocalConsoleListen())
	if addr == "" {
		addr = probeLocalListenAddrDefault
	}
	return fmt.Sprintf("http://%s", addr)
}
