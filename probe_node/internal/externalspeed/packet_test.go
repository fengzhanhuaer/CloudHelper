package externalspeed

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"math/big"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestTestPortIsReservedAndReleased(t *testing.T) {
	port, release, err := reservePort()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	address := &net.TCPAddr{IP: net.IPv4zero, Port: int(port)}
	if listener, err := net.ListenTCP("tcp4", address); err == nil {
		listener.Close()
		t.Fatal("test port can collide with an ordinary local socket")
	}
}

func TestExternalDownloadThroughBusinessPackets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	s := stack.New(stack.Options{NetworkProtocols: []stack.NetworkProtocolFactory{ipv4.NewProtocol}, TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol}})
	defer s.Destroy()
	link := channel.New(256, 1400, "")
	defer link.Close()
	if e := s.CreateNIC(1, link); e != nil {
		t.Fatal(e)
	}
	if e := s.AddProtocolAddress(1, tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: tcpip.AddressWithPrefix{Address: tcpip.AddrFrom4([4]byte{1, 1, 1, 1}), PrefixLen: 32}}, stack.AddressProperties{}); e != nil {
		t.Fatal(e)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})
	listener, err := gonet.ListenTCP(s, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4([4]byte{1, 1, 1, 1}), Port: 443}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"speed.cloudflare.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	tlsListener := tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "speed.cloudflare.com" {
			t.Error(r.Host)
		}
		w.Write(make([]byte, 128<<10))
	})}
	defer server.Close()
	go server.Serve(tlsListener)
	var outbound atomic.Int64
	p, err := NewPacketConn(ctx, "198.18.0.15", "1.1.1.1", func(data []byte) error {
		outbound.Add(1)
		pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(data)})
		link.InjectInbound(ipv4.ProtocolNumber, pkt)
		pkt.DecRef()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	go func() {
		for {
			pkt := link.ReadContext(ctx)
			if pkt == nil {
				return
			}
			v := pkt.ToView()
			data := append([]byte(nil), v.AsSlice()...)
			v.Release()
			pkt.DecRef()
			p.Feed(data)
		}
	}()
	client := Client(p.DialContext)
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	result := Run(ctx, client, 64<<10, time.Second, nil)
	if !result.OK || result.Bytes != 64<<10 || result.Mbps <= 0 {
		t.Fatalf("result=%+v", result)
	}
	if outbound.Load() == 0 {
		t.Fatal("no TCP packets traversed test path")
	}
	bad := make([]byte, 40)
	bad[0] = 0x45
	bad[9] = 6
	copy(bad[12:16], []byte{1, 1, 1, 1})
	copy(bad[16:20], []byte{198, 18, 0, 15})
	bad[20] = 1
	bad[21] = 0xbb
	if p.Feed(bad) {
		t.Fatal("unrelated destination port consumed")
	}
}
