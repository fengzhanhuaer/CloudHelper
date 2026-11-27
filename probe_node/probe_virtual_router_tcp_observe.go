package main

import (
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"sort"
	"sync"
	"time"
)

const probeExitTCPObserveLimit = 64

type probeExitTCPObserved struct {
	ep             tcpip.Endpoint
	source, target string
	opened         time.Time
}
type probeExitTCPObserver struct {
	sync.Mutex
	next    uint64
	entries map[uint64]probeExitTCPObserved
	omitted uint64
}

var probeExitTCPObservation probeExitTCPObserver

type probeExitTCPObservationItem struct {
	Supported          bool    `json:"supported"`
	ID                 uint64  `json:"id"`
	Source             string  `json:"source"`
	Target             string  `json:"target"`
	AgeMS              int64   `json:"age_ms"`
	RTTMS              float64 `json:"rtt_ms"`
	RTTVarMS           float64 `json:"rtt_var_ms"`
	RTOMS              float64 `json:"rto_ms"`
	CongestionControl  string  `json:"congestion_control,omitempty"`
	CwndPackets        uint32  `json:"cwnd_packets"`
	SsthreshPackets    uint32  `json:"ssthresh_packets"`
	State              uint32  `json:"state"`
	CongestionState    uint32  `json:"congestion_state"`
	Reordering         bool    `json:"reordering_seen"`
	SendBufferBytes    *int64  `json:"send_buffer_bytes"`
	ReceiveBufferBytes *int64  `json:"receive_buffer_bytes"`
	SendQueueBytes     *int    `json:"send_queue_bytes"`
	ReceiveQueueBytes  *int    `json:"receive_queue_bytes"`
	SegmentsSent       *uint64 `json:"segments_sent"`
	SegmentsReceived   *uint64 `json:"segments_received"`
	Retransmits        *uint64 `json:"retransmits"`
	Timeouts           *uint64 `json:"timeouts"`
	Error              string  `json:"error,omitempty"`
}
type probeExitTCPObservationSnapshot struct {
	Limit       int                           `json:"limit"`
	Omitted     uint64                        `json:"evictions"`
	CollectedAt string                        `json:"collected_at"`
	Items       []probeExitTCPObservationItem `json:"items"`
}

// Registration happens once per connection. TCP_INFO and endpoint statistics
// are read only by an authenticated, explicit diagnostic request; no TCP probe
// callback or per-packet state copying is installed.
func (o *probeExitTCPObserver) register(ep tcpip.Endpoint, source, target string) func() {
	if ep == nil {
		return func() {}
	}
	o.Lock()
	if len(o.entries) >= probeExitTCPObserveLimit {
		// Keep recent live connections so a newly started diagnostic cannot
		// disappear behind long-lived idle sessions. Eviction never closes ep.
		var oldest uint64
		for id := range o.entries {
			if oldest == 0 || id < oldest {
				oldest = id
			}
		}
		delete(o.entries, oldest)
		o.omitted++
	}

	if o.entries == nil {
		o.entries = make(map[uint64]probeExitTCPObserved)
	}
	o.next++
	id := o.next
	o.entries[id] = probeExitTCPObserved{ep: ep, source: source, target: target, opened: time.Now()}
	o.Unlock()
	return func() { o.Lock(); delete(o.entries, id); o.Unlock() }
}
func (o *probeExitTCPObserver) snapshot() probeExitTCPObservationSnapshot {
	o.Lock()
	entries := make(map[uint64]probeExitTCPObserved, len(o.entries))
	for id, e := range o.entries {
		entries[id] = e
	}
	omitted := o.omitted
	o.Unlock()
	now := time.Now()
	result := probeExitTCPObservationSnapshot{Limit: probeExitTCPObserveLimit, Omitted: omitted, CollectedAt: now.UTC().Format(time.RFC3339Nano), Items: make([]probeExitTCPObservationItem, 0, len(entries))}
	for id, e := range entries {
		item := probeExitTCPObservationItem{ID: id, Source: e.source, Target: e.target, AgeMS: now.Sub(e.opened).Milliseconds()}
		var info tcpip.TCPInfoOption
		if err := e.ep.GetSockOpt(&info); err != nil {
			item.Error = err.String()
		} else {
			item.Supported = true
			item.RTTMS = float64(info.RTT) / float64(time.Millisecond)
			item.RTTVarMS = float64(info.RTTVar) / float64(time.Millisecond)
			item.RTOMS = float64(info.RTO) / float64(time.Millisecond)
			item.CwndPackets = info.SndCwnd
			item.SsthreshPackets = info.SndSsthresh
			item.State = uint32(info.State)
			item.CongestionState = uint32(info.CcState)
			item.Reordering = info.ReorderSeen
		}
		var congestion tcpip.CongestionControlOption
		if err := e.ep.GetSockOpt(&congestion); err == nil {
			item.CongestionControl = string(congestion)
		}
		if opts := e.ep.SocketOptions(); opts != nil {
			send, receive := opts.GetSendBufferSize(), opts.GetReceiveBufferSize()
			item.SendBufferBytes = &send
			item.ReceiveBufferBytes = &receive
		}
		if n, err := e.ep.GetSockOptInt(tcpip.SendQueueSizeOption); err == nil {
			item.SendQueueBytes = &n
		}
		if n, err := e.ep.GetSockOptInt(tcpip.ReceiveQueueSizeOption); err == nil {
			item.ReceiveQueueBytes = &n
		}
		if stats, ok := e.ep.Stats().(*tcp.Stats); ok && stats != nil {
			sent, received, retransmits, timeouts := stats.SegmentsSent.Value(), stats.SegmentsReceived.Value(), stats.SendErrors.Retransmits.Value(), stats.SendErrors.Timeouts.Value()
			item.SegmentsSent = &sent
			item.SegmentsReceived = &received
			item.Retransmits = &retransmits
			item.Timeouts = &timeouts
		}
		result.Items = append(result.Items, item)
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].ID < result.Items[j].ID })
	return result
}
