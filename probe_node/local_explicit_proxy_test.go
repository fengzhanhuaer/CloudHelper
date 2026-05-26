package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHandleProbeLocalExplicitHTTPProxyConnectRelaysTCP(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	defer echoLn.Close()
	go func() {
		conn, acceptErr := echoLn.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	client, server := net.Pipe()
	defer client.Close()
	go handleProbeLocalExplicitHTTPProxyConn(server)

	target := echoLn.Addr().String()
	if _, err := fmt.Fprintf(client, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatalf("write connect request: %v", err)
	}
	reader := bufio.NewReader(client)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read connect response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("connect status=%d", resp.StatusCode)
	}
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatalf("write tunneled payload: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatalf("read tunneled echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo=%q", string(buf))
	}
}

func TestResolveProbeChainHTTPProxyTargetConnectIPv6(t *testing.T) {
	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader("CONNECT [2001:db8::1]:443 HTTP/1.1\r\nHost: [2001:db8::1]:443\r\n\r\n")))
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	target, err := resolveProbeChainHTTPProxyTarget(req)
	if err != nil {
		t.Fatalf("resolve target: %v", err)
	}
	if target != "[2001:db8::1]:443" {
		t.Fatalf("target=%q", target)
	}
}
