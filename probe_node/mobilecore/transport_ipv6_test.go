package mobilecore

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudhelper/probe_node/internal/routewire"
)

func TestMobileIPv6CarrierAuthSlotsAndPayload(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mac := buildMobileVRouteHMAC("fixture-secret", "vrouter-ipv6", nonce, r.Method, r.URL.Path, "7", mobileVRouteBridgeRoleToNext)
		slot, err := strconv.Atoi(r.Header.Get(mobileVRouteCodexCarrierSlotHeader))
		if !routewire.DirectRequest(r) || err != nil || slot < 0 || slot >= routewire.DirectCarrierSlots || nonce == "" || r.Header.Get(mobileVRouteCodexMACHeader) != mac || r.Header.Get(mobileVRouteCodexAuthTicketHeader) != "fixture-ticket" || r.TLS.ServerName != "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, _, err := routewire.AcceptDirect(w)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.Copy(conn, conn)
	}))
	server.Listener.Close()
	server.Listener = listener
	server.StartTLS()
	defer server.Close()
	plan := mobileVRouteForwardPlan{LocalNode: "7", RouteID: "vrouter-ipv6", Layer: "websocket-h3", RelayHost: "::1", RelayPort: listener.Addr().(*net.TCPAddr).Port, BridgeRole: mobileVRouteBridgeRoleToNext, Rule: mobileVRouteTopology{Secret: "fixture-secret", AuthTicket: "fixture-ticket"}}
	for slot := range routewire.DirectCarrierSlots {
		plan.CarrierSlot = slot
		conn, err := dialMobileVRouteCarrier(plan)
		if err != nil {
			t.Fatalf("slot %d: %v", slot, err)
		}
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Write([]byte("carrier")); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		data := make([]byte, 7)
		_, err = io.ReadFull(conn, data)
		conn.Close()
		if err != nil || string(data) != "carrier" {
			t.Fatalf("payload=%q err=%v", data, err)
		}
	}
}
