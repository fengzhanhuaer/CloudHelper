package main

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
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
)

const (
	probeDDNSACMEDirectoryURL       = "https://acme-v02.api.letsencrypt.org/directory"
	probeDDNSCertificateRenewBefore = 30 * 24 * time.Hour
	probeDDNSDNSPropagationTimeout  = 3 * time.Minute
	probeDDNSDNSPropagationInterval = 3 * time.Second
	probeDDNSDNSLookupTimeout       = 5 * time.Second
	probeDDNSAccountKeyFileName     = "acme_account.key.pem"
	probeDDNSCertificateFileName    = "tls.crt.pem"
	probeDDNSPrivateKeyFileName     = "tls.key.pem"
	probeDDNSCertificateMetaName    = "tls.meta.json"
)

type probeDDNSCertificateMeta struct {
	Domains   []string `json:"domains"`
	NotBefore string   `json:"not_before"`
	NotAfter  string   `json:"not_after"`
	RenewedAt string   `json:"renewed_at"`
}

type probeDDNSIssuedCertificate struct {
	CertPEM   []byte
	KeyPEM    []byte
	Domains   []string
	NotBefore time.Time
	NotAfter  time.Time
	RenewedAt time.Time
}

var probeDDNSCertificateIssuer = issueProbeDDNSCertificate
var probeDDNSCertificateTXTLookup = lookupProbeDDNSPublicTXT
var probeDDNSCertificateDNSPropagationTimeout = probeDDNSDNSPropagationTimeout
var probeDDNSCertificateDNSPropagationInterval = probeDDNSDNSPropagationInterval

func ensureProbeDDNSCertificate(ctx context.Context) error {
	config, err := loadProbeDDNSConfig()
	if err != nil {
		return err
	}
	if !config.Enabled {
		return nil
	}
	domains := allProbeDDNSDomains(config)
	if len(domains) == 0 {
		return nil
	}
	if strings.TrimSpace(config.APIToken) == "" {
		return errors.New("cloudflare api token is required")
	}

	checkAt := time.Now().UTC()
	existing, existingErr := readProbeDDNSCertificate()
	hasUsableExisting := existingErr == nil && time.Now().Add(5*time.Minute).Before(existing.NotAfter)
	if hasUsableExisting && reflect.DeepEqual(existing.Domains, domains) && time.Until(existing.NotAfter) > probeDDNSCertificateRenewBefore {
		return updateProbeDDNSCertificateState(existing, checkAt, nil)
	}

	issued, issueErr := probeDDNSCertificateIssuer(ctx, config.APIToken, domains)
	if issueErr != nil {
		if hasUsableExisting {
			_ = updateProbeDDNSCertificateState(existing, checkAt, issueErr)
			return issueErr
		}
		_ = updateProbeDDNSState(func(state *probeDDNSState) {
			state.LastCertificateCheckAt = checkAt.Format(time.RFC3339)
			state.LastCertificateStatus = "failed"
			state.LastCertificateError = issueErr.Error()
		})
		return issueErr
	}
	if err := writeProbeDDNSCertificate(issued); err != nil {
		return err
	}
	return updateProbeDDNSCertificateState(issued, checkAt, nil)
}

func updateProbeDDNSCertificateState(cert probeDDNSIssuedCertificate, checkAt time.Time, certErr error) error {
	return updateProbeDDNSState(func(state *probeDDNSState) {
		state.LastCertificateCheckAt = checkAt.UTC().Format(time.RFC3339)
		state.CertificateDomains = append([]string{}, cert.Domains...)
		state.CertificateNotBefore = cert.NotBefore.UTC().Format(time.RFC3339)
		state.CertificateNotAfter = cert.NotAfter.UTC().Format(time.RFC3339)
		state.CertificateLastRenewedAt = cert.RenewedAt.UTC().Format(time.RFC3339)
		if certErr != nil {
			state.LastCertificateStatus = "failed"
			state.LastCertificateError = certErr.Error()
		} else {
			state.LastCertificateStatus = "success"
			state.LastCertificateError = ""
		}
	})
}

func issueProbeDDNSCertificate(ctx context.Context, token string, domains []string) (probeDDNSIssuedCertificate, error) {
	domains, err := normalizeProbeDDNSDomains(domains)
	if err != nil || len(domains) == 0 {
		if err == nil {
			err = errors.New("at least one certificate domain is required")
		}
		return probeDDNSIssuedCertificate{}, err
	}
	zones, err := listProbeDDNSCloudflareZones(ctx, token)
	if err != nil {
		return probeDDNSIssuedCertificate{}, err
	}
	accountKey, err := loadOrCreateProbeDDNSACMEAccountKey()
	if err != nil {
		return probeDDNSIssuedCertificate{}, err
	}
	client := &acme.Client{Key: accountKey, DirectoryURL: probeDDNSACMEDirectoryURL}
	if _, err := client.Register(ctx, &acme.Account{}, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return probeDDNSIssuedCertificate{}, fmt.Errorf("acme register: %w", err)
	}
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(domains...))
	if err != nil {
		return probeDDNSIssuedCertificate{}, fmt.Errorf("acme authorize order: %w", err)
	}

	type challengeRecord struct{ zoneID, recordID string }
	created := []challengeRecord{}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, record := range created {
			if err := deleteProbeDDNSCloudflareRecord(cleanupCtx, token, record.zoneID, record.recordID); err != nil {
				logProbeWarnf("probe ddns acme txt cleanup failed: record_id=%s err=%v", record.recordID, err)
			}
		}
	}()

	for _, authzURL := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, authzURL)
		if err != nil {
			return probeDDNSIssuedCertificate{}, fmt.Errorf("acme get authorization: %w", err)
		}
		if authz.Status == acme.StatusValid {
			continue
		}
		challenge, err := selectProbeDDNSDNS01Challenge(authz)
		if err != nil {
			return probeDDNSIssuedCertificate{}, err
		}
		value, err := client.DNS01ChallengeRecord(challenge.Token)
		if err != nil {
			return probeDDNSIssuedCertificate{}, fmt.Errorf("acme dns challenge value: %w", err)
		}
		domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(authz.Identifier.Value)), ".")
		zone, err := matchProbeDDNSCloudflareZone(domain, zones)
		if err != nil {
			return probeDDNSIssuedCertificate{}, err
		}
		recordID, err := createProbeDDNSCloudflareRecord(ctx, token, zone.ID, "_acme-challenge."+domain, "TXT", value)
		if err != nil {
			return probeDDNSIssuedCertificate{}, fmt.Errorf("create acme txt record: %w", err)
		}
		created = append(created, challengeRecord{zoneID: zone.ID, recordID: recordID})
		recordName := "_acme-challenge." + domain
		if err := waitProbeDDNSCertificateTXT(ctx, recordName, value); err != nil {
			return probeDDNSIssuedCertificate{}, err
		}
		if _, err := client.Accept(ctx, challenge); err != nil {
			return probeDDNSIssuedCertificate{}, fmt.Errorf("acme accept challenge: %w", err)
		}
		if _, err := client.WaitAuthorization(ctx, authz.URI); err != nil {
			return probeDDNSIssuedCertificate{}, fmt.Errorf("acme wait authorization: %w", err)
		}
	}
	if _, err := client.WaitOrder(ctx, order.URI); err != nil {
		return probeDDNSIssuedCertificate{}, fmt.Errorf("acme wait order: %w", err)
	}
	csrDER, privateKey, err := createProbeDDNSCSR(domains)
	if err != nil {
		return probeDDNSIssuedCertificate{}, err
	}
	certDERs, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csrDER, true)
	if err != nil {
		return probeDDNSIssuedCertificate{}, fmt.Errorf("acme create certificate: %w", err)
	}
	if len(certDERs) == 0 {
		return probeDDNSIssuedCertificate{}, errors.New("acme returned an empty certificate chain")
	}
	var certBuffer bytes.Buffer
	for _, der := range certDERs {
		if err := pem.Encode(&certBuffer, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			return probeDDNSIssuedCertificate{}, err
		}
	}
	keyPEM, err := encodeProbeDDNSPrivateKey(privateKey)
	if err != nil {
		return probeDDNSIssuedCertificate{}, err
	}
	leaf, err := parseProbeDDNSCertificate(certBuffer.Bytes(), keyPEM)
	if err != nil {
		return probeDDNSIssuedCertificate{}, err
	}
	for _, domain := range domains {
		if err := leaf.VerifyHostname(domain); err != nil {
			return probeDDNSIssuedCertificate{}, fmt.Errorf("certificate does not cover %s: %w", domain, err)
		}
	}
	return probeDDNSIssuedCertificate{
		CertPEM: certBuffer.Bytes(), KeyPEM: keyPEM, Domains: domains,
		NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC(), RenewedAt: time.Now().UTC(),
	}, nil
}

func waitProbeDDNSCertificateTXT(ctx context.Context, recordName, expectedValue string) error {
	waitCtx, cancel := context.WithTimeout(ctx, probeDDNSCertificateDNSPropagationTimeout)
	defer cancel()

	var lastResult string
	for {
		values, err := probeDDNSCertificateTXTLookup(waitCtx, recordName)
		if err != nil {
			lastResult = "lookup error: " + err.Error()
		} else {
			lastResult = fmt.Sprintf("TXT values: %q", values)
			for _, value := range values {
				if normalizeProbeDDNSTXTValue(value) == normalizeProbeDDNSTXTValue(expectedValue) {
					return nil
				}
			}
		}

		timer := time.NewTimer(probeDDNSCertificateDNSPropagationInterval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return fmt.Errorf("acme dns challenge TXT propagation timeout for %s: expected %q; last %s: %w", recordName, expectedValue, lastResult, waitCtx.Err())
		case <-timer.C:
		}
	}
}

func normalizeProbeDDNSTXTValue(value string) string {
	return strings.Trim(strings.TrimSpace(value), `"`)
}

func lookupProbeDDNSPublicTXT(ctx context.Context, recordName string) ([]string, error) {
	type lookupResult struct {
		values []string
		err    error
	}
	resolvers := []*net.Resolver{
		net.DefaultResolver,
		newProbeDDNSResolver("1.1.1.1:53"),
		newProbeDDNSResolver("8.8.8.8:53"),
	}
	results := make(chan lookupResult, len(resolvers))
	for _, resolver := range resolvers {
		go func(resolver *net.Resolver) {
			lookupCtx, cancel := context.WithTimeout(ctx, probeDDNSDNSLookupTimeout)
			defer cancel()
			values, err := resolver.LookupTXT(lookupCtx, recordName)
			results <- lookupResult{values: values, err: err}
		}(resolver)
	}

	var values []string
	var lookupErrors []error
	for range resolvers {
		result := <-results
		values = append(values, result.values...)
		if result.err != nil {
			lookupErrors = append(lookupErrors, result.err)
		}
	}
	if len(values) > 0 || len(lookupErrors) < len(resolvers) {
		return values, nil
	}
	return nil, errors.Join(lookupErrors...)
}

func newProbeDDNSResolver(address string) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "udp", address)
		},
	}
}

func selectProbeDDNSDNS01Challenge(authz *acme.Authorization) (*acme.Challenge, error) {
	if authz == nil {
		return nil, errors.New("acme authorization is nil")
	}
	for _, challenge := range authz.Challenges {
		if challenge != nil && strings.EqualFold(challenge.Type, "dns-01") {
			return challenge, nil
		}
	}
	return nil, fmt.Errorf("dns-01 challenge is unavailable for %s", authz.Identifier.Value)
}

func loadOrCreateProbeDDNSACMEAccountKey() (crypto.Signer, error) {
	path, err := resolveProbeDDNSPath(probeDDNSAccountKeyFileName)
	if err != nil {
		return nil, err
	}
	if raw, err := os.ReadFile(path); err == nil {
		return parseProbeDDNSPrivateKey(raw)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	raw, err := encodeProbeDDNSPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := writeProbeDDNSSecretFile(path, raw); err != nil {
		return nil, err
	}
	return key, nil
}

func createProbeDDNSCSR(domains []string) ([]byte, crypto.Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	request := &x509.CertificateRequest{Subject: pkix.Name{CommonName: domains[0]}, DNSNames: append([]string{}, domains...)}
	der, err := x509.CreateCertificateRequest(rand.Reader, request, key)
	if err != nil {
		return nil, nil, err
	}
	return der, key, nil
}

func encodeProbeDDNSPrivateKey(key crypto.Signer) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

func parseProbeDDNSPrivateKey(raw []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("invalid private key pem")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("private key is not a signer")
	}
	return signer, nil
}

func parseProbeDDNSCertificate(certPEM, keyPEM []byte) (*x509.Certificate, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if len(pair.Certificate) == 0 {
		return nil, errors.New("certificate chain is empty")
	}
	return x509.ParseCertificate(pair.Certificate[0])
}

func readProbeDDNSCertificate() (probeDDNSIssuedCertificate, error) {
	certPath, err := resolveProbeDDNSPath(probeDDNSCertificateFileName)
	if err != nil {
		return probeDDNSIssuedCertificate{}, err
	}
	keyPath, _ := resolveProbeDDNSPath(probeDDNSPrivateKeyFileName)
	metaPath, _ := resolveProbeDDNSPath(probeDDNSCertificateMetaName)
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return probeDDNSIssuedCertificate{}, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return probeDDNSIssuedCertificate{}, err
	}
	leaf, err := parseProbeDDNSCertificate(certPEM, keyPEM)
	if err != nil {
		return probeDDNSIssuedCertificate{}, err
	}
	meta := probeDDNSCertificateMeta{}
	if raw, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(raw, &meta)
	}
	domains, err := normalizeProbeDDNSDomains(leaf.DNSNames)
	if err != nil {
		return probeDDNSIssuedCertificate{}, err
	}
	renewedAt, _ := time.Parse(time.RFC3339, meta.RenewedAt)
	if renewedAt.IsZero() {
		if info, err := os.Stat(certPath); err == nil {
			renewedAt = info.ModTime().UTC()
		}
	}
	return probeDDNSIssuedCertificate{CertPEM: certPEM, KeyPEM: keyPEM, Domains: domains, NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC(), RenewedAt: renewedAt.UTC()}, nil
}

func writeProbeDDNSCertificate(cert probeDDNSIssuedCertificate) error {
	domains, err := normalizeProbeDDNSDomains(cert.Domains)
	if err != nil {
		return err
	}
	certPath, err := resolveProbeDDNSPath(probeDDNSCertificateFileName)
	if err != nil {
		return err
	}
	keyPath, _ := resolveProbeDDNSPath(probeDDNSPrivateKeyFileName)
	metaPath, _ := resolveProbeDDNSPath(probeDDNSCertificateMetaName)
	if err := writeProbeDDNSSecretFile(certPath, cert.CertPEM); err != nil {
		return err
	}
	if err := writeProbeDDNSSecretFile(keyPath, cert.KeyPEM); err != nil {
		return err
	}
	meta := probeDDNSCertificateMeta{Domains: domains, NotBefore: cert.NotBefore.UTC().Format(time.RFC3339), NotAfter: cert.NotAfter.UTC().Format(time.RFC3339), RenewedAt: cert.RenewedAt.UTC().Format(time.RFC3339)}
	return persistProbeDDNSJSON(metaPath, meta)
}

func writeProbeDDNSSecretFile(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func sortedProbeDDNSDomains(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	return out
}
