// Package routewire implements the negotiated, big-endian path control format.
// Authentication, frame checksums and payload encoding are unchanged.
package routewire

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const Header = "X-Codex-VRouter-Binary-Path"
const MaxNodes = 16

func IsBinary(b []byte) bool { return len(b) > 0 && b[0] == 0 }

func validNode(s string) bool {
	return len(s) > 0 && len(s) <= 128 && !strings.ContainsRune(s, 0) && utf8.ValidString(s)
}

func MarshalPath(path []string) ([]byte, error) {
	if len(path) < 2 || len(path) > MaxNodes {
		return nil, errors.New("invalid binary path count")
	}
	n := 6
	for i, s := range path {
		if !validNode(s) {
			return nil, errors.New("invalid binary path node")
		}
		for _, previous := range path[:i] {
			if previous == s {
				return nil, errors.New("duplicate binary path node")
			}
		}
		n += 2 + len(s)
	}
	b := make([]byte, n)
	b[1] = 'P'
	b[2] = 1
	binary.BigEndian.PutUint16(b[4:6], uint16(len(path)))
	at := 6
	for _, s := range path {
		binary.BigEndian.PutUint16(b[at:at+2], uint16(len(s)))
		at += 2
		copy(b[at:], s)
		at += len(s)
	}
	return b, nil
}

func ParsePath(b []byte) ([]string, error) {
	if len(b) < 6 || b[0] != 0 || b[1] != 'P' || b[2] != 1 || b[3] != 0 {
		return nil, errors.New("invalid binary path header")
	}
	count := int(binary.BigEndian.Uint16(b[4:6]))
	if count < 2 || count > MaxNodes {
		return nil, errors.New("invalid binary path count")
	}
	path := make([]string, 0, count)
	at := 6
	for i := 0; i < count; i++ {
		if len(b)-at < 2 {
			return nil, errors.New("truncated binary path length")
		}
		n := int(binary.BigEndian.Uint16(b[at : at+2]))
		at += 2
		if n < 1 || n > 128 || n > len(b)-at {
			return nil, errors.New("invalid binary path length")
		}
		s := string(b[at : at+n])
		at += n
		if !validNode(s) {
			return nil, errors.New("invalid binary path node")
		}
		for _, previous := range path {
			if previous == s {
				return nil, errors.New("duplicate binary path node")
			}
		}
		path = append(path, s)
	}
	if at != len(b) {
		return nil, errors.New("trailing binary path data")
	}
	return path, nil
}

// ForPeer downgrades only the known binary format for an older next hop.
// JSON trace and future extension fields pass through without loss.
func ForPeer(control []byte, binaryPath bool) ([]byte, error) {
	if binaryPath || !IsBinary(control) {
		return control, nil
	}
	path, err := ParsePath(control)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Path []string `json:"path"`
	}{path})
}
