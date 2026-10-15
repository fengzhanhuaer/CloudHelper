package dnsupstream

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func wire(t *testing.T) ([]byte, []byte) {
	t.Helper()
	name, _ := dnsmessage.NewName("example.com.")
	q := dnsmessage.Message{Header: dnsmessage.Header{ID: 123, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
	query, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	q.Header.Response = true
	q.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30}, Body: &dnsmessage.AResource{A: [4]byte{203, 0, 113, 1}}}}
	reply, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return query, reply
}

func fixtureCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("192.0.2.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func TestDoHDoTAndCertificateVerification(t *testing.T) {
	q, a := wire(t)
	cert, pool := fixtureCertificate(t)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/dns-message" || !reflect.DeepEqual(body, q) {
			t.Error("invalid DoH request")
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(a)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	defer server.Close()
	dot, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer dot.Close()
	go func() {
		for {
			conn, err := dot.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				var h [2]byte
				if _, err := io.ReadFull(conn, h[:]); err != nil {
					return
				}
				b := make([]byte, binary.BigEndian.Uint16(h[:]))
				if _, err := io.ReadFull(conn, b); err != nil {
					return
				}
				binary.BigEndian.PutUint16(h[:], uint16(len(a)))
				_, _ = conn.Write(append(h[:], a...))
			}()
		}
	}()
	r := Resolver{TLSConfig: &tls.Config{RootCAs: pool}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		target := server.Listener.Addr().String()
		if address == "192.0.2.1:853" {
			target = dot.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}}
	c := Config{"https://192.0.2.1/dns-query", "192.0.2.1:853", "192.0.2.53:53"}
	for _, candidate := range Candidates(c, nil)[:2] {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		reply, err := r.Query(ctx, candidate, q, c, nil)
		cancel()
		if err != nil || !reflect.DeepEqual(reply, a) {
			t.Fatalf("%s: %v", candidate.Kind, err)
		}
	}
	r.TLSConfig = nil
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = r.Query(ctx, Candidate{"doh", c.DoH}, q, c, nil); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	wrong := a[:]
	wrong = append([]byte(nil), wrong...)
	wrong[1]++
	if ValidateReply(q, wrong) == nil {
		t.Fatal("mismatched reply accepted")
	}
}

func TestFallbackToSystemAndUDPTruncation(t *testing.T) {
	q, a := wire(t)
	var targets []string
	r := Resolver{Timeout: time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		targets = append(targets, network+"|"+address)
		if address != "192.0.2.54:53" {
			return nil, errors.New("fixture unavailable")
		}
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			_ = server.SetDeadline(time.Now().Add(time.Second))
			if network == "udp" {
				b := make([]byte, 4096)
				_, _ = server.Read(b)
				truncated := append([]byte(nil), a...)
				truncated[2] |= 2
				_, _ = server.Write(truncated)
			} else {
				var h [2]byte
				_, _ = io.ReadFull(server, h[:])
				b := make([]byte, binary.BigEndian.Uint16(h[:]))
				_, _ = io.ReadFull(server, b)
				binary.BigEndian.PutUint16(h[:], uint16(len(a)))
				_, _ = server.Write(append(h[:], a...))
			}
		}()
		return client, nil
	}}
	c := Config{"https://192.0.2.1/dns-query", "192.0.2.1:853", "192.0.2.53:53"}
	reply, err := r.Resolve(q, c, []string{"192.0.2.54"})
	if err != nil || !reflect.DeepEqual(reply, a) {
		t.Fatalf("fallback: %v", err)
	}
	want := []string{"tcp|192.0.2.1:443", "tcp|192.0.2.1:853", "udp|192.0.2.53:53", "udp|192.0.2.54:53", "tcp|192.0.2.54:53"}
	if !reflect.DeepEqual(targets, want) {
		t.Fatalf("chain %v", targets)
	}
}
