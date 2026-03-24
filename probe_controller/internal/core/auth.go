package core

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	adminPublicKeyStoreField = "admin_public_key"
	adminUsernameStoreField  = "admin_username"
	adminRoleStoreField      = "admin_user_role"
	adminCertTypeStoreField  = "admin_cert_type"
	rootCACertFile           = "root_ca.crt.pem"
	rootCAKeyFile            = "root_ca.key.pem"
	adminPublicKeyFile       = "admin_public_key.pem"
	adminPrivateKeyFile      = "initial_admin_private_key.pem"
	adminCertFile            = "admin_key.crt.pem"
	defaultUsername          = "admin"
	defaultUserRole          = "admin"
	defaultCertType          = "admin"

	// Long-term certificate validity (years).
	rootCAValidityYears = 100
	adminCertYears      = 100
)

var (
	certUsernameExtensionOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 0}
	certRoleExtensionOID     = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}
	certTypeExtensionOID     = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 2}
	usernameOUKeyPrefix      = "user:"
	roleOUKeyPrefix          = "role:"
	certTypeOUKeyPrefix      = "type:"
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

	adminPublicKey ed25519.PublicKey
	username       string
	userRole       string
	certType       string

	nonces             map[string]time.Time
	sessions           map[string]time.Time
	nonceRequestFailed map[string]int

	blacklist *BlacklistStore
}

type LoginRequest struct {
	Nonce     string `json:"nonce"`
	Signature string `json:"signature,omitempty"`
}

type probeLinkUserIdentity struct {
	Username string `json:"username"`
	UserRole string `json:"user_role"`
	CertType string `json:"cert_type"`
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

	caCert, caKey, err := loadOrCreateRootCA()
	if err != nil {
		log.Fatalf("failed to initialize root ca: %v", err)
	}

	adminPub, username, userRole, certType, err := loadOrCreateAdminIdentity(caCert, caKey)
	if err != nil {
		log.Fatalf("failed to initialize admin key identity: %v", err)
	}

	authManager.mu.Lock()
	authManager.adminPublicKey = adminPub
	authManager.username = normalizeUsername(username)
	authManager.userRole = normalizeRole(userRole)
	authManager.certType = normalizeCertType(certType)
	authManager.mu.Unlock()

	if !IsChallengeResponseReady() {
		log.Println("warning: challenge-response is not ready because admin public key is missing")
	}

	go authManager.gcLoop()
}

func loadOrCreateRootCA() (*x509.Certificate, crypto.Signer, error) {
	certPath := filepath.Join(dataDir, rootCACertFile)
	keyPath := filepath.Join(dataDir, rootCAKeyFile)

	certExists := fileExists(certPath)
	keyExists := fileExists(keyPath)
	if certExists && keyExists {
		return loadRootCA(certPath, keyPath)
	}

	if certExists != keyExists {
		return nil, nil, errors.New("root ca files are inconsistent: cert/key must both exist or both be absent")
	}

	return createRootCA(certPath, keyPath)
}

func loadRootCA(certPath, keyPath string) (*x509.Certificate, crypto.Signer, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, errors.New("failed to decode root ca certificate pem")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}

	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, errors.New("failed to decode root ca private key pem")
	}

	var keyAny interface{}
	keyAny, err = x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		if ecKey, ecErr := x509.ParseECPrivateKey(keyBlock.Bytes); ecErr == nil {
			keyAny = ecKey
		} else {
			return nil, nil, err
		}
	}

	signer, ok := keyAny.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("root ca private key is not a signer")
	}
	if !cert.IsCA {
		return nil, nil, errors.New("loaded root certificate is not a ca")
	}

	return cert, signer, nil
}

func createRootCA(certPath, keyPath string) (*x509.Certificate, crypto.Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serial, err := randomSerialNumber()
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "CloudHelper Root CA",
			Organization: []string{"CloudHelper"},
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(rootCAValidityYears, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, nil, err
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, nil, err
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, err
	}

	log.Printf("root ca generated: cert=%s key=%s", certPath, keyPath)
	return cert, key, nil
}

func loadOrCreateAdminIdentity(caCert *x509.Certificate, caKey crypto.Signer) (ed25519.PublicKey, string, string, string, error) {
	Store.mu.RLock()
	raw, _ := Store.Data[adminPublicKeyStoreField].(string)
	storedUsername, _ := Store.Data[adminUsernameStoreField].(string)
	storedRole, _ := Store.Data[adminRoleStoreField].(string)
	storedCertType, _ := Store.Data[adminCertTypeStoreField].(string)
	Store.mu.RUnlock()

	if strings.TrimSpace(raw) != "" {
		pub, err := decodeAdminPublicKey(raw)
		if err != nil {
			return nil, "", "", "", err
		}
		if err := writeAdminPublicKeyFile(pub); err != nil {
			return nil, "", "", "", err
		}

		username, role, certType := loadIdentityClaims(storedUsername, storedRole, storedCertType)
		if err := ensureAdminCertificateFile(pub, caCert, caKey, username, role, certType); err != nil {
			return nil, "", "", "", err
		}
		if err := persistIdentityClaims(username, role, certType); err != nil {
			return nil, "", "", "", err
		}
		return pub, username, role, certType, nil
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", "", "", err
	}

	username := defaultUsername
	role := defaultUserRole
	certType := defaultCertType

	encodedPub := base64.StdEncoding.EncodeToString(pub)
	Store.mu.Lock()
	Store.Data[adminPublicKeyStoreField] = encodedPub
	Store.Data[adminUsernameStoreField] = username
	Store.Data[adminRoleStoreField] = role
	Store.Data[adminCertTypeStoreField] = certType
	delete(Store.Data, "admin_key_hash")
	Store.mu.Unlock()

	if err := Store.Save(); err != nil {
		return nil, "", "", "", err
	}
	if err := writeAdminPublicKeyFile(pub); err != nil {
		return nil, "", "", "", err
	}
	if err := writeAdminPrivateKeyFile(priv); err != nil {
		return nil, "", "", "", err
	}
	if err := writeAdminCertificateFile(pub, caCert, caKey, username, role, certType); err != nil {
		return nil, "", "", "", err
	}

	log.Printf("admin key pair generated. public=%s private=%s cert=%s", filepath.Join(dataDir, adminPublicKeyFile), filepath.Join(dataDir, adminPrivateKeyFile), filepath.Join(dataDir, adminCertFile))
	return pub, username, role, certType, nil
}

func loadIdentityClaims(storedUsername, storedRole, storedCertType string) (string, string, string) {
	username := normalizeUsername(storedUsername)
	role := normalizeRole(storedRole)
	certType := normalizeCertType(storedCertType)
	if username != "" && role != "" && certType != "" {
		return username, role, certType
	}

	certUsername, certRole, certTypeClaim, err := readIdentityClaimsFromCertificate(filepath.Join(dataDir, adminCertFile))
	if err == nil {
		if username == "" {
			username = normalizeUsername(certUsername)
		}
		if role == "" {
			role = normalizeRole(certRole)
		}
		if certType == "" {
			certType = normalizeCertType(certTypeClaim)
		}
	}

	if username == "" {
		username = defaultUsername
	}
	if role == "" {
		role = defaultUserRole
	}
	if certType == "" {
		certType = defaultCertType
	}
	return username, role, certType
}

func persistIdentityClaims(username, role, certType string) error {
	username = normalizeUsername(username)
	role = normalizeRole(role)
	certType = normalizeCertType(certType)
	if username == "" {
		username = defaultUsername
	}
	if role == "" {
		role = defaultUserRole
	}
	if certType == "" {
		certType = defaultCertType
	}

	Store.mu.Lock()
	Store.Data[adminUsernameStoreField] = username
	Store.Data[adminRoleStoreField] = role
	Store.Data[adminCertTypeStoreField] = certType
	Store.mu.Unlock()
	return Store.Save()
}

func ensureAdminCertificateFile(pub ed25519.PublicKey, caCert *x509.Certificate, caKey crypto.Signer, username, role, certType string) error {
	certPath := filepath.Join(dataDir, adminCertFile)
	if fileExists(certPath) {
		return nil
	}
	return writeAdminCertificateFile(pub, caCert, caKey, username, role, certType)
}

func readIdentityClaimsFromCertificate(certPath string) (string, string, string, error) {
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return "", "", "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", "", "", errors.New("failed to decode admin certificate pem")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", "", "", err
	}
	username, role, certType := extractIdentityClaimsFromCertificate(cert)
	return username, role, certType, nil
}

func extractIdentityClaimsFromCertificate(cert *x509.Certificate) (string, string, string) {
	username := ""
	role := ""
	certType := ""

	for _, ext := range cert.Extensions {
		if ext.Id.Equal(certUsernameExtensionOID) {
			var v string
			if _, err := asn1.Unmarshal(ext.Value, &v); err == nil {
				username = normalizeUsername(v)
			}
		}
		if ext.Id.Equal(certRoleExtensionOID) {
			var v string
			if _, err := asn1.Unmarshal(ext.Value, &v); err == nil {
				role = normalizeRole(v)
			}
		}
		if ext.Id.Equal(certTypeExtensionOID) {
			var v string
			if _, err := asn1.Unmarshal(ext.Value, &v); err == nil {
				certType = normalizeCertType(v)
			}
		}
	}

	for _, ou := range cert.Subject.OrganizationalUnit {
		itemRaw := strings.TrimSpace(ou)
		itemLower := strings.ToLower(itemRaw)

		if strings.HasPrefix(itemLower, usernameOUKeyPrefix) && username == "" {
			username = normalizeUsername(strings.TrimSpace(itemRaw[len(usernameOUKeyPrefix):]))
		}
		if strings.HasPrefix(itemLower, roleOUKeyPrefix) && role == "" {
			role = normalizeRole(strings.TrimSpace(itemRaw[len(roleOUKeyPrefix):]))
		}
		if strings.HasPrefix(itemLower, certTypeOUKeyPrefix) && certType == "" {
			certType = normalizeCertType(strings.TrimSpace(itemRaw[len(certTypeOUKeyPrefix):]))
		}
	}

	return username, role, certType
}

func normalizeUsername(v string) string {
	return strings.TrimSpace(v)
}

func normalizeRole(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func normalizeCertType(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func decodeAdminPublicKey(v string) (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
	if err != nil {
		return nil, err
	}
	if len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("invalid admin public key size")
	}
	pub := make(ed25519.PublicKey, ed25519.PublicKeySize)
	copy(pub, decoded)
	return pub, nil
}

func writeAdminPublicKeyFile(pub ed25519.PublicKey) error {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return os.WriteFile(filepath.Join(dataDir, adminPublicKeyFile), pemBytes, 0o644)
}

func writeAdminPrivateKeyFile(priv ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	privatePath := filepath.Join(dataDir, adminPrivateKeyFile)
	return os.WriteFile(privatePath, pemBytes, 0o600)
}

func writeAdminCertificateFile(pub ed25519.PublicKey, caCert *x509.Certificate, caKey crypto.Signer, username, role, certType string) error {
	serial, err := randomSerialNumber()
	if err != nil {
		return err
	}

	username = normalizeUsername(username)
	role = normalizeRole(role)
	certType = normalizeCertType(certType)
	if username == "" {
		username = defaultUsername
	}
	if role == "" {
		role = defaultUserRole
	}
	if certType == "" {
		certType = defaultCertType
	}

	usernameValue, err := asn1.Marshal(username)
	if err != nil {
		return err
	}
	roleValue, err := asn1.Marshal(role)
	if err != nil {
		return err
	}
	certTypeValue, err := asn1.Marshal(certType)
	if err != nil {
		return err
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "CloudHelper Admin Key",
			Organization: []string{"CloudHelper"},
			OrganizationalUnit: []string{
				usernameOUKeyPrefix + username,
				roleOUKeyPrefix + role,
				certTypeOUKeyPrefix + certType,
			},
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(adminCertYears, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		ExtraExtensions: []pkix.Extension{
			{
				Id:    certUsernameExtensionOID,
				Value: usernameValue,
			},
			{
				Id:    certRoleExtensionOID,
				Value: roleValue,
			},
			{
				Id:    certTypeExtensionOID,
				Value: certTypeValue,
			},
		},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, pub, caKey)
	if err != nil {
		return err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return os.WriteFile(filepath.Join(dataDir, adminCertFile), certPEM, 0o644)
}

func randomSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	if serial.Sign() == 0 {
		return big.NewInt(1), nil
	}
	return serial, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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
			"error": "challenge-response unavailable: admin public key is not loaded",
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
	req.Signature = strings.TrimSpace(req.Signature)
	if req.Nonce == "" || req.Signature == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nonce and signature are required"})
		return
	}

	if err := ValidateAndConsumeNonce(req.Nonce); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	if !verifyLoginCredential(req.Nonce, req.Signature) {
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
	username, role, certType := currentIdentityClaims()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_token": token,
		"ttl":           int(sessionTTL.Seconds()),
		"username":      username,
		"user_role":     role,
		"cert_type":     certType,
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

func verifyLoginCredential(nonce, signature string) bool {
	authManager.mu.RLock()
	pub := make([]byte, len(authManager.adminPublicKey))
	copy(pub, authManager.adminPublicKey)
	authManager.mu.RUnlock()

	if len(pub) != ed25519.PublicKeySize {
		return false
	}

	sigBytes, ok := decodeLoginSignature(signature)
	if !ok || len(sigBytes) != ed25519.SignatureSize {
		return false
	}

	return ed25519.Verify(ed25519.PublicKey(pub), []byte(nonce), sigBytes)
}

func decodeLoginSignature(v string) ([]byte, bool) {
	candidate := strings.TrimSpace(v)
	if candidate == "" {
		return nil, false
	}

	if b, err := base64.StdEncoding.DecodeString(candidate); err == nil {
		return b, true
	}
	if b, err := base64.RawStdEncoding.DecodeString(candidate); err == nil {
		return b, true
	}
	if b, err := base64.RawURLEncoding.DecodeString(candidate); err == nil {
		return b, true
	}
	if b, err := hex.DecodeString(candidate); err == nil {
		return b, true
	}
	return nil, false
}

func currentIdentityClaims() (string, string, string) {
	if authManager == nil {
		return defaultUsername, defaultUserRole, defaultCertType
	}
	authManager.mu.RLock()
	defer authManager.mu.RUnlock()

	username := normalizeUsername(authManager.username)
	role := normalizeRole(authManager.userRole)
	certType := normalizeCertType(authManager.certType)
	if username == "" {
		username = defaultUsername
	}
	if role == "" {
		role = defaultUserRole
	}
	if certType == "" {
		certType = defaultCertType
	}
	return username, role, certType
}

func listProbeLinkUserIdentities() []probeLinkUserIdentity {
	username, role, certType := currentIdentityClaims()
	username = normalizeUsername(username)
	if username == "" {
		return []probeLinkUserIdentity{}
	}
	return []probeLinkUserIdentity{
		{
			Username: username,
			UserRole: normalizeRole(role),
			CertType: normalizeCertType(certType),
		},
	}
}

func currentAdminPublicKeyBase64() (string, error) {
	if authManager == nil {
		return "", errors.New("auth manager is not initialized")
	}
	authManager.mu.RLock()
	defer authManager.mu.RUnlock()
	if len(authManager.adminPublicKey) != ed25519.PublicKeySize {
		return "", errors.New("admin public key is not loaded")
	}
	der, err := x509.MarshalPKIXPublicKey(authManager.adminPublicKey)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

func resolveProbeLinkUserIdentityAndPublicKey(username string) (probeLinkUserIdentity, string, error) {
	identities := listProbeLinkUserIdentities()
	if len(identities) == 0 {
		return probeLinkUserIdentity{}, "", errors.New("no identity user available")
	}

	target := normalizeUsername(username)
	selected := probeLinkUserIdentity{}
	if target == "" {
		selected = identities[0]
	} else {
		for _, item := range identities {
			if normalizeUsername(item.Username) == target {
				selected = item
				break
			}
		}
		if strings.TrimSpace(selected.Username) == "" {
			return probeLinkUserIdentity{}, "", errors.New("user not found")
		}
	}

	pubKey, err := currentAdminPublicKeyBase64()
	if err != nil {
		return probeLinkUserIdentity{}, "", err
	}
	return selected, pubKey, nil
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
	return len(authManager.adminPublicKey) == ed25519.PublicKeySize
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
		username:           defaultUsername,
		userRole:           defaultUserRole,
		certType:           defaultCertType,
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

func (a *AuthManager) SetAdminPublicKeyForTest(pub ed25519.PublicKey) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.adminPublicKey = make(ed25519.PublicKey, len(pub))
	copy(a.adminPublicKey, pub)
}

func (a *AuthManager) HasCIDRForTest(cidr string) bool {
	if a.blacklist == nil {
		return false
	}
	return a.blacklist.HasCIDR(cidr)
}
