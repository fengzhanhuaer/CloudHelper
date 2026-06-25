# Probe Chain Frame Protocol Refactor

## Background

The current probe chain substream implementation exposes a yamux-like
`Open/Accept` byte stream API on top of CloudHelper's custom frame protocol.
This made the first version easy to integrate, but it leaves protocol-specific
capabilities unused:

- Substream open metadata is sent as JSON inside the byte stream.
- Prepared streams require a second in-stream JSON handshake before data starts.
- Close semantics collapse FIN, normal close, and error close into one path.
- Frame length and flow-control limits are compile-time constants.
- Relay nodes cannot inspect or forward stream intent without consuming stream
  bytes.

The refactor moves stream lifecycle and tunnel intent into frame control
messages. After a stream is opened, the data channel carries only application
payload.

## Goals

- Make stream open, prepare, activate, close, and error explicit frame-level
  protocol operations.
- Keep the existing `net.Conn` surface for callers that relay byte streams.
- Allow relay nodes to forward open metadata before data relay starts.
- Add a negotiation surface for frame size, flow-control, close semantics, and
  feature flags.
- Prioritize realtime traffic such as remote desktop, interactive shells, and
  pointer/keyboard driven flows over bulk transfer throughput.
- Treat proxy and port-forward traffic as logical flows that can be rebound to
  a new physical substream after bridge/session rebuild.

## Non-Goals

- Replacing WebSocket/H3 transport negotiation.
- Rewriting TCP/UDP proxy business logic in the first phase.
- Full congestion control. The first flow-control target is credit-based
  backpressure, not bandwidth estimation.
- Maintaining wire compatibility with the old in-stream JSON substream open
  protocol.

## Protocol Layers

### Session Layer

Session-level frames use `StreamID = 0`.

Required controls:

- `hello`: sent by both sides after session creation.
- `hello_ack`: optional acknowledgement containing the effective negotiated
  values.
- `ping` / `pong`: existing heartbeat frames remain valid.

Negotiated fields:

- `version`
- `features`
- `max_frame_data`
- `min_frame_data`
- `preferred_frame_data`
- `bulk_frame_data`
- `max_control_bytes`
- `max_concurrent_streams`
- `initial_stream_window`
- `initial_session_window`
- `idle_timeout_ms`
- `ping_interval_ms`
- `realtime_preferred_frame_data`

The effective numeric value is the minimum of both peers' advertised limits.
Feature flags are the intersection of both peers' feature lists.

Adaptive frame-size defaults:

- `max_frame_data`: 65536 bytes
- `min_frame_data`: 4096 bytes
- `realtime_preferred_frame_data`: 4096 bytes
- `preferred_frame_data`: 16384 bytes
- `bulk_frame_data`: 65536 bytes
- Control frames use a priority write queue and must not wait behind queued
  data frames.
- Data frame size is selected per stream and per write:

Realtime or small writes use `realtime_preferred_frame_data`. Normal mixed
traffic starts with `preferred_frame_data`. Sustained large writes may grow
toward `bulk_frame_data`. Frame size never exceeds the negotiated
`max_frame_data`.

The sender must never wait for more bytes just to fill a larger frame. Adaptive
frame size is a maximum chunk size for bytes that are already available in the
current application write, not a batching delay policy.

### Stream Lifecycle

Stream-level control frames use a non-zero `StreamID`.

Controls:

- `open`: creates a stream and carries the initial tunnel intent.
- `open_result`: returns success/failure for `open`.
- `open_update`: activates a prepared stream or updates stream intent.
- `open_update_result`: returns success/failure for `open_update`.
- `fin`: sender will not write more data, but can still read.
- `close`: normal bidirectional close.
- `rst`: abnormal close with an error string/code.
- `window_update`: returns consumed credit to the peer.
- `flow_resume`: rebinds an existing logical flow to this physical stream.
- `flow_resume_result`: accepts or rejects the rebind and reports replay
  offsets.
- `priority`: optional open metadata hint. Values are `realtime`, `normal`, and
  `bulk`.

The existing `data` frame continues to carry payload bytes.

### Open Metadata

Open controls carry the same business metadata that was previously encoded as
the first JSON object in the byte stream:

```json
{
  "type": "open",
  "network": "tcp",
  "address": "example.com:443",
  "flow_id": "port-forward-...",
  "resume_token": "opaque-resume-token",
  "resume_epoch": 2,
  "read_offset": 1048576,
  "write_offset": 983040,
  "session_id": "downstream-forward-1",
  "association_v2": {},
  "source_ip": "203.0.113.10",
  "priority": "realtime"
}
```

Prepared stream flow:

1. Caller sends `open` with `type = "prepare"`.
2. Exit or next relay returns `open_result{ok:true}` when the relay path is
   ready and idle.
3. Caller later sends `open_update` with `type = "open"` and target metadata.
4. Exit returns `open_update_result`.
5. Payload starts only after a successful update result.

### Rebuildable Logical Flows

The frame stream is a physical carrier, not the identity of a proxy or
port-forward flow. `flow_id` identifies the logical flow. `resume_token`
authorizes reattachment, `resume_epoch` orders successive carriers, and
`read_offset` / `write_offset` describe the highest contiguous bytes processed
by each side.

Rebuild policy:

- A bridge/session failure suspends affected logical flows instead of
  immediately treating them as application EOF.
- A replacement stream sends `flow_resume` or `open{type:"resume"}` with the
  same `flow_id`, valid `resume_token`, incremented `resume_epoch`, and the
  caller's durable offsets.
- The peer accepts only if the logical flow is still within its resume TTL and
  the requested offsets fit its replay window.
- TCP port-forward resume requires a replay buffer and ordered ACK accounting on
  both directions. Without those, the implementation must fail fast instead of
  pretending a fresh TCP connection is the old flow.
- UDP association resume can rebind more cheaply because datagrams are already
  message-oriented; stale datagrams may be dropped after the association TTL.
- `fin`, `close`, and `rst` end the logical flow. Physical carrier loss only
  suspends the flow until resume TTL expires.

Initial implementation adds the control fields and keeps `flow_id` stable
across opens. Full byte-exact TCP resume is a later phase because it needs ACK,
sequence, and replay-window enforcement.

### Close Semantics

- `fin`: half-close write direction. Reader continues until peer finishes.
- `close`: normal full close. Both sides release stream state after draining.
- `rst`: error close. Both sides release stream state immediately and surface
  the error to readers/writers.

There is no legacy close fallback in the new protocol. A peer that does not
understand the close controls must fail session negotiation.

### Flow Control

The protocol should use credit-based flow control:

- Each stream starts with `initial_stream_window`.
- The session starts with `initial_session_window`.
- Sending data consumes both stream and session window.
- Reading data and freeing buffer space emits `window_update`.
- A sender blocks or times out when credit is exhausted.

Phase 1 records and negotiates the values and adds write queue prioritization.
Phase 2 enforces credit accounting.

## Realtime Policy

Remote desktop and similar workloads are latency-sensitive and bursty. The
transport must avoid head-of-line delay caused by bulk streams:

- Control frames are always written before data frames.
- Realtime streams use smaller chunks than bulk streams.
- Frame size is adaptive, not fixed. The initial heuristic is priority plus
  write size; later phases can include RTT, pending control frames, stream
  window pressure, and packet loss signals.
- Adaptive sizing must not introduce a Nagle-like delay. If an application write
  is small, it is sent immediately as a small frame.
- The scheduler should eventually round-robin between active streams instead of
  letting one stream enqueue unlimited data.
- Open metadata should include a priority hint; absent hints default to
  `normal`.
- Bulk streams may use larger chunks only when no realtime stream is waiting.
- Rebuild/resume controls have the same priority as other control frames and
  must not queue behind bulk data.

## Implementation Phases

### Phase 1: Control-Plane Open

- Add session `hello` negotiation and effective config storage.
- Add `OpenWithRequest`, `WaitOpenUpdate`, and response helpers.
- Migrate probe chain port-forward open/prepare from in-stream JSON to frame
  control.
- Relay nodes forward `open` and `open_update` controls before byte relay.
- Remove in-stream JSON open handling from the probe chain path.

### Phase 2: Close Semantics

- Split `CloseWrite`, `Close`, and `Reset`.
- Map relay copy direction completion to `fin`.
- Surface `rst` errors to readers.

### Phase 3: Flow Control

- Enforce stream/session windows.
- Emit `window_update` after application reads.
- Add metrics for blocked writers and effective window.
- Add realtime/bulk scheduling based on priority and stream activity.

### Phase 4: Flow Rebuild

- Add per-flow state keyed by `flow_id`.
- Add directional sequence numbers, ACKs, and bounded replay buffers.
- Implement `flow_resume` / `flow_resume_result` and suspend TTLs.
- Rebind UDP associations first, then TCP port-forward flows once replay
  accounting is available.
- Expose suspended/resumed/expired counters in substream monitoring.

### Phase 5: Mobile Sync

- Port the same control-plane and negotiation changes to
  `probe_node/mobilecore`.
- Android and desktop nodes must be upgraded together for this protocol change.
