package routewire

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestDirectUpgradeRetainsCoalescedFramesAndRejectsWebSocket(t *testing.T) {
	for _, protocol := range []string{DirectUpgrade, "websocket"} {
		t.Run(protocol, func(t *testing.T) {
			client, server := net.Pipe()
			done := make(chan struct{})
			defer func() {
				client.Close()
				server.Close()
				<-done
			}()
			deadline := time.Now().Add(2 * time.Second)
			server.SetDeadline(deadline)
			go func() {
				defer close(done)
				req, err := http.ReadRequest(bufio.NewReader(server))
				if err != nil {
					t.Error(err)
					return
				}
				if req.Header.Get("Upgrade") != DirectUpgrade || req.Header.Get("Connection") != "Upgrade" {
					t.Errorf("unexpected request: %v", req.Header)
				}
				_, err = io.WriteString(server, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: "+protocol+"\r\n\r\nVR-frame")
				if err != nil {
					t.Error(err)
				}
			}()
			req, err := http.NewRequest(http.MethodGet, "https://localhost/api/node/route/relay", nil)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := OpenDirect(client, req, deadline)
			if protocol != DirectUpgrade {
				if err == nil {
					t.Fatal("accepted WebSocket response")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			conn.SetReadDeadline(deadline)
			data := make([]byte, len("VR-frame"))
			if _, err := io.ReadFull(conn, data); err != nil || string(data) != "VR-frame" {
				t.Fatalf("first carrier bytes = %q, err = %v", data, err)
			}
		})
	}
}
