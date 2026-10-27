package voice

import (
	"bytes"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
)

const msgQueueFull = "Voice shard queue full, dropping packet"

// fanoutFixture is a bound voice server with one talker and some listeners,
// all in coalition Blue with an enabled radio on 243.000 MHz.
type fanoutFixture struct {
	s         *Server
	h         *capturingHandler
	talker    uuid.UUID
	talkAddr  *net.UDPAddr
	listeners []*net.UDPConn
}

const fanoutFreqKHz = 243000

func newFanoutFixture(t *testing.T, nListeners int) *fanoutFixture {
	t.Helper()
	ss := &state.ServerState{}
	s, h := newCapturingTestServer(t, ss)
	f := &fanoutFixture{s: s, h: h}

	bind := func(id uuid.UUID, addr *net.UDPAddr) {
		ss.AddClient(id, &state.ClientState{Name: "Pilot", Coalition: "Blue"})
		ss.Lock()
		ss.RadioClients[id] = &state.RadioState{Radios: []state.Radio{{ID: 1, Frequency: 243.0, Enabled: true}}}
		ss.Unlock()
		s.Lock()
		s.clients[id] = &Client{Addr: addr, LastSeen: time.Now()}
		s.Unlock()
	}

	f.talker = uuid.New()
	talkPeer := newTestPeer(t)
	f.talkAddr = talkPeer.LocalAddr().(*net.UDPAddr)
	bind(f.talker, f.talkAddr)
	for i := 0; i < nListeners; i++ {
		peer := newTestPeer(t)
		_ = peer.SetReadBuffer(8 << 20)
		bind(uuid.New(), peer.LocalAddr().(*net.UDPAddr))
		f.listeners = append(f.listeners, peer)
	}
	return f
}

// frame returns a serialised voice frame from sender with sequence seq.
func frame(sender uuid.UUID, seq uint32) []byte {
	return NewVCSVoicePacket(sender, seq, fanoutFreqKHz, []byte("opus-payload-bytes")).SerializePacket()
}

// readSequences reads voice frames on peer until want arrive or idle passes
// with nothing received.
func readSequences(peer *net.UDPConn, want int, idle time.Duration) []uint32 {
	var seqs []uint32
	buf := make([]byte, BufferSize)
	for len(seqs) < want {
		_ = peer.SetReadDeadline(time.Now().Add(idle))
		n, _, err := peer.ReadFromUDP(buf)
		if err != nil {
			return seqs
		}
		if p, err := ParsePacket(buf[:n]); err == nil {
			seqs = append(seqs, p.Sequence)
		}
	}
	return seqs
}

// SRV-13: frames from one sender leave the server in arrival order.
func TestVoiceFanOutPreservesPerSenderOrder(t *testing.T) {
	f := newFanoutFixture(t, 1)
	f.s.startShards(4, 4096)
	defer f.s.stopShards()

	const total = 2000
	got := make(chan []uint32, 1)
	go func() { got <- readSequences(f.listeners[0], total, 2*time.Second) }()

	for i := 1; i <= total; i++ {
		f.s.dispatch(frame(f.talker, uint32(i)), f.talkAddr)
	}
	seqs := <-got
	if len(seqs) < total*95/100 {
		t.Fatalf("listener received %d of %d frames", len(seqs), total)
	}
	inversions := 0
	for i := 1; i < len(seqs); i++ {
		if seqs[i] < seqs[i-1] {
			inversions++
		}
	}
	if inversions != 0 {
		t.Fatalf("%d sequence inversions across %d received frames, want 0", inversions, len(seqs))
	}
}

// SRV-13: the goroutine count is bounded whatever is sent.
func TestVoiceDispatchGoroutinesBounded(t *testing.T) {
	s, _ := newCapturingTestServer(t, &state.ServerState{})
	release := make(chan struct{})
	s.shardHook = func(int) { <-release }
	base := runtime.NumGoroutine()
	s.startShards(4, 256)

	addr := loopbackUDPAddr()
	for i := 0; i < 10000; i++ {
		s.dispatch(frame(uuid.New(), uint32(i)), addr)
	}
	if n := runtime.NumGoroutine(); n > base+4+4 {
		t.Fatalf("%d goroutines after 10,000 datagrams, want <= %d", n, base+8)
	}
	close(release)
	s.stopShards()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > base+2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > base+2 {
		t.Fatalf("%d goroutines after stopShards, want <= %d", n, base+2)
	}
}

func TestShardForStableAndInRange(t *testing.T) {
	for _, n := range []int{2, 3, 8} {
		for i := 0; i < 1000; i++ {
			id := uuid.New()
			a := shardFor(id, n)
			if a < 0 || a >= n {
				t.Fatalf("shardFor(%s, %d) = %d, out of range", id, n, a)
			}
			if b := shardFor(id, n); b != a {
				t.Fatalf("shardFor(%s, %d) unstable: %d then %d", id, n, a, b)
			}
		}
	}
}

func TestVoiceQueueFullDropIsRateLimited(t *testing.T) {
	s, h := newCapturingTestServer(t, &state.ServerState{})
	release := make(chan struct{})
	s.shardHook = func(int) { <-release }
	s.startShards(1, 1)
	defer s.stopShards()
	defer close(release)

	addr := loopbackUDPAddr()
	sender := uuid.New()
	for i := 0; i < 50; i++ {
		s.dispatch(frame(sender, uint32(i)), addr)
	}
	if n := len(h.recordsWithMsg(msgQueueFull)); n != 1 {
		t.Fatalf("%d %q records for 48 drops in one window, want 1", n, msgQueueFull)
	}
	for i := 0; i < 10; i++ {
		s.dispatch([]byte{0xFF}, addr)
	}
	if n := len(h.recordsWithMsg(msgParseFailed)); n != 1 {
		t.Fatalf("%d %q records, want 1 (its limiter is independent of the queue-full one)", n, msgParseFailed)
	}
}

// One flooded shard blocks neither the read loop nor the other shards.
func TestFloodedShardDoesNotBlockOthers(t *testing.T) {
	f := newFanoutFixture(t, 1)
	const n = 4
	flooder := uuid.New()
	for shardFor(flooder, n) == shardFor(f.talker, n) {
		flooder = uuid.New()
	}
	blocked := shardFor(flooder, n)
	release := make(chan struct{})
	f.s.shardHook = func(shard int) {
		if shard == blocked {
			<-release
		}
	}
	f.s.startShards(n, 256)
	defer f.s.stopShards()
	defer close(release)

	start := time.Now()
	for i := 0; i < 300; i++ {
		f.s.dispatch(frame(flooder, uint32(i)), loopbackUDPAddr())
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("dispatching 300 frames to a blocked shard took %v: dispatch blocked", d)
	}

	f.s.dispatch(frame(f.talker, 1), f.talkAddr)
	if seqs := readSequences(f.listeners[0], 1, 500*time.Millisecond); len(seqs) != 1 {
		t.Fatal("a frame on an unblocked shard did not reach its listener within 500ms")
	}
}

func TestBroadcastSendsIdenticalBytes(t *testing.T) {
	f := newFanoutFixture(t, 3)
	sent := frame(f.talker, 42)
	pkt, err := ParsePacket(sent)
	if err != nil {
		t.Fatal(err)
	}
	f.s.handleParsedPacket(pkt, f.talkAddr)

	buf := make([]byte, BufferSize)
	for i, l := range f.listeners {
		_ = l.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := l.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("listener %d: %v", i, err)
		}
		if !bytes.Equal(buf[:n], sent) {
			t.Fatalf("listener %d received different bytes than were sent", i)
		}
	}
}

func TestListenWaitsForWorkersOnStop(t *testing.T) {
	base := runtime.NumGoroutine()
	s := newTestServer()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.Listen("127.0.0.1:0", stop)
	}()
	waitFor(t, 2*time.Second, s.isRunning, "voice server running")
	close(stop)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Listen did not return within 2s of stop")
	}
	defer s.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > base+2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > base+2 {
		t.Fatalf("%d goroutines after Listen returned, want <= %d", n, base+2)
	}
}
