//go:build linux_router

package main

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"testing"
	"time"
)

func TestProbeLinuxRouterQUICSniffsSplitClientHelloAndRejects(t *testing.T) {
	useProbeLinuxRouterSNITestState(t, []probeVirtualRouterRouteRule{{
		Name:    "Reject QUIC ads",
		Action:  "reject",
		Entries: []string{"domain_suffix:ads.example"},
	}})
	resetProbeLinuxRouterQUICState()
	t.Cleanup(resetProbeLinuxRouterQUICState)
	hello := buildProbeLinuxRouterTestClientHello("cdn.ads.example")[5:]
	split := len(hello) / 2
	second := buildProbeLinuxRouterTestQUICPacket(t, 1, uint64(split), hello[split:])
	binary.BigEndian.PutUint16(second[22:24], 8443)
	if probeLinuxRouterQUICRejectsPacket(second) {
		t.Fatal("out-of-order QUIC fragment was rejected before ClientHello completed")
	}
	first := buildProbeLinuxRouterTestQUICPacket(t, 0, 0, hello[:split])
	binary.BigEndian.PutUint16(first[22:24], 8443)
	if !probeLinuxRouterQUICRejectsPacket(first) {
		t.Fatal("matching QUIC ClientHello was not rejected")
	}
	items, _, err := snapshotProbeDomainObservations()
	if err != nil || len(items) != 1 {
		t.Fatalf("observations=%+v err=%v", items, err)
	}
	item := items[0]
	if item.Domain != "cdn.ads.example" || item.QUICObservations != 1 || item.LastAction != "reject" || !reflect.DeepEqual(item.ObservedVia, []string{"quic"}) {
		t.Fatalf("QUIC observation=%+v", item)
	}
	if !probeLinuxRouterQUICRejectsPacket(first) {
		t.Fatal("known rejected QUIC flow was not retained")
	}
	items, _, _ = snapshotProbeDomainObservations()
	if items[0].Events != 1 {
		t.Fatalf("retransmission duplicated observation: %+v", items[0])
	}
}

func TestProbeLinuxRouterQUICIgnoresNonQUICUDP(t *testing.T) {
	useProbeLinuxRouterSNITestState(t, nil)
	resetProbeLinuxRouterQUICState()
	t.Cleanup(resetProbeLinuxRouterQUICState)
	packet := buildProbeLinuxRouterTestUDPPacket(443, []byte("not quic"))
	if probeLinuxRouterQUICRejectsPacket(packet) {
		t.Fatal("ordinary UDP packet was rejected")
	}
	items, _, err := snapshotProbeDomainObservations()
	if err != nil || len(items) != 0 {
		t.Fatalf("ordinary UDP produced observations=%+v err=%v", items, err)
	}
}

func TestProbeLinuxRouterQUICFlowCleanupIsBounded(t *testing.T) {
	resetProbeLinuxRouterQUICState()
	t.Cleanup(resetProbeLinuxRouterQUICState)
	probeLinuxRouterQUICState.mu.Lock()
	now := time.Now()
	for i := 0; i < probeLinuxRouterQUICMaxFlows; i++ {
		key := probeLinuxRouterQUICFlowKey{sourceIP: uint32(i + 1)}
		probeLinuxRouterQUICState.flows[key] = &probeLinuxRouterQUICFlow{updatedAt: now.Add(time.Duration(i) * time.Nanosecond)}
	}
	makeProbeLinuxRouterQUICFlowRoomLocked(now.Add(time.Second))
	got := len(probeLinuxRouterQUICState.flows)
	probeLinuxRouterQUICState.mu.Unlock()
	if got >= probeLinuxRouterQUICMaxFlows {
		t.Fatalf("flow count=%d, want room below %d", got, probeLinuxRouterQUICMaxFlows)
	}
}

func buildProbeLinuxRouterTestQUICPacket(t *testing.T, packetNumber, cryptoOffset uint64, cryptoData []byte) []byte {
	t.Helper()
	dcid := decodeProbeLinuxRouterTestHex(t, "8394c8f03e515708")
	key := decodeProbeLinuxRouterTestHex(t, "1f369613dd76d5467730efcbe3b1a22d")
	iv := decodeProbeLinuxRouterTestHex(t, "fa044b2f42a3fd3b46fb255c")
	hp := decodeProbeLinuxRouterTestHex(t, "9f50449e04a0e810283a1e9933adedd2")
	plaintext := []byte{0x06}
	plaintext = appendProbeLinuxRouterTestQUICVarint(plaintext, cryptoOffset)
	plaintext = appendProbeLinuxRouterTestQUICVarint(plaintext, uint64(len(cryptoData)))
	plaintext = append(plaintext, cryptoData...)
	for len(plaintext) < 40 {
		plaintext = append(plaintext, 0)
	}
	header := []byte{0xc1, 0, 0, 0, 1, byte(len(dcid))}
	header = append(header, dcid...)
	header = append(header, 0, 0)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	header = appendProbeLinuxRouterTestQUICVarint(header, uint64(2+len(plaintext)+aead.Overhead()))
	pnOffset := len(header)
	header = append(header, byte(packetNumber>>8), byte(packetNumber))
	nonce := append([]byte(nil), iv...)
	for i := 0; i < 8; i++ {
		nonce[len(nonce)-1-i] ^= byte(packetNumber >> (8 * i))
	}
	quicPacket := append([]byte(nil), header...)
	quicPacket = aead.Seal(quicPacket, nonce, plaintext, header)
	hpBlock, err := aes.NewCipher(hp)
	if err != nil {
		t.Fatal(err)
	}
	var mask [aes.BlockSize]byte
	hpBlock.Encrypt(mask[:], quicPacket[pnOffset+4:pnOffset+4+aes.BlockSize])
	quicPacket[0] ^= mask[0] & 0x0f
	quicPacket[pnOffset] ^= mask[1]
	quicPacket[pnOffset+1] ^= mask[2]
	return buildProbeLinuxRouterTestUDPPacket(443, quicPacket)
}

func buildProbeLinuxRouterTestUDPPacket(destinationPort uint16, payload []byte) []byte {
	packet := make([]byte, 20+8+len(payload))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[8] = 64
	packet[9] = 17
	copy(packet[12:16], []byte{192, 168, 51, 20})
	copy(packet[16:20], []byte{203, 0, 113, 8})
	udp := packet[20:]
	binary.BigEndian.PutUint16(udp[0:2], 43210)
	binary.BigEndian.PutUint16(udp[2:4], destinationPort)
	binary.BigEndian.PutUint16(udp[4:6], uint16(len(udp)))
	copy(udp[8:], payload)
	return packet
}

func appendProbeLinuxRouterTestQUICVarint(dst []byte, value uint64) []byte {
	if value < 1<<6 {
		return append(dst, byte(value))
	}
	if value < 1<<14 {
		return append(dst, byte(value>>8)|0x40, byte(value))
	}
	panic("test QUIC varint is too large")
}

func decodeProbeLinuxRouterTestHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
