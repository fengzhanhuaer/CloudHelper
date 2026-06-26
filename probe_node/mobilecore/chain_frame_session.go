package mobilecore

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	mobileChainFrameControlOpen             = "open"
	mobileChainFrameControlOpenResult       = "open_result"
	mobileChainFrameControlOpenUpdate       = "open_update"
	mobileChainFrameControlOpenUpdateResult = "open_update_result"
	mobileChainFrameControlClose            = "close"
	mobileChainFrameControlError            = "error"
	mobileChainFrameControlHello            = "hello"
	mobileChainFrameControlHelloAck         = "hello_ack"
	mobileChainFrameControlFin              = "fin"
	mobileChainFrameControlReset            = "rst"
	mobileChainFrameControlWindowUpdate     = "window_update"

	mobileChainFrameSessionInboundBuffer = 64
	mobileChainFramePingInterval         = 10 * time.Second
	mobileChainFramePingTimeout          = 5 * time.Second
	mobileChainFrameOpenResultTimeout    = 30 * time.Second
	mobileChainFramePreferredDataBytes   = 16 * 1024
	mobileChainFrameRealtimeDataBytes    = 4 * 1024
	mobileChainFrameBulkDataBytes        = 64 * 1024
)

type mobileChainFrameSession struct {
	conn      net.Conn
	local     mobileChainFrameSessionAddr
	remote    mobileChainFrameSessionAddr
	initiator bool

	controlWriteCh chan mobileChainFrameWriteRequest
	dataWriteCh    chan mobileChainFrameWriteRequest
	closeCh        chan struct{}
	closed         atomic.Bool

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

	configMu        sync.RWMutex
	localConfig     mobileChainFrameSessionConfig
	remoteConfig    mobileChainFrameSessionConfig
	effectiveConfig mobileChainFrameSessionConfig
}

type mobileChainFrameWriteRequest struct {
	frame mobileChainFrame
	errCh chan error
}

type mobileChainFrameSessionControl struct {
	Type    string                         `json:"type"`
	OK      bool                           `json:"ok,omitempty"`
	Error   string                         `json:"error,omitempty"`
	Request *linkTunnelOpenRequest         `json:"request,omitempty"`
	Mobile  *mobileChainTunnelOpenRequest  `json:"mobile,omitempty"`
	Config  *mobileChainFrameSessionConfig `json:"config,omitempty"`
}

type mobileChainFrameSessionConfig struct {
	Version              int      `json:"version"`
	Features             []string `json:"features,omitempty"`
	MaxFrameData         int      `json:"max_frame_data"`
	MinFrameData         int      `json:"min_frame_data"`
	PreferredFrameData   int      `json:"preferred_frame_data"`
	BulkFrameData        int      `json:"bulk_frame_data"`
	MaxControlBytes      int      `json:"max_control_bytes"`
	MaxConcurrentStreams int      `json:"max_concurrent_streams"`
	InitialStreamWindow  int      `json:"initial_stream_window"`
	InitialSessionWindow int      `json:"initial_session_window"`
	IdleTimeoutMS        int64    `json:"idle_timeout_ms"`
	PingIntervalMS       int64    `json:"ping_interval_ms"`
	RealtimeFrameData    int      `json:"realtime_preferred_frame_data"`
}

type mobileChainFrameOpenResult struct {
	OK    bool
	Error string
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
	localConfig := defaultMobileChainFrameSessionConfig()
	s := &mobileChainFrameSession{
		conn:            conn,
		local:           mobileChainFrameSessionAddr{label: conn.LocalAddr().String()},
		remote:          mobileChainFrameSessionAddr{label: conn.RemoteAddr().String()},
		initiator:       initiator,
		controlWriteCh:  make(chan mobileChainFrameWriteRequest, mobileChainFrameSessionInboundBuffer),
		dataWriteCh:     make(chan mobileChainFrameWriteRequest, mobileChainFrameSessionInboundBuffer),
		closeCh:         make(chan struct{}),
		streams:         make(map[uint64]*mobileChainFrameStream),
		acceptCh:        make(chan *mobileChainFrameStream, mobileChainFrameSessionInboundBuffer),
		pendingPings:    make(map[uint64]time.Time),
		localConfig:     localConfig,
		effectiveConfig: localConfig,
	}
	s.nextStreamID.Store(start)
	go s.writeLoop()
	go s.readLoop()
	go s.pingLoop()
	go func() {
		_ = s.writeControlFrame(mobileChainFrame{Kind: mobileChainFrameKindControl}, mobileChainFrameSessionControl{Type: mobileChainFrameControlHello, Config: &localConfig})
	}()
	return s, nil
}

func (s *mobileChainFrameSession) Open() (net.Conn, error) {
	return s.openWithRequest(nil, nil, false, 0)
}

func (s *mobileChainFrameSession) OpenWithRequest(req linkTunnelOpenRequest, timeout time.Duration) (net.Conn, error) {
	return s.openWithRequest(&req, nil, true, timeout)
}

func (s *mobileChainFrameSession) OpenWithMobileRequest(req mobileChainTunnelOpenRequest, timeout time.Duration) (net.Conn, error) {
	return s.openWithRequest(nil, &req, true, timeout)
}

func (s *mobileChainFrameSession) openWithRequest(req *linkTunnelOpenRequest, mobileReq *mobileChainTunnelOpenRequest, waitResult bool, timeout time.Duration) (net.Conn, error) {
	if s == nil {
		return nil, errors.New("frame session is nil")
	}
	if s.IsClosed() {
		return nil, net.ErrClosed
	}
	streamID := s.nextStreamID.Add(2) - 2
	stream := newMobileChainFrameStream(s, streamID)
	s.registerStream(stream)
	control := mobileChainFrameSessionControl{Type: mobileChainFrameControlOpen}
	if req != nil {
		request := *req
		stream.setOpenRequest(request)
		control.Request = &request
	}
	if mobileReq != nil {
		request := *mobileReq
		stream.setMobileOpenRequest(request)
		control.Mobile = &request
	}
	if err := s.writeControlFrame(mobileChainFrame{Kind: mobileChainFrameKindControl, StreamID: streamID}, control); err != nil {
		s.removeStream(streamID, stream)
		_ = stream.closeLocal()
		return nil, err
	}
	if waitResult {
		if timeout <= 0 {
			timeout = mobileChainFrameOpenResultTimeout
		}
		if err := stream.waitOpenResult(timeout); err != nil {
			_ = stream.closeLocal()
			return nil, err
		}
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

func (s *mobileChainFrameSession) NegotiatedConfig() mobileChainFrameSessionConfig {
	if s == nil {
		return defaultMobileChainFrameSessionConfig()
	}
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.effectiveConfig
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
		case req := <-s.controlWriteCh:
			if !s.writeOne(req) {
				return
			}
			continue
		default:
		}
		select {
		case <-s.closeCh:
			return
		case req := <-s.controlWriteCh:
			if !s.writeOne(req) {
				return
			}
		case req := <-s.dataWriteCh:
			if !s.writeOne(req) {
				return
			}
		}
	}
}

func (s *mobileChainFrameSession) writeOne(req mobileChainFrameWriteRequest) bool {
	err := writeMobileChainFrame(s.conn, req.frame)
	select {
	case req.errCh <- err:
	default:
	}
	if err != nil {
		_ = s.Close()
		return false
	}
	return true
}

func (s *mobileChainFrameSession) enqueueWriteFrame(frame mobileChainFrame, errCh chan error) error {
	req := mobileChainFrameWriteRequest{frame: frame, errCh: errCh}
	targetCh := s.dataWriteCh
	if frame.Kind != mobileChainFrameKindData {
		targetCh = s.controlWriteCh
	}
	select {
	case targetCh <- req:
		return nil
	case <-s.closeCh:
		return net.ErrClosed
	}
}

func (s *mobileChainFrameSession) finishWriteFrame(errCh chan error) error {
	select {
	case err := <-errCh:
		return err
	case <-s.closeCh:
		return net.ErrClosed
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
	case mobileChainFrameControlHello:
		s.handleHelloControl(control)
	case mobileChainFrameControlHelloAck:
		s.handleHelloControl(control)
	case mobileChainFrameControlOpen:
		stream := newMobileChainFrameStream(s, frame.StreamID)
		if control.Request != nil {
			stream.setOpenRequest(*control.Request)
		}
		if control.Mobile != nil {
			stream.setMobileOpenRequest(*control.Mobile)
		}
		s.registerStream(stream)
		select {
		case s.acceptCh <- stream:
		case <-s.closeCh:
			_ = stream.closeRemote(io.ErrClosedPipe)
		}
	case mobileChainFrameControlOpenResult:
		if stream := s.getStream(frame.StreamID); stream != nil {
			stream.deliverOpenResult(mobileChainFrameOpenResult{OK: control.OK, Error: control.Error})
		}
	case mobileChainFrameControlOpenUpdate:
		if stream := s.getStream(frame.StreamID); stream != nil {
			if control.Request != nil {
				stream.deliverOpenUpdate(*control.Request)
			}
			if control.Mobile != nil {
				stream.deliverMobileOpenUpdate(*control.Mobile)
			}
		}
	case mobileChainFrameControlOpenUpdateResult:
		if stream := s.getStream(frame.StreamID); stream != nil {
			stream.deliverOpenUpdateResult(mobileChainFrameOpenResult{OK: control.OK, Error: control.Error})
		}
	case mobileChainFrameControlFin:
		if stream := s.getStream(frame.StreamID); stream != nil {
			_ = stream.closeRemote(io.EOF)
		}
	case mobileChainFrameControlClose:
		if stream := s.getStream(frame.StreamID); stream != nil {
			_ = stream.closeRemote(io.EOF)
		}
	case mobileChainFrameControlError, mobileChainFrameControlReset:
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
	return s.writeControlFrame(mobileChainFrame{Kind: mobileChainFrameKindControl, StreamID: streamID}, mobileChainFrameSessionControl{Type: controlType, Error: errText})
}

func (s *mobileChainFrameSession) writeControlFrame(frame mobileChainFrame, control mobileChainFrameSessionControl) error {
	payload, err := marshalMobileChainFrameControl(control)
	if err != nil {
		return err
	}
	frame.Kind = mobileChainFrameKindControl
	frame.Control = payload
	return s.writeFrame(frame)
}

func (s *mobileChainFrameSession) writeControlFrameAsync(frame mobileChainFrame, control mobileChainFrameSessionControl) {
	payload, err := marshalMobileChainFrameControl(control)
	if err != nil {
		return
	}
	frame.Kind = mobileChainFrameKindControl
	frame.Control = payload
	s.writeFrameAsync(frame)
}

func (s *mobileChainFrameSession) handleHelloControl(control mobileChainFrameSessionControl) {
	if s == nil || control.Config == nil {
		return
	}
	s.configMu.Lock()
	s.remoteConfig = normalizeMobileChainFrameSessionConfig(*control.Config)
	s.effectiveConfig = mergeMobileChainFrameSessionConfig(s.localConfig, s.remoteConfig)
	effective := s.effectiveConfig
	s.configMu.Unlock()
	if control.Type == mobileChainFrameControlHello {
		s.writeControlFrameAsync(mobileChainFrame{Kind: mobileChainFrameKindControl}, mobileChainFrameSessionControl{Type: mobileChainFrameControlHelloAck, Config: &effective})
	}
}

func (s *mobileChainFrameSession) writeFrame(frame mobileChainFrame) error {
	if s == nil {
		return errors.New("frame session is nil")
	}
	if s.IsClosed() {
		return net.ErrClosed
	}
	errCh := make(chan error, 1)
	if err := s.enqueueWriteFrame(frame, errCh); err != nil {
		return err
	}
	return s.finishWriteFrame(errCh)
}

func (s *mobileChainFrameSession) writeFrameAsync(frame mobileChainFrame) {
	if s == nil || s.IsClosed() {
		return
	}
	errCh := make(chan error, 1)
	_ = s.enqueueWriteFrame(frame, errCh)
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

	openMu                     sync.RWMutex
	openRequest                linkTunnelOpenRequest
	openRequestAvailable       bool
	mobileOpenRequest          mobileChainTunnelOpenRequest
	mobileOpenRequestAvailable bool
	priority                   string
	openResultCh               chan mobileChainFrameOpenResult
	openUpdateCh               chan linkTunnelOpenRequest
	mobileOpenUpdateCh         chan mobileChainTunnelOpenRequest
	openUpdateResultCh         chan mobileChainFrameOpenResult
}

func newMobileChainFrameStream(session *mobileChainFrameSession, streamID uint64) *mobileChainFrameStream {
	return &mobileChainFrameStream{
		session:            session,
		id:                 streamID,
		readCh:             make(chan []byte, mobileChainFrameSessionInboundBuffer),
		localDone:          make(chan struct{}),
		remoteDone:         make(chan struct{}),
		openResultCh:       make(chan mobileChainFrameOpenResult, 1),
		openUpdateCh:       make(chan linkTunnelOpenRequest, 1),
		mobileOpenUpdateCh: make(chan mobileChainTunnelOpenRequest, 1),
		openUpdateResultCh: make(chan mobileChainFrameOpenResult, 1),
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
	if s == nil || s.session == nil {
		return 0, io.ErrClosedPipe
	}
	if len(payload) == 0 {
		return 0, nil
	}
	written := 0
	for written < len(payload) {
		chunkBytes := s.frameDataChunkBytes(len(payload) - written)
		end := written + chunkBytes
		if end > len(payload) {
			end = len(payload)
		}
		timer, timerCh := mobileChainDeadlineTimer(s.writeDeadline.Load())
		done := make(chan error, 1)
		data := append([]byte(nil), payload[written:end]...)
		go func() {
			done <- s.session.writeFrame(mobileChainFrame{Kind: mobileChainFrameKindData, StreamID: s.id, Data: data})
		}()
		select {
		case err := <-done:
			if timer != nil {
				timer.Stop()
			}
			if err != nil {
				return written, err
			}
			written = end
		case <-timerCh:
			return written, mobileChainTimeoutError{}
		case <-s.localDone:
			if timer != nil {
				timer.Stop()
			}
			return written, io.ErrClosedPipe
		}
	}
	return written, nil
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
	return s.session.writeControlFrame(mobileChainFrame{Kind: mobileChainFrameKindControl, StreamID: s.id}, mobileChainFrameSessionControl{Type: mobileChainFrameControlFin})
}

func (s *mobileChainFrameStream) closeLocal() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.localDone)
		if s.session != nil {
			err = s.session.writeControlFrame(mobileChainFrame{Kind: mobileChainFrameKindControl, StreamID: s.id}, mobileChainFrameSessionControl{Type: mobileChainFrameControlClose})
			s.session.removeStream(s.id, s)
		}
	})
	return err
}

func (s *mobileChainFrameStream) Reset(errText string) error {
	if s == nil {
		return nil
	}
	if strings.TrimSpace(errText) == "" {
		errText = "stream reset"
	}
	return s.session.writeControlFrame(mobileChainFrame{Kind: mobileChainFrameKindControl, StreamID: s.id}, mobileChainFrameSessionControl{Type: mobileChainFrameControlReset, Error: errText})
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

func (s *mobileChainFrameStream) setOpenRequest(req linkTunnelOpenRequest) {
	if s == nil {
		return
	}
	s.openMu.Lock()
	s.openRequest = req
	s.openRequestAvailable = true
	s.priority = resolveMobileChainFrameLinkPriority(req)
	s.openMu.Unlock()
}

func (s *mobileChainFrameStream) OpenRequest() (linkTunnelOpenRequest, bool) {
	if s == nil {
		return linkTunnelOpenRequest{}, false
	}
	s.openMu.RLock()
	defer s.openMu.RUnlock()
	return s.openRequest, s.openRequestAvailable
}

func (s *mobileChainFrameStream) setMobileOpenRequest(req mobileChainTunnelOpenRequest) {
	if s == nil {
		return
	}
	s.openMu.Lock()
	s.mobileOpenRequest = req
	s.mobileOpenRequestAvailable = true
	s.priority = resolveMobileChainFrameMobilePriority(req)
	s.openMu.Unlock()
}

func (s *mobileChainFrameStream) MobileOpenRequest() (mobileChainTunnelOpenRequest, bool) {
	if s == nil {
		return mobileChainTunnelOpenRequest{}, false
	}
	s.openMu.RLock()
	defer s.openMu.RUnlock()
	return s.mobileOpenRequest, s.mobileOpenRequestAvailable
}

func (s *mobileChainFrameStream) frameDataChunkBytes(available int) int {
	if s == nil || s.session == nil {
		return mobileChainFrameRealtimeDataBytes
	}
	cfg := s.session.NegotiatedConfig()
	chunk := cfg.PreferredFrameData
	switch s.Priority() {
	case "realtime":
		chunk = cfg.RealtimeFrameData
	case "bulk":
		chunk = cfg.BulkFrameData
	default:
		if available <= cfg.RealtimeFrameData {
			chunk = cfg.RealtimeFrameData
		} else if available >= cfg.BulkFrameData {
			chunk = cfg.BulkFrameData
		}
	}
	chunk = clampMobileChainFrameDataBytes(chunk, cfg.PreferredFrameData, cfg.MaxFrameData)
	if available > 0 && chunk > available {
		return available
	}
	return chunk
}

func (s *mobileChainFrameStream) Priority() string {
	if s == nil {
		return "normal"
	}
	s.openMu.RLock()
	defer s.openMu.RUnlock()
	return normalizeMobileChainFramePriority(s.priority)
}

func (s *mobileChainFrameStream) RespondOpen(resp linkTunnelOpenResponse) error {
	if s == nil || s.session == nil {
		return io.ErrClosedPipe
	}
	return s.session.writeControlFrame(mobileChainFrame{Kind: mobileChainFrameKindControl, StreamID: s.id}, mobileChainFrameSessionControl{
		Type:  mobileChainFrameControlOpenResult,
		OK:    resp.OK,
		Error: strings.TrimSpace(resp.Error),
	})
}

func (s *mobileChainFrameStream) RespondMobileOpen(resp mobileChainTunnelOpenResponse) error {
	if s == nil || s.session == nil {
		return io.ErrClosedPipe
	}
	return s.session.writeControlFrame(mobileChainFrame{Kind: mobileChainFrameKindControl, StreamID: s.id}, mobileChainFrameSessionControl{
		Type:  mobileChainFrameControlOpenResult,
		OK:    resp.OK,
		Error: strings.TrimSpace(resp.Error),
	})
}

func (s *mobileChainFrameStream) waitOpenResult(timeout time.Duration) error {
	if s == nil {
		return io.ErrClosedPipe
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-s.openResultCh:
		if result.OK {
			return nil
		}
		if strings.TrimSpace(result.Error) == "" {
			return errors.New("remote open rejected")
		}
		return errors.New(strings.TrimSpace(result.Error))
	case <-timer.C:
		return mobileChainTimeoutError{}
	case <-s.localDone:
		return io.ErrClosedPipe
	case <-s.remoteDone:
		if s.remoteErr != nil {
			return s.remoteErr
		}
		return io.ErrClosedPipe
	}
}

func (s *mobileChainFrameStream) deliverOpenResult(result mobileChainFrameOpenResult) {
	if s == nil {
		return
	}
	select {
	case s.openResultCh <- result:
	default:
	}
}

func (s *mobileChainFrameStream) SendOpenUpdate(req linkTunnelOpenRequest, timeout time.Duration) error {
	if s == nil || s.session == nil {
		return io.ErrClosedPipe
	}
	if timeout <= 0 {
		timeout = mobileChainFrameOpenResultTimeout
	}
	request := req
	if err := s.session.writeControlFrame(mobileChainFrame{Kind: mobileChainFrameKindControl, StreamID: s.id}, mobileChainFrameSessionControl{
		Type:    mobileChainFrameControlOpenUpdate,
		Request: &request,
	}); err != nil {
		return err
	}
	return s.waitOpenUpdateResult(timeout)
}

func (s *mobileChainFrameStream) WaitOpenUpdate(timeout time.Duration) (linkTunnelOpenRequest, error) {
	if s == nil {
		return linkTunnelOpenRequest{}, io.ErrClosedPipe
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case req := <-s.openUpdateCh:
		return req, nil
	case <-timer.C:
		return linkTunnelOpenRequest{}, mobileChainTimeoutError{}
	case <-s.localDone:
		return linkTunnelOpenRequest{}, io.ErrClosedPipe
	case <-s.remoteDone:
		if s.remoteErr != nil {
			return linkTunnelOpenRequest{}, s.remoteErr
		}
		return linkTunnelOpenRequest{}, io.ErrClosedPipe
	}
}

func (s *mobileChainFrameStream) RespondOpenUpdate(resp linkTunnelOpenResponse) error {
	if s == nil || s.session == nil {
		return io.ErrClosedPipe
	}
	return s.session.writeControlFrame(mobileChainFrame{Kind: mobileChainFrameKindControl, StreamID: s.id}, mobileChainFrameSessionControl{
		Type:  mobileChainFrameControlOpenUpdateResult,
		OK:    resp.OK,
		Error: strings.TrimSpace(resp.Error),
	})
}

func (s *mobileChainFrameStream) deliverOpenUpdate(req linkTunnelOpenRequest) {
	if s == nil {
		return
	}
	select {
	case s.openUpdateCh <- req:
	default:
	}
}

func (s *mobileChainFrameStream) deliverMobileOpenUpdate(req mobileChainTunnelOpenRequest) {
	if s == nil {
		return
	}
	select {
	case s.mobileOpenUpdateCh <- req:
	default:
	}
}

func (s *mobileChainFrameStream) deliverOpenUpdateResult(result mobileChainFrameOpenResult) {
	if s == nil {
		return
	}
	select {
	case s.openUpdateResultCh <- result:
	default:
	}
}

func (s *mobileChainFrameStream) waitOpenUpdateResult(timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-s.openUpdateResultCh:
		if result.OK {
			return nil
		}
		if strings.TrimSpace(result.Error) == "" {
			return errors.New("remote open update rejected")
		}
		return errors.New(strings.TrimSpace(result.Error))
	case <-timer.C:
		return mobileChainTimeoutError{}
	case <-s.localDone:
		return io.ErrClosedPipe
	case <-s.remoteDone:
		if s.remoteErr != nil {
			return s.remoteErr
		}
		return io.ErrClosedPipe
	}
}

func (s *mobileChainFrameStream) LocalAddr() net.Addr {
	if s == nil || s.session == nil {
		return mobileChainFrameSessionAddr{}
	}
	return s.session.local
}

func (s *mobileChainFrameStream) RemoteAddr() net.Addr {
	if s == nil || s.session == nil {
		return mobileChainFrameSessionAddr{}
	}
	return s.session.remote
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

func normalizeMobileChainFramePriority(priority string) string {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "realtime", "interactive", "latency":
		return "realtime"
	case "bulk", "throughput":
		return "bulk"
	default:
		return "normal"
	}
}

func resolveMobileChainFrameLinkPriority(req linkTunnelOpenRequest) string {
	if req.LatencySensitive {
		return "realtime"
	}
	switch strings.ToLower(strings.TrimSpace(req.AppProtocol)) {
	case "rdp", "vnc", "nomachine", "ssh", "udp-association", "interactive":
		return "realtime"
	}
	if strings.EqualFold(strings.TrimSpace(req.ResumePolicy), "rebind") {
		return "realtime"
	}
	return normalizeMobileChainFramePriority(req.Priority)
}

func resolveMobileChainFrameMobilePriority(req mobileChainTunnelOpenRequest) string {
	if req.LatencySensitive {
		return "realtime"
	}
	switch strings.ToLower(strings.TrimSpace(req.AppProtocol)) {
	case "rdp", "vnc", "nomachine", "ssh", "udp-association", "interactive":
		return "realtime"
	}
	if strings.EqualFold(strings.TrimSpace(req.ResumePolicy), "rebind") {
		return "realtime"
	}
	return normalizeMobileChainFramePriority(req.Priority)
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

func defaultMobileChainFrameSessionConfig() mobileChainFrameSessionConfig {
	return mobileChainFrameSessionConfig{
		Version:              mobileChainFrameVersion,
		Features:             []string{"open_result", "open_update", "fin", "rst", "window_update"},
		MaxFrameData:         mobileChainFrameMaxDataBytes,
		MinFrameData:         mobileChainFrameRealtimeDataBytes,
		PreferredFrameData:   mobileChainFramePreferredDataBytes,
		BulkFrameData:        mobileChainFrameBulkDataBytes,
		MaxControlBytes:      mobileChainFrameMaxControlBytes,
		MaxConcurrentStreams: mobileChainFrameSessionInboundBuffer,
		InitialStreamWindow:  mobileChainFrameMaxDataBytes * mobileChainFrameSessionInboundBuffer,
		InitialSessionWindow: mobileChainFrameMaxDataBytes * mobileChainFrameSessionInboundBuffer,
		IdleTimeoutMS:        int64((mobileChainFramePingInterval + mobileChainFramePingTimeout).Milliseconds()),
		PingIntervalMS:       int64(mobileChainFramePingInterval.Milliseconds()),
		RealtimeFrameData:    mobileChainFrameRealtimeDataBytes,
	}
}

func normalizeMobileChainFrameSessionConfig(cfg mobileChainFrameSessionConfig) mobileChainFrameSessionConfig {
	if cfg.Version <= 0 {
		cfg.Version = mobileChainFrameVersion
	}
	if cfg.MaxFrameData <= 0 || cfg.MaxFrameData > mobileChainFrameMaxDataBytes {
		cfg.MaxFrameData = mobileChainFrameMaxDataBytes
	}
	if cfg.MinFrameData <= 0 || cfg.MinFrameData > cfg.MaxFrameData {
		cfg.MinFrameData = mobileChainFrameRealtimeDataBytes
		if cfg.MinFrameData > cfg.MaxFrameData {
			cfg.MinFrameData = cfg.MaxFrameData
		}
	}
	if cfg.PreferredFrameData <= 0 || cfg.PreferredFrameData > cfg.MaxFrameData {
		cfg.PreferredFrameData = cfg.MaxFrameData
	}
	if cfg.PreferredFrameData < cfg.MinFrameData {
		cfg.PreferredFrameData = cfg.MinFrameData
	}
	if cfg.BulkFrameData <= 0 || cfg.BulkFrameData > cfg.MaxFrameData {
		cfg.BulkFrameData = cfg.MaxFrameData
	}
	if cfg.BulkFrameData < cfg.PreferredFrameData {
		cfg.BulkFrameData = cfg.PreferredFrameData
	}
	if cfg.RealtimeFrameData <= 0 || cfg.RealtimeFrameData > cfg.PreferredFrameData {
		cfg.RealtimeFrameData = mobileChainFrameRealtimeDataBytes
		if cfg.RealtimeFrameData > cfg.PreferredFrameData {
			cfg.RealtimeFrameData = cfg.PreferredFrameData
		}
	}
	if cfg.RealtimeFrameData > cfg.MinFrameData {
		cfg.RealtimeFrameData = cfg.MinFrameData
	}
	if cfg.MaxControlBytes <= 0 || cfg.MaxControlBytes > mobileChainFrameMaxControlBytes {
		cfg.MaxControlBytes = mobileChainFrameMaxControlBytes
	}
	if cfg.MaxConcurrentStreams <= 0 {
		cfg.MaxConcurrentStreams = mobileChainFrameSessionInboundBuffer
	}
	if cfg.InitialStreamWindow <= 0 {
		cfg.InitialStreamWindow = cfg.MaxFrameData * mobileChainFrameSessionInboundBuffer
	}
	if cfg.InitialSessionWindow <= 0 {
		cfg.InitialSessionWindow = cfg.InitialStreamWindow
	}
	if cfg.IdleTimeoutMS <= 0 {
		cfg.IdleTimeoutMS = int64((mobileChainFramePingInterval + mobileChainFramePingTimeout).Milliseconds())
	}
	if cfg.PingIntervalMS <= 0 {
		cfg.PingIntervalMS = int64(mobileChainFramePingInterval.Milliseconds())
	}
	return cfg
}

func mergeMobileChainFrameSessionConfig(local mobileChainFrameSessionConfig, remote mobileChainFrameSessionConfig) mobileChainFrameSessionConfig {
	local = normalizeMobileChainFrameSessionConfig(local)
	remote = normalizeMobileChainFrameSessionConfig(remote)
	out := local
	if remote.Version < out.Version {
		out.Version = remote.Version
	}
	out.Features = intersectMobileChainFrameFeatures(local.Features, remote.Features)
	out.MaxFrameData = minPositiveMobileInt(local.MaxFrameData, remote.MaxFrameData)
	out.MinFrameData = minPositiveMobileInt(local.MinFrameData, remote.MinFrameData)
	out.PreferredFrameData = minPositiveMobileInt(local.PreferredFrameData, remote.PreferredFrameData)
	if out.PreferredFrameData > out.MaxFrameData {
		out.PreferredFrameData = out.MaxFrameData
	}
	out.BulkFrameData = minPositiveMobileInt(local.BulkFrameData, remote.BulkFrameData)
	if out.BulkFrameData > out.MaxFrameData {
		out.BulkFrameData = out.MaxFrameData
	}
	out.RealtimeFrameData = minPositiveMobileInt(local.RealtimeFrameData, remote.RealtimeFrameData)
	if out.RealtimeFrameData > out.PreferredFrameData {
		out.RealtimeFrameData = out.PreferredFrameData
	}
	out.MaxControlBytes = minPositiveMobileInt(local.MaxControlBytes, remote.MaxControlBytes)
	out.MaxConcurrentStreams = minPositiveMobileInt(local.MaxConcurrentStreams, remote.MaxConcurrentStreams)
	out.InitialStreamWindow = minPositiveMobileInt(local.InitialStreamWindow, remote.InitialStreamWindow)
	out.InitialSessionWindow = minPositiveMobileInt(local.InitialSessionWindow, remote.InitialSessionWindow)
	out.IdleTimeoutMS = minPositiveMobileInt64(local.IdleTimeoutMS, remote.IdleTimeoutMS)
	out.PingIntervalMS = minPositiveMobileInt64(local.PingIntervalMS, remote.PingIntervalMS)
	return normalizeMobileChainFrameSessionConfig(out)
}

func intersectMobileChainFrameFeatures(left []string, right []string) []string {
	if len(left) == 0 || len(right) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(right))
	for _, item := range right {
		clean := strings.TrimSpace(item)
		if clean != "" {
			seen[clean] = struct{}{}
		}
	}
	out := make([]string, 0, len(left))
	added := map[string]struct{}{}
	for _, item := range left {
		clean := strings.TrimSpace(item)
		if clean == "" {
			continue
		}
		if _, ok := seen[clean]; !ok {
			continue
		}
		if _, ok := added[clean]; ok {
			continue
		}
		added[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func minPositiveMobileInt(left int, right int) int {
	if left <= 0 {
		return right
	}
	if right <= 0 {
		return left
	}
	if left < right {
		return left
	}
	return right
}

func minPositiveMobileInt64(left int64, right int64) int64 {
	if left <= 0 {
		return right
	}
	if right <= 0 {
		return left
	}
	if left < right {
		return left
	}
	return right
}

func clampMobileChainFrameDataBytes(value int, fallback int, maxFrameData int) int {
	if fallback <= 0 {
		fallback = mobileChainFramePreferredDataBytes
	}
	if value <= 0 {
		value = fallback
	}
	if maxFrameData <= 0 || maxFrameData > mobileChainFrameMaxDataBytes {
		maxFrameData = mobileChainFrameMaxDataBytes
	}
	if value > maxFrameData {
		value = maxFrameData
	}
	if value > mobileChainFrameMaxDataBytes {
		value = mobileChainFrameMaxDataBytes
	}
	if value <= 0 {
		value = mobileChainFrameRealtimeDataBytes
	}
	return value
}
