module github.com/cloudhelper/probe_node

go 1.26.4

require (
	github.com/gorilla/websocket v1.5.3
	github.com/hashicorp/yamux v0.1.2
	github.com/quic-go/quic-go v0.60.0
	golang.org/x/net v0.56.0
	golang.org/x/sys v0.46.0
	gvisor.dev/gvisor v0.0.0-20250523182742-eede7a881b20
)

require (
	github.com/google/btree v1.1.3 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/mobile v0.0.0-20260611195102-4dd8f1dbf5d2 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.org/x/tools v0.46.0 // indirect
)

tool (
	golang.org/x/mobile/cmd/gobind
	golang.org/x/mobile/cmd/gomobile
)
