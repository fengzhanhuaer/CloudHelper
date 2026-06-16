package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

const (
	probeChainFrameMagic           uint16 = 0x4348
	probeChainFrameVersion                = 1
	probeChainFrameHeaderBytes            = 38
	probeChainFrameMaxControlBytes        = 8096
	probeChainFrameMaxDataBytes           = 65536
	probeChainFrameMaxBytes               = probeChainFrameHeaderBytes + probeChainFrameMaxControlBytes + probeChainFrameMaxDataBytes
)

var probeChainFrameCRC32Table = crc32.MakeTable(crc32.Castagnoli)

type probeChainFrameKind uint8

const (
	probeChainFrameKindData probeChainFrameKind = 1 + iota
	probeChainFrameKindControl
	probeChainFrameKindProgress
	probeChainFrameKindClose
	probeChainFrameKindError
	probeChainFrameKindPing
	probeChainFrameKindPong
)

type probeChainFrame struct {
	Kind     probeChainFrameKind
	Flags    uint16
	StreamID uint64
	Seq      uint64
	Control  []byte
	Data     []byte
}

func marshalProbeChainFrameControl(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(payload) > probeChainFrameMaxControlBytes {
		return nil, fmt.Errorf("control payload too large: %d", len(payload))
	}
	return payload, nil
}

func writeProbeChainFrame(writer io.Writer, frame probeChainFrame) error {
	encoded, err := encodeProbeChainFrame(frame, nil)
	if err != nil {
		return err
	}
	return writeAll(writer, encoded)
}

func readProbeChainFrame(reader io.Reader) (probeChainFrame, error) {
	header := make([]byte, probeChainFrameHeaderBytes)
	if _, err := io.ReadFull(reader, header); err != nil {
		return probeChainFrame{}, err
	}

	frame, controlLen, dataLen, payloadLen, err := decodeProbeChainFrameHeader(header)
	if err != nil {
		return probeChainFrame{}, err
	}

	if payloadLen > 0 {
		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return probeChainFrame{}, err
		}
		if controlLen > 0 {
			frame.Control = append([]byte(nil), payload[:controlLen]...)
		}
		if dataLen > 0 {
			frame.Data = append([]byte(nil), payload[controlLen:controlLen+dataLen]...)
		}
	}
	if err := verifyProbeChainFrameChecksum(header, frame.Control, frame.Data); err != nil {
		return probeChainFrame{}, err
	}
	return frame, nil
}

func encodeProbeChainFrame(frame probeChainFrame, scratch []byte) ([]byte, error) {
	controlLen := len(frame.Control)
	dataLen := len(frame.Data)
	if controlLen > probeChainFrameMaxControlBytes {
		return nil, fmt.Errorf("control payload too large: %d", controlLen)
	}
	if dataLen > probeChainFrameMaxDataBytes {
		return nil, fmt.Errorf("data payload too large: %d", dataLen)
	}

	frameLen := probeChainFrameHeaderBytes + controlLen + dataLen
	if frameLen > probeChainFrameMaxBytes {
		return nil, fmt.Errorf("frame payload too large: %d", frameLen)
	}

	if cap(scratch) < frameLen {
		scratch = make([]byte, frameLen)
	}
	scratch = scratch[:frameLen]
	for i := range scratch {
		scratch[i] = 0
	}

	binary.BigEndian.PutUint16(scratch[0:2], probeChainFrameMagic)
	scratch[2] = probeChainFrameVersion
	scratch[3] = byte(frame.Kind)
	binary.BigEndian.PutUint16(scratch[4:6], frame.Flags)
	binary.BigEndian.PutUint16(scratch[6:8], probeChainFrameHeaderBytes)
	binary.BigEndian.PutUint32(scratch[8:12], uint32(frameLen))
	binary.BigEndian.PutUint16(scratch[12:14], uint16(controlLen))
	binary.BigEndian.PutUint32(scratch[14:18], uint32(dataLen))
	binary.BigEndian.PutUint64(scratch[18:26], frame.StreamID)
	binary.BigEndian.PutUint64(scratch[26:34], frame.Seq)
	if controlLen > 0 {
		copy(scratch[probeChainFrameHeaderBytes:probeChainFrameHeaderBytes+controlLen], frame.Control)
	}
	if dataLen > 0 {
		copy(scratch[probeChainFrameHeaderBytes+controlLen:], frame.Data)
	}

	checksum := crc32.Update(0, probeChainFrameCRC32Table, scratch[:probeChainFrameHeaderBytes-4])
	if controlLen > 0 {
		checksum = crc32.Update(checksum, probeChainFrameCRC32Table, frame.Control)
	}
	if dataLen > 0 {
		checksum = crc32.Update(checksum, probeChainFrameCRC32Table, frame.Data)
	}
	binary.BigEndian.PutUint32(scratch[34:38], checksum)
	return scratch, nil
}

func decodeProbeChainFrame(payload []byte) (probeChainFrame, error) {
	if len(payload) < probeChainFrameHeaderBytes {
		return probeChainFrame{}, errors.New("frame too short")
	}
	header := payload[:probeChainFrameHeaderBytes]
	frame, controlLen, dataLen, totalPayloadLen, err := decodeProbeChainFrameHeader(header)
	if err != nil {
		return probeChainFrame{}, err
	}
	if len(payload) != probeChainFrameHeaderBytes+totalPayloadLen {
		return probeChainFrame{}, fmt.Errorf("frame length mismatch: got=%d want=%d", len(payload), probeChainFrameHeaderBytes+totalPayloadLen)
	}
	body := payload[probeChainFrameHeaderBytes:]
	if controlLen > 0 {
		frame.Control = body[:controlLen]
	}
	if dataLen > 0 {
		frame.Data = body[controlLen : controlLen+dataLen]
	}
	if err := verifyProbeChainFrameChecksum(header, frame.Control, frame.Data); err != nil {
		return probeChainFrame{}, err
	}
	return frame, nil
}

func decodeProbeChainFrameHeader(header []byte) (probeChainFrame, int, int, int, error) {
	if len(header) != probeChainFrameHeaderBytes {
		return probeChainFrame{}, 0, 0, 0, errors.New("invalid frame header length")
	}
	if binary.BigEndian.Uint16(header[0:2]) != probeChainFrameMagic {
		return probeChainFrame{}, 0, 0, 0, errors.New("invalid frame magic")
	}
	if header[2] != probeChainFrameVersion {
		return probeChainFrame{}, 0, 0, 0, fmt.Errorf("unsupported frame version: %d", header[2])
	}
	frame := probeChainFrame{
		Kind:     probeChainFrameKind(header[3]),
		Flags:    binary.BigEndian.Uint16(header[4:6]),
		StreamID: binary.BigEndian.Uint64(header[18:26]),
		Seq:      binary.BigEndian.Uint64(header[26:34]),
	}
	headerLen := int(binary.BigEndian.Uint16(header[6:8]))
	if headerLen != probeChainFrameHeaderBytes {
		return probeChainFrame{}, 0, 0, 0, fmt.Errorf("invalid frame header length: %d", headerLen)
	}
	frameLen := int(binary.BigEndian.Uint32(header[8:12]))
	controlLen := int(binary.BigEndian.Uint16(header[12:14]))
	dataLen := int(binary.BigEndian.Uint32(header[14:18]))
	if controlLen < 0 || controlLen > probeChainFrameMaxControlBytes {
		return probeChainFrame{}, 0, 0, 0, fmt.Errorf("control payload too large: %d", controlLen)
	}
	if dataLen < 0 || dataLen > probeChainFrameMaxDataBytes {
		return probeChainFrame{}, 0, 0, 0, fmt.Errorf("data payload too large: %d", dataLen)
	}
	if frameLen != probeChainFrameHeaderBytes+controlLen+dataLen {
		return probeChainFrame{}, 0, 0, 0, fmt.Errorf("frame length mismatch: got=%d want=%d", frameLen, probeChainFrameHeaderBytes+controlLen+dataLen)
	}
	if frameLen > probeChainFrameMaxBytes {
		return probeChainFrame{}, 0, 0, 0, fmt.Errorf("frame payload too large: %d", frameLen)
	}
	return frame, controlLen, dataLen, controlLen + dataLen, nil
}

func verifyProbeChainFrameChecksum(header []byte, control []byte, data []byte) error {
	if len(header) != probeChainFrameHeaderBytes {
		return errors.New("invalid frame header length")
	}
	checksum := binary.BigEndian.Uint32(header[34:38])
	expected := crc32.Update(0, probeChainFrameCRC32Table, header[:34])
	if len(control) > 0 {
		expected = crc32.Update(expected, probeChainFrameCRC32Table, control)
	}
	if len(data) > 0 {
		expected = crc32.Update(expected, probeChainFrameCRC32Table, data)
	}
	if checksum != expected {
		return errors.New("frame checksum mismatch")
	}
	return nil
}

func writeAll(w io.Writer, payload []byte) error {
	written := 0
	for written < len(payload) {
		n, err := w.Write(payload[written:])
		if n > 0 {
			written += n
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
