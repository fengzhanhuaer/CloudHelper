package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type controllerDirectEndpoint struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
	Pin  string `json:"tls_spki_sha256"`
}
type controllerDirectDiscovery struct {
	Version   int                        `json:"version"`
	ExpiresAt int64                      `json:"expires_at"`
	Endpoints []controllerDirectEndpoint `json:"endpoints"`
}

var controllerDirectState = struct {
	sync.RWMutex
	endpoints []controllerDirectEndpoint
}{}

type authenticatedDirectProbeKey struct{}

func withAuthenticatedDirectProbe(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, authenticatedDirectProbeKey{}, id)
}

// This listener has no management handler, regardless of Host or proxy headers.
func probeOnlyDirectHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/probe", "/api/probe/auth/challenge", "/api/probe/route/config", "/api/probe/route/settings",
			"/api/probe/route/fake_ip/resolve", "/api/probe/route/fake_ip/renew", "/api/probe/info_box",
			"/api/probe/certificate", "/api/probe/proxy/github/latest", "/api/probe/proxy/download":
		default:
			http.NotFound(w, r)
			return
		}
		// Business handlers retain their original node signature/nonce checks.
		// Challenge issuance is the only unauthenticated bootstrap operation.
		if r.URL.Path != "/api/probe/auth/challenge" {
			id, err := authenticateProbeRequest(r)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
				return
			}
			r = r.WithContext(withAuthenticatedDirectProbe(r.Context(), id))
		}
		next.ServeHTTP(w, r)
	})
}

func ProbeControllerEndpointsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", 405)
		return
	}
	if _, err := authenticateProbeRequest(r); err != nil {
		writeJSON(w, 401, map[string]string{"error": err.Error()})
		return
	}
	controllerDirectState.RLock()
	items := append([]controllerDirectEndpoint{}, controllerDirectState.endpoints...)
	controllerDirectState.RUnlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, controllerDirectDiscovery{1, time.Now().Add(30 * 24 * time.Hour).Unix(), items})
}

func controllerDirectCertificate(path string) (tls.Certificate, string, error) {
	bytes, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			return tls.Certificate{}, "", e
		}
		serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if e != nil {
			return tls.Certificate{}, "", e
		}
		now := time.Now()
		template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "CloudHelper probe controller"}, NotBefore: now.Add(-24 * time.Hour), NotAfter: now.AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if e != nil {
			return tls.Certificate{}, "", e
		}
		private, e := x509.MarshalPKCS8PrivateKey(key)
		if e != nil {
			return tls.Certificate{}, "", e
		}
		bytes = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})...)
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return tls.Certificate{}, "", e
		}
		// Never replace an existing identity, including during concurrent startup.
		f, e := os.CreateTemp(filepath.Dir(path), ".controller-identity-*")
		if e == nil {
			defer os.Remove(f.Name())
			_, e = f.Write(bytes)
			if e == nil {
				e = f.Sync()
			}
			closeErr := f.Close()
			if e == nil {
				e = closeErr
			}
			if e == nil {
				e = os.Link(f.Name(), path)
				if errors.Is(e, os.ErrExist) {
					bytes, e = os.ReadFile(path)
				}
			}
		}
		if e != nil {
			return tls.Certificate{}, "", e
		}
	} else if err != nil {
		return tls.Certificate{}, "", err
	}
	cert, err := tls.X509KeyPair(bytes, bytes)
	if err != nil {
		return cert, "", err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return cert, "", err
	}
	if !time.Now().Before(leaf.NotAfter) {
		return cert, "", errors.New("controller direct certificate expired")
	}
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return cert, hex.EncodeToString(sum[:]), nil
}

func controllerDirectPublicIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() {
		return false
	}
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32"} {
		_, block, _ := net.ParseCIDR(cidr)
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

func startControllerDirectListeners(handler http.Handler) error {
	if os.Getenv("CLOUDHELPER_PROBE_DIRECT_DISABLED") == "1" {
		return nil
	}
	var local []string
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return err
	}
	for _, a := range addresses {
		ip, _, e := net.ParseCIDR(a.String())
		if e == nil && controllerDirectPublicIP(ip) {
			local = append(local, ip.String())
		}
	}
	advertised := local
	// Prefer IPv4 endpoints so older IPv4-only router runtimes can use the list.
	sort.SliceStable(advertised, func(i, j int) bool {
		return net.ParseIP(advertised[i]).To4() != nil && net.ParseIP(advertised[j]).To4() == nil
	})
	if value := strings.TrimSpace(os.Getenv("CLOUDHELPER_PROBE_DIRECT_LISTEN_IPS")); value != "" {
		local = nil
		for _, raw := range strings.Split(value, ",") {
			ip := net.ParseIP(strings.TrimSpace(raw))
			if ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() {
				return errors.New("invalid direct listen interface IP")
			}
			local = append(local, ip.String())
		}
	}
	if value := strings.TrimSpace(os.Getenv("CLOUDHELPER_PROBE_DIRECT_IPS")); value != "" {
		advertised = nil
		for _, s := range strings.Split(value, ",") {
			ip := net.ParseIP(strings.TrimSpace(s))
			if !controllerDirectPublicIP(ip) {
				return fmt.Errorf("invalid public direct IP")
			}
			advertised = append(advertised, ip.String())
		}
	}
	if len(local) == 0 || len(advertised) == 0 {
		return nil
	}
	cert, pin, err := controllerDirectCertificate(filepath.Join(dataDir, "controller-direct-identity.pem"))
	if err != nil {
		return err
	}
	var listeners []net.Listener
	for _, ip := range local {
		l, e := net.Listen("tcp", net.JoinHostPort(ip, "15030"))
		if e != nil {
			for _, v := range listeners {
				v.Close()
			}
			return e
		}
		listeners = append(listeners, l)
	}
	items := []controllerDirectEndpoint{}
	for _, ip := range advertised {
		if len(items) == 4 {
			break
		}
		items = append(items, controllerDirectEndpoint{ip, 15030, pin})
	}
	for _, l := range listeners {
		server := &http.Server{Handler: probeOnlyDirectHandler(handler), ReadHeaderTimeout: 8 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 * 1024, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}}
		go func(l net.Listener) {
			if e := server.ServeTLS(l, "", ""); e != nil && !errors.Is(e, http.ErrServerClosed) {
				logControllerErrorf("probe direct TLS listener stopped: %v", e)
			}
		}(l)
	}
	controllerDirectState.Lock()
	controllerDirectState.endpoints = items
	controllerDirectState.Unlock()
	logControllerInfof("Probe-only direct TLS active: port=15030 addresses=%d", len(items))
	return nil
}
