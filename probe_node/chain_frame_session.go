package main

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
	probeChainFrameControlOpen             = "open"
	probeChainFrameControlOpenResult       = "open_result"
	probeChainFrameControlOpenUpdate       = "open_update"
	probeChainFrameControlOpenUpdateResult = "open_update_result"
	probeChainFrameControlClose            = "close"
	probeChainFrameControlError            = "error"
	probeChainFrameControlHello            = "hello"
	probeChainFrameControlHelloAck         = "hello_ack"
	probeChainFrameControlFin              = "fin"
	probeChainFrameControlReset            = "rst"
	probeChainFrameControlWindowUpdate     = "window_update"

	probeChainFrameSessionInboundBuffer = 64
	probeChainFramePingInterval         = 10 * time.Second
	probeChainFramePingTimeout          = 5 * time.Second
	probeChainFrameOpenResultTimeout    = 30 * time.Second
	probeChainFramePreferredDataBytes   = 16 * 1024
	probeChainFrameRealtimeDataBytes    = 4 * 1024
	probeChainFrameBulkDataBytes        = 64 * 1024
)

type probeChainFrameSession struct {
	conn      net.Conn
	local     probeChainFrameSessionAddr
	remote    probeChainFrameSessionAddr
	initiator bool

	controlWriteCh chan probeChainFrameWriteRequest
	dataWriteCh    chan probeChainFrameWriteRequest
	closeCh        chan struct{}
	closed         atomic.Bool

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

	configMu        sync.RWMutex
	localConfig     probeChainFrameSessionConfig
	remoteConfig    probeChainFrameSessionConfig
	effectiveConfig probeChainFrameSessionConfig
}

type probeChainFrameWriteRequest struct {
	frame probeChainFrame
	errCh chan error
}

type probeChainFrameSessionControl struct {
	Type    string                        `json:"type"`
	OK      bool                          `json:"ok,omitempty"`
	Error   string                        `json:"error,omitempty"`
	Request *probeChainTunnelOpenRequest  `json:"request,omitempty"`
	Config  *probeChainFrameSessionConfig `json:"config,omitempty"`
}

type probeChainFrameSessionConfig struct {
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

type probeChainFrameOpenResult struct {
	OK    bool
	Error string
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
	localConfig := defaultProbeChainFrameSessionConfig()
	s := &probeChainFrameSession{
		conn:            conn,
		local:           probeChainFrameSessionAddr{label: conn.LocalAddr().String()},
		remote:          probeChainFrameSessionAddr{label: conn.RemoteAddr().String()},
		initiator:       initiator,
		controlWriteCh:  make(chan probeChainFrameWriteRequest, probeChainFrameSessionInboundBuffer),
		dataWriteCh:     make(chan probeChainFrameWriteRequest, probeChainFrameSessionInboundBuffer),
		closeCh:         make(chan struct{}),
		streams:         make(map[uint64]*probeChainFrameStream),
		acceptCh:        make(chan *probeChainFrameStream, probeChainFrameSessionInboundBuffer),
		pendingPings:    make(map[uint64]time.Time),
		localConfig:     localConfig,
		effectiveConfig: localConfig,
	}
	s.nextStreamID.Store(start)
	go s.writeLoop()
	go s.readLoop()
	go s.pingLoop()
	go func() {
		_ = s.writeControlFrame(probeChainFrame{Kind: probeChainFrameKindControl}, probeChainFrameSessionControl{Type: probeChainFrameControlHello, Config: &localConfig})
	}()
	return s, nil
}

func (s *probeChainFrameSession) Open() (net.Conn, error) {
	return s.openWithRequest(nil, false, 0)
}

func (s *probeChainFrameSession) OpenWithRequest(req probeChainTunnelOpenRequest, timeout time.Duration) (net.Conn, error) {
	return s.openWithRequest(&req, true, timeout)
}

func (s *probeChainFrameSession) openWithRequest(req *probeChainTunnelOpenRequest, waitResult bool, timeout time.Duration) (net.Conn, error) {
	if s == nil {
		return nil, errors.New("frame session is nil")
	}
	if s.IsClosed() {
		return nil, net.ErrClosed
	}
	streamID := s.nextStreamID.Add(2) - 2
	stream := newProbeChainFrameStream(s, streamID)
	s.registerStream(stream)
	control := probeChainFrameSessionControl{Type: probeChainFrameControlOpen}
	if req != nil {
		request := *req
		stream.setOpenRequest(request)
		control.Request = &request
	}
	if err := s.writeControlFrame(probeChainFrame{Kind: probeChainFrameKindControl, StreamID: streamID}, control); err != nil {
		s.removeStream(streamID, stream)
		_ = stream.closeLocal()
		return nil, err
	}
	if waitResult {
		if timeout <= 0 {
			timeout = probeChainFrameOpenResultTimeout
		}
		if err := stream.waitOpenResult(timeout); err != nil {
			_ = stream.closeLocal()
			return nil, err
		}
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

func (s *probeChainFrameSession) NegotiatedConfig() probeChainFrameSessionConfig {
	if s == nil {
		return defaultProbeChainFrameSessionConfig()
	}
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.effectiveConfig
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

func (s *probeChainFrameSession) writeOne(req probeChainFrameWriteRequest) bool {
	err := writeProbeChainFrame(s.conn, req.frame)
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

func (s *probeChainFrameSession) enqueueWriteFrame(frame probeChainFrame, errCh chan error) error {
	req := probeChainFrameWriteRequest{frame: frame, errCh: errCh}
	targetCh := s.dataWriteCh
	if frame.Kind != probeChainFrameKindData {
		targetCh = s.controlWriteCh
	}
	select {
	case targetCh <- req:
		return nil
	case <-s.closeCh:
		return net.ErrClosed
	}
}

func (s *probeChainFrameSession) finishWriteFrame(errCh chan error) error {
	select {
	case err := <-errCh:
		return err
	case <-s.closeCh:
		return net.ErrClosed
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
	case probeChainFrameControlHello:
		s.handleHelloControl(control)
	case probeChainFrameControlHelloAck:
		s.handleHelloControl(control)
	case probeChainFrameControlOpen:
		stream := newProbeChainFrameStream(s, frame.StreamID)
		if control.Request != nil {
			stream.setOpenRequest(*control.Request)
		}
		s.registerStream(stream)
		select {
		case s.acceptCh <- stream:
		case <-s.closeCh:
			_ = stream.closeRemote(io.ErrClosedPipe)
		}
	case probeChainFrameControlOpenResult:
		if stream := s.getStream(frame.StreamID); stream != nil {
			stream.deliverOpenResult(probeChainFrameOpenResult{OK: control.OK, Error: control.Error})
		}
	case probeChainFrameControlOpenUpdate:
		if stream := s.getStream(frame.StreamID); stream != nil && control.Request != nil {
			stream.deliverOpenUpdate(*control.Request)
		}
	case probeChainFrameControlOpenUpdateResult:
		if stream := s.getStream(frame.StreamID); stream != nil {
			stream.deliverOpenUpdateResult(probeChainFrameOpenResult{OK: control.OK, Error: control.Error})
		}
	case probeChainFrameControlFin:
		if stream := s.getStream(frame.StreamID); stream != nil {
			_ = stream.closeRemote(io.EOF)
		}
	case probeChainFrameControlClose:
		if stream := s.getStream(frame.StreamID); stream != nil {
			_ = stream.closeRemote(io.EOF)
		}
	case probeChainFrameControlError, probeChainFrameControlReset:
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
	return s.writeControlFrame(probeChainFrame{Kind: probeChainFrameKindControl, StreamID: streamID}, probeChainFrameSessionControl{Type: controlType, Error: errText})
}

func (s *probeChainFrameSession) writeControlFrame(frame probeChainFrame, control probeChainFrameSessionControl) error {
	payload, err := marshalProbeChainFrameControl(control)
	if err != nil {
		return err
	}
	frame.Kind = probeChainFrameKindControl
	frame.Control = payload
	return s.writeFrame(frame)
}

func (s *probeChainFrameSession) writeControlFrameAsync(frame probeChainFrame, control probeChainFrameSessionControl) {
	payload, err := marshalProbeChainFrameControl(control)
	if err != nil {
		return
	}
	frame.Kind = probeChainFrameKindControl
	frame.Control = payload
	s.writeFrameAsync(frame)
}

func (s *probeChainFrameSession) handleHelloControl(control probeChainFrameSessionControl) {
	if s == nil || control.Config == nil {
		return
	}
	s.configMu.Lock()
	s.remoteConfig = normalizeProbeChainFrameSessionConfig(*control.Config)
	s.effectiveConfig = mergeProbeChainFrameSessionConfig(s.localConfig, s.remoteConfig)
	effective := s.effectiveConfig
	s.configMu.Unlock()
	if control.Type == probeChainFrameControlHello {
		s.writeControlFrameAsync(probeChainFrame{Kind: probeChainFrameKindControl}, probeChainFrameSessionControl{Type: probeChainFrameControlHelloAck, Config: &effective})
	}
}

func (s *probeChainFrameSession) writeFrame(frame probeChainFrame) error {
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

func (s *probeChainFrameSession) writeFrameAsync(frame probeChainFrame) {
	if s == nil || s.IsClosed() {
		return
	}
	errCh := make(chan error, 1)
	_ = s.enqueueWriteFrame(frame, errCh)
}

func (s *probeChainFrameSession) frameDataChunkBytes() int {
	cfg := s.NegotiatedConfig()
	chunk := cfg.PreferredFrameData
	if chunk <= 0 || chunk > cfg.MaxFrameData {
		chunk = cfg.MaxFrameData
	}
	if chunk <= 0 || chunk > probeChainFrameMaxDataBytes {
		chunk = probeChainFrameMaxDataBytes
	}
	return chunk
}

func clampProbeChainFrameDataBytes(value int, fallback int, maxFrameData int) int {
	if fallback <= 0 {
		fallback = probeChainFramePreferredDataBytes
	}
	if value <= 0 {
		value = fallback
	}
	if maxFrameData <= 0 || maxFrameData > probeChainFrameMaxDataBytes {
		maxFrameData = probeChainFrameMaxDataBytes
	}
	if value > maxFrameData {
		value = maxFrameData
	}
	if value > probeChainFrameMaxDataBytes {
		value = probeChainFrameMaxDataBytes
	}
	if value <= 0 {
		value = probeChainFrameRealtimeDataBytes
	}
	return value
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

	openMu               sync.RWMutex
	openRequest          probeChainTunnelOpenRequest
	openRequestAvailable bool
	priority             string
	openResultCh         chan probeChainFrameOpenResult
	openUpdateCh         chan probeChainTunnelOpenRequest
	openUpdateResultCh   chan probeChainFrameOpenResult
}

func newProbeChainFrameStream(session *probeChainFrameSession, streamID uint64) *probeChainFrameStream {
	return &probeChainFrameStream{
		session:            session,
		id:                 streamID,
		readCh:             make(chan []byte, probeChainFrameSessionInboundBuffer),
		localDone:          make(chan struct{}),
		remoteDone:         make(chan struct{}),
		openResultCh:       make(chan probeChainFrameOpenResult, 1),
		openUpdateCh:       make(chan probeChainTunnelOpenRequest, 1),
		openUpdateResultCh: make(chan probeChainFrameOpenResult, 1),
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
		timer, timerCh := deadlineTimer(s.writeDeadline.Load())
		done := make(chan error, 1)
		data := append([]byte(nil), payload[written:end]...)
		go func() {
			done <- s.session.writeFrame(probeChainFrame{Kind: probeChainFrameKindData, StreamID: s.id, Data: data})
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
			return written, osErrTimeout{}
		case <-s.localDone:
			if timer != nil {
				timer.Stop()
			}
			return written, io.ErrClosedPipe
		}
	}
	return written, nil
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
	return s.session.writeControlFrame(probeChainFrame{Kind: probeChainFrameKindControl, StreamID: s.id}, probeChainFrameSessionControl{Type: probeChainFrameControlFin})
}

func (s *probeChainFrameStream) closeLocal() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.localDone)
		if s.session != nil {
			err = s.session.writeControlFrame(probeChainFrame{Kind: probeChainFrameKindControl, StreamID: s.id}, probeChainFrameSessionControl{Type: probeChainFrameControlClose})
			s.session.removeStream(s.id, s)
		}
	})
	return err
}

func (s *probeChainFrameStream) Reset(errText string) error {
	if s == nil {
		return nil
	}
	if strings.TrimSpace(errText) == "" {
		errText = "stream reset"
	}
	return s.session.writeControlFrame(probeChainFrame{Kind: probeChainFrameKindControl, StreamID: s.id}, probeChainFrameSessionControl{Type: probeChainFrameControlReset, Error: errText})
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

func (s *probeChainFrameStream) setOpenRequest(req probeChainTunnelOpenRequest) {
	if s == nil {
		return
	}
	s.openMu.Lock()
	s.openRequest = req
	s.openRequestAvailable = true
	s.priority = resolveProbeChainFrameStreamPriority(req)
	s.openMu.Unlock()
}

func (s *probeChainFrameStream) OpenRequest() (probeChainTunnelOpenRequest, bool) {
	if s == nil {
		return probeChainTunnelOpenRequest{}, false
	}
	s.openMu.RLock()
	defer s.openMu.RUnlock()
	return s.openRequest, s.openRequestAvailable
}

func (s *probeChainFrameStream) frameDataChunkBytes(available int) int {
	if s == nil || s.session == nil {
		return probeChainFrameRealtimeDataBytes
	}
	cfg := s.session.NegotiatedConfig()
	chunk := cfg.PreferredFrameData
	priority := s.Priority()
	switch priority {
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
	chunk = clampProbeChainFrameDataBytes(chunk, cfg.PreferredFrameData, cfg.MaxFrameData)
	if available > 0 && chunk > available {
		return available
	}
	return chunk
}

func (s *probeChainFrameStream) Priority() string {
	if s == nil {
		return "normal"
	}
	s.openMu.RLock()
	defer s.openMu.RUnlock()
	return normalizeProbeChainFrameStreamPriority(s.priority)
}

func (s *probeChainFrameStream) RespondOpen(resp probeChainTunnelOpenResponse) error {
	if s == nil || s.session == nil {
		return io.ErrClosedPipe
	}
	return s.session.writeControlFrame(probeChainFrame{Kind: probeChainFrameKindControl, StreamID: s.id}, probeChainFrameSessionControl{
		Type:  probeChainFrameControlOpenResult,
		OK:    resp.OK,
		Error: strings.TrimSpace(resp.Error),
	})
}

func (s *probeChainFrameStream) waitOpenResult(timeout time.Duration) error {
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
		return osErrTimeout{}
	case <-s.localDone:
		return io.ErrClosedPipe
	case <-s.remoteDone:
		if s.remoteErr != nil {
			return s.remoteErr
		}
		return io.ErrClosedPipe
	}
}

func (s *probeChainFrameStream) deliverOpenResult(result probeChainFrameOpenResult) {
	if s == nil {
		return
	}
	select {
	case s.openResultCh <- result:
	default:
	}
}

func (s *probeChainFrameStream) SendOpenUpdate(req probeChainTunnelOpenRequest, timeout time.Duration) error {
	if s == nil || s.session == nil {
		return io.ErrClosedPipe
	}
	if timeout <= 0 {
		timeout = probeChainFrameOpenResultTimeout
	}
	request := req
	if err := s.session.writeControlFrame(probeChainFrame{Kind: probeChainFrameKindControl, StreamID: s.id}, probeChainFrameSessionControl{
		Type:    probeChainFrameControlOpenUpdate,
		Request: &request,
	}); err != nil {
		return err
	}
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
		return osErrTimeout{}
	case <-s.localDone:
		return io.ErrClosedPipe
	case <-s.remoteDone:
		if s.remoteErr != nil {
			return s.remoteErr
		}
		return io.ErrClosedPipe
	}
}

func (s *probeChainFrameStream) WaitOpenUpdate(timeout time.Duration) (probeChainTunnelOpenRequest, error) {
	if s == nil {
		return probeChainTunnelOpenRequest{}, io.ErrClosedPipe
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case req := <-s.openUpdateCh:
		return req, nil
	case <-timer.C:
		return probeChainTunnelOpenRequest{}, osErrTimeout{}
	case <-s.localDone:
		return probeChainTunnelOpenRequest{}, io.ErrClosedPipe
	case <-s.remoteDone:
		if s.remoteErr != nil {
			return probeChainTunnelOpenRequest{}, s.remoteErr
		}
		return probeChainTunnelOpenRequest{}, io.ErrClosedPipe
	}
}

func (s *probeChainFrameStream) RespondOpenUpdate(resp probeChainTunnelOpenResponse) error {
	if s == nil || s.session == nil {
		return io.ErrClosedPipe
	}
	return s.session.writeControlFrame(probeChainFrame{Kind: probeChainFrameKindControl, StreamID: s.id}, probeChainFrameSessionControl{
		Type:  probeChainFrameControlOpenUpdateResult,
		OK:    resp.OK,
		Error: strings.TrimSpace(resp.Error),
	})
}

func (s *probeChainFrameStream) deliverOpenUpdate(req probeChainTunnelOpenRequest) {
	if s == nil {
		return
	}
	s.openMu.Lock()
	s.priority = resolveProbeChainFrameStreamPriority(req)
	s.openMu.Unlock()
	select {
	case s.openUpdateCh <- req:
	default:
	}
}

func (s *probeChainFrameStream) deliverOpenUpdateResult(result probeChainFrameOpenResult) {
	if s == nil {
		return
	}
	select {
	case s.openUpdateResultCh <- result:
	default:
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

func normalizeProbeChainFrameStreamPriority(priority string) string {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "realtime", "interactive", "latency":
		return "realtime"
	case "bulk", "throughput":
		return "bulk"
	default:
		return "normal"
	}
}

func resolveProbeChainFrameStreamPriority(req probeChainTunnelOpenRequest) string {
	if req.LatencySensitive {
		return "realtime"
	}
	switch strings.ToLower(strings.TrimSpace(req.AppProtocol)) {
	case "rdp", "vnc", "nomachine", "ssh", "udp-association", "interactive":
		return "realtime"
	}
	switch strings.ToLower(strings.TrimSpace(req.ResumePolicy)) {
	case "rebind":
		return "realtime"
	}
	return normalizeProbeChainFrameStreamPriority(req.Priority)
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

func defaultProbeChainFrameSessionConfig() probeChainFrameSessionConfig {
	return probeChainFrameSessionConfig{
		Version:              probeChainFrameVersion,
		Features:             []string{"open_result", "open_update", "fin", "rst", "window_update"},
		MaxFrameData:         probeChainFrameMaxDataBytes,
		MinFrameData:         probeChainFrameRealtimeDataBytes,
		PreferredFrameData:   probeChainFramePreferredDataBytes,
		BulkFrameData:        probeChainFrameBulkDataBytes,
		MaxControlBytes:      probeChainFrameMaxControlBytes,
		MaxConcurrentStreams: probeChainFrameSessionInboundBuffer,
		InitialStreamWindow:  probeChainFrameMaxDataBytes * probeChainFrameSessionInboundBuffer,
		InitialSessionWindow: probeChainFrameMaxDataBytes * probeChainFrameSessionInboundBuffer,
		IdleTimeoutMS:        int64((probeChainFramePingInterval + probeChainFramePingTimeout).Milliseconds()),
		PingIntervalMS:       int64(probeChainFramePingInterval.Milliseconds()),
		RealtimeFrameData:    probeChainFrameRealtimeDataBytes,
	}
}

func normalizeProbeChainFrameSessionConfig(cfg probeChainFrameSessionConfig) probeChainFrameSessionConfig {
	if cfg.Version <= 0 {
		cfg.Version = probeChainFrameVersion
	}
	if cfg.MaxFrameData <= 0 || cfg.MaxFrameData > probeChainFrameMaxDataBytes {
		cfg.MaxFrameData = probeChainFrameMaxDataBytes
	}
	if cfg.MinFrameData <= 0 || cfg.MinFrameData > cfg.MaxFrameData {
		cfg.MinFrameData = probeChainFrameRealtimeDataBytes
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
		cfg.RealtimeFrameData = probeChainFrameRealtimeDataBytes
		if cfg.RealtimeFrameData > cfg.PreferredFrameData {
			cfg.RealtimeFrameData = cfg.PreferredFrameData
		}
	}
	if cfg.RealtimeFrameData > cfg.MinFrameData {
		cfg.RealtimeFrameData = cfg.MinFrameData
	}
	if cfg.MaxControlBytes <= 0 || cfg.MaxControlBytes > probeChainFrameMaxControlBytes {
		cfg.MaxControlBytes = probeChainFrameMaxControlBytes
	}
	if cfg.MaxConcurrentStreams <= 0 {
		cfg.MaxConcurrentStreams = probeChainFrameSessionInboundBuffer
	}
	if cfg.InitialStreamWindow <= 0 {
		cfg.InitialStreamWindow = cfg.MaxFrameData * probeChainFrameSessionInboundBuffer
	}
	if cfg.InitialSessionWindow <= 0 {
		cfg.InitialSessionWindow = cfg.InitialStreamWindow
	}
	if cfg.IdleTimeoutMS <= 0 {
		cfg.IdleTimeoutMS = int64((probeChainFramePingInterval + probeChainFramePingTimeout).Milliseconds())
	}
	if cfg.PingIntervalMS <= 0 {
		cfg.PingIntervalMS = int64(probeChainFramePingInterval.Milliseconds())
	}
	return cfg
}

func mergeProbeChainFrameSessionConfig(local probeChainFrameSessionConfig, remote probeChainFrameSessionConfig) probeChainFrameSessionConfig {
	local = normalizeProbeChainFrameSessionConfig(local)
	remote = normalizeProbeChainFrameSessionConfig(remote)
	out := local
	if remote.Version < out.Version {
		out.Version = remote.Version
	}
	out.Features = intersectProbeChainFrameFeatures(local.Features, remote.Features)
	out.MaxFrameData = minPositiveInt(local.MaxFrameData, remote.MaxFrameData)
	out.MinFrameData = minPositiveInt(local.MinFrameData, remote.MinFrameData)
	out.PreferredFrameData = minPositiveInt(local.PreferredFrameData, remote.PreferredFrameData)
	if out.PreferredFrameData > out.MaxFrameData {
		out.PreferredFrameData = out.MaxFrameData
	}
	out.BulkFrameData = minPositiveInt(local.BulkFrameData, remote.BulkFrameData)
	if out.BulkFrameData > out.MaxFrameData {
		out.BulkFrameData = out.MaxFrameData
	}
	out.RealtimeFrameData = minPositiveInt(local.RealtimeFrameData, remote.RealtimeFrameData)
	if out.RealtimeFrameData > out.PreferredFrameData {
		out.RealtimeFrameData = out.PreferredFrameData
	}
	out.MaxControlBytes = minPositiveInt(local.MaxControlBytes, remote.MaxControlBytes)
	out.MaxConcurrentStreams = minPositiveInt(local.MaxConcurrentStreams, remote.MaxConcurrentStreams)
	out.InitialStreamWindow = minPositiveInt(local.InitialStreamWindow, remote.InitialStreamWindow)
	out.InitialSessionWindow = minPositiveInt(local.InitialSessionWindow, remote.InitialSessionWindow)
	out.IdleTimeoutMS = minPositiveInt64(local.IdleTimeoutMS, remote.IdleTimeoutMS)
	out.PingIntervalMS = minPositiveInt64(local.PingIntervalMS, remote.PingIntervalMS)
	return normalizeProbeChainFrameSessionConfig(out)
}

func intersectProbeChainFrameFeatures(left []string, right []string) []string {
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

func minPositiveInt(left int, right int) int {
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

func minPositiveInt64(left int64, right int64) int64 {
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
