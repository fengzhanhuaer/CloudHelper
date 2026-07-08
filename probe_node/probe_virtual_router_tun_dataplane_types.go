package main

type probeVirtualRouterTUNDataPlaneStats struct {
	Running              bool
	RXPackets            uint64
	RXBytes              uint64
	TXPackets            uint64
	TXBytes              uint64
	InboundQueueDepth    int
	InboundQueueCapacity int
}

type probeVirtualRouterTUNDataPlane interface {
	Close() error
	Stats() probeVirtualRouterTUNDataPlaneStats
	WritePacket(packet []byte) error
}
