package voice

import (
	"fmt"
	"testing"
)

// benchGridN, benchGridL and benchArms are the shared B-0 grid.
var (
	benchGridN = []int{100, 1000, 5000}
	benchGridL = []int{10, 100}
	benchArms  = []loggerArm{armText, armBus}
)

// BenchmarkSocketSend measures only the kernel-send cost of a fan-out: per op
// the packet is serialised once and written to each of the l listeners with
// WriteToUDP, with no locks and no roster scan.
//
// Parallel mode calls b.SetParallelism(1) and b.RunParallel, which starts
// 1 x GOMAXPROCS goroutines (https://pkg.go.dev/testing#B.RunParallel).
//
// mode=parallel ns/send is wall time divided by the total sends across all
// goroutines, i.e. an inverse-throughput figure that measures contention on
// the one shared socket. It is not per-send latency, is not comparable with
// mode=single, and must not be fed into t_send; the model uses mode=single.
//
// The log= arm is a no-op for this benchmark: the loop calls only
// SerializePacket and WriteToUDP, neither of which logs, so the text and bus
// arms run identical code and differences between them are noise. The arm
// exists only to keep the grid aligned with the other benchmarks (the bus arm
// matters for handleVoicePacket). Aggregate t_send over arms, or take the
// median over arms.
//
// Cell N=100/L=100 deviates from its label: a roster of 100 cannot hold 100
// listeners beside the talker, so it runs with effectiveListeners = 99 and
// ns/send is divided by 99. Benchmarks reusing the grid must call
// effectiveListeners and divide their metrics by the effective L.
//
// Drain policy: the listener sockets are NOT drained. They overflow their
// 8<<20 receive buffer and the kernel drops the excess; WriteToUDP on loopback
// does not block on a full receiver, so the send cost stays measurable.
func BenchmarkSocketSend(b *testing.B) {
	for _, n := range benchGridN {
		for _, l := range benchGridL {
			for _, arm := range benchArms {
				name := fmt.Sprintf("N=%d/L=%d/log=%s", n, l, arm)
				b.Run(name, func(b *testing.B) {
					eff := effectiveListeners(n, l)
					r := newBenchRoster(b, n, eff, arm)
					clients := r.s.GetListeningClients(r.pkt, r.talker)
					if len(clients) != eff {
						b.Fatalf("roster has %d listeners, want %d", len(clients), eff)
					}
					b.Run("mode=single", func(b *testing.B) {
						b.ReportAllocs()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							data := r.pkt.SerializePacket()
							for _, c := range clients {
								_, _ = r.s.conn.WriteToUDP(data, c.Addr)
							}
						}
						b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*eff), "ns/send")
					})
					b.Run("mode=parallel", func(b *testing.B) {
						b.ReportAllocs()
						b.SetParallelism(1)
						b.ResetTimer()
						b.RunParallel(func(pb *testing.PB) {
							for pb.Next() {
								data := r.pkt.SerializePacket()
								for _, c := range clients {
									_, _ = r.s.conn.WriteToUDP(data, c.Addr)
								}
							}
						})
						b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*eff), "ns/send")
					})
				})
			}
		}
	}
}
