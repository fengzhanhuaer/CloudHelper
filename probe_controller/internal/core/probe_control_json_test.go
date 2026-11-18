package core

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type probeJSONTestConn struct {
	reader    io.Reader
	read      int
	chunkSize int
	deadlines []time.Time
}

func (c *probeJSONTestConn) Read(p []byte) (int, error) {
	if c.chunkSize > 0 && len(p) > c.chunkSize {
		p = p[:c.chunkSize]
	}
	n, err := c.reader.Read(p)
	c.read += n
	return n, err
}
func (*probeJSONTestConn) Write([]byte) (int, error)        { return 0, errors.New("unused") }
func (*probeJSONTestConn) Close() error                     { return nil }
func (*probeJSONTestConn) LocalAddr() net.Addr              { return nil }
func (*probeJSONTestConn) RemoteAddr() net.Addr             { return nil }
func (*probeJSONTestConn) SetDeadline(time.Time) error      { return nil }
func (*probeJSONTestConn) SetWriteDeadline(time.Time) error { return nil }
func (c *probeJSONTestConn) SetReadDeadline(deadline time.Time) error {
	c.deadlines = append(c.deadlines, deadline)
	return nil
}

func TestProbeControlJSONPreservesValueStreamAndReadAhead(t *testing.T) {
	values := []string{`{"type":"report"}`, "{\n\"type\":\"future\",\"nested\": [1, {\"a\":\"escaped \\\" quote\"}]\n}", `{"type":"report"}`}
	for _, separator := range []string{"", "\n", "\r\n  "} {
		for _, chunk := range []int{1, 17, 0} {
			conn := &probeJSONTestConn{reader: strings.NewReader(strings.Join(values, separator)), chunkSize: chunk}
			decoder := newProbeControlJSONDecoder(conn, 128)
			for _, expected := range values {
				got, err := decoder.Decode()
				if err != nil || string(got) != expected {
					t.Fatalf("separator=%q chunk=%d got=%s err=%v", separator, chunk, got, err)
				}
			}
			if _, err := decoder.Decode(); !errors.Is(err, io.EOF) {
				t.Fatalf("expected EOF, got %v", err)
			}
		}
	}
}

func TestProbeControlJSONRejectsOversizeAcrossFragmentsAndValues(t *testing.T) {
	const limit = 1024
	for _, prefix := range []string{"", `{"type":"report"}` + "\n", strings.Repeat(`{}`+"\n", 200)} {
		for _, oversized := range []string{
			`{"version":"` + strings.Repeat("a", 2*limit), // no terminator required
			`{"extra":["` + strings.Repeat("a", 2*limit) + `"]}`,
		} {
			conn := &probeJSONTestConn{reader: strings.NewReader(prefix + oversized), chunkSize: 31}
			decoder := newProbeControlJSONDecoder(conn, limit)
			for i := 0; i < strings.Count(prefix, "\n"); i++ {
				if _, err := decoder.Decode(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := decoder.Decode(); !errors.Is(err, errProbeControlMessageTooLarge) {
				t.Fatalf("expected size rejection, got %v", err)
			}
			if conn.read > len(prefix)+limit+2 {
				t.Fatalf("budget bypass: read=%d prefix=%d", conn.read, len(prefix))
			}
		}
	}
	whitespace := &probeJSONTestConn{reader: strings.NewReader(strings.Repeat(" ", probeControlSeparatorMaxBytes+2))}
	if _, err := newProbeControlJSONDecoder(whitespace, limit).Decode(); !errors.Is(err, errProbeControlMessageTooLarge) {
		t.Fatalf("unbounded separator accepted: %v", err)
	}
	if whitespace.read > probeControlSeparatorMaxBytes+1 {
		t.Fatalf("separator budget bypass: %d", whitespace.read)
	}
}

func TestProbeControlJSONAcceptsBoundaryValueAndLargeBridgeResult(t *testing.T) {
	const limit = 1024
	value := `"` + strings.Repeat("a", limit-2) + `"`
	conn := &probeJSONTestConn{reader: strings.NewReader("{}\n" + value + "\n" + value)}
	decoder := newProbeControlJSONDecoder(conn, limit)
	for _, expected := range []string{`{}`, value, value} {
		if got, err := decoder.Decode(); err != nil || string(got) != expected {
			t.Fatalf("boundary value rejected: len=%d err=%v", len(got), err)
		}
	}
	// Existing responses contain up to 8 MiB encoded as base64, not raw bytes.
	body := base64.StdEncoding.EncodeToString(make([]byte, probeLocalConsoleBridgeMaxBodyBytes))
	wire, err := json.Marshal(probeLocalConsoleBridgeResultMessage{Type: "local_console_bridge_result", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	large := newProbeControlJSONDecoder(&probeJSONTestConn{reader: strings.NewReader(string(wire))}, probeControlMessageMaxBytes)
	if raw, err := large.Decode(); err != nil || len(raw) != len(wire) {
		t.Fatalf("normal maximum console result rejected: len=%d err=%v", len(raw), err)
	}
}

func TestProbeControlJSONDeadlineStartsWithMessageAndIsNotExtended(t *testing.T) {
	conn := &probeJSONTestConn{reader: strings.NewReader(`{"type":"report"}`), chunkSize: 1}
	decoder := newProbeControlJSONDecoder(conn, 1024)
	if len(conn.deadlines) != 0 {
		t.Fatal("idle stream must not have a new read deadline")
	}
	started := time.Now()
	if _, err := decoder.Decode(); err != nil {
		t.Fatal(err)
	}
	if len(conn.deadlines) != 2 || conn.deadlines[0].IsZero() || !conn.deadlines[1].IsZero() {
		t.Fatalf("deadline must start once and clear after decoding: %v", conn.deadlines)
	}
	if remaining := conn.deadlines[0].Sub(started); remaining < probeControlMessageTimeout-time.Second || remaining > probeControlMessageTimeout+time.Second {
		t.Fatalf("unexpected deadline duration: %v", remaining)
	}
}

type probeJSONDeadlineConn struct {
	net.Conn
	deadlines chan time.Time
}

func (c *probeJSONDeadlineConn) SetReadDeadline(deadline time.Time) error {
	c.deadlines <- deadline
	if !deadline.IsZero() {
		// Exercise real blocked reads without waiting the production 30 seconds.
		deadline = time.Now().Add(50 * time.Millisecond)
	}
	return c.Conn.SetReadDeadline(deadline)
}

func TestProbeControlJSONIdleSeparatorsDoNotStartDeadline(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	conn := &probeJSONDeadlineConn{Conn: server, deadlines: make(chan time.Time, 8)}
	decoder := newProbeControlJSONDecoder(conn, 1024)
	results := make(chan error, 2)
	go func() {
		_, err := decoder.Decode()
		results <- err
		if err == nil {
			_, err = decoder.Decode()
			results <- err
		}
	}()
	if err := json.NewEncoder(client).Encode(map[string]string{"type": "report"}); err != nil {
		t.Fatal(err)
	}
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	// The first message starts and clears a deadline. Its Encoder newline may
	// remain buffered while the peer is legitimately idle before its next report.
	if (<-conn.deadlines).IsZero() || !(<-conn.deadlines).IsZero() {
		t.Fatal("first message deadline was not started and cleared")
	}
	if _, err := io.WriteString(client, "\r\n\t "); err != nil {
		t.Fatal(err)
	}
	select {
	case deadline := <-conn.deadlines:
		t.Fatalf("idle separators started deadline: %v", deadline)
	case err := <-results:
		t.Fatalf("idle stream ended early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := io.WriteString(client, `{"version":"`); err != nil {
		t.Fatal(err)
	}
	select {
	case deadline := <-conn.deadlines:
		if deadline.IsZero() {
			t.Fatal("message deadline unexpectedly cleared before reading")
		}
	case <-time.After(time.Second):
		t.Fatal("partial message did not start a deadline")
	}
	select {
	case err := <-results:
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("expected real read timeout for incomplete message, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("incomplete message was not timed out")
	}
}
