package main

import (
	"bufio"
	"bytes"
	"net"
	"testing"
	"time"
)

func TestBuildTunnelWSURL(t *testing.T) {
	u, err := buildTunnelWSURL("https://controller.example.com", "cloudserver", "tok-1")
	if err != nil {
		t.Fatalf("buildTunnelWSURL returned error: %v", err)
	}
	if u != "wss://controller.example.com/api/ws/tunnel/cloudserver?token=tok-1" {
		t.Fatalf("unexpected tunnel ws url: %s", u)
	}
}

func TestSocks5HandshakeNoAuth(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	respCh := make(chan []byte, 1)
	errCh := make(chan error, 1)

	go func() {
		if _, err := client.Write([]byte{0x05, 0x01, 0x00}); err != nil {
			errCh <- err
			return
		}
		buf := make([]byte, 2)
		if _, err := client.Read(buf); err != nil {
			errCh <- err
			return
		}
		respCh <- buf
	}()

	if err := socks5Handshake(bufio.NewReader(server), server); err != nil {
		t.Fatalf("socks5Handshake returned error: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("client side error: %v", err)
	case resp := <-respCh:
		expect := []byte{0x05, 0x00}
		if !bytes.Equal(resp, expect) {
			t.Fatalf("unexpected handshake response: %v", resp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for handshake response")
	}
}

func TestSocks5ReadConnectRequestDomain(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	request := []byte{
		0x05, 0x01, 0x00, 0x03,
		0x0b,
		'e', 'x', 'a', 'm', 'p', 'l', 'e', '.', 'c', 'o', 'm',
		0x00, 0x50,
	}

	addr, err := socks5ReadConnectRequest(bufio.NewReader(bytes.NewReader(request)), server)
	if err != nil {
		t.Fatalf("socks5ReadConnectRequest returned error: %v", err)
	}
	if addr != "example.com:80" {
		t.Fatalf("unexpected target address: %s", addr)
	}
}
