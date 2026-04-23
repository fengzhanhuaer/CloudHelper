package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	probeLocalSessionTTL         = 8 * time.Hour
	probeLocalMinPasswordLength  = 8
	probeLocalMaxPasswordLength  = 128
	probeLocalMaxUsernameLength  = 64
	probeLocalAuthReadBodyMaxLen = 64 * 1024

	probeLocalProxyModeDirect = "direct"
	probeLocalProxyModeTUN    = "tunnel"
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
