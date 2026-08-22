package tlssniff

import (
	"encoding/binary"
	"net"
	"strings"
)

// ClientHelloServerName returns complete=false while the first TLS record is
// incomplete. A complete result can have an empty server name.
func ClientHelloServerName(data []byte) (serverName string, complete bool) {
	if len(data) == 0 {
		return "", false
	}
	if data[0] != 0x16 {
		return "", true
	}
	if len(data) < 5 {
		return "", false
	}
	recordLen := int(binary.BigEndian.Uint16(data[3:5]))
	if recordLen <= 0 {
		return "", true
	}
	if len(data) < 5+recordLen {
		return "", false
	}
	return ClientHelloHandshakeServerName(data[5 : 5+recordLen])
}

// ClientHelloHandshakeServerName parses a TLS ClientHello handshake without
// the TLS record header, as carried by QUIC CRYPTO frames.
func ClientHelloHandshakeServerName(data []byte) (serverName string, complete bool) {
	if len(data) < 4 {
		return "", false
	}
	if data[0] != 0x01 {
		return "", true
	}
	helloLen := int(data[1])<<16 | int(data[2])<<8 | int(data[3])
	if helloLen <= 0 {
		return "", true
	}
	if len(data) < 4+helloLen {
		return "", false
	}
	data = data[:4+helloLen]
	offset := 4
	if offset+34 > len(data) {
		return "", true
	}
	offset += 34
	if offset >= len(data) {
		return "", true
	}
	sessionLen := int(data[offset])
	offset++
	if offset+sessionLen+2 > len(data) {
		return "", true
	}
	offset += sessionLen
	cipherLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
	offset += 2
	if offset+cipherLen+1 > len(data) {
		return "", true
	}
	offset += cipherLen
	compressionLen := int(data[offset])
	offset++
	if offset+compressionLen+2 > len(data) {
		return "", true
	}
	offset += compressionLen
	extensionsLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
	offset += 2
	extensionsEnd := offset + extensionsLen
	if extensionsEnd > len(data) {
		return "", true
	}
	for offset+4 <= extensionsEnd {
		extType := binary.BigEndian.Uint16(data[offset : offset+2])
		extLen := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		offset += 4
		if offset+extLen > extensionsEnd {
			return "", true
		}
		if extType == 0 {
			return serverNameFromExtension(data[offset : offset+extLen]), true
		}
		offset += extLen
	}
	return "", true
}

func IsValidServerName(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "."))
	if host == "" || len(host) > 253 || strings.ContainsAny(host, " \t\r\n:/\\") {
		return false
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func serverNameFromExtension(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	listLen := int(binary.BigEndian.Uint16(data[:2]))
	offset := 2
	end := offset + listLen
	if end > len(data) {
		return ""
	}
	for offset+3 <= end {
		nameType := data[offset]
		nameLen := int(binary.BigEndian.Uint16(data[offset+1 : offset+3]))
		offset += 3
		if offset+nameLen > end {
			return ""
		}
		if nameType == 0 {
			return strings.ToLower(strings.TrimSpace(string(data[offset : offset+nameLen])))
		}
		offset += nameLen
	}
	return ""
}
