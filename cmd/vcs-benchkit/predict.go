package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	benchSend = "BenchmarkSocketSend"
	benchScan = "BenchmarkGetListeningClients"

	// budgetNsPerSec is the 80% utilisation budget of one core, in ns per second.
	budgetNsPerSec = 0.8e9
	// framesPerSec is the 20 ms voice frame rate.
	framesPerSec = 50
	// orgTalkerFraction and orgListeners come from design section 4.6:
	// 5% of the roster talks, each to ((4+12)/2 - 1) * 1.1 = 7.7 listeners.
	orgTalkerFraction = 0.05
	orgListeners      = 7.7
	maxKneeN          = 1 << 24

	orgOpBasis = "not a validated prediction; sweep start only"
	capNote    = "; knee hit the 2^24 search cap, the model never reaches the budget"
)

// BenchRow is one benchmark key with the median over -count repeats.
type BenchRow struct {
	Bench       string
	N, L        int
	Logger      string
	Mode        string
	NsPerOp     float64
	Metric      map[string]float64
	AllocsPerOp float64
}

// Prediction is one predicted knee for a scenario and arm.
type Prediction struct {
	Scenario  string `json:"scenario"`
	Arm       string `json:"arm"`
	KneeN     int    `json:"knee_n"`
	Validated bool   `json:"validated"`
	Basis     string `json:"basis"`
}

type rowKey struct {
	bench, logger, mode string
	n, l                int
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

// ParseBenchOutput parses `go test -bench` output. Non-benchmark lines and
// bare single-field parent lines (from -test.v) are ignored; any other
// malformed Benchmark line is an error.
func ParseBenchOutput(r io.Reader) ([]BenchRow, error) {
	samples := map[rowKey]map[string][]float64{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if !strings.HasPrefix(line, "Benchmark") {
			continue
		}
		f := strings.Fields(line)
		if len(f) == 1 {
			// Bare parent line emitted by -test.v; no result on it.
			continue
		}
		if len(f) < 4 || len(f)%2 != 0 {
			return nil, fmt.Errorf("line %d: malformed benchmark line %q", lineNo, line)
		}
		key, err := parseName(f[0])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		if _, err := strconv.ParseInt(f[1], 10, 64); err != nil {
			return nil, fmt.Errorf("line %d: bad iteration count %q", lineNo, f[1])
		}
		m := samples[key]
		if m == nil {
			m = map[string][]float64{}
			samples[key] = m
		}
		for i := 2; i < len(f); i += 2 {
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil {
				return nil, fmt.Errorf("line %d: bad value %q", lineNo, f[i])
			}
			m[f[i+1]] = append(m[f[i+1]], v)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	rows := make([]BenchRow, 0, len(samples))
	for k, m := range samples {
		row := BenchRow{Bench: k.bench, N: k.n, L: k.l, Logger: k.logger, Mode: k.mode, Metric: map[string]float64{}}
		for unit, vs := range m {
			med := median(vs)
			switch unit {
			case "ns/op":
				row.NsPerOp = med
			case "allocs/op":
				row.AllocsPerOp = med
			default:
				row.Metric[unit] = med
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Bench != b.Bench {
			return a.Bench < b.Bench
		}
		if a.N != b.N {
			return a.N < b.N
		}
		if a.L != b.L {
			return a.L < b.L
		}
		if a.Logger != b.Logger {
			return a.Logger < b.Logger
		}
		return a.Mode < b.Mode
	})
	return rows, nil
}

// parseName splits Benchmark<Name>/N=<n>/L=<l>/log=<arm>/mode=<mode>-<procs>.
func parseName(name string) (rowKey, error) {
	parts := strings.Split(name, "/")
	last := len(parts) - 1
	if i := strings.LastIndexByte(parts[last], '-'); i > 0 {
		if _, err := strconv.Atoi(parts[last][i+1:]); err == nil {
			parts[last] = parts[last][:i]
		}
	}
	k := rowKey{bench: parts[0]}
	for _, p := range parts[1:] {
		switch {
		case strings.HasPrefix(p, "N="):
			n, err := strconv.Atoi(p[2:])
			if err != nil {
				return k, fmt.Errorf("bad N in %q", name)
			}
			k.n = n
		case strings.HasPrefix(p, "L="):
			l, err := strconv.Atoi(p[2:])
			if err != nil {
				return k, fmt.Errorf("bad L in %q", name)
			}
			k.l = l
		case strings.HasPrefix(p, "log="):
			k.logger = p[4:]
		case strings.HasPrefix(p, "mode="):
			k.mode = p[5:]
		}
	}
	return k, nil
}

// gridPoint is one (x, y) sample of a model curve.
type gridPoint struct{ x, y float64 }

// pickCurve returns, per value of the grid axis (N or L), the median of the
// metric over the other axis, for mode=single rows of one logger arm.
func pickCurve(rows []BenchRow, bench, logger, unit string, byN bool) []gridPoint {
	vals := map[int][]float64{}
	for _, r := range rows {
		if r.Bench != bench || r.Logger != logger || r.Mode != "single" {
			continue
		}
		v, ok := r.Metric[unit]
		if !ok {
			continue
		}
		x := r.L
		if byN {
			x = r.N
		}
		vals[x] = append(vals[x], v)
	}
	pts := make([]gridPoint, 0, len(vals))
	for x, vs := range vals {
		pts = append(pts, gridPoint{float64(x), median(vs)})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].x < pts[j].x })
	// Cost cannot fall as the axis grows; a noisy dip is measurement error.
	// A running max keeps the curve monotone, which knee() relies on.
	for i := 1; i < len(pts); i++ {
		if pts[i].y < pts[i-1].y {
			pts[i].y = pts[i-1].y
		}
	}
	return pts
}

// interp is linear between grid points and linear beyond the ends from the
// nearest two points. It never returns a negative value.
func interp(pts []gridPoint, x float64) float64 {
	if len(pts) == 1 {
		return pts[0].y
	}
	i := sort.Search(len(pts), func(i int) bool { return pts[i].x >= x })
	switch {
	case i == 0:
		i = 1
	case i == len(pts):
		i = len(pts) - 1
	}
	a, b := pts[i-1], pts[i]
	y := a.y + (b.y-a.y)*(x-a.x)/(b.x-a.x)
	if y < 0 {
		return 0
	}
	return y
}

// knee is the largest N in [0, maxKneeN] with cost(N) <= budget, by bisection
// (cost is non-decreasing in N).
func knee(cost func(n float64) float64, budget float64) int {
	if cost(1) > budget {
		return 0
	}
	lo, hi := 1, maxKneeN
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		if cost(float64(mid)) <= budget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// Predict applies the design section 5 model to mode=single rows of one
// logger arm and returns the one-big-net and org-op knees for S, Seq and D2.
func Predict(rows []BenchRow, logger string) ([]Prediction, error) {
	send := pickCurve(rows, benchSend, logger, "ns/send", false)
	scan := pickCurve(rows, benchScan, logger, "ns/scan", true)
	if len(send) == 0 {
		return nil, fmt.Errorf("no %s mode=single ns/send rows for logger %q", benchSend, logger)
	}
	if len(scan) == 0 {
		return nil, fmt.Errorf("no %s mode=single ns/scan rows for logger %q", benchScan, logger)
	}
	if len(scan) < 2 {
		return nil, fmt.Errorf("%s needs at least two N grid points, got %d", benchScan, len(scan))
	}
	hasL := func(l float64) bool {
		for _, p := range send {
			if p.x == l {
				return true
			}
		}
		return false
	}
	if !hasL(10) || !hasL(100) {
		return nil, fmt.Errorf("%s needs L=10 and L=100 rows", benchSend)
	}
	tSend := func(l float64) float64 { return interp(send, l) }
	tScan := func(n float64) float64 { return interp(scan, n) }

	// one-big-net: one talker, one shard goroutine, so one core for every arm.
	bigCost := func(n float64) float64 {
		return framesPerSec*(n-1)*tSend(100) + framesPerSec*tScan(n)
	}
	bigKnee := knee(bigCost, budgetNsPerSec)

	var out []Prediction
	for _, arm := range []string{"S", "Seq", "D2"} {
		out = append(out, Prediction{
			Scenario: "one-big-net", Arm: arm, KneeN: bigKnee, Validated: true,
			Basis: withCapNote("one talker on one shard goroutine; capped at one core in every arm", bigKnee),
		})
	}

	orgArms := []struct {
		arm   string
		cores float64
		split float64 // each node's roster is N/split
	}{{"S", 1, 1}, {"Seq", 2, 1}, {"D2", 2, 2}}
	for _, a := range orgArms {
		cost := func(n float64) float64 {
			talkers := orgTalkerFraction * n
			return framesPerSec*talkers*orgListeners*tSend(10) + framesPerSec*talkers*tScan(n/a.split)
		}
		k := knee(cost, budgetNsPerSec*a.cores)
		out = append(out, Prediction{
			Scenario: "org-op", Arm: a.arm, KneeN: k,
			Validated: false, Basis: withCapNote(orgOpBasis, k),
		})
	}
	return out, nil
}

// withCapNote appends capNote to basis when the knee hit the search cap.
func withCapNote(basis string, kneeN int) string {
	if kneeN >= maxKneeN {
		return basis + capNote
	}
	return basis
}

func fmtMetric(row BenchRow, unit string) string {
	v, ok := row.Metric[unit]
	if !ok {
		return ""
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// writeMicroCSV writes one CSV line per benchmark key. ns_per_send_full
// (BenchmarkHandleVoicePacket) is appended after the plan's columns.
func writeMicroCSV(w io.Writer, rows []BenchRow) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"bench", "n", "l", "logger", "mode", "ns_per_op", "ns_per_send", "ns_per_scan", "allocs_per_op", "ns_per_send_full"}); err != nil {
		return err
	}
	for _, r := range rows {
		rec := []string{
			r.Bench, strconv.Itoa(r.N), strconv.Itoa(r.L), r.Logger, r.Mode,
			strconv.FormatFloat(r.NsPerOp, 'g', -1, 64),
			fmtMetric(r, "ns/send"), fmtMetric(r, "ns/scan"),
			strconv.FormatFloat(r.AllocsPerOp, 'g', -1, 64),
			fmtMetric(r, "ns/send-full"),
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func runPredict(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("predict", flag.ContinueOnError)
	micro := fs.String("micro", "", "go test -bench output file (required)")
	logger := fs.String("logger", "bus", "logger arm to model (text or bus)")
	csvOut := fs.String("csv", "", "optional path to write the micro CSV")
	lbl := fs.String("label", label, "label written to predict.json; must say \"relative only\"")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := checkLabel(*lbl); err != nil {
		return err
	}
	if *micro == "" {
		return fmt.Errorf("--micro is required")
	}
	f, err := os.Open(*micro)
	if err != nil {
		return err
	}
	defer f.Close()
	rows, err := ParseBenchOutput(f)
	if err != nil {
		return fmt.Errorf("%s: %w", *micro, err)
	}
	preds, err := Predict(rows, *logger)
	if err != nil {
		return err
	}
	if *csvOut != "" {
		cf, err := os.Create(*csvOut)
		if err != nil {
			return err
		}
		if err := writeMicroCSV(cf, rows); err != nil {
			cf.Close()
			return err
		}
		if err := cf.Close(); err != nil {
			return err
		}
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Label       string       `json:"label"`
		Predictions []Prediction `json:"predictions"`
	}{*lbl, preds})
}
