package voice

import (
	"hash/fnv"
	"net"
	"runtime"
	"time"

	"github.com/google/uuid"
)

// Fan-out scheduling (SRV-13). The read loop parses each datagram and hands
// it to one of a fixed set of shard workers, chosen by a hash of the sender
// ID. One sender always lands on the same shard and each shard is FIFO, so a
// sender's frames leave the server in the order they arrived. The goroutine
// count is fixed whatever arrives: there is no per-packet or per-sender
// goroutine, and no per-sender state a spoofed ID could grow.
//
// Only scheduling changes here: the payload is forwarded byte for byte.
const (
	// maxShards caps the number of shard workers.
	maxShards = 8
	// minShards keeps one talker's fan-out from delaying every other talker.
	minShards = 2
	// shardQueueDepth bounds each shard's queue; a full queue drops.
	shardQueueDepth = 256
)

type shardJob struct {
	pkt  *VCSPacket
	addr *net.UDPAddr
}

// shardCount is min(GOMAXPROCS, maxShards), at least minShards.
func shardCount() int {
	return max(minShards, min(runtime.GOMAXPROCS(0), maxShards))
}

// shardFor maps a sender ID to a shard in [0, n): FNV-1a 32 over the ID bytes.
func shardFor(id uuid.UUID, n int) int {
	h := fnv.New32a()
	_, _ = h.Write(id[:])
	return int(h.Sum32() % uint32(n))
}

// startShards starts n shard workers, each with a queue of depth jobs.
func (v *Server) startShards(n, depth int) {
	v.shardQueues = make([]chan shardJob, n)
	for i := range v.shardQueues {
		q := make(chan shardJob, depth)
		v.shardQueues[i] = q
		v.shardWG.Add(1)
		go v.shardWorker(i, q)
	}
}

func (v *Server) shardWorker(shard int, q <-chan shardJob) {
	defer v.shardWG.Done()
	for job := range q {
		if v.shardHook != nil {
			v.shardHook(shard)
		}
		v.handleParsedPacket(job.pkt, job.addr)
	}
}

// stopShards closes every shard queue and waits for the workers to drain and
// exit. The caller must be the only sender (the read loop, after it ended).
func (v *Server) stopShards() {
	for _, q := range v.shardQueues {
		close(q)
	}
	v.shardWG.Wait()
	v.shardQueues = nil
}

// dispatch parses one datagram and queues it on its sender's shard. It never
// blocks: when the shard's queue is full the packet is dropped, with a
// rate-limited WARN keyed by a fixed reason (never by address).
func (v *Server) dispatch(data []byte, addr *net.UDPAddr) {
	packet, err := ParsePacket(data)
	if err != nil {
		v.logParseFailure(addr, err)
		return
	}
	shard := shardFor(packet.SenderID, len(v.shardQueues))
	select {
	case v.shardQueues[shard] <- shardJob{pkt: packet, addr: addr}:
	default:
		if ok, n := v.queueFullRejects.shouldLog(time.Now()); ok {
			v.logger.Warn("Voice shard queue full, dropping packet", "shard", shard, "suppressed_since_last", n)
		}
	}
}
