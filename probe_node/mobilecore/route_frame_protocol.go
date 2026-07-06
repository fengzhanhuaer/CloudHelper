package mobilecore

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"sync"
)

const (
	mobileRouteFrameMagic           uint16 = 0x4348
	mobileRouteFrameVersion                = 1
	mobileRouteFrameHeaderBytes            = 38
	mobileRouteFrameMaxControlBytes        = 8096
	mobileRouteFrameMaxDataBytes           = 65536
	mobileRouteFrameMaxBytes               = mobileRouteFrameHeaderBytes + mobileRouteFrameMaxControlBytes + mobileRouteFrameMaxDataBytes
)

var mobileRouteFrameCRC32Table = crc32.MakeTable(crc32.Castagnoli)

var mobileRouteFrameBufferPool = sync.Pool{
	New: func() any {
		return make([]byte, mobileRouteFrameMaxBytes)
	},
}

type mobileRouteFrameKind uint8

const (
	mobileRouteFrameKindData mobileRouteFrameKind = 1 + iota
	mobileRouteFrameKindControl
	mobileRouteFrameKindProgress
	mobileRouteFrameKindClose
	mobileRouteFrameKindError
	mobileRouteFrameKindPing
	mobileRouteFrameKindPong
)

type mobileRouteFrame struct {
	Kind     mobileRouteFrameKind
	Flags    uint16
	StreamID uint64
	Seq      uint64
	Control  []byte
	Data     []byte
}

func marshalMobileRouteFrameControl(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(payload) > mobileRouteFrameMaxControlBytes {
		return nil, fmt.Errorf("control payload too large: %d", len(payload))
	}
	return payload, nil
}

func writeMobileRouteFrame(writer io.Writer, frame mobileRouteFrame) error {
	encoded, err := encodeMobileRouteFrame(frame, nil)
	if err != nil {
		return err
	}
	return writeAll(writer, encoded)
}

func readMobileRouteFrame(reader io.Reader) (mobileRouteFrame, error) {
	header := make([]byte, mobileRouteFrameHeaderBytes)
	if _, err := io.ReadFull(reader, header); err != nil {
		return mobileRouteFrame{}, err
	}

	frame, controlLen, dataLen, payloadLen, err := decodeMobileRouteFrameHeader(header)
	if err != nil {
		return mobileRouteFrame{}, err
	}

	if payloadLen > 0 {
		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return mobileRouteFrame{}, err
		}
		if controlLen > 0 {
			frame.Control = append([]byte(nil), payload[:controlLen]...)
		}
		if dataLen > 0 {
			frame.Data = append([]byte(nil), payload[controlLen:controlLen+dataLen]...)
		}
	}
	if err := verifyMobileRouteFrameChecksum(header, frame.Control, frame.Data); err != nil {
		return mobileRouteFrame{}, err
	}
	return frame, nil
}

func decodeMobileRouteFrame(payload []byte) (mobileRouteFrame, error) {
	if len(payload) < mobileRouteFrameHeaderBytes {
		return mobileRouteFrame{}, errors.New("frame too short")
	}
	header := payload[:mobileRouteFrameHeaderBytes]
	frame, controlLen, dataLen, totalPayloadLen, err := decodeMobileRouteFrameHeader(header)
	if err != nil {
		return mobileRouteFrame{}, err
	}
	if len(payload) != mobileRouteFrameHeaderBytes+totalPayloadLen {
		return mobileRouteFrame{}, fmt.Errorf("frame length mismatch: got=%d want=%d", len(payload), mobileRouteFrameHeaderBytes+totalPayloadLen)
	}
	body := payload[mobileRouteFrameHeaderBytes:]
	if controlLen > 0 {
		frame.Control = body[:controlLen]
	}
	if dataLen > 0 {
		frame.Data = body[controlLen : controlLen+dataLen]
	}
	if err := verifyMobileRouteFrameChecksum(header, frame.Control, frame.Data); err != nil {
		return mobileRouteFrame{}, err
	}
	return frame, nil
}

func encodeMobileRouteFrame(frame mobileRouteFrame, scratch []byte) ([]byte, error) {
	controlLen := len(frame.Control)
	dataLen := len(frame.Data)
	if controlLen > mobileRouteFrameMaxControlBytes {
		return nil, fmt.Errorf("control payload too large: %d", controlLen)
	}
	if dataLen > mobileRouteFrameMaxDataBytes {
		return nil, fmt.Errorf("data payload too large: %d", dataLen)
	}

	frameLen := mobileRouteFrameHeaderBytes + controlLen + dataLen
	if frameLen > mobileRouteFrameMaxBytes {
		return nil, fmt.Errorf("frame payload too large: %d", frameLen)
	}

	if cap(scratch) < frameLen {
		scratch = make([]byte, frameLen)
	}
	scratch = scratch[:frameLen]
	for i := range scratch {
		scratch[i] = 0
	}

	binary.BigEndian.PutUint16(scratch[0:2], mobileRouteFrameMagic)
	scratch[2] = mobileRouteFrameVersion
	scratch[3] = byte(frame.Kind)
	binary.BigEndian.PutUint16(scratch[4:6], frame.Flags)
	binary.BigEndian.PutUint16(scratch[6:8], mobileRouteFrameHeaderBytes)
	binary.BigEndian.PutUint32(scratch[8:12], uint32(frameLen))
	binary.BigEndian.PutUint16(scratch[12:14], uint16(controlLen))
	binary.BigEndian.PutUint32(scratch[14:18], uint32(dataLen))
	binary.BigEndian.PutUint64(scratch[18:26], frame.StreamID)
	binary.BigEndian.PutUint64(scratch[26:34], frame.Seq)
	if controlLen > 0 {
		copy(scratch[mobileRouteFrameHeaderBytes:mobileRouteFrameHeaderBytes+controlLen], frame.Control)
	}
	if dataLen > 0 {
		copy(scratch[mobileRouteFrameHeaderBytes+controlLen:], frame.Data)
	}

	checksum := crc32.Update(0, mobileRouteFrameCRC32Table, scratch[:mobileRouteFrameHeaderBytes-4])
	if controlLen > 0 {
		checksum = crc32.Update(checksum, mobileRouteFrameCRC32Table, frame.Control)
	}
	if dataLen > 0 {
		checksum = crc32.Update(checksum, mobileRouteFrameCRC32Table, frame.Data)
	}
	binary.BigEndian.PutUint32(scratch[34:38], checksum)
	return scratch, nil
}

func decodeMobileRouteFrameHeader(header []byte) (mobileRouteFrame, int, int, int, error) {
	if len(header) != mobileRouteFrameHeaderBytes {
		return mobileRouteFrame{}, 0, 0, 0, errors.New("invalid frame header length")
	}
	if binary.BigEndian.Uint16(header[0:2]) != mobileRouteFrameMagic {
		return mobileRouteFrame{}, 0, 0, 0, errors.New("invalid frame magic")
	}
	if header[2] != mobileRouteFrameVersion {
		return mobileRouteFrame{}, 0, 0, 0, fmt.Errorf("unsupported frame version: %d", header[2])
	}
	frame := mobileRouteFrame{
		Kind:     mobileRouteFrameKind(header[3]),
		Flags:    binary.BigEndian.Uint16(header[4:6]),
		StreamID: binary.BigEndian.Uint64(header[18:26]),
		Seq:      binary.BigEndian.Uint64(header[26:34]),
	}
	headerLen := int(binary.BigEndian.Uint16(header[6:8]))
	if headerLen != mobileRouteFrameHeaderBytes {
		return mobileRouteFrame{}, 0, 0, 0, fmt.Errorf("invalid frame header length: %d", headerLen)
	}
	frameLen := int(binary.BigEndian.Uint32(header[8:12]))
	controlLen := int(binary.BigEndian.Uint16(header[12:14]))
	dataLen := int(binary.BigEndian.Uint32(header[14:18]))
	if controlLen < 0 || controlLen > mobileRouteFrameMaxControlBytes {
		return mobileRouteFrame{}, 0, 0, 0, fmt.Errorf("control payload too large: %d", controlLen)
	}
	if dataLen < 0 || dataLen > mobileRouteFrameMaxDataBytes {
		return mobileRouteFrame{}, 0, 0, 0, fmt.Errorf("data payload too large: %d", dataLen)
	}
	if frameLen != mobileRouteFrameHeaderBytes+controlLen+dataLen {
		return mobileRouteFrame{}, 0, 0, 0, fmt.Errorf("frame length mismatch: got=%d want=%d", frameLen, mobileRouteFrameHeaderBytes+controlLen+dataLen)
	}
	if frameLen > mobileRouteFrameMaxBytes {
		return mobileRouteFrame{}, 0, 0, 0, fmt.Errorf("frame payload too large: %d", frameLen)
	}
	return frame, controlLen, dataLen, controlLen + dataLen, nil
}

func verifyMobileRouteFrameChecksum(header []byte, control []byte, data []byte) error {
	if len(header) != mobileRouteFrameHeaderBytes {
		return errors.New("invalid frame header length")
	}
	checksum := binary.BigEndian.Uint32(header[34:38])
	expected := crc32.Update(0, mobileRouteFrameCRC32Table, header[:34])
	if len(control) > 0 {
		expected = crc32.Update(expected, mobileRouteFrameCRC32Table, control)
	}
	if len(data) > 0 {
		expected = crc32.Update(expected, mobileRouteFrameCRC32Table, data)
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
