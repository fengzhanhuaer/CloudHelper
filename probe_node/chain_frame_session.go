package main

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
	probeChainFrameControlOpen  = "open"
	probeChainFrameControlClose = "close"
	probeChainFrameControlError = "error"

	probeChainFrameSessionInboundBuffer = 64
	probeChainFramePingInterval         = 10 * time.Second
	probeChainFramePingTimeout          = 5 * time.Second
)

type probeChainFrameSession struct {
	conn      net.Conn
	local     probeChainFrameSessionAddr
	remote    probeChainFrameSessionAddr
	initiator bool

	writeCh chan probeChainFrameWriteRequest
	closeCh chan struct{}
	closed  atomic.Bool

	nextStreamID atomic.Uint64
	streamsMu    sync.Mutex
	streams      map[uint64]*probeChainFrameStream
	acceptCh     chan *probeChainFrameStream

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

type probeChainFrameWriteRequest struct {
	frame probeChainFrame
	errCh chan error
}

type probeChainFrameSessionControl struct {
	Type  string `json:"type"`
	Error string `json:"error,omitempty"`
}

type probeChainFramePingStats struct {
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

type probeChainFrameSessionAddr struct {
	label string
}

func (a probeChainFrameSessionAddr) Network() string { return "probe-chain-frame" }
func (a probeChainFrameSessionAddr) String() string {
	if a.label == "" {
		return "probe-chain-frame"
	}
	return a.label
}

func newProbeChainFrameClient(conn net.Conn) (*probeChainFrameSession, error) {
	return newProbeChainFrameSession(conn, true)
}

func newProbeChainFrameServer(conn net.Conn) (*probeChainFrameSession, error) {
	return newProbeChainFrameSession(conn, false)
}

func newProbeChainFrameSession(conn net.Conn, initiator bool) (*probeChainFrameSession, error) {
	if conn == nil {
		return nil, errors.New("frame session connection is nil")
	}
	start := uint64(1)
	if !initiator {
		start = 2
	}
	s := &probeChainFrameSession{
		conn:         conn,
		local:        probeChainFrameSessionAddr{label: conn.LocalAddr().String()},
		remote:       probeChainFrameSessionAddr{label: conn.RemoteAddr().String()},
		initiator:    initiator,
		writeCh:      make(chan probeChainFrameWriteRequest, probeChainFrameSessionInboundBuffer),
		closeCh:      make(chan struct{}),
		streams:      make(map[uint64]*probeChainFrameStream),
		acceptCh:     make(chan *probeChainFrameStream, probeChainFrameSessionInboundBuffer),
		pendingPings: make(map[uint64]time.Time),
	}
	s.nextStreamID.Store(start)
	go s.writeLoop()
	go s.readLoop()
	go s.pingLoop()
	return s, nil
}

func (s *probeChainFrameSession) Open() (net.Conn, error) {
	if s == nil {
		return nil, errors.New("frame session is nil")
	}
	if s.IsClosed() {
		return nil, net.ErrClosed
	}
	streamID := s.nextStreamID.Add(2) - 2
	stream := newProbeChainFrameStream(s, streamID)
	s.registerStream(stream)
	if err := s.writeControl(streamID, probeChainFrameControlOpen, ""); err != nil {
		s.removeStream(streamID, stream)
		_ = stream.closeLocal()
		return nil, err
	}
	return stream, nil
}

func (s *probeChainFrameSession) Accept() (net.Conn, error) {
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

func (s *probeChainFrameSession) Close() error {
	if s == nil {
		return nil
	}
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(s.closeCh)
	_ = s.conn.Close()
	s.streamsMu.Lock()
	streams := make([]*probeChainFrameStream, 0, len(s.streams))
	for _, stream := range s.streams {
		streams = append(streams, stream)
	}
	s.streams = make(map[uint64]*probeChainFrameStream)
	s.streamsMu.Unlock()
	for _, stream := range streams {
		_ = stream.closeRemote(io.ErrClosedPipe)
	}
	close(s.acceptCh)
	return nil
}

func (s *probeChainFrameSession) IsClosed() bool {
	return s == nil || s.closed.Load()
}

func (s *probeChainFrameSession) NumStreams() int {
	if s == nil {
		return 0
	}
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	return len(s.streams)
}

func (s *probeChainFrameSession) PingStats() probeChainFramePingStats {
	if s == nil {
		return probeChainFramePingStats{}
	}
	lastPingUnixNS := s.lastPingUnixNS.Load()
	lastPongUnixNS := s.lastPongUnixNS.Load()
	stats := probeChainFramePingStats{
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

func (s *probeChainFrameSession) registerStream(stream *probeChainFrameStream) {
	if s == nil || stream == nil {
		return
	}
	s.streamsMu.Lock()
	s.streams[stream.id] = stream
	s.streamsMu.Unlock()
}

func (s *probeChainFrameSession) removeStream(streamID uint64, stream *probeChainFrameStream) {
	if s == nil {
		return
	}
	s.streamsMu.Lock()
	if current := s.streams[streamID]; current == stream || stream == nil {
		delete(s.streams, streamID)
	}
	s.streamsMu.Unlock()
}

func (s *probeChainFrameSession) getStream(streamID uint64) *probeChainFrameStream {
	if s == nil {
		return nil
	}
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	return s.streams[streamID]
}

func (s *probeChainFrameSession) readLoop() {
	for {
		frame, err := readProbeChainFrame(s.conn)
		if err != nil {
			_ = s.Close()
			return
		}
		switch frame.Kind {
		case probeChainFrameKindControl:
			s.handleControlFrame(frame)
		case probeChainFrameKindData:
			if stream := s.getStream(frame.StreamID); stream != nil {
				stream.deliverData(frame.Data)
			}
		case probeChainFrameKindClose:
			if stream := s.getStream(frame.StreamID); stream != nil {
				_ = stream.closeRemote(io.EOF)
			}
		case probeChainFrameKindError:
			if stream := s.getStream(frame.StreamID); stream != nil {
				_ = stream.closeRemote(errors.New(string(frame.Control)))
			}
		case probeChainFrameKindPing:
			s.writeFrameAsync(probeChainFrame{Kind: probeChainFrameKindPong, StreamID: frame.StreamID, Seq: frame.Seq})
		case probeChainFrameKindPong:
			s.handlePongFrame(frame)
		}
	}
}

func (s *probeChainFrameSession) writeLoop() {
	if s == nil {
		return
	}
	for {
		select {
		case <-s.closeCh:
			return
		case req := <-s.writeCh:
			err := writeProbeChainFrame(s.conn, req.frame)
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

func (s *probeChainFrameSession) pingLoop() {
	if s == nil {
		return
	}
	_ = s.sendPingFrame()
	ticker := time.NewTicker(probeChainFramePingInterval)
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

func (s *probeChainFrameSession) sendPingFrame() error {
	if s == nil || s.IsClosed() {
		return net.ErrClosed
	}
	seq := s.pingSeq.Add(1)
	now := time.Now()
	s.pingMu.Lock()
	s.pendingPings[seq] = now
	s.pingMu.Unlock()
	if err := s.writeFrame(probeChainFrame{Kind: probeChainFrameKindPing, Seq: seq}); err != nil {
		s.pingMu.Lock()
		delete(s.pendingPings, seq)
		s.pingMu.Unlock()
		return err
	}
	s.pingsSent.Add(1)
	s.lastPingUnixNS.Store(now.UnixNano())
	return nil
}

func (s *probeChainFrameSession) handlePongFrame(frame probeChainFrame) {
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

func (s *probeChainFrameSession) expirePingFrames(now time.Time) {
	if s == nil {
		return
	}
	expired := int64(0)
	s.pingMu.Lock()
	for seq, startedAt := range s.pendingPings {
		if now.Sub(startedAt) >= probeChainFramePingTimeout {
			delete(s.pendingPings, seq)
			expired++
		}
	}
	s.pingMu.Unlock()
	if expired > 0 {
		s.pingTimeouts.Add(expired)
	}
}

func (s *probeChainFrameSession) handleControlFrame(frame probeChainFrame) {
	var control probeChainFrameSessionControl
	if len(frame.Control) > 0 {
		_ = json.Unmarshal(frame.Control, &control)
	}
	switch control.Type {
	case probeChainFrameControlOpen:
		stream := newProbeChainFrameStream(s, frame.StreamID)
		s.registerStream(stream)
		select {
		case s.acceptCh <- stream:
		case <-s.closeCh:
			_ = stream.closeRemote(io.ErrClosedPipe)
		}
	case probeChainFrameControlClose:
		if stream := s.getStream(frame.StreamID); stream != nil {
			_ = stream.closeRemote(io.EOF)
		}
	case probeChainFrameControlError:
		if stream := s.getStream(frame.StreamID); stream != nil {
			errText := control.Error
			if errText == "" {
				errText = "remote stream error"
			}
			_ = stream.closeRemote(errors.New(errText))
		}
	}
}

func (s *probeChainFrameSession) writeControl(streamID uint64, controlType string, errText string) error {
	payload, err := marshalProbeChainFrameControl(probeChainFrameSessionControl{Type: controlType, Error: errText})
	if err != nil {
		return err
	}
	return s.writeFrame(probeChainFrame{Kind: probeChainFrameKindControl, StreamID: streamID, Control: payload})
}

func (s *probeChainFrameSession) writeFrame(frame probeChainFrame) error {
	if s == nil {
		return errors.New("frame session is nil")
	}
	if s.IsClosed() {
		return net.ErrClosed
	}
	errCh := make(chan error, 1)
	select {
	case s.writeCh <- probeChainFrameWriteRequest{frame: frame, errCh: errCh}:
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

func (s *probeChainFrameSession) writeFrameAsync(frame probeChainFrame) {
	if s == nil || s.IsClosed() {
		return
	}
	errCh := make(chan error, 1)
	select {
	case s.writeCh <- probeChainFrameWriteRequest{frame: frame, errCh: errCh}:
	case <-s.closeCh:
	}
}

type probeChainFrameStream struct {
	session *probeChainFrameSession
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

func newProbeChainFrameStream(session *probeChainFrameSession, streamID uint64) *probeChainFrameStream {
	return &probeChainFrameStream{
		session:    session,
		id:         streamID,
		readCh:     make(chan []byte, probeChainFrameSessionInboundBuffer),
		localDone:  make(chan struct{}),
		remoteDone: make(chan struct{}),
	}
}

func (s *probeChainFrameStream) Read(payload []byte) (int, error) {
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

		timer, timerCh := deadlineTimer(s.readDeadline.Load())
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
			return 0, osErrTimeout{}
		case <-s.localDone:
			if timer != nil {
				timer.Stop()
			}
			return 0, io.ErrClosedPipe
		}
	}
}

func (s *probeChainFrameStream) Write(payload []byte) (int, error) {
	if s == nil {
		return 0, io.ErrClosedPipe
	}
	if len(payload) == 0 {
		return 0, nil
	}
	timer, timerCh := deadlineTimer(s.writeDeadline.Load())
	done := make(chan error, 1)
	data := append([]byte(nil), payload...)
	go func() {
		done <- s.session.writeFrame(probeChainFrame{Kind: probeChainFrameKindData, StreamID: s.id, Data: data})
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
		return 0, osErrTimeout{}
	case <-s.localDone:
		if timer != nil {
			timer.Stop()
		}
		return 0, io.ErrClosedPipe
	}
}

func (s *probeChainFrameStream) Close() error {
	if s == nil {
		return nil
	}
	return s.closeLocal()
}

func (s *probeChainFrameStream) CloseWrite() error {
	if s == nil {
		return nil
	}
	return s.session.writeFrame(probeChainFrame{Kind: probeChainFrameKindClose, StreamID: s.id})
}

func (s *probeChainFrameStream) closeLocal() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.localDone)
		if s.session != nil {
			err = s.session.writeFrame(probeChainFrame{Kind: probeChainFrameKindClose, StreamID: s.id})
			s.session.removeStream(s.id, s)
		}
	})
	return err
}

func (s *probeChainFrameStream) closeRemote(err error) error {
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

func (s *probeChainFrameStream) deliverData(payload []byte) {
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

func (s *probeChainFrameStream) LocalAddr() net.Addr {
	if s == nil || s.session == nil {
		return probeChainFrameSessionAddr{}
	}
	return s.session.local
}

func (s *probeChainFrameStream) RemoteAddr() net.Addr {
	if s == nil || s.session == nil {
		return probeChainFrameSessionAddr{}
	}
	return s.session.remote
}

func (s *probeChainFrameStream) SetDeadline(t time.Time) error {
	_ = s.SetReadDeadline(t)
	_ = s.SetWriteDeadline(t)
	return nil
}

func (s *probeChainFrameStream) SetReadDeadline(t time.Time) error {
	if s == nil {
		return nil
	}
	s.readDeadline.Store(t)
	return nil
}

func (s *probeChainFrameStream) SetWriteDeadline(t time.Time) error {
	if s == nil {
		return nil
	}
	s.writeDeadline.Store(t)
	return nil
}

func deadlineTimer(raw any) (*time.Timer, <-chan time.Time) {
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

type osErrTimeout struct{}

func (osErrTimeout) Error() string   { return "i/o timeout" }
func (osErrTimeout) Timeout() bool   { return true }
func (osErrTimeout) Temporary() bool { return true }
