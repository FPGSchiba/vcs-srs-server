package voice

import (
	"fmt"
	"testing"
	"time"
)

// TestHandleVoicePacketReachesEveryListener guards the benchmark fixture: a
// single handleVoicePacket call must deliver exactly one datagram to each
// listener socket, so BenchmarkHandleVoicePacket measures a real fan-out.
func TestHandleVoicePacketReachesEveryListener(t *testing.T) {
	r := newBenchRoster(t, 100, 10, armText)
	r.s.handleVoicePacket(r.pkt, r.talkAddr)
	buf := make([]byte, 2048)
	for i, c := range r.listeners {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := c.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("listener %d: no datagram: %v", i, err)
		}
		p, err := ParsePacket(buf[:n])
		if err != nil {
			t.Fatalf("listener %d: parse: %v", i, err)
		}
		if p.Sequence != 1 {
			t.Errorf("listener %d: sequence = %d, want 1", i, p.Sequence)
		}
	}
}

// BenchmarkHandleVoicePacket measures the full receive path of one voice
// frame (voice/server.go handleVoicePacket): isBoundAddr, the v.Lock for
// LastSeen, IsClientMuted and broadcastVoice, including its per-send Debug
// log. ns/send-full is ns/op divided by the effective listener count, so it
// is comparable with BenchmarkSocketSend's ns/send; the difference is the
// lock, scan and logging overhead. Here the log= arm matters: the bus arm
// builds and publishes every Debug record.
//
// The grid, mode semantics, the N=100/L=100 deviation and the drain policy
// are as documented on BenchmarkSocketSend.
func BenchmarkHandleVoicePacket(b *testing.B) {
	for _, n := range benchGridN {
		for _, l := range benchGridL {
			for _, arm := range benchArms {
				name := fmt.Sprintf("N=%d/L=%d/log=%s", n, l, arm)
				b.Run(name, func(b *testing.B) {
					eff := effectiveListeners(n, l)
					r := newBenchRoster(b, n, eff, arm)
					if got := len(r.s.GetListeningClients(r.pkt, r.talker)); got != eff {
						b.Fatalf("roster has %d listeners, want %d", got, eff)
					}
					b.Run("mode=single", func(b *testing.B) {
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							r.s.handleVoicePacket(r.pkt, r.talkAddr)
						}
						b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*eff), "ns/send-full")
					})
					b.Run("mode=parallel", func(b *testing.B) {
						b.ReportAllocs()
						b.SetParallelism(1)
						b.ResetTimer()
						b.RunParallel(func(pb *testing.PB) {
							for pb.Next() {
								r.s.handleVoicePacket(r.pkt, r.talkAddr)
							}
						})
						b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*eff), "ns/send-full")
					})
				})
			}
		}
	}
}
