package core

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"
)

const (
	// Existing console and subscription results carry up to 8 MiB as base64.
	probeControlMessageMaxBytes   = 16 << 20
	probeControlMessageTimeout    = 30 * time.Second
	probeControlSeparatorMaxBytes = 64 << 10
)

var errProbeControlMessageTooLarge = errors.New("probe control message exceeds size limit")

// Keep JSON value-stream compatibility: a value may span transport messages,
// contain newlines, or be adjacent to another value without a line delimiter.
type probeControlJSONDecoder struct {
	reader   probeControlJSONReader
	decoder  *json.Decoder
	maxBytes int64
}

type probeControlJSONReader struct {
	stream    net.Conn
	remaining int64
	total     int64
	started   bool
	separator int64
}

func newProbeControlJSONDecoder(stream net.Conn, maxBytes int64) *probeControlJSONDecoder {
	d := &probeControlJSONDecoder{maxBytes: maxBytes}
	d.reader.stream = stream
	d.decoder = json.NewDecoder(&d.reader)
	return d
}

func (d *probeControlJSONDecoder) Decode() (json.RawMessage, error) {
	// Decoder may have read into the next value. Charge those bytes to that
	// value rather than granting a fresh budget in addition to the read-ahead.
	buffered := d.reader.total - d.decoder.InputOffset()
	separator := probeControlLeadingWhitespace(d.decoder.Buffered())
	if separator > probeControlSeparatorMaxBytes {
		return nil, errProbeControlMessageTooLarge
	}
	// Whitespace between values has its own bounded allowance. Leave one
	// lookahead byte so even a maximum-size top-level scalar remains valid.
	d.reader.remaining = d.maxBytes + 1 - (buffered - separator)
	if d.reader.remaining < 0 {
		return nil, errProbeControlMessageTooLarge
	}
	d.reader.started = false
	d.reader.separator = separator
	if buffered > separator {
		if err := d.reader.startDeadline(); err != nil {
			return nil, err
		}
	}
	defer d.reader.stream.SetReadDeadline(time.Time{})
	var raw json.RawMessage
	if err := d.decoder.Decode(&raw); err != nil {
		return nil, err
	}
	if int64(len(raw)) > d.maxBytes {
		return nil, errProbeControlMessageTooLarge
	}
	return raw, nil
}

func (r *probeControlJSONReader) startDeadline() error {
	// Idle report intervals are not message timeouts. Start the deadline only
	// when this value has bytes, and never extend it as more fragments arrive.
	if r.started {
		return nil
	}
	r.started = true
	return r.stream.SetReadDeadline(time.Now().Add(probeControlMessageTimeout))
}

func (r *probeControlJSONReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errProbeControlMessageTooLarge
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	if !r.started && int64(len(p)) > probeControlSeparatorMaxBytes-r.separator+1 {
		p = p[:probeControlSeparatorMaxBytes-r.separator+1]
	}
	n, err := r.stream.Read(p)
	r.remaining -= int64(n)
	r.total += int64(n)
	if n > 0 && !r.started {
		leading := int64(probeControlWhitespacePrefix(p[:n]))
		r.separator += leading
		if r.separator > probeControlSeparatorMaxBytes {
			return n, errProbeControlMessageTooLarge
		}
		r.remaining += leading
		if leading < int64(n) {
			if deadlineErr := r.startDeadline(); deadlineErr != nil {
				return n, deadlineErr
			}
		}
	}
	return n, err
}

func probeControlWhitespacePrefix(p []byte) int {
	for i, b := range p {
		switch b {
		case ' ', '\t', '\r', '\n':
		default:
			return i
		}
	}
	return len(p)
}

func probeControlLeadingWhitespace(reader io.Reader) int64 {
	// Buffered returns a view without consuming Decoder's bytes. The normal
	// Encoder newline is a separator, not the start of the next report.
	var buf [512]byte
	var count int64
	for {
		n, err := reader.Read(buf[:])
		leading := probeControlWhitespacePrefix(buf[:n])
		count += int64(leading)
		if leading < n || err != nil || count > probeControlSeparatorMaxBytes {
			return count
		}
	}
}
