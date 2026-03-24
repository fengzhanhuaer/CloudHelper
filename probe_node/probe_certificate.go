package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	probeTLSCertFile       = "probe_tls.crt.pem"
	probeTLSKeyFile        = "probe_tls.key.pem"
	probeTLSMetaFile       = "probe_tls.meta.json"
	probeTLSMinValidity    = 5 * time.Minute
	probeTLSPullTimeout    = 8 * time.Minute
	probeCertificateAPIURI = "/api/probe/certificate"
)

type probeControllerCertificateResponse struct {
	NodeID    string `json:"node_id"`
	Domain    string `json:"domain"`
	CertPEM   string `json:"cert_pem"`
	KeyPEM    string `json:"key_pem"`
	NotBefore string `json:"not_before"`
	NotAfter  string `json:"not_after"`
	RenewedAt string `json:"renewed_at"`
}

type probeServerCertificate struct {
	CertPath string
	KeyPath  string
	Domain   string
	NotAfter time.Time
}

type probeLocalCertificateMeta struct {
	NodeID    string `json:"node_id"`
	Domain    string `json:"domain"`
	NotBefore string `json:"not_before"`
	NotAfter  string `json:"not_after"`
	RenewedAt string `json:"renewed_at"`
}

func prepareProbeServerCertificate(identity nodeIdentity, controllerBaseURL string) (probeServerCertificate, error) {
	dataDir, err := resolveDataDir()
	if err != nil {
		return probeServerCertificate{}, err
	}

	certPath := filepath.Join(dataDir, probeTLSCertFile)
	keyPath := filepath.Join(dataDir, probeTLSKeyFile)
	metaPath := filepath.Join(dataDir, probeTLSMetaFile)

	if existing, ok := loadLocalProbeServerCertificate(certPath, keyPath, metaPath); ok {
		return existing, nil
	}

	controllerBaseURL = strings.TrimSpace(controllerBaseURL)
	if controllerBaseURL == "" {
		return probeServerCertificate{}, fmt.Errorf("controller base url is required to pull probe tls certificate")
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTLSPullTimeout)
	defer cancel()
	response, err := pullProbeCertificateFromController(ctx, controllerBaseURL, identity)
	if err != nil {
		return probeServerCertificate{}, err
	}

	leaf, parseErr := parseProbeCertificateLeaf([]byte(response.CertPEM), []byte(response.KeyPEM))
	if parseErr != nil {
		return probeServerCertificate{}, parseErr
	}
	if !isProbeCertificateUsable(leaf.NotAfter.UTC(), probeTLSMinValidity) {
		return probeServerCertificate{}, fmt.Errorf("controller returned unusable probe certificate: expires=%s", leaf.NotAfter.UTC().Format(time.RFC3339))
	}

	if err := os.WriteFile(certPath, []byte(response.CertPEM), 0o600); err != nil {
		return probeServerCertificate{}, err
	}
	if err := os.WriteFile(keyPath, []byte(response.KeyPEM), 0o600); err != nil {
		return probeServerCertificate{}, err
	}

	meta := probeLocalCertificateMeta{
		NodeID:    strings.TrimSpace(response.NodeID),
		Domain:    strings.TrimSpace(strings.ToLower(response.Domain)),
		NotBefore: leaf.NotBefore.UTC().Format(time.RFC3339),
		NotAfter:  leaf.NotAfter.UTC().Format(time.RFC3339),
		RenewedAt: strings.TrimSpace(response.RenewedAt),
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return probeServerCertificate{}, err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(metaPath, raw, 0o600); err != nil {
		return probeServerCertificate{}, err
	}

	log.Printf("probe tls certificate pulled from controller: node_id=%s domain=%s expires=%s", identity.NodeID, strings.TrimSpace(response.Domain), leaf.NotAfter.UTC().Format(time.RFC3339))
	return probeServerCertificate{
		CertPath: certPath,
		KeyPath:  keyPath,
		Domain:   strings.TrimSpace(strings.ToLower(response.Domain)),
		NotAfter: leaf.NotAfter.UTC(),
	}, nil
}

func loadLocalProbeServerCertificate(certPath string, keyPath string, metaPath string) (probeServerCertificate, bool) {
	certPEM, certErr := os.ReadFile(certPath)
	if certErr != nil {
		return probeServerCertificate{}, false
	}
	keyPEM, keyErr := os.ReadFile(keyPath)
	if keyErr != nil {
		return probeServerCertificate{}, false
	}

	leaf, parseErr := parseProbeCertificateLeaf(certPEM, keyPEM)
	if parseErr != nil {
		return probeServerCertificate{}, false
	}
	if !isProbeCertificateUsable(leaf.NotAfter.UTC(), probeTLSMinValidity) {
		return probeServerCertificate{}, false
	}

	domain := ""
	if raw, readErr := os.ReadFile(metaPath); readErr == nil {
		var meta probeLocalCertificateMeta
		if jsonErr := json.Unmarshal(raw, &meta); jsonErr == nil {
			domain = strings.TrimSpace(strings.ToLower(meta.Domain))
		}
	}
	if domain == "" && len(leaf.DNSNames) > 0 {
		domain = strings.TrimSpace(strings.ToLower(leaf.DNSNames[0]))
	}
	if domain == "" {
		domain = strings.TrimSpace(strings.ToLower(leaf.Subject.CommonName))
	}

	return probeServerCertificate{
		CertPath: certPath,
		KeyPath:  keyPath,
		Domain:   domain,
		NotAfter: leaf.NotAfter.UTC(),
	}, true
}

func pullProbeCertificateFromController(ctx context.Context, controllerBaseURL string, identity nodeIdentity) (probeControllerCertificateResponse, error) {
	requestURL := strings.TrimRight(strings.TrimSpace(controllerBaseURL), "/") + probeCertificateAPIURI
	log.Printf("probe tls certificate pull request: %s", safeURLForLog(requestURL))

	body, err := probeAuthedGet(ctx, requestURL, identity)
	if err != nil {
		return probeControllerCertificateResponse{}, err
	}

	var response probeControllerCertificateResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return probeControllerCertificateResponse{}, err
	}
	if strings.TrimSpace(response.CertPEM) == "" || strings.TrimSpace(response.KeyPEM) == "" {
		return probeControllerCertificateResponse{}, errors.New("controller returned empty certificate payload")
	}
	return response, nil
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

func resolveProbeControllerBaseURL(explicitControllerURL string, explicitWSURL string) string {
	rawController := firstNonEmpty(strings.TrimSpace(explicitControllerURL), strings.TrimSpace(os.Getenv("PROBE_CONTROLLER_URL")))
	if u, ok := normalizeControllerBaseURL(rawController); ok {
		return u
	}

	rawWS := firstNonEmpty(strings.TrimSpace(explicitWSURL), strings.TrimSpace(os.Getenv("PROBE_CONTROLLER_WS")))
	wsURL, err := url.Parse(rawWS)
	if err != nil || wsURL == nil {
		return ""
	}
	scheme := strings.ToLower(strings.TrimSpace(wsURL.Scheme))
	if scheme == "wss" {
		wsURL.Scheme = "https"
	} else if scheme == "ws" {
		wsURL.Scheme = "http"
	} else {
		return ""
	}
	wsURL.Path = ""
	wsURL.RawPath = ""
	wsURL.RawQuery = ""
	wsURL.Fragment = ""
	return strings.TrimRight(wsURL.String(), "/")
}

func normalizeControllerBaseURL(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false
	}
	u, err := url.Parse(value)
	if err != nil || u == nil {
		return "", false
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", false
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), true
}
