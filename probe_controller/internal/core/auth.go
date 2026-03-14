package core

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type BlacklistData struct {
	CIDRs []string `json:"cidrs"`
}

type BlacklistStore struct {
	mu    sync.RWMutex
	path  string
	cidrs map[string]struct{}
}

type AuthManager struct {
	mu sync.RWMutex

	adminKeyHash  string
	adminPlainKey string

	nonces             map[string]time.Time
	sessions           map[string]time.Time
	nonceRequestFailed map[string]int

	blacklist *BlacklistStore
}

type LoginRequest struct {
	Nonce string `json:"nonce"`
	HMAC  string `json:"hmac"`
}

var authManager *AuthManager

func InitBlacklistStore(path string) (*BlacklistStore, error) {
	store := &BlacklistStore{
		path:  path,
		cidrs: make(map[string]struct{}),
	}

	if _, err := os.Stat(path); err == nil {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		if len(raw) > 0 {
			var data BlacklistData
			if unmarshalErr := json.Unmarshal(raw, &data); unmarshalErr != nil {
				return nil, unmarshalErr
			}
			for _, c := range data.CIDRs {
				store.cidrs[c] = struct{}{}
			}
		}
		return store, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	if err := store.persistLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func (b *BlacklistStore) HasCIDR(cidr string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.cidrs[cidr]
	return ok
}

func (b *BlacklistStore) AddCIDR(cidr string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, exists := b.cidrs[cidr]; exists {
		return nil
	}
	b.cidrs[cidr] = struct{}{}
	return b.persistLocked()
}

func (b *BlacklistStore) persistLocked() error {
	cidrs := make([]string, 0, len(b.cidrs))
	for c := range b.cidrs {
		cidrs = append(cidrs, c)
	}
	payload := BlacklistData{CIDRs: cidrs}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(b.path, raw, 0o644)
}

func initAuth() {
	blacklistPath := filepath.Join(dataDir, blacklistStoreFile)
	blacklist, err := InitBlacklistStore(blacklistPath)
	if err != nil {
		log.Fatalf("failed to initialize blacklist store: %v", err)
	}

	authManager = &AuthManager{
		nonces:             make(map[string]time.Time),
		sessions:           make(map[string]time.Time),
		nonceRequestFailed: make(map[string]int),
		blacklist:          blacklist,
	}

	Store.mu.Lock()
	hashVal, _ := Store.Data["admin_key_hash"].(string)
	if strings.TrimSpace(hashVal) == "" {
		plain, genErr := randomHex(32)
		if genErr != nil {
			Store.mu.Unlock()
			log.Fatalf("failed to generate initial admin key: %v", genErr)
		}
		hashed, hashErr := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
		if hashErr != nil {
			Store.mu.Unlock()
			log.Fatalf("failed to hash initial admin key: %v", hashErr)
		}
		hashVal = string(hashed)
		Store.Data["admin_key_hash"] = hashVal
		Store.mu.Unlock()

		if saveErr := Store.Save(); saveErr != nil {
			log.Fatalf("failed to save admin key hash: %v", saveErr)
		}
		if writeErr := os.WriteFile(filepath.Join(dataDir, initialKeyLogFile), []byte(plain+"\n"), 0o600); writeErr != nil {
			log.Fatalf("failed to write initial key log: %v", writeErr)
		}

		authManager.mu.Lock()
		authManager.adminKeyHash = hashVal
		authManager.adminPlainKey = plain
		authManager.mu.Unlock()
		log.Printf("initial admin key generated and written to %s", filepath.Join(dataDir, initialKeyLogFile))
	} else {
		Store.mu.Unlock()
		authManager.mu.Lock()
		authManager.adminKeyHash = hashVal
		authManager.mu.Unlock()
	}

	if envKey := strings.TrimSpace(os.Getenv("CLOUDHELPER_ADMIN_KEY")); envKey != "" {
		if bcrypt.CompareHashAndPassword([]byte(hashVal), []byte(envKey)) == nil {
			authManager.mu.Lock()
			authManager.adminPlainKey = envKey
			authManager.mu.Unlock()
		} else {
			log.Println("warning: CLOUDHELPER_ADMIN_KEY does not match stored admin_key_hash; challenge-response remains unavailable")
		}
	}

	authManager.mu.RLock()
	hasPlain := strings.TrimSpace(authManager.adminPlainKey) != ""
	authManager.mu.RUnlock()
	if !hasPlain {
		if fileKey := tryLoadInitialKeyFromFile(hashVal); fileKey != "" {
			authManager.mu.Lock()
			authManager.adminPlainKey = fileKey
			authManager.mu.Unlock()
		}
	}

	if !IsChallengeResponseReady() {
		log.Println("warning: challenge-response is not ready because no valid plaintext admin key is loaded")
	}

	go authManager.gcLoop()
}

func (a *AuthManager) gcLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		a.mu.Lock()
		for n, exp := range a.nonces {
			if now.After(exp) {
				delete(a.nonces, n)
			}
		}
		for t, exp := range a.sessions {
			if now.After(exp) {
				delete(a.sessions, t)
			}
		}
		a.mu.Unlock()
	}
}

func NonceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ip, addr := getClientIP(r)
	cidr := cidrForAddress(addr)

	if authManager.blacklist.HasCIDR(cidr) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "cidr is blacklisted",
			"cidr":  cidr,
		})
		return
	}

	authManager.mu.Lock()
	authManager.nonceRequestFailed[ip]++
	failed := authManager.nonceRequestFailed[ip]
	authManager.mu.Unlock()

	if failed >= nonceRequestLimit {
		if err := authManager.blacklist.AddCIDR(cidr); err != nil {
			log.Printf("failed to persist blacklist cidr %s: %v", cidr, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to persist blacklist"})
			return
		}
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "too many nonce requests, cidr is now blacklisted",
			"cidr":  cidr,
		})
		return
	}

	nonce, err := randomHex(32)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate nonce"})
		return
	}

	expiresAt := time.Now().Add(nonceTTL)
	authManager.mu.Lock()
	authManager.nonces[nonce] = expiresAt
	authManager.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"nonce":      nonce,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	})
}

func LoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !IsChallengeResponseReady() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "challenge-response unavailable: admin secret is not loaded",
		})
		return
	}

	_, addr := getClientIP(r)
	cidr := cidrForAddress(addr)
	if authManager.blacklist.HasCIDR(cidr) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "cidr is blacklisted",
			"cidr":  cidr,
		})
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}
	req.Nonce = strings.TrimSpace(req.Nonce)
	req.HMAC = strings.TrimSpace(req.HMAC)
	if req.Nonce == "" || req.HMAC == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nonce and hmac are required"})
		return
	}

	if err := ValidateAndConsumeNonce(req.Nonce); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	if !verifyLoginCredential(req.Nonce, req.HMAC) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credential"})
		return
	}

	token, err := randomToken(32)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create session token"})
		return
	}

	ip, _ := getClientIP(r)
	expiresAt := time.Now().Add(sessionTTL)
	authManager.mu.Lock()
	authManager.sessions[token] = expiresAt
	authManager.nonceRequestFailed[ip] = 0
	authManager.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_token": token,
		"ttl":           int(sessionTTL.Seconds()),
	})
}

func AdminStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token, err := extractBearerToken(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	if !IsTokenValid(token) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired session token"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "ok",
		"uptime":      int(time.Since(serverStartTime).Seconds()),
		"server_time": time.Now().UTC().Format(time.RFC3339),
	})
}

func ValidateAndConsumeNonce(nonce string) error {
	authManager.mu.Lock()
	defer authManager.mu.Unlock()

	exp, ok := authManager.nonces[nonce]
	if !ok {
		return errors.New("nonce not found")
	}
	delete(authManager.nonces, nonce)
	if time.Now().After(exp) {
		return errors.New("nonce expired")
	}
	return nil
}

func verifyLoginCredential(nonce, incomingHMAC string) bool {
	authManager.mu.RLock()
	adminPlain := authManager.adminPlainKey
	authManager.mu.RUnlock()

	if adminPlain != "" {
		expected := CalcHMACSHA256Hex(nonce, adminPlain)
		return hmac.Equal([]byte(strings.ToLower(incomingHMAC)), []byte(strings.ToLower(expected)))
	}
	return false
}

func IsTokenValid(token string) bool {
	authManager.mu.RLock()
	defer authManager.mu.RUnlock()
	exp, ok := authManager.sessions[token]
	return ok && time.Now().Before(exp)
}

func extractBearerToken(r *http.Request) (string, error) {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader == "" {
		return "", errors.New("missing authorization header")
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", errors.New("invalid authorization header")
	}
	return strings.TrimSpace(parts[1]), nil
}

func CalcHMACSHA256Hex(message, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func randomHex(byteLen int) (string, error) {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func randomToken(byteLen int) (string, error) {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func getClientIP(r *http.Request) (string, netip.Addr) {
	if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cf != "" {
		if addr, err := netip.ParseAddr(cf); err == nil {
			return addr.String(), addr
		}
	}

	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		items := strings.Split(xff, ",")
		if len(items) > 0 {
			candidate := strings.TrimSpace(items[0])
			if addr, err := netip.ParseAddr(candidate); err == nil {
				return addr.String(), addr
			}
		}
	}

	if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
		if addr, err := netip.ParseAddr(xrip); err == nil {
			return addr.String(), addr
		}
	}

	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		return addr.String(), addr
	}

	unknown := netip.MustParseAddr("0.0.0.0")
	return "0.0.0.0", unknown
}

func cidrForAddress(addr netip.Addr) string {
	if addr.Is4() {
		as4 := addr.As4()
		return strings.Join([]string{
			toDec(as4[0]),
			toDec(as4[1]),
			"0.0/16",
		}, ".")
	}
	if addr.Is6() {
		pfx := netip.PrefixFrom(addr, 64).Masked()
		return pfx.String()
	}
	return "0.0.0.0/16"
}

func toDec(b byte) string {
	return strconv.Itoa(int(b))
}

func IsChallengeResponseReady() bool {
	if authManager == nil {
		return false
	}
	authManager.mu.RLock()
	defer authManager.mu.RUnlock()
	return strings.TrimSpace(authManager.adminPlainKey) != ""
}

func tryLoadInitialKeyFromFile(adminKeyHash string) string {
	keyPath := filepath.Join(dataDir, initialKeyLogFile)
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("warning: failed to read %s: %v", keyPath, err)
		}
		return ""
	}

	candidate := strings.TrimSpace(string(raw))
	if candidate == "" {
		log.Printf("warning: %s is empty, challenge-response key not loaded", keyPath)
		return ""
	}
	if bcrypt.CompareHashAndPassword([]byte(adminKeyHash), []byte(candidate)) != nil {
		log.Printf("warning: key in %s does not match admin_key_hash", keyPath)
		return ""
	}
	log.Printf("challenge-response key loaded from %s", keyPath)
	return candidate
}

func NewAuthManagerForTest(blacklistPath string) (*AuthManager, error) {
	bl, err := InitBlacklistStore(blacklistPath)
	if err != nil {
		return nil, err
	}
	return &AuthManager{
		nonces:             make(map[string]time.Time),
		sessions:           make(map[string]time.Time),
		nonceRequestFailed: make(map[string]int),
		blacklist:          bl,
	}, nil
}

func SetAuthManagerForTest(mgr *AuthManager) {
	authManager = mgr
}

func (a *AuthManager) AddNonceForTest(nonce string, expiresAt time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nonces[nonce] = expiresAt
}

func (a *AuthManager) AddSessionForTest(token string, expiresAt time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions[token] = expiresAt
}

func (a *AuthManager) SetAdminPlainKeyForTest(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.adminPlainKey = key
}

func (a *AuthManager) HasCIDRForTest(cidr string) bool {
	if a.blacklist == nil {
		return false
	}
	return a.blacklist.HasCIDR(cidr)
}
