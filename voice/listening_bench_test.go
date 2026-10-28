package voice

import (
	"fmt"
	"testing"
)

// BenchmarkGetListeningClients measures the roster scan plus the per-entry
// locks (proposal H3 and H-lock): per op, s.GetListeningClients(pkt, talker).
// ns/scan is the cost of one whole scan.
//
// The grid, arms and modes are those of BenchmarkSocketSend; see its comment
// for the mode=parallel meaning (inverse throughput, not latency), the
// N=100/L=100 effective-listener deviation, and the drain policy. The log=
// arm is a no-op here: the scan does not log, so the arms are identical
// code and differ only by noise.
func BenchmarkGetListeningClients(b *testing.B) {
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
							_ = r.s.GetListeningClients(r.pkt, r.talker)
						}
						b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N), "ns/scan")
					})
					b.Run("mode=parallel", func(b *testing.B) {
						b.ReportAllocs()
						b.SetParallelism(1)
						b.ResetTimer()
						b.RunParallel(func(pb *testing.PB) {
							for pb.Next() {
								_ = r.s.GetListeningClients(r.pkt, r.talker)
							}
						})
						b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N), "ns/scan")
					})
				})
			}
		}
	}
}
