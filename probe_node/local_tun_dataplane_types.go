package main

type probeLocalTUNDataPlaneStats struct {
	Running   bool
	RXPackets uint64
	RXBytes   uint64
	TXPackets uint64
	TXBytes   uint64
}

type probeLocalTUNDataPlane interface {
	Close() error
	Stats() probeLocalTUNDataPlaneStats
	WritePacket(packet []byte) error
}
