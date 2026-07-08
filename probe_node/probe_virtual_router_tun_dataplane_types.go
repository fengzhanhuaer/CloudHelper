package main

type probeVirtualRouterTUNDataPlaneStats struct {
	Running                      bool
	RXPackets                    uint64
	RXBytes                      uint64
	TXPackets                    uint64
	TXBytes                      uint64
	InboundQueueDepth            int
	InboundQueueCapacity         int
	InboundEntryQueueDepth       int
	InboundEntryQueueCapacity    int
	InboundDispatchQueueDepth    int
	InboundDispatchQueueCapacity int
	InboundDispatchWorkers       int
}

type probeVirtualRouterTUNDataPlane interface {
	Close() error
	Stats() probeVirtualRouterTUNDataPlaneStats
	WritePacket(packet []byte) error
}

func makeProbeVirtualRouterTUNInboundDispatchShards(shardCount int, totalCapacity int) []chan []byte {
	if shardCount <= 0 {
		return nil
	}
	shardCapacity := totalCapacity / shardCount
	if shardCapacity <= 0 {
		shardCapacity = 1
	}
	shards := make([]chan []byte, 0, shardCount)
	for i := 0; i < shardCount; i++ {
		shards = append(shards, make(chan []byte, shardCapacity))
	}
	return shards
}

func snapshotProbeVirtualRouterTUNInboundQueues(entry chan []byte, shards []chan []byte) (int, int, int, int, int) {
	entryDepth, entryCapacity := 0, 0
	if entry != nil {
		entryDepth = len(entry)
		entryCapacity = cap(entry)
	}
	dispatchDepth, dispatchCapacity := 0, 0
	for _, shard := range shards {
		if shard == nil {
			continue
		}
		dispatchDepth += len(shard)
		dispatchCapacity += cap(shard)
	}
	return entryDepth, entryCapacity, dispatchDepth, dispatchCapacity, len(shards)
}
