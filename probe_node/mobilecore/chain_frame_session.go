package mobilecore

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	mobileChainFrameControlOpen  = "open"
	mobileChainFrameControlClose = "close"
	mobileChainFrameControlError = "error"

	mobileChainFrameSessionInboundBuffer = 64
	mobileChainFramePingInterval         = 10 * time.Second
	mobileChainFramePingTimeout          = 5 * time.Second
)

type mobileChainFrameSession struct {
	conn net.Conn

	writeCh chan mobileChainFrameWriteRequest
	closeCh chan struct{}
	closed  atomic.Bool

	nextStreamID atomic.Uint64
	streamsMu    sync.Mutex
	streams      map[uint64]*mobileChainFrameStream
	acceptCh     chan *mobileChainFrameStream

	pingSeq        atomic.Uint64
	pingMu         sync.Mutex
	pendingPings   map[uint64]time.Time
	pingsSent      atomic.Int64
	pongsReceived  atomic.Int64
	pingTimeouts   atomic.Int64
	lastRTTNS      atomic.Int64
	lastPingUnixNS atomic.Int64
	lastPongUnixNS atomic.Int64
}

type mobileChainFrameWriteRequest struct {
	frame mobileChainFrame
	errCh chan error
}

type mobileChainFrameSessionControl struct {
	Type  string `json:"type"`
	Error string `json:"error,omitempty"`
}

type mobileChainFramePingStats struct {
	RTT              time.Duration
	LastPingAt       time.Time
	LastPongAt       time.Time
	PingsSent        int64
	PongsReceived    int64
	Timeouts         int64
	Pending          int
	LastPingUnixNano int64
	LastPongUnixNano int64
}

type mobileChainFrameSessionAddr struct {
	label string
}

func (a mobileChainFrameSessionAddr) Network() string { return "mobile-chain-frame" }
func (a mobileChainFrameSessionAddr) String() string {
	if a.label == "" {
		return "mobile-chain-frame"
	}
	return a.label
}

func newMobileChainFrameClient(conn net.Conn) (*mobileChainFrameSession, error) {
	return newMobileChainFrameSession(conn, true)
}

func newMobileChainFrameServer(conn net.Conn) (*mobileChainFrameSession, error) {
	return newMobileChainFrameSession(conn, false)
}

func newMobileChainFrameSession(conn net.Conn, initiator bool) (*mobileChainFrameSession, error) {
	if conn == nil {
		return nil, errors.New("frame session connection is nil")
	}
	start := uint64(1)
	if !initiator {
		start = 2
	}
	s := &mobileChainFrameSession{
		conn:         conn,
		writeCh:      make(chan mobileChainFrameWriteRequest, mobileChainFrameSessionInboundBuffer),
		closeCh:      make(chan struct{}),
		streams:      make(map[uint64]*mobileChainFrameStream),
		acceptCh:     make(chan *mobileChainFrameStream, mobileChainFrameSessionInboundBuffer),
		pendingPings: make(map[uint64]time.Time),
	}
	s.nextStreamID.Store(start)
	go s.writeLoop()
	go s.readLoop()
	go s.pingLoop()
	return s, nil
}

func (s *mobileChainFrameSession) Open() (net.Conn, error) {
	if s == nil {
		return nil, errors.New("frame session is nil")
	}
	if s.IsClosed() {
		return nil, net.ErrClosed
	}
	streamID := s.nextStreamID.Add(2) - 2
	stream := newMobileChainFrameStream(s, streamID)
	s.registerStream(stream)
	if err := s.writeControl(streamID, mobileChainFrameControlOpen, ""); err != nil {
		s.removeStream(streamID, stream)
		_ = stream.closeLocal()
		return nil, err
	}
	return stream, nil
}

func (s *mobileChainFrameSession) Accept() (net.Conn, error) {
	if s == nil {
		return nil, errors.New("frame session is nil")
	}
	select {
	case stream, ok := <-s.acceptCh:
		if !ok {
			return nil, net.ErrClosed
		}
		return stream, nil
	case <-s.closeCh:
		return nil, net.ErrClosed
	}
}

func (s *mobileChainFrameSession) Close() error {
	if s == nil {
		return nil
	}
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(s.closeCh)
	_ = s.conn.Close()
	s.streamsMu.Lock()
	streams := make([]*mobileChainFrameStream, 0, len(s.streams))
	for _, stream := range s.streams {
		streams = append(streams, stream)
	}
	s.streams = make(map[uint64]*mobileChainFrameStream)
	s.streamsMu.Unlock()
	for _, stream := range streams {
		_ = stream.closeRemote(io.ErrClosedPipe)
	}
	close(s.acceptCh)
	return nil
}

func (s *mobileChainFrameSession) IsClosed() bool {
	return s == nil || s.closed.Load()
}

func (s *mobileChainFrameSession) NumStreams() int {
	if s == nil {
		return 0
	}
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	return len(s.streams)
}

func (s *mobileChainFrameSession) PingStats() mobileChainFramePingStats {
	if s == nil {
		return mobileChainFramePingStats{}
	}
	lastPingUnixNS := s.lastPingUnixNS.Load()
	lastPongUnixNS := s.lastPongUnixNS.Load()
	stats := mobileChainFramePingStats{
		RTT:              time.Duration(s.lastRTTNS.Load()),
		PingsSent:        s.pingsSent.Load(),
		PongsReceived:    s.pongsReceived.Load(),
		Timeouts:         s.pingTimeouts.Load(),
		LastPingUnixNano: lastPingUnixNS,
		LastPongUnixNano: lastPongUnixNS,
	}
	if lastPingUnixNS > 0 {
		stats.LastPingAt = time.Unix(0, lastPingUnixNS).UTC()
	}
	if lastPongUnixNS > 0 {
		stats.LastPongAt = time.Unix(0, lastPongUnixNS).UTC()
	}
	s.pingMu.Lock()
	stats.Pending = len(s.pendingPings)
	s.pingMu.Unlock()
	return stats
}

func (s *mobileChainFrameSession) registerStream(stream *mobileChainFrameStream) {
	if s == nil || stream == nil {
		return
	}
	s.streamsMu.Lock()
	s.streams[stream.id] = stream
	s.streamsMu.Unlock()
}

func (s *mobileChainFrameSession) removeStream(streamID uint64, stream *mobileChainFrameStream) {
	if s == nil {
		return
	}
	s.streamsMu.Lock()
	if current := s.streams[streamID]; current == stream || stream == nil {
		delete(s.streams, streamID)
	}
	s.streamsMu.Unlock()
}

func (s *mobileChainFrameSession) getStream(streamID uint64) *mobileChainFrameStream {
	if s == nil {
		return nil
	}
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	return s.streams[streamID]
}

func (s *mobileChainFrameSession) readLoop() {
	for {
		frame, err := readMobileChainFrame(s.conn)
		if err != nil {
			_ = s.Close()
			return
		}
		switch frame.Kind {
		case mobileChainFrameKindControl:
			s.handleControlFrame(frame)
		case mobileChainFrameKindData:
			if stream := s.getStream(frame.StreamID); stream != nil {
				stream.deliverData(frame.Data)
			}
		case mobileChainFrameKindClose:
			if stream := s.getStream(frame.StreamID); stream != nil {
				_ = stream.closeRemote(io.EOF)
			}
		case mobileChainFrameKindError:
			if stream := s.getStream(frame.StreamID); stream != nil {
				_ = stream.closeRemote(errors.New(string(frame.Control)))
			}
		case mobileChainFrameKindPing:
			s.writeFrameAsync(mobileChainFrame{Kind: mobileChainFrameKindPong, StreamID: frame.StreamID, Seq: frame.Seq})
		case mobileChainFrameKindPong:
			s.handlePongFrame(frame)
		}
	}
}

func (s *mobileChainFrameSession) writeLoop() {
	if s == nil {
		return
	}
	for {
		select {
		case <-s.closeCh:
			return
		case req := <-s.writeCh:
			err := writeMobileChainFrame(s.conn, req.frame)
			select {
			case req.errCh <- err:
			default:
			}
			if err != nil {
				_ = s.Close()
				return
			}
		}
	}
}

func (s *mobileChainFrameSession) pingLoop() {
	if s == nil {
		return
	}
	_ = s.sendPingFrame()
	ticker := time.NewTicker(mobileChainFramePingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.closeCh:
			return
		case <-ticker.C:
			s.expirePingFrames(time.Now())
			_ = s.sendPingFrame()
		}
	}
}

func (s *mobileChainFrameSession) sendPingFrame() error {
	if s == nil || s.IsClosed() {
		return net.ErrClosed
	}
	seq := s.pingSeq.Add(1)
	now := time.Now()
	s.pingMu.Lock()
	s.pendingPings[seq] = now
	s.pingMu.Unlock()
	if err := s.writeFrame(mobileChainFrame{Kind: mobileChainFrameKindPing, Seq: seq}); err != nil {
		s.pingMu.Lock()
		delete(s.pendingPings, seq)
		s.pingMu.Unlock()
		return err
	}
	s.pingsSent.Add(1)
	s.lastPingUnixNS.Store(now.UnixNano())
	return nil
}

func (s *mobileChainFrameSession) handlePongFrame(frame mobileChainFrame) {
	if s == nil || frame.Seq == 0 {
		return
	}
	now := time.Now()
	s.pingMu.Lock()
	startedAt, ok := s.pendingPings[frame.Seq]
	if ok {
		delete(s.pendingPings, frame.Seq)
	}
	s.pingMu.Unlock()
	if !ok {
		return
	}
	rtt := now.Sub(startedAt)
	if rtt <= 0 {
		rtt = time.Millisecond
	}
	s.lastRTTNS.Store(int64(rtt))
	s.lastPongUnixNS.Store(now.UnixNano())
	s.pongsReceived.Add(1)
}

func (s *mobileChainFrameSession) expirePingFrames(now time.Time) {
	if s == nil {
		return
	}
	expired := int64(0)
	s.pingMu.Lock()
	for seq, startedAt := range s.pendingPings {
		if now.Sub(startedAt) >= mobileChainFramePingTimeout {
			delete(s.pendingPings, seq)
			expired++
		}
	}
	s.pingMu.Unlock()
	if expired > 0 {
		s.pingTimeouts.Add(expired)
	}
}

func (s *mobileChainFrameSession) handleControlFrame(frame mobileChainFrame) {
	var control mobileChainFrameSessionControl
	if len(frame.Control) > 0 {
		_ = json.Unmarshal(frame.Control, &control)
	}
	switch control.Type {
	case mobileChainFrameControlOpen:
		stream := newMobileChainFrameStream(s, frame.StreamID)
		s.registerStream(stream)
		select {
		case s.acceptCh <- stream:
		case <-s.closeCh:
			_ = stream.closeRemote(io.ErrClosedPipe)
		}
	case mobileChainFrameControlClose:
		if stream := s.getStream(frame.StreamID); stream != nil {
			_ = stream.closeRemote(io.EOF)
		}
	case mobileChainFrameControlError:
		if stream := s.getStream(frame.StreamID); stream != nil {
			errText := control.Error
			if errText == "" {
				errText = "remote stream error"
			}
			_ = stream.closeRemote(errors.New(errText))
		}
	}
}

func (s *mobileChainFrameSession) writeControl(streamID uint64, controlType string, errText string) error {
	payload, err := marshalMobileChainFrameControl(mobileChainFrameSessionControl{Type: controlType, Error: errText})
	if err != nil {
		return err
	}
	return s.writeFrame(mobileChainFrame{Kind: mobileChainFrameKindControl, StreamID: streamID, Control: payload})
}

func (s *mobileChainFrameSession) writeFrame(frame mobileChainFrame) error {
	if s == nil {
		return errors.New("frame session is nil")
	}
	if s.IsClosed() {
		return net.ErrClosed
	}
	errCh := make(chan error, 1)
	select {
	case s.writeCh <- mobileChainFrameWriteRequest{frame: frame, errCh: errCh}:
	case <-s.closeCh:
		return net.ErrClosed
	}
	select {
	case err := <-errCh:
		return err
	case <-s.closeCh:
		return net.ErrClosed
	}
}

func (s *mobileChainFrameSession) writeFrameAsync(frame mobileChainFrame) {
	if s == nil || s.IsClosed() {
		return
	}
	errCh := make(chan error, 1)
	select {
	case s.writeCh <- mobileChainFrameWriteRequest{frame: frame, errCh: errCh}:
	case <-s.closeCh:
	}
}

type mobileChainFrameStream struct {
	session *mobileChainFrameSession
	id      uint64

	readCh chan []byte
	readMu sync.Mutex
	read   []byte

	closeOnce  sync.Once
	remoteErr  error
	localDone  chan struct{}
	remoteDone chan struct{}

	readDeadline  atomic.Value
	writeDeadline atomic.Value
}

func newMobileChainFrameStream(session *mobileChainFrameSession, streamID uint64) *mobileChainFrameStream {
	return &mobileChainFrameStream{
		session:    session,
		id:         streamID,
		readCh:     make(chan []byte, mobileChainFrameSessionInboundBuffer),
		localDone:  make(chan struct{}),
		remoteDone: make(chan struct{}),
	}
}

func (s *mobileChainFrameStream) Read(payload []byte) (int, error) {
	if s == nil {
		return 0, io.ErrClosedPipe
	}
	for {
		s.readMu.Lock()
		if len(s.read) > 0 {
			n := copy(payload, s.read)
			s.read = s.read[n:]
			s.readMu.Unlock()
			return n, nil
		}
		s.readMu.Unlock()

		timer, timerCh := mobileChainDeadlineTimer(s.readDeadline.Load())
		select {
		case chunk, ok := <-s.readCh:
			if timer != nil {
				timer.Stop()
			}
			if !ok {
				if s.remoteErr != nil {
					return 0, s.remoteErr
				}
				return 0, io.EOF
			}
			if len(chunk) == 0 {
				continue
			}
			s.readMu.Lock()
			s.read = append(s.read, chunk...)
			s.readMu.Unlock()
		case <-timerCh:
			return 0, mobileChainTimeoutError{}
		case <-s.localDone:
			if timer != nil {
				timer.Stop()
			}
			return 0, io.ErrClosedPipe
		}
	}
}

func (s *mobileChainFrameStream) Write(payload []byte) (int, error) {
	if s == nil {
		return 0, io.ErrClosedPipe
	}
	if len(payload) == 0 {
		return 0, nil
	}
	timer, timerCh := mobileChainDeadlineTimer(s.writeDeadline.Load())
	done := make(chan error, 1)
	data := append([]byte(nil), payload...)
	go func() {
		done <- s.session.writeFrame(mobileChainFrame{Kind: mobileChainFrameKindData, StreamID: s.id, Data: data})
	}()
	select {
	case err := <-done:
		if timer != nil {
			timer.Stop()
		}
		if err != nil {
			return 0, err
		}
		return len(payload), nil
	case <-timerCh:
		return 0, mobileChainTimeoutError{}
	case <-s.localDone:
		if timer != nil {
			timer.Stop()
		}
		return 0, io.ErrClosedPipe
	}
}

func (s *mobileChainFrameStream) Close() error {
	if s == nil {
		return nil
	}
	return s.closeLocal()
}

func (s *mobileChainFrameStream) CloseWrite() error {
	if s == nil {
		return nil
	}
	return s.session.writeFrame(mobileChainFrame{Kind: mobileChainFrameKindClose, StreamID: s.id})
}

func (s *mobileChainFrameStream) closeLocal() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.localDone)
		if s.session != nil {
			err = s.session.writeFrame(mobileChainFrame{Kind: mobileChainFrameKindClose, StreamID: s.id})
			s.session.removeStream(s.id, s)
		}
	})
	return err
}

func (s *mobileChainFrameStream) closeRemote(err error) error {
	if s == nil {
		return nil
	}
	s.remoteErr = err
	select {
	case <-s.remoteDone:
	default:
		close(s.remoteDone)
		close(s.readCh)
	}
	return nil
}

func (s *mobileChainFrameStream) deliverData(payload []byte) {
	if s == nil {
		return
	}
	data := append([]byte(nil), payload...)
	select {
	case s.readCh <- data:
	case <-s.localDone:
	case <-s.remoteDone:
	}
}

func (s *mobileChainFrameStream) LocalAddr() net.Addr {
	if s == nil || s.session == nil || s.session.conn == nil {
		return mobileChainFrameSessionAddr{}
	}
	return mobileChainFrameSessionAddr{label: s.session.conn.LocalAddr().String()}
}

func (s *mobileChainFrameStream) RemoteAddr() net.Addr {
	if s == nil || s.session == nil || s.session.conn == nil {
		return mobileChainFrameSessionAddr{}
	}
	return mobileChainFrameSessionAddr{label: s.session.conn.RemoteAddr().String()}
}

func (s *mobileChainFrameStream) SetDeadline(t time.Time) error {
	_ = s.SetReadDeadline(t)
	_ = s.SetWriteDeadline(t)
	return nil
}

func (s *mobileChainFrameStream) SetReadDeadline(t time.Time) error {
	if s != nil {
		s.readDeadline.Store(t)
	}
	return nil
}

func (s *mobileChainFrameStream) SetWriteDeadline(t time.Time) error {
	if s != nil {
		s.writeDeadline.Store(t)
	}
	return nil
}

func mobileChainDeadlineTimer(raw any) (*time.Timer, <-chan time.Time) {
	t, _ := raw.(time.Time)
	if t.IsZero() {
		return nil, nil
	}
	d := time.Until(t)
	if d <= 0 {
		timer := time.NewTimer(0)
		return timer, timer.C
	}
	timer := time.NewTimer(d)
	return timer, timer.C
}

type mobileChainTimeoutError struct{}

func (mobileChainTimeoutError) Error() string   { return "i/o timeout" }
func (mobileChainTimeoutError) Timeout() bool   { return true }
func (mobileChainTimeoutError) Temporary() bool { return true }
