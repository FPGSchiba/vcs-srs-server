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
// Drain policy: the listener sockets are NOT drained. They overflow their
// 8<<20 receive buffer and the kernel drops the excess; WriteToUDP on loopback
// does not block on a full receiver, so the send cost stays measurable.
func BenchmarkSocketSend(b *testing.B) {
	for _, n := range benchGridN {
		for _, l := range benchGridL {
			for _, arm := range benchArms {
				name := fmt.Sprintf("N=%d/L=%d/log=%s", n, l, arm)
				b.Run(name, func(b *testing.B) {
					l := effectiveListeners(n, l)
					r := newBenchRoster(b, n, l, arm)
					clients := r.s.GetListeningClients(r.pkt, r.talker)
					if len(clients) != l {
						b.Fatalf("roster has %d listeners, want %d", len(clients), l)
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
						b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*l), "ns/send")
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
						b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*l), "ns/send")
					})
				})
			}
		}
	}
}
