package quicsniff

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"io"

	"golang.org/x/crypto/hkdf"
)

const (
	Version1 uint32 = 0x00000001
	Version2 uint32 = 0x6b3343cf
)

var (
	initialSaltV1 = []byte{0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17, 0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a}
	initialSaltV2 = []byte{0x0d, 0xed, 0xe3, 0xde, 0xf7, 0x00, 0xa6, 0xdb, 0x81, 0x93, 0x81, 0xbe, 0x6e, 0x26, 0x9d, 0xcb, 0xf9, 0xbd, 0x2e, 0xd9}
)

type CryptoFragment struct {
	Offset uint64
	Data   []byte
}

type ClientInitial struct {
	DestinationConnectionID []byte
	PacketNumber            uint64
	Fragments               []CryptoFragment
}

// ParseClientInitial authenticates and decrypts a QUIC v1 or v2 client Initial
// packet. Invalid and unsupported datagrams return ok=false so passive sniffing
// can fail open without affecting forwarded traffic.
func ParseClientInitial(datagram []byte, largestPacketNumber uint64, hasLargest bool) (result ClientInitial, ok bool) {
	if len(datagram) < 7 || datagram[0]&0xc0 != 0xc0 {
		return ClientInitial{}, false
	}
	version := binary.BigEndian.Uint32(datagram[1:5])
	packetType := (datagram[0] >> 4) & 0x03
	if version != Version1 && version != Version2 || version == Version1 && packetType != 0 || version == Version2 && packetType != 1 {
		return ClientInitial{}, false
	}
	offset := 5
	dcidLen := int(datagram[offset])
	offset++
	if dcidLen == 0 || dcidLen > 20 || offset+dcidLen+1 > len(datagram) {
		return ClientInitial{}, false
	}
	dcid := datagram[offset : offset+dcidLen]
	offset += dcidLen
	scidLen := int(datagram[offset])
	offset++
	if scidLen > 20 || offset+scidLen > len(datagram) {
		return ClientInitial{}, false
	}
	offset += scidLen
	tokenLen, n, valid := parseVarint(datagram[offset:])
	if !valid || tokenLen > uint64(len(datagram)-offset-n) {
		return ClientInitial{}, false
	}
	offset += n + int(tokenLen)
	packetLen, n, valid := parseVarint(datagram[offset:])
	if !valid {
		return ClientInitial{}, false
	}
	offset += n
	pnOffset := offset
	if packetLen > uint64(len(datagram)-pnOffset) || packetLen < 17 || pnOffset+4+aes.BlockSize > len(datagram) {
		return ClientInitial{}, false
	}
	packetEnd := pnOffset + int(packetLen)
	if pnOffset+4+aes.BlockSize > packetEnd {
		return ClientInitial{}, false
	}
	key, iv, hp, valid := clientInitialKeys(dcid, version)
	if !valid {
		return ClientInitial{}, false
	}
	hpBlock, err := aes.NewCipher(hp)
	if err != nil {
		return ClientInitial{}, false
	}
	var mask [aes.BlockSize]byte
	hpBlock.Encrypt(mask[:], datagram[pnOffset+4:pnOffset+4+aes.BlockSize])
	firstByte := datagram[0] ^ mask[0]&0x0f
	pnLen := int(firstByte&0x03) + 1
	if pnOffset+pnLen > packetEnd {
		return ClientInitial{}, false
	}
	header := append([]byte(nil), datagram[:pnOffset+pnLen]...)
	header[0] = firstByte
	var truncated uint64
	for i := 0; i < pnLen; i++ {
		header[pnOffset+i] ^= mask[i+1]
		truncated = truncated<<8 | uint64(header[pnOffset+i])
	}
	packetNumber := decodePacketNumber(pnLen, largestPacketNumber, hasLargest, truncated)
	block, err := aes.NewCipher(key)
	if err != nil {
		return ClientInitial{}, false
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return ClientInitial{}, false
	}
	nonce := append([]byte(nil), iv...)
	for i := 0; i < 8; i++ {
		nonce[len(nonce)-1-i] ^= byte(packetNumber >> (8 * i))
	}
	plaintext, err := aead.Open(nil, nonce, datagram[pnOffset+pnLen:packetEnd], header)
	if err != nil {
		return ClientInitial{}, false
	}
	fragments, valid := parseCryptoFrames(plaintext)
	if !valid {
		return ClientInitial{}, false
	}
	return ClientInitial{DestinationConnectionID: append([]byte(nil), dcid...), PacketNumber: packetNumber, Fragments: fragments}, true
}

func clientInitialKeys(dcid []byte, version uint32) (key, iv, hp []byte, ok bool) {
	salt := initialSaltV1
	keyLabel, ivLabel, hpLabel := "quic key", "quic iv", "quic hp"
	if version == Version2 {
		salt = initialSaltV2
		keyLabel, ivLabel, hpLabel = "quicv2 key", "quicv2 iv", "quicv2 hp"
	}
	initialSecret := hkdf.Extract(sha256.New, dcid, salt)
	clientSecret, ok := expandLabel(initialSecret, "client in", 32)
	if !ok {
		return nil, nil, nil, false
	}
	key, ok = expandLabel(clientSecret, keyLabel, 16)
	if !ok {
		return nil, nil, nil, false
	}
	iv, ok = expandLabel(clientSecret, ivLabel, 12)
	if !ok {
		return nil, nil, nil, false
	}
	hp, ok = expandLabel(clientSecret, hpLabel, 16)
	return key, iv, hp, ok
}

func expandLabel(secret []byte, label string, length int) ([]byte, bool) {
	fullLabel := "tls13 " + label
	if length <= 0 || len(fullLabel) > 255 {
		return nil, false
	}
	info := make([]byte, 2+1+len(fullLabel)+1)
	binary.BigEndian.PutUint16(info[:2], uint16(length))
	info[2] = byte(len(fullLabel))
	copy(info[3:], fullLabel)
	output := make([]byte, length)
	if _, err := io.ReadFull(hkdf.Expand(sha256.New, secret, info), output); err != nil {
		return nil, false
	}
	return output, true
}

func decodePacketNumber(length int, largest uint64, hasLargest bool, truncated uint64) uint64 {
	expected := uint64(0)
	if hasLargest {
		expected = largest + 1
	}
	window := uint64(1) << (8 * length)
	halfWindow := window / 2
	mask := window - 1
	candidate := expected&^mask | truncated
	if candidate+halfWindow <= expected && candidate < (uint64(1)<<62)-window {
		return candidate + window
	}
	if candidate > expected+halfWindow && candidate >= window {
		return candidate - window
	}
	return candidate
}

func parseCryptoFrames(payload []byte) ([]CryptoFragment, bool) {
	fragments := make([]CryptoFragment, 0, 2)
	for offset := 0; offset < len(payload); {
		frameType, n, ok := parseVarint(payload[offset:])
		if !ok {
			return nil, false
		}
		offset += n
		switch frameType {
		case 0x00, 0x01:
			continue
		case 0x02, 0x03:
			var values [4]uint64
			for i := range values {
				values[i], n, ok = parseVarint(payload[offset:])
				if !ok {
					return nil, false
				}
				offset += n
			}
			if values[2] > uint64(len(payload)-offset)/2 {
				return nil, false
			}
			for i := uint64(0); i < values[2]; i++ {
				for range 2 {
					_, n, ok = parseVarint(payload[offset:])
					if !ok {
						return nil, false
					}
					offset += n
				}
			}
			if frameType == 0x03 {
				for range 3 {
					_, n, ok = parseVarint(payload[offset:])
					if !ok {
						return nil, false
					}
					offset += n
				}
			}
		case 0x06:
			cryptoOffset, read, valid := parseVarint(payload[offset:])
			if !valid {
				return nil, false
			}
			offset += read
			dataLen, read, valid := parseVarint(payload[offset:])
			if !valid || dataLen > uint64(len(payload)-offset-read) {
				return nil, false
			}
			offset += read
			data := append([]byte(nil), payload[offset:offset+int(dataLen)]...)
			offset += int(dataLen)
			fragments = append(fragments, CryptoFragment{Offset: cryptoOffset, Data: data})
		case 0x1c:
			for range 2 {
				_, n, ok = parseVarint(payload[offset:])
				if !ok {
					return nil, false
				}
				offset += n
			}
			reasonLen, read, valid := parseVarint(payload[offset:])
			if !valid || reasonLen > uint64(len(payload)-offset-read) {
				return nil, false
			}
			offset += read + int(reasonLen)
		case 0x1d:
			_, n, ok = parseVarint(payload[offset:])
			if !ok {
				return nil, false
			}
			offset += n
			reasonLen, read, valid := parseVarint(payload[offset:])
			if !valid || reasonLen > uint64(len(payload)-offset-read) {
				return nil, false
			}
			offset += read + int(reasonLen)
		default:
			return fragments, len(fragments) > 0
		}
	}
	return fragments, len(fragments) > 0
}

func parseVarint(data []byte) (uint64, int, bool) {
	if len(data) == 0 {
		return 0, 0, false
	}
	length := 1 << (data[0] >> 6)
	if length > len(data) {
		return 0, 0, false
	}
	value := uint64(data[0] & 0x3f)
	for i := 1; i < length; i++ {
		value = value<<8 | uint64(data[i])
	}
	return value, length, true
}
