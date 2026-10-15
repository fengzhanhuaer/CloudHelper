//go:build ignore

// Development-only peer using the controller's existing WebSocket/yamux libraries.
// Compile from probe_controller; this helper never enters router firmware.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
)

type wsConn struct {
	*websocket.Conn
	reader io.Reader
}

func (c *wsConn) Read(b []byte) (int, error) {
	for {
		if c.reader == nil {
			_, r, e := c.NextReader()
			if e != nil {
				return 0, e
			}
			c.reader = r
		}
		n, e := c.reader.Read(b)
		if e == io.EOF {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, e
	}
}
func (c *wsConn) Write(b []byte) (int, error) {
	if e := c.WriteMessage(websocket.BinaryMessage, b); e != nil {
		return 0, e
	}
	return len(b), nil
}
func (c *wsConn) SetDeadline(t time.Time) error {
	if e := c.SetReadDeadline(t); e != nil {
		return e
	}
	return c.SetWriteDeadline(t)
}

func main() {
	if os.Getenv("CHVR_DISPOSABLE_TEST") != "1" {
		log.Fatal("run only in a disposable test container with CHVR_DISPOSABLE_TEST=1")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		log.Fatal("disposable Docker container required")
	}
	if len(os.Args) != 2 {
		log.Fatal("usage: fixture C-test-executable")
	}
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(e)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "router isolated test"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	must(e)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, e := x509.MarshalECPrivateKey(key)
	must(e)
	cert, e := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	must(e)
	// Only this disposable container's CA bundle is changed.
	must(os.WriteFile("/etc/ssl/certs/ca-certificates.crt", certPEM, 0600))
	ln, e := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	must(e)
	defer ln.Close()
	var reports, authenticated, refreshed atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/probe/auth/challenge", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("node_id") != "7" {
			http.Error(w, "node", 403)
			return
		}
		io.WriteString(w, `{"challenge":"bounded-fixture-challenge"}`)
	})
	auth := func(r *http.Request) bool {
		mac := hmac.New(sha256.New, []byte("fixture-only-key"))
		fmt.Fprintf(mac, "7\nbounded-fixture-challenge\n%s\n%s", r.Method, r.URL.Path)
		return r.Header.Get("X-Probe-Node-Id") == "7" && r.Header.Get("X-Probe-Rand") == "bounded-fixture-challenge" && hmac.Equal([]byte(r.Header.Get("X-Probe-Signature")), []byte(hex.EncodeToString(mac.Sum(nil))))
	}
	mux.HandleFunc("/api/probe/route/config", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "auth", 403)
			return
		}
		refreshed.Add(1)
		io.WriteString(w, `{"revision":2}`)
	})
	mux.HandleFunc("/api/probe", func(w http.ResponseWriter, r *http.Request) {
		if !auth(r) {
			http.Error(w, "auth", 403)
			return
		}
		authenticated.Add(1)
		u := websocket.Upgrader{}
		ws, e := u.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer ws.Close()
		cfg := yamux.DefaultConfig()
		cfg.KeepAliveInterval = time.Second
		session, e := yamux.Server(&wsConn{Conn: ws}, cfg)
		if e != nil {
			return
		}
		defer session.Close()
		stream, e := session.Accept()
		if e != nil {
			return
		}
		defer stream.Close()
		stream.SetDeadline(time.Now().Add(10 * time.Second))
		decoder, encoder := json.NewDecoder(stream), json.NewEncoder(stream)
		for i := 0; i < 2; i++ {
			var report map[string]interface{}
			if e = decoder.Decode(&report); e != nil {
				return
			}
			if report["type"] != "report" || report["build_kind"] != "asus_56uv2" || report["node_id"] != "7" {
				return
			}
			reports.Add(1)
			if e = encoder.Encode(map[string]string{"type": "ack"}); e != nil {
				return
			}
			if i == 0 {
				time.Sleep(1200 * time.Millisecond)
				if _, e = session.Ping(); e != nil {
					return
				}
				if e = encoder.Encode(map[string]string{"type": "report_once"}); e != nil {
					return
				}
			}
		}
		encoder.Encode(map[string]string{"type": "route_config_sync"})
		var b [1]byte
		stream.Read(b[:])
	})
	server := http.Server{Handler: mux}
	go server.Serve(ln)
	defer server.Close()
	cmd := exec.Command(os.Args[1], "https://"+ln.Addr().String())
	cmd.Env = append(os.Environ(), "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	must(cmd.Run())
	if authenticated.Load() != 1 || reports.Load() != 2 || refreshed.Load() != 1 {
		log.Fatalf("bad protocol result auth=%d reports=%d refresh=%d", authenticated.Load(), reports.Load(), refreshed.Load())
	}
	fmt.Println("PASS: existing Go WebSocket/yamux accepted C peer and keepalive")
}
func must(e error) {
	if e != nil {
		log.Fatal(e)
	}
}
