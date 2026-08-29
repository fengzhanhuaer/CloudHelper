package main

import "strconv"

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
	OutboundQueueDepth           int
	OutboundQueueCapacity        int
	OutboundWorkers              int
	TXDropped                    uint64
	TXErrors                     uint64
	TXSlowWrites                 uint64
	TXSlowQueueWaits             uint64
	TXLastWriteMs                uint64
	TXMaxWriteMs                 uint64
	TXLastQueueWaitMs            uint64
	TXMaxQueueWaitMs             uint64
	TXMaxLockWaitUS              uint64
	TXMaxAllocateUS              uint64
	TXMaxCopyUS                  uint64
	TXMaxSendUS                  uint64
}

type probeVirtualRouterTUNDataPlane interface {
	Close() error
	Stats() probeVirtualRouterTUNDataPlaneStats
	WritePacket(packet []byte) error
}

func makeProbeVirtualRouterTUNInboundDispatchShards(idPrefix string, shardCount int, totalCapacity int) []*probeAdaptiveQueue[[]byte] {
	if shardCount <= 0 {
		return nil
	}
	maxShardCapacity := totalCapacity / shardCount
	if maxShardCapacity <= 0 {
		maxShardCapacity = 1
	}
	initialShardCapacity := maxShardCapacity / 16
	if initialShardCapacity <= 0 {
		initialShardCapacity = 1
	}
	shards := make([]*probeAdaptiveQueue[[]byte], 0, shardCount)
	for i := 0; i < shardCount; i++ {
		shards = append(shards, newProbeAdaptiveQueue[[]byte](probeAdaptiveQueueOptions{
			ID:              idPrefix + ".inbound.dispatch." + strconv.Itoa(i),
			Stage:           "tun_inbound_dispatch",
			Direction:       "rx",
			Shard:           i,
			HasShard:        true,
			InitialCapacity: initialShardCapacity,
			MaxCapacity:     maxShardCapacity,
		}))
	}
	return shards
}

func snapshotProbeVirtualRouterTUNInboundQueues(entry *probeAdaptiveQueue[[]byte], shards []*probeAdaptiveQueue[[]byte]) (int, int, int, int, int) {
	entryDepth, entryCapacity := 0, 0
	if entry != nil {
		entryDepth = entry.Len()
		entryCapacity = entry.Capacity()
	}
	dispatchDepth, dispatchCapacity := 0, 0
	for _, shard := range shards {
		if shard == nil {
			continue
		}
		dispatchDepth += shard.Len()
		dispatchCapacity += shard.Capacity()
	}
	return entryDepth, entryCapacity, dispatchDepth, dispatchCapacity, len(shards)
}

func snapshotProbeVirtualRouterTUNOutboundQueue(queue *probeAdaptiveQueue[[]byte]) (int, int, int) {
	if queue == nil {
		return 0, 0, 0
	}
	return queue.Len(), queue.Capacity(), 1
}
