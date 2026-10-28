package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func findRow(t *testing.T, rows []BenchRow, bench string, n, l int, mode string) BenchRow {
	t.Helper()
	for _, r := range rows {
		if r.Bench == bench && r.N == n && r.L == l && r.Mode == mode {
			return r
		}
	}
	t.Fatalf("row %s N=%d L=%d mode=%s not found", bench, n, l, mode)
	return BenchRow{}
}

func TestParseBenchOutput(t *testing.T) {
	f, err := os.Open("testdata/micro-sample.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := ParseBenchOutput(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("want 6 keys (12 lines, 2 repeats), got %d", len(rows))
	}
	r := findRow(t, rows, "BenchmarkSocketSend", 100, 10, "single")
	if r.Logger != "bus" || r.NsPerOp != 91000 || r.Metric["ns/send"] != 9100 || r.AllocsPerOp != 1 {
		t.Errorf("SocketSend L=10 single: %+v", r)
	}
	r = findRow(t, rows, "BenchmarkSocketSend", 100, 100, "parallel")
	if r.Metric["ns/send"] != 10200 {
		t.Errorf("SocketSend L=100 parallel median: %+v", r)
	}
	r = findRow(t, rows, "BenchmarkGetListeningClients", 100, 10, "single")
	if r.Metric["ns/scan"] != 6000 || r.NsPerOp != 6000 {
		t.Errorf("scan median: %+v", r)
	}

	if _, err := ParseBenchOutput(strings.NewReader("BenchmarkX/N=a 10\n")); err == nil {
		t.Error("garbled Benchmark line must be an error")
	}
	if _, err := ParseBenchOutput(strings.NewReader("BenchmarkX/N=a  10  5 ns/op\n")); err == nil {
		t.Error("non-numeric N must be an error")
	}
}

// synth builds the synthetic grid: ns/send 2000 at every point, ns/scan 1000
// at N=100, 10000 at N=1000 and 50000 at N=5000, for L in {10, 100}.
func synth(logger string) []BenchRow {
	scan := map[int]float64{100: 1000, 1000: 10000, 5000: 50000}
	var rows []BenchRow
	for n, s := range scan {
		for _, l := range []int{10, 100} {
			rows = append(rows,
				BenchRow{Bench: benchSend, N: n, L: l, Logger: logger, Mode: "single", Metric: map[string]float64{"ns/send": 2000}},
				BenchRow{Bench: benchScan, N: n, L: l, Logger: logger, Mode: "single", Metric: map[string]float64{"ns/scan": s}},
			)
		}
	}
	return rows
}

func pred(t *testing.T, ps []Prediction, scenario, arm string) Prediction {
	t.Helper()
	for _, p := range ps {
		if p.Scenario == scenario && p.Arm == arm {
			return p
		}
	}
	t.Fatalf("no prediction %s/%s", scenario, arm)
	return Prediction{}
}

func TestPredictOneBigNetEqualAcrossArms(t *testing.T) {
	ps, err := Predict(synth("bus"), "bus")
	if err != nil {
		t.Fatal(err)
	}
	// Hand computation. Budget 0.8e9 ns/s. Cost(N) = 50*(N-1)*2000 + 50*tscan(N).
	// Above N=1000 tscan(N) = 10000 + 10*(N-1000) (slope (50000-10000)/4000 = 10,
	// extended linearly beyond 5000). So
	//   cost = 1e5*(N-1) + 500000 + 500*(N-1000) = 100500*N - 1e5.
	// cost <= 8e8  <=>  N <= (8e8 + 1e5)/100500 = 7961.19, so the knee is 7961.
	const want = 7961
	for _, arm := range []string{"S", "Seq", "D2"} {
		p := pred(t, ps, "one-big-net", arm)
		if p.KneeN != want {
			t.Errorf("one-big-net %s: knee %d, want %d", arm, p.KneeN, want)
		}
		if !p.Validated {
			t.Errorf("one-big-net %s must be Validated", arm)
		}
	}
}

func TestPredictOrgOpUnvalidated(t *testing.T) {
	ps, err := Predict(synth("bus"), "bus")
	if err != nil {
		t.Fatal(err)
	}
	for _, arm := range []string{"S", "Seq", "D2"} {
		p := pred(t, ps, "org-op", arm)
		if p.Validated {
			t.Errorf("org-op %s must not be Validated", arm)
		}
		if p.Basis != "not a validated prediction; sweep start only" {
			t.Errorf("org-op %s basis %q", arm, p.Basis)
		}
		if p.KneeN <= 0 {
			t.Errorf("org-op %s knee %d", arm, p.KneeN)
		}
	}
	if s, q := pred(t, ps, "org-op", "S"), pred(t, ps, "org-op", "Seq"); q.KneeN < s.KneeN {
		t.Errorf("org-op Seq knee %d < S knee %d", q.KneeN, s.KneeN)
	}
}

func TestPredictRejectsMissingGrid(t *testing.T) {
	var rows []BenchRow
	for _, r := range synth("bus") {
		if r.Bench == benchSend {
			rows = append(rows, r)
		}
	}
	_, err := Predict(rows, "bus")
	if err == nil || !strings.Contains(err.Error(), "BenchmarkGetListeningClients") {
		t.Fatalf("want error naming BenchmarkGetListeningClients, got %v", err)
	}
	_, err = Predict(synth("text"), "bus")
	if err == nil {
		t.Fatal("no rows for the requested logger must be an error")
	}
}

func TestWriteMicroCSV(t *testing.T) {
	rows := []BenchRow{{Bench: benchSend, N: 100, L: 10, Logger: "bus", Mode: "single", NsPerOp: 90000, AllocsPerOp: 1, Metric: map[string]float64{"ns/send": 9000}}}
	var b bytes.Buffer
	if err := writeMicroCSV(&b, rows); err != nil {
		t.Fatal(err)
	}
	want := "bench,n,l,logger,mode,ns_per_op,ns_per_send,ns_per_scan,allocs_per_op\n" +
		"BenchmarkSocketSend,100,10,bus,single,90000,9000,,1\n"
	if b.String() != want {
		t.Errorf("csv:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestPredictOrgOpHandComputed(t *testing.T) {
	ps, err := Predict(synth("bus"), "bus")
	if err != nil {
		t.Fatal(err)
	}
	// ns/send is 2000 at every L. For N >= 1000 tscan(N) = 10N (see above), so
	// org-op cost(S) = 50*0.05N*7.7*2000 + 50*0.05N*tscan(N/split)
	//                = 38500N + 2.5N*tscan(N/split).
	// S   (1 core, split 1): 25N^2   + 38500N <= 8e8   -> N <= 4939.02 -> 4939
	// Seq (2 cores, split 1): 25N^2  + 38500N <= 1.6e9 -> N <= 7266.97 -> 7266
	// D2  (2 cores, split 2): tscan(N/2) = 5N for N >= 2000, so
	//                         12.5N^2 + 38500N <= 1.6e9 -> N <= 9878.04 -> 9878
	want := map[string]int{"S": 4939, "Seq": 7266, "D2": 9878}
	for arm, k := range want {
		if got := pred(t, ps, "org-op", arm).KneeN; got != k {
			t.Errorf("org-op %s: knee %d, want %d", arm, got, k)
		}
	}
}

func TestPredictEnforcesMonotoneScanCurve(t *testing.T) {
	// A noisy grid where tscan dips at N=5000. Without a running max the
	// extrapolation beyond 5000 has a negative slope and the knee is wrong.
	scan := map[int]float64{100: 1000, 1000: 10000, 5000: 5000}
	var rows []BenchRow
	for n, s := range scan {
		for _, l := range []int{10, 100} {
			rows = append(rows,
				BenchRow{Bench: benchSend, N: n, L: l, Logger: "bus", Mode: "single", Metric: map[string]float64{"ns/send": 2000}},
				BenchRow{Bench: benchScan, N: n, L: l, Logger: "bus", Mode: "single", Metric: map[string]float64{"ns/scan": s}},
			)
		}
	}
	ps, err := Predict(rows, "bus")
	if err != nil {
		t.Fatal(err)
	}
	// Running max flattens tscan at 10000 from N=1000, so
	// cost = 1e5*(N-1) + 5e5 <= 8e8 -> N <= 7996.
	if got := pred(t, ps, "one-big-net", "S").KneeN; got != 7996 {
		t.Errorf("knee %d, want 7996", got)
	}
}

func TestPredictNotesKneeAtCap(t *testing.T) {
	var rows []BenchRow
	for _, r := range synth("bus") {
		for k := range r.Metric {
			r.Metric[k] = 0
		}
		rows = append(rows, r)
	}
	ps, err := Predict(rows, "bus")
	if err != nil {
		t.Fatal(err)
	}
	p := pred(t, ps, "one-big-net", "S")
	if p.KneeN != maxKneeN || !strings.Contains(p.Basis, "2^24") {
		t.Errorf("want cap note, got knee %d basis %q", p.KneeN, p.Basis)
	}
	o := pred(t, ps, "org-op", "S")
	if !strings.HasPrefix(o.Basis, orgOpBasis) || !strings.Contains(o.Basis, "2^24") {
		t.Errorf("org-op basis %q", o.Basis)
	}
}

func TestParseBenchOutputSkipsBareParentLines(t *testing.T) {
	in := "BenchmarkSocketSend\nBenchmarkSocketSend/N=100/L=10/log=bus/mode=single-8  10  91000 ns/op  9100 ns/send  1 allocs/op\n"
	rows, err := ParseBenchOutput(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
}
