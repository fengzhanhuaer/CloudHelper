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
	mobileChainFrameMagic           uint16 = 0x4348
	mobileChainFrameVersion                = 1
	mobileChainFrameHeaderBytes            = 38
	mobileChainFrameMaxControlBytes        = 8096
	mobileChainFrameMaxDataBytes           = 65536
	mobileChainFrameMaxBytes               = mobileChainFrameHeaderBytes + mobileChainFrameMaxControlBytes + mobileChainFrameMaxDataBytes
)

var mobileChainFrameCRC32Table = crc32.MakeTable(crc32.Castagnoli)

var mobileChainFrameBufferPool = sync.Pool{
	New: func() any {
		return make([]byte, mobileChainFrameMaxBytes)
	},
}

type mobileChainFrameKind uint8

const (
	mobileChainFrameKindData mobileChainFrameKind = 1 + iota
	mobileChainFrameKindControl
	mobileChainFrameKindProgress
	mobileChainFrameKindClose
	mobileChainFrameKindError
	mobileChainFrameKindPing
	mobileChainFrameKindPong
)

type mobileChainFrame struct {
	Kind     mobileChainFrameKind
	Flags    uint16
	StreamID uint64
	Seq      uint64
	Control  []byte
	Data     []byte
}

func marshalMobileChainFrameControl(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(payload) > mobileChainFrameMaxControlBytes {
		return nil, fmt.Errorf("control payload too large: %d", len(payload))
	}
	return payload, nil
}

func writeMobileChainFrame(writer io.Writer, frame mobileChainFrame) error {
	encoded, err := encodeMobileChainFrame(frame, nil)
	if err != nil {
		return err
	}
	return writeAll(writer, encoded)
}

func readMobileChainFrame(reader io.Reader) (mobileChainFrame, error) {
	header := make([]byte, mobileChainFrameHeaderBytes)
	if _, err := io.ReadFull(reader, header); err != nil {
		return mobileChainFrame{}, err
	}

	frame, controlLen, dataLen, payloadLen, err := decodeMobileChainFrameHeader(header)
	if err != nil {
		return mobileChainFrame{}, err
	}

	if payloadLen > 0 {
		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return mobileChainFrame{}, err
		}
		if controlLen > 0 {
			frame.Control = append([]byte(nil), payload[:controlLen]...)
		}
		if dataLen > 0 {
			frame.Data = append([]byte(nil), payload[controlLen:controlLen+dataLen]...)
		}
	}
	if err := verifyMobileChainFrameChecksum(header, frame.Control, frame.Data); err != nil {
		return mobileChainFrame{}, err
	}
	return frame, nil
}

func decodeMobileChainFrame(payload []byte) (mobileChainFrame, error) {
	if len(payload) < mobileChainFrameHeaderBytes {
		return mobileChainFrame{}, errors.New("frame too short")
	}
	header := payload[:mobileChainFrameHeaderBytes]
	frame, controlLen, dataLen, totalPayloadLen, err := decodeMobileChainFrameHeader(header)
	if err != nil {
		return mobileChainFrame{}, err
	}
	if len(payload) != mobileChainFrameHeaderBytes+totalPayloadLen {
		return mobileChainFrame{}, fmt.Errorf("frame length mismatch: got=%d want=%d", len(payload), mobileChainFrameHeaderBytes+totalPayloadLen)
	}
	body := payload[mobileChainFrameHeaderBytes:]
	if controlLen > 0 {
		frame.Control = body[:controlLen]
	}
	if dataLen > 0 {
		frame.Data = body[controlLen : controlLen+dataLen]
	}
	if err := verifyMobileChainFrameChecksum(header, frame.Control, frame.Data); err != nil {
		return mobileChainFrame{}, err
	}
	return frame, nil
}

func encodeMobileChainFrame(frame mobileChainFrame, scratch []byte) ([]byte, error) {
	controlLen := len(frame.Control)
	dataLen := len(frame.Data)
	if controlLen > mobileChainFrameMaxControlBytes {
		return nil, fmt.Errorf("control payload too large: %d", controlLen)
	}
	if dataLen > mobileChainFrameMaxDataBytes {
		return nil, fmt.Errorf("data payload too large: %d", dataLen)
	}

	frameLen := mobileChainFrameHeaderBytes + controlLen + dataLen
	if frameLen > mobileChainFrameMaxBytes {
		return nil, fmt.Errorf("frame payload too large: %d", frameLen)
	}

	if cap(scratch) < frameLen {
		scratch = make([]byte, frameLen)
	}
	scratch = scratch[:frameLen]
	for i := range scratch {
		scratch[i] = 0
	}

	binary.BigEndian.PutUint16(scratch[0:2], mobileChainFrameMagic)
	scratch[2] = mobileChainFrameVersion
	scratch[3] = byte(frame.Kind)
	binary.BigEndian.PutUint16(scratch[4:6], frame.Flags)
	binary.BigEndian.PutUint16(scratch[6:8], mobileChainFrameHeaderBytes)
	binary.BigEndian.PutUint32(scratch[8:12], uint32(frameLen))
	binary.BigEndian.PutUint16(scratch[12:14], uint16(controlLen))
	binary.BigEndian.PutUint32(scratch[14:18], uint32(dataLen))
	binary.BigEndian.PutUint64(scratch[18:26], frame.StreamID)
	binary.BigEndian.PutUint64(scratch[26:34], frame.Seq)
	if controlLen > 0 {
		copy(scratch[mobileChainFrameHeaderBytes:mobileChainFrameHeaderBytes+controlLen], frame.Control)
	}
	if dataLen > 0 {
		copy(scratch[mobileChainFrameHeaderBytes+controlLen:], frame.Data)
	}

	checksum := crc32.Update(0, mobileChainFrameCRC32Table, scratch[:mobileChainFrameHeaderBytes-4])
	if controlLen > 0 {
		checksum = crc32.Update(checksum, mobileChainFrameCRC32Table, frame.Control)
	}
	if dataLen > 0 {
		checksum = crc32.Update(checksum, mobileChainFrameCRC32Table, frame.Data)
	}
	binary.BigEndian.PutUint32(scratch[34:38], checksum)
	return scratch, nil
}

func decodeMobileChainFrameHeader(header []byte) (mobileChainFrame, int, int, int, error) {
	if len(header) != mobileChainFrameHeaderBytes {
		return mobileChainFrame{}, 0, 0, 0, errors.New("invalid frame header length")
	}
	if binary.BigEndian.Uint16(header[0:2]) != mobileChainFrameMagic {
		return mobileChainFrame{}, 0, 0, 0, errors.New("invalid frame magic")
	}
	if header[2] != mobileChainFrameVersion {
		return mobileChainFrame{}, 0, 0, 0, fmt.Errorf("unsupported frame version: %d", header[2])
	}
	frame := mobileChainFrame{
		Kind:     mobileChainFrameKind(header[3]),
		Flags:    binary.BigEndian.Uint16(header[4:6]),
		StreamID: binary.BigEndian.Uint64(header[18:26]),
		Seq:      binary.BigEndian.Uint64(header[26:34]),
	}
	headerLen := int(binary.BigEndian.Uint16(header[6:8]))
	if headerLen != mobileChainFrameHeaderBytes {
		return mobileChainFrame{}, 0, 0, 0, fmt.Errorf("invalid frame header length: %d", headerLen)
	}
	frameLen := int(binary.BigEndian.Uint32(header[8:12]))
	controlLen := int(binary.BigEndian.Uint16(header[12:14]))
	dataLen := int(binary.BigEndian.Uint32(header[14:18]))
	if controlLen < 0 || controlLen > mobileChainFrameMaxControlBytes {
		return mobileChainFrame{}, 0, 0, 0, fmt.Errorf("control payload too large: %d", controlLen)
	}
	if dataLen < 0 || dataLen > mobileChainFrameMaxDataBytes {
		return mobileChainFrame{}, 0, 0, 0, fmt.Errorf("data payload too large: %d", dataLen)
	}
	if frameLen != mobileChainFrameHeaderBytes+controlLen+dataLen {
		return mobileChainFrame{}, 0, 0, 0, fmt.Errorf("frame length mismatch: got=%d want=%d", frameLen, mobileChainFrameHeaderBytes+controlLen+dataLen)
	}
	if frameLen > mobileChainFrameMaxBytes {
		return mobileChainFrame{}, 0, 0, 0, fmt.Errorf("frame payload too large: %d", frameLen)
	}
	return frame, controlLen, dataLen, controlLen + dataLen, nil
}

func verifyMobileChainFrameChecksum(header []byte, control []byte, data []byte) error {
	if len(header) != mobileChainFrameHeaderBytes {
		return errors.New("invalid frame header length")
	}
	checksum := binary.BigEndian.Uint32(header[34:38])
	expected := crc32.Update(0, mobileChainFrameCRC32Table, header[:34])
	if len(control) > 0 {
		expected = crc32.Update(expected, mobileChainFrameCRC32Table, control)
	}
	if len(data) > 0 {
		expected = crc32.Update(expected, mobileChainFrameCRC32Table, data)
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
