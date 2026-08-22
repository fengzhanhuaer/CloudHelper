package quicsniff

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestClientInitialKeysMatchRFC9001Vector(t *testing.T) {
	dcid := mustDecodeHex(t, "8394c8f03e515708")
	key, iv, hp, ok := clientInitialKeys(dcid, Version1)
	if !ok {
		t.Fatal("client Initial key derivation failed")
	}
	if got := hex.EncodeToString(key); got != "1f369613dd76d5467730efcbe3b1a22d" {
		t.Fatalf("key=%s", got)
	}
	if got := hex.EncodeToString(iv); got != "fa044b2f42a3fd3b46fb255c" {
		t.Fatalf("iv=%s", got)
	}
	if got := hex.EncodeToString(hp); got != "9f50449e04a0e810283a1e9933adedd2" {
		t.Fatalf("hp=%s", got)
	}
}

func TestParseClientInitialCryptoFrame(t *testing.T) {
	dcid := mustDecodeHex(t, "8394c8f03e515708")
	key := mustDecodeHex(t, "1f369613dd76d5467730efcbe3b1a22d")
	iv := mustDecodeHex(t, "fa044b2f42a3fd3b46fb255c")
	hp := mustDecodeHex(t, "9f50449e04a0e810283a1e9933adedd2")
	want := []byte("client hello fragment")
	packet := buildProtectedClientInitial(t, Version1, dcid, key, iv, hp, 7, 19, want)
	initial, ok := ParseClientInitial(packet, 6, true)
	if !ok || initial.PacketNumber != 7 || len(initial.Fragments) != 1 || string(initial.DestinationConnectionID) != string(dcid) {
		t.Fatalf("initial=%+v ok=%t", initial, ok)
	}
	fragment := initial.Fragments[0]
	if fragment.Offset != 19 || string(fragment.Data) != string(want) {
		t.Fatalf("fragment=%+v", fragment)
	}
	packet[len(packet)-1] ^= 0xff
	if _, ok := ParseClientInitial(packet, 6, true); ok {
		t.Fatal("tampered Initial unexpectedly authenticated")
	}
}

func buildProtectedClientInitial(t *testing.T, version uint32, dcid, key, iv, hp []byte, packetNumber, cryptoOffset uint64, cryptoData []byte) []byte {
	t.Helper()
	plaintext := []byte{0x06}
	plaintext = appendTestVarint(plaintext, cryptoOffset)
	plaintext = appendTestVarint(plaintext, uint64(len(cryptoData)))
	plaintext = append(plaintext, cryptoData...)
	for len(plaintext) < 40 {
		plaintext = append(plaintext, 0)
	}
	pnLen := 2
	firstByte := byte(0xc1)
	if version == Version2 {
		firstByte = 0xd1
	}
	header := []byte{firstByte}
	var versionBytes [4]byte
	binary.BigEndian.PutUint32(versionBytes[:], version)
	header = append(header, versionBytes[:]...)
	header = append(header, byte(len(dcid)))
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
	header = appendTestVarint(header, uint64(pnLen+len(plaintext)+aead.Overhead()))
	pnOffset := len(header)
	header = append(header, byte(packetNumber>>8), byte(packetNumber))
	nonce := append([]byte(nil), iv...)
	for i := 0; i < 8; i++ {
		nonce[len(nonce)-1-i] ^= byte(packetNumber >> (8 * i))
	}
	packet := append([]byte(nil), header...)
	packet = aead.Seal(packet, nonce, plaintext, header)
	hpBlock, err := aes.NewCipher(hp)
	if err != nil {
		t.Fatal(err)
	}
	var mask [aes.BlockSize]byte
	hpBlock.Encrypt(mask[:], packet[pnOffset+4:pnOffset+4+aes.BlockSize])
	packet[0] ^= mask[0] & 0x0f
	for i := 0; i < pnLen; i++ {
		packet[pnOffset+i] ^= mask[i+1]
	}
	return packet
}

func appendTestVarint(dst []byte, value uint64) []byte {
	switch {
	case value < 1<<6:
		return append(dst, byte(value))
	case value < 1<<14:
		return append(dst, byte(value>>8)|0x40, byte(value))
	default:
		panic("test QUIC varint is too large")
	}
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
