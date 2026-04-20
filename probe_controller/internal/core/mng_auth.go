package core

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	mngAuthStoreField      = "mng_auth"
	mngSessionCookieName   = "mng_session"
	mngSessionTTL          = 8 * time.Hour
	mngLoginFailThreshold  = 5
	mngLoginFreezeDuration = 5 * time.Minute
	mngMinPasswordLength   = 8
	mngMaxPasswordLength   = 128
	mngMaxUsernameLength   = 64
)

type mngAuthState struct {
	Registered   bool   `json:"registered"`
	Username     string `json:"username,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

type mngSessionState struct {
	Username  string
	ExpiresAt time.Time
}

type mngAuthManager struct {
	mu sync.RWMutex

	state mngAuthState

	sessions        map[string]mngSessionState
	loginFailed     map[string]int
	loginFrozenTill map[string]time.Time
}

type mngHTTPError struct {
	Status  int
	Message string
}

func (e *mngHTTPError) Error() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.Message)
}

var (
	mngAuthInitMu   sync.Mutex
	mngAuthInstance *mngAuthManager
)

func initMngAuth() {
	if _, err := ensureMngAuthManager(); err != nil {
		log.Fatalf("failed to initialize mng auth: %v", err)
	}
}

func ensureMngAuthManager() (*mngAuthManager, error) {
	mngAuthInitMu.Lock()
	defer mngAuthInitMu.Unlock()

	if mngAuthInstance != nil {
		return mngAuthInstance, nil
	}

	state, err := loadMngAuthStateFromStore()
	if err != nil {
		return nil, err
	}

	mngAuthInstance = &mngAuthManager{
		state:           state,
		sessions:        make(map[string]mngSessionState),
		loginFailed:     make(map[string]int),
		loginFrozenTill: make(map[string]time.Time),
	}
	return mngAuthInstance, nil
}

func loadMngAuthStateFromStore() (mngAuthState, error) {
	state := mngAuthState{}
	if Store == nil {
		return state, nil
	}

	Store.mu.RLock()
	raw, exists := Store.Data[mngAuthStoreField]
	Store.mu.RUnlock()
	if !exists || raw == nil {
		return state, nil
	}

	blob, err := json.Marshal(raw)
	if err != nil {
		return mngAuthState{}, err
	}
	if err := json.Unmarshal(blob, &state); err != nil {
		return mngAuthState{}, err
	}

	state.Username = normalizeUsername(state.Username)
	state.PasswordHash = strings.TrimSpace(state.PasswordHash)
	state.CreatedAt = strings.TrimSpace(state.CreatedAt)
	state.UpdatedAt = strings.TrimSpace(state.UpdatedAt)

	if !state.Registered {
		return mngAuthState{}, nil
	}
	if state.Username == "" || state.PasswordHash == "" {
		return mngAuthState{}, errors.New("invalid mng_auth data in main store")
	}

	return state, nil
}

func persistMngAuthState(state mngAuthState) error {
	if Store == nil {
		return errors.New("store is not initialized")
	}

	Store.mu.Lock()
	Store.Data[mngAuthStoreField] = state
	Store.mu.Unlock()

	return Store.Save()
}

func (m *mngAuthManager) registered() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.Registered
}

func (m *mngAuthManager) register(username, password, confirmPassword string) error {
	username = normalizeUsername(username)
	if username == "" {
		return &mngHTTPError{Status: http.StatusBadRequest, Message: "username is required"}
	}
	if len([]rune(username)) > mngMaxUsernameLength {
		return &mngHTTPError{Status: http.StatusBadRequest, Message: "username is too long"}
	}
	if strings.TrimSpace(password) == "" {
		return &mngHTTPError{Status: http.StatusBadRequest, Message: "password is required"}
	}
	if len(password) < mngMinPasswordLength {
		return &mngHTTPError{Status: http.StatusBadRequest, Message: "password is too short"}
	}
	if len(password) > mngMaxPasswordLength {
		return &mngHTTPError{Status: http.StatusBadRequest, Message: "password is too long"}
	}
	if password != confirmPassword {
		return &mngHTTPError{Status: http.StatusBadRequest, Message: "password confirmation does not match"}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	nowRFC3339 := time.Now().UTC().Format(time.RFC3339)
	newState := mngAuthState{
		Registered:   true,
		Username:     username,
		PasswordHash: string(hash),
		CreatedAt:    nowRFC3339,
		UpdatedAt:    nowRFC3339,
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Registered {
		return &mngHTTPError{Status: http.StatusForbidden, Message: "registration is closed"}
	}
	if err := persistMngAuthState(newState); err != nil {
		return err
	}
	m.state = newState
	m.sessions = make(map[string]mngSessionState)
	m.loginFailed = make(map[string]int)
	m.loginFrozenTill = make(map[string]time.Time)
	return nil
}

func (m *mngAuthManager) login(ip, username, password string) (string, mngSessionState, error) {
	username = normalizeUsername(username)
	if username == "" || strings.TrimSpace(password) == "" {
		return "", mngSessionState{}, &mngHTTPError{Status: http.StatusBadRequest, Message: "username and password are required"}
	}

	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupExpiredLocked(now)

	if !m.state.Registered {
		return "", mngSessionState{}, &mngHTTPError{Status: http.StatusForbidden, Message: "account is not registered"}
	}

	if freezeUntil, blocked := m.loginFrozenTill[ip]; blocked && now.Before(freezeUntil) {
		return "", mngSessionState{}, &mngHTTPError{Status: http.StatusTooManyRequests, Message: "too many failed attempts, try again later"}
	}

	if username != m.state.Username || bcrypt.CompareHashAndPassword([]byte(m.state.PasswordHash), []byte(password)) != nil {
		failed := m.loginFailed[ip] + 1
		if failed >= mngLoginFailThreshold {
			m.loginFailed[ip] = 0
			m.loginFrozenTill[ip] = now.Add(mngLoginFreezeDuration)
			return "", mngSessionState{}, &mngHTTPError{Status: http.StatusTooManyRequests, Message: "too many failed attempts, try again later"}
		}
		m.loginFailed[ip] = failed
		return "", mngSessionState{}, &mngHTTPError{Status: http.StatusUnauthorized, Message: "invalid username or password"}
	}

	token, err := randomToken(32)
	if err != nil {
		return "", mngSessionState{}, err
	}
	state := mngSessionState{
		Username:  m.state.Username,
		ExpiresAt: now.Add(mngSessionTTL),
	}
	m.sessions[token] = state
	delete(m.loginFailed, ip)
	delete(m.loginFrozenTill, ip)
	return token, state, nil
}

func (m *mngAuthManager) sessionByToken(token string) (mngSessionState, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return mngSessionState{}, false
	}

	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupExpiredLocked(now)

	session, ok := m.sessions[token]
	if !ok {
		return mngSessionState{}, false
	}
	if now.After(session.ExpiresAt) {
		delete(m.sessions, token)
		return mngSessionState{}, false
	}
	return session, true
}

func (m *mngAuthManager) logoutToken(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}

	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

func (m *mngAuthManager) cleanupExpiredLocked(now time.Time) {
	for token, session := range m.sessions {
		if now.After(session.ExpiresAt) {
			delete(m.sessions, token)
		}
	}
	for ip, until := range m.loginFrozenTill {
		if now.After(until) {
			delete(m.loginFrozenTill, ip)
		}
	}
}

func extractMngSessionToken(r *http.Request) (string, error) {
	cookie, err := r.Cookie(mngSessionCookieName)
	if err != nil {
		return "", errors.New("missing mng session")
	}
	token := strings.TrimSpace(cookie.Value)
	if token == "" {
		return "", errors.New("missing mng session")
	}
	return token, nil
}

func currentMngSessionFromRequest(r *http.Request) (mngSessionState, string, error) {
	mgr, err := ensureMngAuthManager()
	if err != nil {
		return mngSessionState{}, "", err
	}

	token, err := extractMngSessionToken(r)
	if err != nil {
		return mngSessionState{}, "", err
	}

	session, ok := mgr.sessionByToken(token)
	if !ok {
		return mngSessionState{}, "", errors.New("invalid or expired mng session")
	}
	return session, token, nil
}

func setMngSessionCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     mngSessionCookieName,
		Value:    token,
		Path:     "/mng",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isHTTPSRequest(r),
		MaxAge:   maxAge,
		Expires:  expiresAt,
	})
}

func clearMngSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     mngSessionCookieName,
		Value:    "",
		Path:     "/mng",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   isHTTPSRequest(r),
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

func writeMngError(w http.ResponseWriter, err error) {
	if httpErr, ok := err.(*mngHTTPError); ok {
		writeJSON(w, httpErr.Status, map[string]string{"error": httpErr.Message})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
}

func ResetMngAuthManagerForTest() {
	mngAuthInitMu.Lock()
	mngAuthInstance = nil
	mngAuthInitMu.Unlock()
}
