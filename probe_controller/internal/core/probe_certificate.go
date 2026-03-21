package core

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
)

const (
	probeCertificateDirEnv               = "PROBE_CERT_DIR"
	probeCertificateDefaultDir           = "/data/probe"
	probeCertificateACMEDirectoryEnv     = "PROBE_CERT_ACME_DIRECTORY_URL"
	probeCertificateACMEDefaultDirectory = "https://acme-v02.api.letsencrypt.org/directory"
	probeCertificateACMEEmailEnv         = "PROBE_CERT_ACME_EMAIL"
	probeCertificateRenewBefore          = 30 * 24 * time.Hour
	probeCertificateMinServeValidity     = 5 * time.Minute
	probeCertificateRenewLoopInterval    = 6 * time.Hour
	probeCertificateInitialRenewDelay    = 20 * time.Second
	probeCertificateDNSWaitSecEnv        = "PROBE_CERT_DNS_PROPAGATION_WAIT_SEC"
	probeCertificateDefaultDNSWait       = 20 * time.Second
)

type probeCertificateResponse struct {
	NodeID    string `json:"node_id"`
	Domain    string `json:"domain"`
	CertPEM   string `json:"cert_pem"`
	KeyPEM    string `json:"key_pem"`
	NotBefore string `json:"not_before"`
	NotAfter  string `json:"not_after"`
	RenewedAt string `json:"renewed_at,omitempty"`
}

type probeCertificateMeta struct {
	NodeID    string `json:"node_id"`
	Domain    string `json:"domain"`
	NotBefore string `json:"not_before"`
	NotAfter  string `json:"not_after"`
	RenewedAt string `json:"renewed_at"`
}

type probeIssuedCertificate struct {
	NodeID    string
	Domain    string
	CertPEM   []byte
	KeyPEM    []byte
	NotBefore time.Time
	NotAfter  time.Time
	RenewedAt time.Time
}

type probeCertificateManager struct {
	baseDir          string
	acmeDirectoryURL string
	acmeContactEmail string
	dnsWait          time.Duration
	renewBefore      time.Duration
	minServeValidity time.Duration

	acmeMu    sync.Mutex
	nodeLocks sync.Map // map[nodeID]*sync.Mutex
}

var probeCertificateManagerState = struct {
	once sync.Once
	mgr  *probeCertificateManager
	err  error
}{}

func initProbeCertificateManager() {
	_, _ = getProbeCertificateManager()
}

func getProbeCertificateManager() (*probeCertificateManager, error) {
	probeCertificateManagerState.once.Do(func() {
		mgr, err := newProbeCertificateManager()
		if err != nil {
			probeCertificateManagerState.err = err
			log.Printf("warning: probe certificate manager disabled: %v", err)
			return
		}
		probeCertificateManagerState.mgr = mgr
		mgr.startRenewLoop()
		log.Printf("probe certificate manager started: base_dir=%s", mgr.baseDir)
	})
	return probeCertificateManagerState.mgr, probeCertificateManagerState.err
}

func newProbeCertificateManager() (*probeCertificateManager, error) {
	baseDir := resolveProbeCertificateBaseDir()
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		fallback := filepath.Join(dataDir, "probe")
		if mkErr := os.MkdirAll(fallback, 0o700); mkErr != nil {
			return nil, fmt.Errorf("create probe certificate dir failed: %w", err)
		}
		log.Printf("warning: failed to use %s (%v), fallback to %s", baseDir, err, fallback)
		baseDir = fallback
	}

	acmeDirectoryURL := strings.TrimSpace(os.Getenv(probeCertificateACMEDirectoryEnv))
	if acmeDirectoryURL == "" {
		acmeDirectoryURL = probeCertificateACMEDefaultDirectory
	}
	acmeEmail := strings.TrimSpace(os.Getenv(probeCertificateACMEEmailEnv))
	dnsWait := time.Duration(parsePositiveIntEnv(probeCertificateDNSWaitSecEnv, int(probeCertificateDefaultDNSWait/time.Second))) * time.Second
	if dnsWait <= 0 {
		dnsWait = probeCertificateDefaultDNSWait
	}

	return &probeCertificateManager{
		baseDir:          baseDir,
		acmeDirectoryURL: acmeDirectoryURL,
		acmeContactEmail: acmeEmail,
		dnsWait:          dnsWait,
		renewBefore:      probeCertificateRenewBefore,
		minServeValidity: probeCertificateMinServeValidity,
	}, nil
}

func resolveProbeCertificateBaseDir() string {
	if v := strings.TrimSpace(os.Getenv(probeCertificateDirEnv)); v != "" {
		return v
	}
	return probeCertificateDefaultDir
}

func (m *probeCertificateManager) startRenewLoop() {
	go func() {
		timer := time.NewTimer(probeCertificateInitialRenewDelay)
		defer timer.Stop()

		for {
			<-timer.C
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
			m.ensureAllNodeCertificates(ctx)
			cancel()
			timer.Reset(probeCertificateRenewLoopInterval)
		}
	}()
}

func (m *probeCertificateManager) ensureAllNodeCertificates(ctx context.Context) {
	if ProbeStore == nil {
		return
	}

	ProbeStore.mu.RLock()
	nodes := loadProbeNodesLocked()
	ProbeStore.mu.RUnlock()

	for _, node := range nodes {
		select {
		case <-ctx.Done():
			return
		default:
		}

		nodeID := normalizeProbeNodeID(fmt.Sprintf("%d", node.NodeNo))
		if nodeID == "" {
			continue
		}

		if _, err := m.EnsureNodeCertificate(ctx, nodeID); err != nil {
			log.Printf("probe certificate auto-renew failed: node_id=%s err=%v", nodeID, err)
		}
	}
}

func (m *probeCertificateManager) EnsureNodeCertificate(ctx context.Context, nodeID string) (probeIssuedCertificate, error) {
	normalizedNodeID := normalizeProbeNodeID(nodeID)
	if normalizedNodeID == "" {
		return probeIssuedCertificate{}, fmt.Errorf("node_id is required")
	}

	domain, err := resolveProbeNodeCertificateDomain(normalizedNodeID)
	if err != nil {
		return probeIssuedCertificate{}, err
	}

	lock := m.nodeMutex(normalizedNodeID)
	lock.Lock()
	defer lock.Unlock()

	existing, existingErr := m.readStoredNodeCertificate(normalizedNodeID)
	hasUsableExisting := existingErr == nil &&
		strings.EqualFold(strings.TrimSpace(existing.Domain), strings.TrimSpace(domain)) &&
		isProbeCertificateUsable(existing.NotAfter, m.minServeValidity)

	if hasUsableExisting && time.Until(existing.NotAfter) > m.renewBefore {
		return existing, nil
	}

	issued, issueErr := m.issueAndPersistNodeCertificate(ctx, normalizedNodeID, domain)
	if issueErr != nil {
		if hasUsableExisting {
			log.Printf("warning: probe certificate renew failed, keep existing cert: node_id=%s domain=%s err=%v", normalizedNodeID, domain, issueErr)
			return existing, nil
		}
		return probeIssuedCertificate{}, issueErr
	}
	return issued, nil
}

func (m *probeCertificateManager) issueAndPersistNodeCertificate(ctx context.Context, nodeID string, domain string) (probeIssuedCertificate, error) {
	certPEM, keyPEM, notBefore, notAfter, err := m.issueCertificateWithDNS01(ctx, domain)
	if err != nil {
		return probeIssuedCertificate{}, err
	}
	renewedAt := time.Now().UTC()
	issued := probeIssuedCertificate{
		NodeID:    nodeID,
		Domain:    domain,
		CertPEM:   certPEM,
		KeyPEM:    keyPEM,
		NotBefore: notBefore.UTC(),
		NotAfter:  notAfter.UTC(),
		RenewedAt: renewedAt,
	}
	if err := m.writeStoredNodeCertificate(issued); err != nil {
		return probeIssuedCertificate{}, err
	}
	log.Printf("probe certificate ready: node_id=%s domain=%s expires=%s", nodeID, domain, issued.NotAfter.Format(time.RFC3339))
	return issued, nil
}

func (m *probeCertificateManager) readStoredNodeCertificate(nodeID string) (probeIssuedCertificate, error) {
	_, certPath, keyPath, metaPath := m.nodePaths(nodeID)

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return probeIssuedCertificate{}, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return probeIssuedCertificate{}, err
	}

	leaf, err := parseProbeCertificateLeaf(certPEM, keyPEM)
	if err != nil {
		return probeIssuedCertificate{}, err
	}

	meta := probeCertificateMeta{}
	if raw, readErr := os.ReadFile(metaPath); readErr == nil {
		_ = json.Unmarshal(raw, &meta)
	}

	domain := strings.TrimSpace(strings.ToLower(meta.Domain))
	if domain == "" && len(leaf.DNSNames) > 0 {
		domain = strings.TrimSpace(strings.ToLower(leaf.DNSNames[0]))
	}
	if domain == "" {
		domain = strings.TrimSpace(strings.ToLower(leaf.Subject.CommonName))
	}

	renewedAt := time.Time{}
	if strings.TrimSpace(meta.RenewedAt) != "" {
		if ts, tsErr := time.Parse(time.RFC3339, strings.TrimSpace(meta.RenewedAt)); tsErr == nil {
			renewedAt = ts.UTC()
		}
	}
	if renewedAt.IsZero() {
		if st, statErr := os.Stat(certPath); statErr == nil {
			renewedAt = st.ModTime().UTC()
		}
	}

	return probeIssuedCertificate{
		NodeID:    normalizeProbeNodeID(nodeID),
		Domain:    domain,
		CertPEM:   certPEM,
		KeyPEM:    keyPEM,
		NotBefore: leaf.NotBefore.UTC(),
		NotAfter:  leaf.NotAfter.UTC(),
		RenewedAt: renewedAt,
	}, nil
}

func (m *probeCertificateManager) writeStoredNodeCertificate(cert probeIssuedCertificate) error {
	nodeDir, certPath, keyPath, metaPath := m.nodePaths(cert.NodeID)
	if err := os.MkdirAll(nodeDir, 0o700); err != nil {
		return err
	}

	if err := os.WriteFile(certPath, cert.CertPEM, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, cert.KeyPEM, 0o600); err != nil {
		return err
	}

	meta := probeCertificateMeta{
		NodeID:    cert.NodeID,
		Domain:    strings.TrimSpace(strings.ToLower(cert.Domain)),
		NotBefore: cert.NotBefore.UTC().Format(time.RFC3339),
		NotAfter:  cert.NotAfter.UTC().Format(time.RFC3339),
		RenewedAt: cert.RenewedAt.UTC().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(metaPath, raw, 0o600); err != nil {
		return err
	}
	return nil
}

func (m *probeCertificateManager) nodePaths(nodeID string) (string, string, string, string) {
	safeID := sanitizeProbeCertificateNodeID(nodeID)
	nodeDir := filepath.Join(m.baseDir, safeID)
	certPath := filepath.Join(nodeDir, "tls.crt.pem")
	keyPath := filepath.Join(nodeDir, "tls.key.pem")
	metaPath := filepath.Join(nodeDir, "tls.meta.json")
	return nodeDir, certPath, keyPath, metaPath
}

func (m *probeCertificateManager) nodeMutex(nodeID string) *sync.Mutex {
	normalized := normalizeProbeNodeID(nodeID)
	if normalized == "" {
		normalized = sanitizeProbeCertificateNodeID(nodeID)
	}
	lock, _ := m.nodeLocks.LoadOrStore(normalized, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func sanitizeProbeCertificateNodeID(nodeID string) string {
	value := strings.TrimSpace(strings.ToLower(nodeID))
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func (m *probeCertificateManager) issueCertificateWithDNS01(ctx context.Context, domain string) ([]byte, []byte, time.Time, time.Time, error) {
	token, zoneID, err := resolveCloudflareCertificateConfig()
	if err != nil {
		return nil, nil, time.Time{}, time.Time{}, err
	}

	accountKey, err := m.loadOrCreateACMEAccountKey()
	if err != nil {
		return nil, nil, time.Time{}, time.Time{}, err
	}

	client := &acme.Client{
		Key:          accountKey,
		DirectoryURL: m.acmeDirectoryURL,
	}
	acct := &acme.Account{}
	if m.acmeContactEmail != "" {
		acct.Contact = []string{"mailto:" + m.acmeContactEmail}
	}
	if _, err := client.Register(ctx, acct, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, nil, time.Time{}, time.Time{}, fmt.Errorf("acme register failed: %w", err)
	}

	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(domain))
	if err != nil {
		return nil, nil, time.Time{}, time.Time{}, fmt.Errorf("acme authorize order failed: %w", err)
	}

	txtRecordIDs := make([]string, 0, 2)
	defer func() {
		for _, recordID := range txtRecordIDs {
			if deleteErr := cloudflareDeleteDNSRecord(token, zoneID, recordID); deleteErr != nil {
				log.Printf("warning: cleanup acme txt record failed: id=%s err=%v", strings.TrimSpace(recordID), deleteErr)
			}
		}
	}()

	for _, authzURL := range order.AuthzURLs {
		authz, authzErr := client.GetAuthorization(ctx, authzURL)
		if authzErr != nil {
			return nil, nil, time.Time{}, time.Time{}, fmt.Errorf("acme get authorization failed: %w", authzErr)
		}
		if authz.Status == acme.StatusValid {
			continue
		}

		chal, challengeErr := selectDNS01Challenge(authz)
		if challengeErr != nil {
			return nil, nil, time.Time{}, time.Time{}, challengeErr
		}

		recordValue, recordErr := client.DNS01ChallengeRecord(chal.Token)
		if recordErr != nil {
			return nil, nil, time.Time{}, time.Time{}, fmt.Errorf("acme dns challenge record failed: %w", recordErr)
		}
		recordName := "_acme-challenge." + strings.TrimSpace(strings.ToLower(domain))
		recordID, createErr := cloudflareCreateDNSRecord(token, zoneID, recordName, "TXT", recordValue)
		if createErr != nil {
			return nil, nil, time.Time{}, time.Time{}, fmt.Errorf("create dns txt record failed: %w", createErr)
		}
		txtRecordIDs = append(txtRecordIDs, recordID)

		if m.dnsWait > 0 {
			waitTimer := time.NewTimer(m.dnsWait)
			select {
			case <-ctx.Done():
				waitTimer.Stop()
				return nil, nil, time.Time{}, time.Time{}, ctx.Err()
			case <-waitTimer.C:
			}
		}

		if _, acceptErr := client.Accept(ctx, chal); acceptErr != nil {
			return nil, nil, time.Time{}, time.Time{}, fmt.Errorf("acme accept challenge failed: %w", acceptErr)
		}
		if _, waitErr := client.WaitAuthorization(ctx, authz.URI); waitErr != nil {
			return nil, nil, time.Time{}, time.Time{}, fmt.Errorf("acme wait authorization failed: %w", waitErr)
		}
	}

	if _, err := client.WaitOrder(ctx, order.URI); err != nil {
		return nil, nil, time.Time{}, time.Time{}, fmt.Errorf("acme wait order failed: %w", err)
	}

	csrDER, certKey, err := createProbeCertificateCSR(domain)
	if err != nil {
		return nil, nil, time.Time{}, time.Time{}, err
	}
	derChain, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csrDER, true)
	if err != nil {
		return nil, nil, time.Time{}, time.Time{}, fmt.Errorf("acme create cert failed: %w", err)
	}
	if len(derChain) == 0 {
		return nil, nil, time.Time{}, time.Time{}, errors.New("acme returned empty certificate chain")
	}

	var certBuf bytes.Buffer
	for _, der := range derChain {
		if pemErr := pem.Encode(&certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); pemErr != nil {
			return nil, nil, time.Time{}, time.Time{}, pemErr
		}
	}

	keyPEM, err := encodeProbeCertificatePrivateKey(certKey)
	if err != nil {
		return nil, nil, time.Time{}, time.Time{}, err
	}
	leaf, err := parseProbeCertificateLeaf(certBuf.Bytes(), keyPEM)
	if err != nil {
		return nil, nil, time.Time{}, time.Time{}, err
	}
	if verifyErr := leaf.VerifyHostname(domain); verifyErr != nil {
		return nil, nil, time.Time{}, time.Time{}, fmt.Errorf("certificate hostname mismatch: %w", verifyErr)
	}
	return certBuf.Bytes(), keyPEM, leaf.NotBefore.UTC(), leaf.NotAfter.UTC(), nil
}

func (m *probeCertificateManager) loadOrCreateACMEAccountKey() (crypto.Signer, error) {
	m.acmeMu.Lock()
	defer m.acmeMu.Unlock()

	path := filepath.Join(m.baseDir, "acme_account.key.pem")
	raw, err := os.ReadFile(path)
	if err == nil {
		return parseProbeCertificateSigner(raw)
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	keyPEM, err := encodeProbeCertificatePrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, keyPEM, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func resolveProbeNodeCertificateDomain(nodeID string) (string, error) {
	node, ok := getProbeNodeByID(nodeID)
	if !ok {
		return "", fmt.Errorf("probe node not found")
	}

	if host := normalizeProbeCertificateHost(node.PublicHost); strings.HasPrefix(host, "api.") {
		return host, nil
	}

	zoneName := normalizeCloudflareZoneName(getCloudflareZone().ZoneName)
	if zoneName == "" {
		return "", fmt.Errorf("cloudflare zone is not configured")
	}

	businessBase := buildCloudflareBusinessRecordBase(node.NodeNo)
	if businessBase == "" {
		return "", fmt.Errorf("failed to build api domain for node")
	}
	domain := strings.TrimSpace(strings.ToLower(buildCloudflareRecordName(businessBase, zoneName, 1)))
	if domain == "" {
		return "", fmt.Errorf("failed to resolve probe api domain")
	}
	if !strings.HasPrefix(domain, "api.") {
		return "", fmt.Errorf("probe api domain must start with api.")
	}
	return domain, nil
}

func normalizeProbeCertificateHost(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil && parsed != nil {
			value = strings.TrimSpace(strings.ToLower(parsed.Host))
		}
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.TrimSpace(strings.Trim(value, "[]"))
	value = strings.TrimSuffix(value, ".")
	return value
}

func resolveCloudflareCertificateConfig() (string, string, error) {
	if CloudflareStore == nil {
		return "", "", fmt.Errorf("cloudflare datastore is not initialized")
	}
	CloudflareStore.mu.RLock()
	token := strings.TrimSpace(CloudflareStore.data.APIToken)
	zoneName := normalizeCloudflareZoneName(CloudflareStore.data.ZoneName)
	CloudflareStore.mu.RUnlock()

	if token == "" {
		return "", "", fmt.Errorf("cloudflare api key is not configured")
	}
	if zoneName == "" {
		return "", "", fmt.Errorf("cloudflare zone is not configured")
	}

	zoneID, err := cloudflareResolveZoneID(token, zoneName)
	if err != nil {
		return "", "", err
	}
	return token, zoneID, nil
}

func parseProbeCertificateSigner(raw []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("invalid pem private key")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if signer, ok := key.(crypto.Signer); ok {
			return signer, nil
		}
		return nil, errors.New("pkcs8 private key is not signer")
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("unsupported private key format")
}

func createProbeCertificateCSR(domain string) ([]byte, crypto.Signer, error) {
	normalizedDomain := strings.TrimSpace(strings.ToLower(domain))
	if normalizedDomain == "" {
		return nil, nil, errors.New("domain is required")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	req := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName: normalizedDomain,
		},
		DNSNames: []string{normalizedDomain},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, req, key)
	if err != nil {
		return nil, nil, err
	}
	return csrDER, key, nil
}

func encodeProbeCertificatePrivateKey(signer crypto.Signer) ([]byte, error) {
	pkcs8, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), nil
}

func parseProbeCertificateLeaf(certPEM []byte, keyPEM []byte) (*x509.Certificate, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if len(pair.Certificate) == 0 {
		return nil, errors.New("certificate chain is empty")
	}
	return x509.ParseCertificate(pair.Certificate[0])
}

func isProbeCertificateUsable(notAfter time.Time, minRemain time.Duration) bool {
	if notAfter.IsZero() {
		return false
	}
	return time.Now().Add(minRemain).Before(notAfter)
}

func selectDNS01Challenge(authz *acme.Authorization) (*acme.Challenge, error) {
	if authz == nil {
		return nil, fmt.Errorf("authorization is nil")
	}
	for _, chal := range authz.Challenges {
		if chal != nil && strings.EqualFold(strings.TrimSpace(chal.Type), "dns-01") {
			return chal, nil
		}
	}
	return nil, fmt.Errorf("dns-01 challenge not offered for %s", strings.TrimSpace(authz.URI))
}

func ProbeCertificateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isHTTPSRequest(r) {
		writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "https is required"})
		return
	}

	nodeID, err := authenticateProbeRequestOrQuerySecret(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	mgr, mgrErr := getProbeCertificateManager()
	if mgrErr != nil || mgr == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "probe certificate manager is unavailable"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Minute)
	defer cancel()
	cert, certErr := mgr.EnsureNodeCertificate(ctx, nodeID)
	if certErr != nil {
		status := http.StatusBadGateway
		errText := strings.ToLower(strings.TrimSpace(certErr.Error()))
		if strings.Contains(errText, "probe node not found") {
			status = http.StatusNotFound
		} else if errors.Is(certErr, context.Canceled) || errors.Is(certErr, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		writeJSON(w, status, map[string]string{"error": certErr.Error()})
		return
	}

	writeJSON(w, http.StatusOK, probeCertificateResponse{
		NodeID:    cert.NodeID,
		Domain:    cert.Domain,
		CertPEM:   string(cert.CertPEM),
		KeyPEM:    string(cert.KeyPEM),
		NotBefore: cert.NotBefore.UTC().Format(time.RFC3339),
		NotAfter:  cert.NotAfter.UTC().Format(time.RFC3339),
		RenewedAt: cert.RenewedAt.UTC().Format(time.RFC3339),
	})
}
