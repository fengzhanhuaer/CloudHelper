package routewire

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	DirectUpgrade      = "chvr-tls"
	DirectTransport    = "tls-direct"
	DirectCarrierSlots = 2
)

const (
	KeepaliveMain uint16 = 0
	KeepalivePing uint16 = 1
	KeepalivePong uint16 = 2
)

type bufferedConn struct {
	net.Conn
	reader io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func ValidKeepalive(sub uint16, control, data []byte) bool {
	return (sub == KeepalivePing || sub == KeepalivePong) && len(control) == 0 && len(data) == 0
}

func DirectRequest(r *http.Request) bool {
	return r.TLS != nil && r.Method == http.MethodGet && r.ProtoMajor == 1 &&
		r.ProtoMinor == 1 && r.ContentLength == 0 && len(r.TransferEncoding) == 0 &&
		directHeaders(r.Header)
}

func directHeaders(header http.Header) bool {
	upgrade := header.Values("Upgrade")
	if len(upgrade) != 1 || !strings.EqualFold(strings.TrimSpace(upgrade[0]), DirectUpgrade) {
		return false
	}
	for _, line := range header.Values("Connection") {
		for _, token := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

// OpenDirect runs on an established TLS connection. It preserves any carrier
// bytes read together with the upgrade response. The caller owns conn on error.
func OpenDirect(conn net.Conn, req *http.Request, deadline time.Time) (net.Conn, error) {
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", DirectUpgrade)
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	// Limit HTTP parsing without imposing a limit on the carrier byte stream.
	limited := &io.LimitedReader{R: conn, N: 16 << 10}
	reader := bufio.NewReader(limited)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.ProtoMajor != 1 ||
		resp.ProtoMinor != 1 || !directHeaders(resp.Header) {
		return nil, fmt.Errorf("private TLS carrier upgrade rejected: HTTP %d", resp.StatusCode)
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	if reader.Buffered() == 0 {
		return conn, nil
	}
	// Limit the buffered reader to bytes already read in the HTTP response.
	return &bufferedConn{Conn: conn, reader: io.MultiReader(
		io.LimitReader(reader, int64(reader.Buffered())), conn)}, nil
}

// AcceptDirect must only be called after the request's private authentication.
func AcceptDirect(w http.ResponseWriter) (net.Conn, net.Conn, error) {
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return nil, nil, err
	}
	if err = conn.SetDeadline(time.Now().Add(5 * time.Second)); err == nil {
		_, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + DirectUpgrade + "\r\n\r\n")
	}
	if err == nil {
		err = rw.Flush()
	}
	if err == nil {
		err = conn.SetDeadline(time.Time{})
	}
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return &bufferedConn{Conn: conn, reader: rw.Reader}, conn, nil
}
