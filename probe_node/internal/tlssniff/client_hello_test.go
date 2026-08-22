package tlssniff

import (
	"encoding/binary"
	"testing"
)

func buildTestClientHello(host string) []byte {
	serverName := make([]byte, 5+len(host))
	binary.BigEndian.PutUint16(serverName[0:2], uint16(3+len(host)))
	serverName[2] = 0
	binary.BigEndian.PutUint16(serverName[3:5], uint16(len(host)))
	copy(serverName[5:], host)

	extension := make([]byte, 4+len(serverName))
	binary.BigEndian.PutUint16(extension[2:4], uint16(len(serverName)))
	copy(extension[4:], serverName)

	body := make([]byte, 2+32+1+2+2+1+1+2+len(extension))
	body[0], body[1] = 0x03, 0x03
	offset := 34
	body[offset] = 0
	offset++
	binary.BigEndian.PutUint16(body[offset:offset+2], 2)
	offset += 2
	body[offset], body[offset+1] = 0x13, 0x01
	offset += 2
	body[offset], body[offset+1] = 1, 0
	offset += 2
	binary.BigEndian.PutUint16(body[offset:offset+2], uint16(len(extension)))
	offset += 2
	copy(body[offset:], extension)

	handshake := make([]byte, 4+len(body))
	handshake[0] = 0x01
	handshake[1] = byte(len(body) >> 16)
	handshake[2] = byte(len(body) >> 8)
	handshake[3] = byte(len(body))
	copy(handshake[4:], body)

	record := make([]byte, 5+len(handshake))
	record[0], record[1], record[2] = 0x16, 0x03, 0x01
	binary.BigEndian.PutUint16(record[3:5], uint16(len(handshake)))
	copy(record[5:], handshake)
	return record
}

func TestClientHelloServerName(t *testing.T) {
	hello := buildTestClientHello("Ads.Example.com")
	if host, complete := ClientHelloServerName(hello[:7]); complete || host != "" {
		t.Fatalf("partial hello host=%q complete=%t", host, complete)
	}
	if host, complete := ClientHelloServerName(hello); !complete || host != "ads.example.com" {
		t.Fatalf("complete hello host=%q complete=%t", host, complete)
	}
	handshake := hello[5:]
	if host, complete := ClientHelloHandshakeServerName(handshake[:7]); complete || host != "" {
		t.Fatalf("partial QUIC hello host=%q complete=%t", host, complete)
	}
	if host, complete := ClientHelloHandshakeServerName(handshake); !complete || host != "ads.example.com" {
		t.Fatalf("complete QUIC hello host=%q complete=%t", host, complete)
	}
}

func TestClientHelloServerNameRejectsNonTLSAndInvalidHost(t *testing.T) {
	if host, complete := ClientHelloServerName([]byte("GET / HTTP/1.1\r\n")); !complete || host != "" {
		t.Fatalf("plain HTTP host=%q complete=%t", host, complete)
	}
	if IsValidServerName("127.0.0.1") || IsValidServerName("invalid") || !IsValidServerName("cdn.example.com") {
		t.Fatal("unexpected server-name validation result")
	}
}
