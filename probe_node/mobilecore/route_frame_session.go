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
	mobileRouteFrameControlOpen             = "open"
	mobileRouteFrameControlOpenResult       = "open_result"
	mobileRouteFrameControlOpenUpdate       = "open_update"
	mobileRouteFrameControlOpenUpdateResult = "open_update_result"
	mobileRouteFrameControlClose            = "close"
	mobileRouteFrameControlError            = "error"
	mobileRouteFrameControlHello            = "hello"
	mobileRouteFrameControlHelloAck         = "hello_ack"
	mobileRouteFrameControlFin              = "fin"
	mobileRouteFrameControlReset            = "rst"
	mobileRouteFrameControlWindowUpdate     = "window_update"

	mobileRouteFrameSessionInboundBuffer = 64
	mobileRouteFramePingInterval         = 10 * time.Second
	mobileRouteFramePingTimeout          = 5 * time.Second
	mobileRouteFrameOpenResultTimeout    = 30 * time.Second
	mobileRouteFramePreferredDataBytes   = 16 * 1024
	mobileRouteFrameRealtimeDataBytes    = 4 * 1024
	mobileRouteFrameBulkDataBytes        = 64 * 1024
)

type mobileRouteFrameSession struct {
	conn      net.Conn
	local     mobileRouteFrameSessionAddr
	remote    mobileRouteFrameSessionAddr
	initiator bool

	controlWriteCh chan mobileRouteFrameWriteRequest
	dataWriteCh    chan mobileRouteFrameWriteRequest
	closeCh        chan struct{}
	closed         atomic.Bool

	nextStreamID atomic.Uint64
	streamsMu    sync.Mutex
	streams      map[uint64]*mobileRouteFrameStream
	acceptCh     chan *mobileRouteFrameStream

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
	localConfig     mobileRouteFrameSessionConfig
	remoteConfig    mobileRouteFrameSessionConfig
	effectiveConfig mobileRouteFrameSessionConfig
	readyCh         chan struct{}
	readyOnce       sync.Once
}

type mobileRouteFrameWriteRequest struct {
	frame mobileRouteFrame
	errCh chan error
}

type mobileRouteFrameSessionControl struct {
	Type    string                         `json:"type"`
	OK      bool                           `json:"ok,omitempty"`
	Error   string                         `json:"error,omitempty"`
	Request *routeTunnelOpenRequest        `json:"request,omitempty"`
	Mobile  *mobileRouteTunnelOpenRequest  `json:"mobile,omitempty"`
	Config  *mobileRouteFrameSessionConfig `json:"config,omitempty"`
}

type mobileRouteFrameSessionConfig struct {
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

type mobileRouteFrameOpenResult struct {
	OK    bool
	Error string
}

type mobileRouteFramePingStats struct {
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

type mobileRouteFrameSessionAddr struct {
	label string
}

func (a mobileRouteFrameSessionAddr) Network() string { return "mobile-route-frame" }
func (a mobileRouteFrameSessionAddr) String() string {
	if a.label == "" {
		return "mobile-route-frame"
	}
	return a.label
}

func newMobileRouteFrameClient(conn net.Conn) (*mobileRouteFrameSession, error) {
	return newMobileRouteFrameSession(conn, true)
}

func newMobileRouteFrameServer(conn net.Conn) (*mobileRouteFrameSession, error) {
	return newMobileRouteFrameSession(conn, false)
}

func newMobileRouteFrameSession(conn net.Conn, initiator bool) (*mobileRouteFrameSession, error) {
	if conn == nil {
		return nil, errors.New("frame session connection is nil")
	}
	start := uint64(1)
	if !initiator {
		start = 2
	}
	localConfig := defaultMobileRouteFrameSessionConfig()
	s := &mobileRouteFrameSession{
		conn:            conn,
		local:           mobileRouteFrameSessionAddr{label: conn.LocalAddr().String()},
		remote:          mobileRouteFrameSessionAddr{label: conn.RemoteAddr().String()},
		initiator:       initiator,
		controlWriteCh:  make(chan mobileRouteFrameWriteRequest, mobileRouteFrameSessionInboundBuffer),
		dataWriteCh:     make(chan mobileRouteFrameWriteRequest, mobileRouteFrameSessionInboundBuffer),
		closeCh:         make(chan struct{}),
		streams:         make(map[uint64]*mobileRouteFrameStream),
		acceptCh:        make(chan *mobileRouteFrameStream, mobileRouteFrameSessionInboundBuffer),
		pendingPings:    make(map[uint64]time.Time),
		localConfig:     localConfig,
		effectiveConfig: localConfig,
		readyCh:         make(chan struct{}),
	}
	s.nextStreamID.Store(start)
	go s.writeLoop()
	go s.readLoop()
	go s.pingLoop()
	go func() {
		_ = s.writeControlFrame(mobileRouteFrame{Kind: mobileRouteFrameKindControl}, mobileRouteFrameSessionControl{Type: mobileRouteFrameControlHello, Config: &localConfig})
	}()
	return s, nil
}

func (s *mobileRouteFrameSession) Open() (net.Conn, error) {
	return s.openWithRequest(nil, nil, false, 0)
}

func (s *mobileRouteFrameSession) OpenWithRequest(req routeTunnelOpenRequest, timeout time.Duration) (net.Conn, error) {
	return s.openWithRequest(&req, nil, true, timeout)
}

func (s *mobileRouteFrameSession) OpenWithMobileRequest(req mobileRouteTunnelOpenRequest, timeout time.Duration) (net.Conn, error) {
	return s.openWithRequest(nil, &req, true, timeout)
}

func (s *mobileRouteFrameSession) openWithRequest(req *routeTunnelOpenRequest, mobileReq *mobileRouteTunnelOpenRequest, waitResult bool, timeout time.Duration) (net.Conn, error) {
	if s == nil {
		return nil, errors.New("frame session is nil")
	}
	if s.IsClosed() {
		return nil, net.ErrClosed
	}
	streamID := s.nextStreamID.Add(2) - 2
	stream := newMobileRouteFrameStream(s, streamID)
	s.registerStream(stream)
	control := mobileRouteFrameSessionControl{Type: mobileRouteFrameControlOpen}
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
	if err := s.writeControlFrame(mobileRouteFrame{Kind: mobileRouteFrameKindControl, StreamID: streamID}, control); err != nil {
		s.removeStream(streamID, stream)
		_ = stream.closeLocal()
		return nil, err
	}
	if waitResult {
		if timeout <= 0 {
			timeout = mobileRouteFrameOpenResultTimeout
		}
		if err := stream.waitOpenResult(timeout); err != nil {
			_ = stream.closeLocal()
			return nil, err
		}
	}
	return stream, nil
}

func (s *mobileRouteFrameSession) Accept() (net.Conn, error) {
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

func (s *mobileRouteFrameSession) Close() error {
	if s == nil {
		return nil
	}
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(s.closeCh)
	_ = s.conn.Close()
	s.streamsMu.Lock()
	streams := make([]*mobileRouteFrameStream, 0, len(s.streams))
	for _, stream := range s.streams {
		streams = append(streams, stream)
	}
	s.streams = make(map[uint64]*mobileRouteFrameStream)
	s.streamsMu.Unlock()
	for _, stream := range streams {
		_ = stream.closeRemote(io.ErrClosedPipe)
	}
	close(s.acceptCh)
	return nil
}

func (s *mobileRouteFrameSession) IsClosed() bool {
	return s == nil || s.closed.Load()
}

func (s *mobileRouteFrameSession) NumStreams() int {
	if s == nil {
		return 0
	}
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	return len(s.streams)
}

func (s *mobileRouteFrameSession) PingStats() mobileRouteFramePingStats {
	if s == nil {
		return mobileRouteFramePingStats{}
	}
	lastPingUnixNS := s.lastPingUnixNS.Load()
	lastPongUnixNS := s.lastPongUnixNS.Load()
	stats := mobileRouteFramePingStats{
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

func (s *mobileRouteFrameSession) NegotiatedConfig() mobileRouteFrameSessionConfig {
	if s == nil {
		return defaultMobileRouteFrameSessionConfig()
	}
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.effectiveConfig
}

func (s *mobileRouteFrameSession) WaitReady(timeout time.Duration) bool {
	if s == nil {
		return false
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.readyCh:
		return true
	case <-s.closeCh:
		return false
	case <-timer.C:
		return false
	}
}

func (s *mobileRouteFrameSession) registerStream(stream *mobileRouteFrameStream) {
	if s == nil || stream == nil {
		return
	}
	s.streamsMu.Lock()
	s.streams[stream.id] = stream
	s.streamsMu.Unlock()
}

func (s *mobileRouteFrameSession) removeStream(streamID uint64, stream *mobileRouteFrameStream) {
	if s == nil {
		return
	}
	s.streamsMu.Lock()
	if current := s.streams[streamID]; current == stream || stream == nil {
		delete(s.streams, streamID)
	}
	s.streamsMu.Unlock()
}

func (s *mobileRouteFrameSession) getStream(streamID uint64) *mobileRouteFrameStream {
	if s == nil {
		return nil
	}
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	return s.streams[streamID]
}

func (s *mobileRouteFrameSession) readLoop() {
	for {
		frame, err := readMobileRouteFrame(s.conn)
		if err != nil {
			_ = s.Close()
			return
		}
		switch frame.Kind {
		case mobileRouteFrameKindControl:
			s.handleControlFrame(frame)
		case mobileRouteFrameKindData:
			if stream := s.getStream(frame.StreamID); stream != nil {
				stream.deliverData(frame.Data)
			}
		case mobileRouteFrameKindClose:
			if stream := s.getStream(frame.StreamID); stream != nil {
				_ = stream.closeRemote(io.EOF)
			}
		case mobileRouteFrameKindError:
			if stream := s.getStream(frame.StreamID); stream != nil {
				_ = stream.closeRemote(errors.New(string(frame.Control)))
			}
		case mobileRouteFrameKindPing:
			s.writeFrameAsync(mobileRouteFrame{Kind: mobileRouteFrameKindPong, StreamID: frame.StreamID, Seq: frame.Seq})
		case mobileRouteFrameKindPong:
			s.handlePongFrame(frame)
		}
	}
}

func (s *mobileRouteFrameSession) writeLoop() {
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

func (s *mobileRouteFrameSession) writeOne(req mobileRouteFrameWriteRequest) bool {
	err := writeMobileRouteFrame(s.conn, req.frame)
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

func (s *mobileRouteFrameSession) enqueueWriteFrame(frame mobileRouteFrame, errCh chan error) error {
	req := mobileRouteFrameWriteRequest{frame: frame, errCh: errCh}
	targetCh := s.dataWriteCh
	if frame.Kind != mobileRouteFrameKindData {
		targetCh = s.controlWriteCh
	}
	select {
	case targetCh <- req:
		return nil
	case <-s.closeCh:
		return net.ErrClosed
	}
}

func (s *mobileRouteFrameSession) finishWriteFrame(errCh chan error) error {
	select {
	case err := <-errCh:
		return err
	case <-s.closeCh:
		return net.ErrClosed
	}
}

func (s *mobileRouteFrameSession) pingLoop() {
	if s == nil {
		return
	}
	_ = s.sendPingFrame()
	ticker := time.NewTicker(mobileRouteFramePingInterval)
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

func (s *mobileRouteFrameSession) sendPingFrame() error {
	if s == nil || s.IsClosed() {
		return net.ErrClosed
	}
	seq := s.pingSeq.Add(1)
	now := time.Now()
	s.pingMu.Lock()
	s.pendingPings[seq] = now
	s.pingMu.Unlock()
	if err := s.writeFrame(mobileRouteFrame{Kind: mobileRouteFrameKindPing, Seq: seq}); err != nil {
		s.pingMu.Lock()
		delete(s.pendingPings, seq)
		s.pingMu.Unlock()
		return err
	}
	s.pingsSent.Add(1)
	s.lastPingUnixNS.Store(now.UnixNano())
	return nil
}

func (s *mobileRouteFrameSession) handlePongFrame(frame mobileRouteFrame) {
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

func (s *mobileRouteFrameSession) expirePingFrames(now time.Time) {
	if s == nil {
		return
	}
	expired := int64(0)
	s.pingMu.Lock()
	for seq, startedAt := range s.pendingPings {
		if now.Sub(startedAt) >= mobileRouteFramePingTimeout {
			delete(s.pendingPings, seq)
			expired++
		}
	}
	s.pingMu.Unlock()
	if expired > 0 {
		s.pingTimeouts.Add(expired)
	}
}

func (s *mobileRouteFrameSession) handleControlFrame(frame mobileRouteFrame) {
	var control mobileRouteFrameSessionControl
	if len(frame.Control) > 0 {
		_ = json.Unmarshal(frame.Control, &control)
	}
	switch control.Type {
	case mobileRouteFrameControlHello:
		s.handleHelloControl(control)
	case mobileRouteFrameControlHelloAck:
		s.handleHelloControl(control)
	case mobileRouteFrameControlOpen:
		stream := newMobileRouteFrameStream(s, frame.StreamID)
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
	case mobileRouteFrameControlOpenResult:
		if stream := s.getStream(frame.StreamID); stream != nil {
			stream.deliverOpenResult(mobileRouteFrameOpenResult{OK: control.OK, Error: control.Error})
		}
	case mobileRouteFrameControlOpenUpdate:
		if stream := s.getStream(frame.StreamID); stream != nil {
			if control.Request != nil {
				stream.deliverOpenUpdate(*control.Request)
			}
			if control.Mobile != nil {
				stream.deliverMobileOpenUpdate(*control.Mobile)
			}
		}
	case mobileRouteFrameControlOpenUpdateResult:
		if stream := s.getStream(frame.StreamID); stream != nil {
			stream.deliverOpenUpdateResult(mobileRouteFrameOpenResult{OK: control.OK, Error: control.Error})
		}
	case mobileRouteFrameControlFin:
		if stream := s.getStream(frame.StreamID); stream != nil {
			_ = stream.closeRemote(io.EOF)
		}
	case mobileRouteFrameControlClose:
		if stream := s.getStream(frame.StreamID); stream != nil {
			_ = stream.closeRemote(io.EOF)
		}
	case mobileRouteFrameControlError, mobileRouteFrameControlReset:
		if stream := s.getStream(frame.StreamID); stream != nil {
			errText := control.Error
			if errText == "" {
				errText = "remote stream error"
			}
			_ = stream.closeRemote(errors.New(errText))
		}
	}
}

func (s *mobileRouteFrameSession) writeControl(streamID uint64, controlType string, errText string) error {
	return s.writeControlFrame(mobileRouteFrame{Kind: mobileRouteFrameKindControl, StreamID: streamID}, mobileRouteFrameSessionControl{Type: controlType, Error: errText})
}

func (s *mobileRouteFrameSession) writeControlFrame(frame mobileRouteFrame, control mobileRouteFrameSessionControl) error {
	payload, err := marshalMobileRouteFrameControl(control)
	if err != nil {
		return err
	}
	frame.Kind = mobileRouteFrameKindControl
	frame.Control = payload
	return s.writeFrame(frame)
}

func (s *mobileRouteFrameSession) writeControlFrameAsync(frame mobileRouteFrame, control mobileRouteFrameSessionControl) {
	payload, err := marshalMobileRouteFrameControl(control)
	if err != nil {
		return
	}
	frame.Kind = mobileRouteFrameKindControl
	frame.Control = payload
	s.writeFrameAsync(frame)
}

func (s *mobileRouteFrameSession) handleHelloControl(control mobileRouteFrameSessionControl) {
	if s == nil || control.Config == nil {
		return
	}
	s.configMu.Lock()
	s.remoteConfig = normalizeMobileRouteFrameSessionConfig(*control.Config)
	s.effectiveConfig = mergeMobileRouteFrameSessionConfig(s.localConfig, s.remoteConfig)
	effective := s.effectiveConfig
	s.configMu.Unlock()
	if control.Type == mobileRouteFrameControlHello {
		s.writeControlFrameAsync(mobileRouteFrame{Kind: mobileRouteFrameKindControl}, mobileRouteFrameSessionControl{Type: mobileRouteFrameControlHelloAck, Config: &effective})
	}
	s.readyOnce.Do(func() {
		close(s.readyCh)
	})
}

func (s *mobileRouteFrameSession) writeFrame(frame mobileRouteFrame) error {
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

func (s *mobileRouteFrameSession) writeFrameAsync(frame mobileRouteFrame) {
	if s == nil || s.IsClosed() {
		return
	}
	errCh := make(chan error, 1)
	_ = s.enqueueWriteFrame(frame, errCh)
}

type mobileRouteFrameStream struct {
	session *mobileRouteFrameSession
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
	openRequest                routeTunnelOpenRequest
	openRequestAvailable       bool
	mobileOpenRequest          mobileRouteTunnelOpenRequest
	mobileOpenRequestAvailable bool
	priority                   string
	openResultCh               chan mobileRouteFrameOpenResult
	openUpdateCh               chan routeTunnelOpenRequest
	mobileOpenUpdateCh         chan mobileRouteTunnelOpenRequest
	openUpdateResultCh         chan mobileRouteFrameOpenResult
}

func newMobileRouteFrameStream(session *mobileRouteFrameSession, streamID uint64) *mobileRouteFrameStream {
	return &mobileRouteFrameStream{
		session:            session,
		id:                 streamID,
		readCh:             make(chan []byte, mobileRouteFrameSessionInboundBuffer),
		localDone:          make(chan struct{}),
		remoteDone:         make(chan struct{}),
		openResultCh:       make(chan mobileRouteFrameOpenResult, 1),
		openUpdateCh:       make(chan routeTunnelOpenRequest, 1),
		mobileOpenUpdateCh: make(chan mobileRouteTunnelOpenRequest, 1),
		openUpdateResultCh: make(chan mobileRouteFrameOpenResult, 1),
	}
}

func (s *mobileRouteFrameStream) Read(payload []byte) (int, error) {
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

		timer, timerCh := mobileRouteDeadlineTimer(s.readDeadline.Load())
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
			return 0, mobileRouteTimeoutError{}
		case <-s.localDone:
			if timer != nil {
				timer.Stop()
			}
			return 0, io.ErrClosedPipe
		}
	}
}

func (s *mobileRouteFrameStream) Write(payload []byte) (int, error) {
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
		timer, timerCh := mobileRouteDeadlineTimer(s.writeDeadline.Load())
		done := make(chan error, 1)
		data := append([]byte(nil), payload[written:end]...)
		go func() {
			done <- s.session.writeFrame(mobileRouteFrame{Kind: mobileRouteFrameKindData, StreamID: s.id, Data: data})
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
			return written, mobileRouteTimeoutError{}
		case <-s.localDone:
			if timer != nil {
				timer.Stop()
			}
			return written, io.ErrClosedPipe
		}
	}
	return written, nil
}

func (s *mobileRouteFrameStream) Close() error {
	if s == nil {
		return nil
	}
	return s.closeLocal()
}

func (s *mobileRouteFrameStream) CloseWrite() error {
	if s == nil {
		return nil
	}
	return s.session.writeControlFrame(mobileRouteFrame{Kind: mobileRouteFrameKindControl, StreamID: s.id}, mobileRouteFrameSessionControl{Type: mobileRouteFrameControlFin})
}

func (s *mobileRouteFrameStream) closeLocal() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.localDone)
		if s.session != nil {
			err = s.session.writeControlFrame(mobileRouteFrame{Kind: mobileRouteFrameKindControl, StreamID: s.id}, mobileRouteFrameSessionControl{Type: mobileRouteFrameControlClose})
			s.session.removeStream(s.id, s)
		}
	})
	return err
}

func (s *mobileRouteFrameStream) Reset(errText string) error {
	if s == nil {
		return nil
	}
	if strings.TrimSpace(errText) == "" {
		errText = "stream reset"
	}
	return s.session.writeControlFrame(mobileRouteFrame{Kind: mobileRouteFrameKindControl, StreamID: s.id}, mobileRouteFrameSessionControl{Type: mobileRouteFrameControlReset, Error: errText})
}

func (s *mobileRouteFrameStream) closeRemote(err error) error {
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

func (s *mobileRouteFrameStream) deliverData(payload []byte) {
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

func (s *mobileRouteFrameStream) setOpenRequest(req routeTunnelOpenRequest) {
	if s == nil {
		return
	}
	s.openMu.Lock()
	s.openRequest = req
	s.openRequestAvailable = true
	s.priority = resolveMobileRouteFrameLinkPriority(req)
	s.openMu.Unlock()
}

func (s *mobileRouteFrameStream) OpenRequest() (routeTunnelOpenRequest, bool) {
	if s == nil {
		return routeTunnelOpenRequest{}, false
	}
	s.openMu.RLock()
	defer s.openMu.RUnlock()
	return s.openRequest, s.openRequestAvailable
}

func (s *mobileRouteFrameStream) setMobileOpenRequest(req mobileRouteTunnelOpenRequest) {
	if s == nil {
		return
	}
	s.openMu.Lock()
	s.mobileOpenRequest = req
	s.mobileOpenRequestAvailable = true
	s.priority = resolveMobileRouteFrameMobilePriority(req)
	s.openMu.Unlock()
}

func (s *mobileRouteFrameStream) MobileOpenRequest() (mobileRouteTunnelOpenRequest, bool) {
	if s == nil {
		return mobileRouteTunnelOpenRequest{}, false
	}
	s.openMu.RLock()
	defer s.openMu.RUnlock()
	return s.mobileOpenRequest, s.mobileOpenRequestAvailable
}

func (s *mobileRouteFrameStream) frameDataChunkBytes(available int) int {
	if s == nil || s.session == nil {
		return mobileRouteFrameRealtimeDataBytes
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
	chunk = clampMobileRouteFrameDataBytes(chunk, cfg.PreferredFrameData, cfg.MaxFrameData)
	if available > 0 && chunk > available {
		return available
	}
	return chunk
}

func (s *mobileRouteFrameStream) Priority() string {
	if s == nil {
		return "normal"
	}
	s.openMu.RLock()
	defer s.openMu.RUnlock()
	return normalizeMobileRouteFramePriority(s.priority)
}

func (s *mobileRouteFrameStream) RespondOpen(resp routeTunnelOpenResponse) error {
	if s == nil || s.session == nil {
		return io.ErrClosedPipe
	}
	return s.session.writeControlFrame(mobileRouteFrame{Kind: mobileRouteFrameKindControl, StreamID: s.id}, mobileRouteFrameSessionControl{
		Type:  mobileRouteFrameControlOpenResult,
		OK:    resp.OK,
		Error: strings.TrimSpace(resp.Error),
	})
}

func (s *mobileRouteFrameStream) RespondMobileOpen(resp mobileRouteTunnelOpenResponse) error {
	if s == nil || s.session == nil {
		return io.ErrClosedPipe
	}
	return s.session.writeControlFrame(mobileRouteFrame{Kind: mobileRouteFrameKindControl, StreamID: s.id}, mobileRouteFrameSessionControl{
		Type:  mobileRouteFrameControlOpenResult,
		OK:    resp.OK,
		Error: strings.TrimSpace(resp.Error),
	})
}

func (s *mobileRouteFrameStream) waitOpenResult(timeout time.Duration) error {
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
		return mobileRouteTimeoutError{}
	case <-s.localDone:
		return io.ErrClosedPipe
	case <-s.remoteDone:
		if s.remoteErr != nil {
			return s.remoteErr
		}
		return io.ErrClosedPipe
	}
}

func (s *mobileRouteFrameStream) deliverOpenResult(result mobileRouteFrameOpenResult) {
	if s == nil {
		return
	}
	select {
	case s.openResultCh <- result:
	default:
	}
}

func (s *mobileRouteFrameStream) SendOpenUpdate(req routeTunnelOpenRequest, timeout time.Duration) error {
	if s == nil || s.session == nil {
		return io.ErrClosedPipe
	}
	if timeout <= 0 {
		timeout = mobileRouteFrameOpenResultTimeout
	}
	request := req
	if err := s.session.writeControlFrame(mobileRouteFrame{Kind: mobileRouteFrameKindControl, StreamID: s.id}, mobileRouteFrameSessionControl{
		Type:    mobileRouteFrameControlOpenUpdate,
		Request: &request,
	}); err != nil {
		return err
	}
	return s.waitOpenUpdateResult(timeout)
}

func (s *mobileRouteFrameStream) WaitOpenUpdate(timeout time.Duration) (routeTunnelOpenRequest, error) {
	if s == nil {
		return routeTunnelOpenRequest{}, io.ErrClosedPipe
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case req := <-s.openUpdateCh:
		return req, nil
	case <-timer.C:
		return routeTunnelOpenRequest{}, mobileRouteTimeoutError{}
	case <-s.localDone:
		return routeTunnelOpenRequest{}, io.ErrClosedPipe
	case <-s.remoteDone:
		if s.remoteErr != nil {
			return routeTunnelOpenRequest{}, s.remoteErr
		}
		return routeTunnelOpenRequest{}, io.ErrClosedPipe
	}
}

func (s *mobileRouteFrameStream) RespondOpenUpdate(resp routeTunnelOpenResponse) error {
	if s == nil || s.session == nil {
		return io.ErrClosedPipe
	}
	return s.session.writeControlFrame(mobileRouteFrame{Kind: mobileRouteFrameKindControl, StreamID: s.id}, mobileRouteFrameSessionControl{
		Type:  mobileRouteFrameControlOpenUpdateResult,
		OK:    resp.OK,
		Error: strings.TrimSpace(resp.Error),
	})
}

func (s *mobileRouteFrameStream) deliverOpenUpdate(req routeTunnelOpenRequest) {
	if s == nil {
		return
	}
	select {
	case s.openUpdateCh <- req:
	default:
	}
}

func (s *mobileRouteFrameStream) deliverMobileOpenUpdate(req mobileRouteTunnelOpenRequest) {
	if s == nil {
		return
	}
	select {
	case s.mobileOpenUpdateCh <- req:
	default:
	}
}

func (s *mobileRouteFrameStream) deliverOpenUpdateResult(result mobileRouteFrameOpenResult) {
	if s == nil {
		return
	}
	select {
	case s.openUpdateResultCh <- result:
	default:
	}
}

func (s *mobileRouteFrameStream) waitOpenUpdateResult(timeout time.Duration) error {
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
		return mobileRouteTimeoutError{}
	case <-s.localDone:
		return io.ErrClosedPipe
	case <-s.remoteDone:
		if s.remoteErr != nil {
			return s.remoteErr
		}
		return io.ErrClosedPipe
	}
}

func (s *mobileRouteFrameStream) LocalAddr() net.Addr {
	if s == nil || s.session == nil {
		return mobileRouteFrameSessionAddr{}
	}
	return s.session.local
}

func (s *mobileRouteFrameStream) RemoteAddr() net.Addr {
	if s == nil || s.session == nil {
		return mobileRouteFrameSessionAddr{}
	}
	return s.session.remote
}

func (s *mobileRouteFrameStream) SetDeadline(t time.Time) error {
	_ = s.SetReadDeadline(t)
	_ = s.SetWriteDeadline(t)
	return nil
}

func (s *mobileRouteFrameStream) SetReadDeadline(t time.Time) error {
	if s != nil {
		s.readDeadline.Store(t)
	}
	return nil
}

func (s *mobileRouteFrameStream) SetWriteDeadline(t time.Time) error {
	if s != nil {
		s.writeDeadline.Store(t)
	}
	return nil
}

func normalizeMobileRouteFramePriority(priority string) string {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "realtime", "interactive", "latency":
		return "realtime"
	case "bulk", "throughput":
		return "bulk"
	default:
		return "normal"
	}
}

func resolveMobileRouteFrameLinkPriority(req routeTunnelOpenRequest) string {
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
	return normalizeMobileRouteFramePriority(req.Priority)
}

func resolveMobileRouteFrameMobilePriority(req mobileRouteTunnelOpenRequest) string {
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
	return normalizeMobileRouteFramePriority(req.Priority)
}

func mobileRouteDeadlineTimer(raw any) (*time.Timer, <-chan time.Time) {
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

type mobileRouteTimeoutError struct{}

func (mobileRouteTimeoutError) Error() string   { return "i/o timeout" }
func (mobileRouteTimeoutError) Timeout() bool   { return true }
func (mobileRouteTimeoutError) Temporary() bool { return true }

func defaultMobileRouteFrameSessionConfig() mobileRouteFrameSessionConfig {
	return mobileRouteFrameSessionConfig{
		Version:              mobileRouteFrameVersion,
		Features:             []string{"open_result", "open_update", "fin", "rst", "window_update"},
		MaxFrameData:         mobileRouteFrameMaxDataBytes,
		MinFrameData:         mobileRouteFrameRealtimeDataBytes,
		PreferredFrameData:   mobileRouteFramePreferredDataBytes,
		BulkFrameData:        mobileRouteFrameBulkDataBytes,
		MaxControlBytes:      mobileRouteFrameMaxControlBytes,
		MaxConcurrentStreams: mobileRouteFrameSessionInboundBuffer,
		InitialStreamWindow:  mobileRouteFrameMaxDataBytes * mobileRouteFrameSessionInboundBuffer,
		InitialSessionWindow: mobileRouteFrameMaxDataBytes * mobileRouteFrameSessionInboundBuffer,
		IdleTimeoutMS:        int64((mobileRouteFramePingInterval + mobileRouteFramePingTimeout).Milliseconds()),
		PingIntervalMS:       int64(mobileRouteFramePingInterval.Milliseconds()),
		RealtimeFrameData:    mobileRouteFrameRealtimeDataBytes,
	}
}

func normalizeMobileRouteFrameSessionConfig(cfg mobileRouteFrameSessionConfig) mobileRouteFrameSessionConfig {
	if cfg.Version <= 0 {
		cfg.Version = mobileRouteFrameVersion
	}
	if cfg.MaxFrameData <= 0 || cfg.MaxFrameData > mobileRouteFrameMaxDataBytes {
		cfg.MaxFrameData = mobileRouteFrameMaxDataBytes
	}
	if cfg.MinFrameData <= 0 || cfg.MinFrameData > cfg.MaxFrameData {
		cfg.MinFrameData = mobileRouteFrameRealtimeDataBytes
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
		cfg.RealtimeFrameData = mobileRouteFrameRealtimeDataBytes
		if cfg.RealtimeFrameData > cfg.PreferredFrameData {
			cfg.RealtimeFrameData = cfg.PreferredFrameData
		}
	}
	if cfg.RealtimeFrameData > cfg.MinFrameData {
		cfg.RealtimeFrameData = cfg.MinFrameData
	}
	if cfg.MaxControlBytes <= 0 || cfg.MaxControlBytes > mobileRouteFrameMaxControlBytes {
		cfg.MaxControlBytes = mobileRouteFrameMaxControlBytes
	}
	if cfg.MaxConcurrentStreams <= 0 {
		cfg.MaxConcurrentStreams = mobileRouteFrameSessionInboundBuffer
	}
	if cfg.InitialStreamWindow <= 0 {
		cfg.InitialStreamWindow = cfg.MaxFrameData * mobileRouteFrameSessionInboundBuffer
	}
	if cfg.InitialSessionWindow <= 0 {
		cfg.InitialSessionWindow = cfg.InitialStreamWindow
	}
	if cfg.IdleTimeoutMS <= 0 {
		cfg.IdleTimeoutMS = int64((mobileRouteFramePingInterval + mobileRouteFramePingTimeout).Milliseconds())
	}
	if cfg.PingIntervalMS <= 0 {
		cfg.PingIntervalMS = int64(mobileRouteFramePingInterval.Milliseconds())
	}
	return cfg
}

func mergeMobileRouteFrameSessionConfig(local mobileRouteFrameSessionConfig, remote mobileRouteFrameSessionConfig) mobileRouteFrameSessionConfig {
	local = normalizeMobileRouteFrameSessionConfig(local)
	remote = normalizeMobileRouteFrameSessionConfig(remote)
	out := local
	if remote.Version < out.Version {
		out.Version = remote.Version
	}
	out.Features = intersectMobileRouteFrameFeatures(local.Features, remote.Features)
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
	return normalizeMobileRouteFrameSessionConfig(out)
}

func intersectMobileRouteFrameFeatures(left []string, right []string) []string {
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

func clampMobileRouteFrameDataBytes(value int, fallback int, maxFrameData int) int {
	if fallback <= 0 {
		fallback = mobileRouteFramePreferredDataBytes
	}
	if value <= 0 {
		value = fallback
	}
	if maxFrameData <= 0 || maxFrameData > mobileRouteFrameMaxDataBytes {
		maxFrameData = mobileRouteFrameMaxDataBytes
	}
	if value > maxFrameData {
		value = maxFrameData
	}
	if value > mobileRouteFrameMaxDataBytes {
		value = mobileRouteFrameMaxDataBytes
	}
	if value <= 0 {
		value = mobileRouteFrameRealtimeDataBytes
	}
	return value
}
