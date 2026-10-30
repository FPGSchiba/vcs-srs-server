package main

import (
	"bytes"
	"embed"
	"fmt"
	"html"
	"html/template"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/FPGSchiba/vcs-srs-server/tools/benchsite/schema"
)

//go:embed templates/*.html
var tplFS embed.FS

var tpls = template.Must(template.ParseFS(tplFS, "templates/*.html"))

var armOrder = []string{"S", "Seq", "D2"}

type loaded struct {
	ID  string
	Run schema.Run
}

// Render reads every run directory under in (each must decode against the
// bench-results/1 schema) and writes a static site into out: index.html and
// runs/<id>/index.html. It is deterministic: the same input is byte-identical.
func Render(in, out string) error {
	ents, err := os.ReadDir(in)
	if err != nil {
		return err
	}
	var runs []loaded
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		r, err := schema.ReadRun(filepath.Join(in, e.Name()))
		if err != nil {
			return fmt.Errorf("run %s: %w", filepath.Join(in, e.Name()), err)
		}
		runs = append(runs, loaded{ID: e.Name(), Run: r})
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID }) // newest first

	prev := make([]int, len(runs)) // index of nearest older comparable run, or -1
	for i := range runs {
		prev[i] = -1
		for j := i + 1; j < len(runs); j++ {
			if comparableRuns(runs[i].Run.Manifest, runs[j].Run.Manifest) {
				prev[i] = j
				break
			}
		}
	}

	idx := indexView{Title: "VCS server benchmark results", Label: schema.Label}
	for i, r := range runs {
		m := r.Run.Manifest
		row := indexRow{ID: r.ID, ServerSHA: short(m.ServerSHA), ClientSHA: short(m.ClientSHA),
			Image: m.Env.ImageVersion, Topology: m.Env.TopologyLabel, Knees: kneeSummary(r.Run)}
		if prev[i] >= 0 {
			row.Prev = runs[prev[i]].ID
		}
		idx.Runs = append(idx.Runs, row)
	}
	if err := write(filepath.Join(out, "index.html"), "index.html", idx); err != nil {
		return err
	}
	for i, r := range runs {
		var p *loaded
		if prev[i] >= 0 {
			p = &runs[prev[i]]
		}
		if err := write(filepath.Join(out, "runs", r.ID, "index.html"), "run.html", runPage(r, p)); err != nil {
			return err
		}
	}
	return nil
}

// comparableRuns reports whether two runs may be compared: same ImageVersion
// and the same machine profile (vCPU count and topology label).
func comparableRuns(a, b schema.Manifest) bool {
	return a.Env.ImageVersion == b.Env.ImageVersion && a.Env.VCPUs == b.Env.VCPUs &&
		a.Env.TopologyLabel == b.Env.TopologyLabel
}

func write(path, tpl string, data any) error {
	var buf bytes.Buffer
	if err := tpls.ExecuteTemplate(&buf, tpl, data); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

type indexRow struct{ ID, ServerSHA, ClientSHA, Image, Topology, Knees, Prev string }
type indexView struct {
	Title, Label string
	Runs         []indexRow
}

type kv struct{ K, V string }
type compareRow struct{ Scenario, Arm, Now, Then, Delta string }
type compareView struct {
	ID, Image string
	Rows      []compareRow
}
type headRow struct {
	Arm, ArmLabel, VCPUs, Topology, Knee, PerVCPU, RatioS, RatioSeq, Predicted string
	JoinP99, SecretP99, CPUSplit, Softirq, GenCap, Deregs                      string
}
type ratioRow struct{ Ratio, Legs, Median, Min, Max, Censored, Topology string }
type predRow struct{ Arm, Predicted, Measured, Ratio string }
type chartView struct {
	Arm string
	SVG template.HTML
}
type scenarioView struct {
	Name        string
	IsOneBigNet bool
	Headline    []headRow
	Charts      []chartView
	Ratios      []ratioRow
	Pred        []predRow
}
type microRow struct {
	Bench, N, L, Logger, Mode, NsPerOp, NsPerSend, NsPerScan, Allocs, NsPerSendFull string
}
type runView struct {
	Title, Label, ID                                   string
	RunURL, ServerURL, ClientURL, ServerSHA, ClientSHA string
	ServerDirty                                        bool
	Compare                                            *compareView
	Scenarios                                          []scenarioView
	Micro                                              []microRow
	Env                                                []kv
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func scenarios(r schema.Run) []string {
	set := map[string]bool{}
	for _, s := range r.Summary {
		set[s.Scenario] = true
	}
	for _, s := range r.Steps {
		set[s.Scenario] = true
	}
	for _, s := range r.Knees {
		set[s.Scenario] = true
	}
	var out []string
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func armsOf(r schema.Run, scenario string) []string {
	var out []string
	for _, a := range armOrder {
		for _, s := range r.Summary {
			if s.Scenario == scenario && s.Arm == a {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

func kneeSummary(r schema.Run) string {
	var parts []string
	for _, sc := range scenarios(r) {
		var xs []string
		for _, a := range armsOf(r, sc) {
			if s, ok := summaryOf(r, sc, a); ok {
				xs = append(xs, a+" "+strconv.Itoa(s.MedianN))
			}
		}
		if len(xs) > 0 {
			parts = append(parts, sc+": "+strings.Join(xs, ", "))
		}
	}
	return strings.Join(parts, "; ")
}

func summaryOf(r schema.Run, sc, arm string) (schema.SummaryRow, bool) {
	for _, s := range r.Summary {
		if s.Scenario == sc && s.Arm == arm {
			return s, true
		}
	}
	return schema.SummaryRow{}, false
}

func runPage(l loaded, p *loaded) runView {
	r, m := l.Run, l.Run.Manifest
	v := runView{Title: "Run " + l.ID, Label: schema.Label, ID: l.ID, RunURL: m.RunURL,
		ServerURL: "https://github.com/FPGSchiba/vcs-srs-server/commit/" + m.ServerSHA,
		ClientURL: "https://github.com/FPGSchiba/vcs-srs-client/commit/" + m.ClientSHA,
		ServerSHA: short(m.ServerSHA), ClientSHA: short(m.ClientSHA), ServerDirty: m.ServerDirty}
	if p != nil {
		v.Compare = compareWith(l, *p)
	}
	for _, sc := range scenarios(r) {
		v.Scenarios = append(v.Scenarios, scenarioPage(r, sc))
	}
	for _, x := range r.Micro {
		v.Micro = append(v.Micro, microRow{x.Bench, strconv.Itoa(x.N), strconv.Itoa(x.L), x.Logger, x.Mode,
			num(x.NsPerOp, 1), optF(x.NsPerSend, 1), optF(x.NsPerScan, 1), num(x.AllocsPerOp, 1), optF(x.NsPerSendFull, 1)})
	}
	v.Env = envBlock(m)
	return v
}

func compareWith(l, p loaded) *compareView {
	c := &compareView{ID: p.ID, Image: p.Run.Manifest.Env.ImageVersion}
	for _, sc := range scenarios(l.Run) {
		for _, a := range armsOf(l.Run, sc) {
			now, _ := summaryOf(l.Run, sc, a)
			then, ok := summaryOf(p.Run, sc, a)
			row := compareRow{Scenario: sc, Arm: a, Now: strconv.Itoa(now.MedianN), Then: "-", Delta: "-"}
			if ok {
				row.Then = strconv.Itoa(then.MedianN)
				if then.MedianN > 0 {
					row.Delta = fmt.Sprintf("%+.1f%%", 100*(float64(now.MedianN)/float64(then.MedianN)-1))
				}
			}
			c.Rows = append(c.Rows, row)
		}
	}
	return c
}

func envBlock(m schema.Manifest) []kv {
	e := m.Env
	out := []kv{
		{"Image OS", e.ImageOS}, {"Image version", e.ImageVersion}, {"Kernel", e.Kernel}, {"CPU model", e.CPUModel},
		{"vCPUs", strconv.Itoa(e.VCPUs)}, {"SMT", strconv.FormatBool(e.SMT)},
		{"Sibling sets", strings.Join(e.Siblings, " | ")},
		{"Topology", e.TopologyLabel},
		{"Steal ticks", fmt.Sprintf("%d over %d s", e.StealTicks, e.StealSeconds)},
		{"IRQ time accounting", e.IRQTimeAccounting},
		{"net.core.rmem_max", strconv.FormatInt(e.RmemMax, 10)}, {"net.core.wmem_max", strconv.FormatInt(e.WmemMax, 10)},
		{"Local port range", fmt.Sprintf("%d-%d", e.PortRange[0], e.PortRange[1])},
		{"Server Go", m.ServerGo}, {"Client Go", m.ClientGo},
		{"Join measurement", m.JoinLabel},
	}
	if c := m.Corpus; c != nil {
		out = append(out, kv{"Corpus sha256", c.SHA256},
			kv{"Corpus frames", strconv.Itoa(c.Frames)},
			kv{"Corpus frame bytes (min / median / p95 / max)", fmt.Sprintf("%d / %d / %d / %d", c.SizeMin, c.SizeMedian, c.SizeP95, c.SizeMax)})
	}
	for _, lg := range m.Legs {
		out = append(out, kv{fmt.Sprintf("Leg %d", lg.Leg),
			fmt.Sprintf("%s, seed %d, arm order %s", lg.Scenario, lg.Seed, strings.Join(lg.ArmOrder, ","))})
	}
	for _, pn := range m.Pins {
		out = append(out, kv{"Pin " + pn.Arm + " " + pn.Role,
			fmt.Sprintf("cpus %s, GOMAXPROCS %d, voice shards %d", pn.CPUs, pn.GOMAXPROCS, pn.Shards)})
	}
	return out
}

func scenarioPage(r schema.Run, sc string) scenarioView {
	v := scenarioView{Name: sc, IsOneBigNet: sc == "one-big-net"}
	ratio := func(name string) string {
		for _, x := range r.Ratios {
			if x.Scenario == sc && x.Ratio == name {
				return optF(x.Median, 2) + " (" + optF(x.Min, 2) + "-" + optF(x.Max, 2) + ")"
			}
		}
		return "-"
	}
	gen := genCapacity(r, sc) // per scenario (per leg), identical for every arm
	for _, a := range armsOf(r, sc) {
		s, _ := summaryOf(r, sc, a)
		h := headRow{Arm: a, ArmLabel: s.ArmLabel, Topology: s.Topology, Knee: kneeText(r, sc, s),
			RatioS: "-", RatioSeq: "-", Predicted: optI(s.PredictedKnee), VCPUs: "-", PerVCPU: "-", GenCap: gen}
		if n := pinnedCPUs(r.Manifest, a); n > 0 {
			h.VCPUs = strconv.Itoa(n)
			h.PerVCPU = num(float64(s.MedianN)/float64(n), 0)
		}
		switch a {
		case "Seq":
			h.RatioS = ratio("Seq/S")
		case "D2":
			h.RatioS, h.RatioSeq = ratio("D2/S"), ratio("D2/Seq")
		}
		fillStepStats(&h, r, sc, a)
		v.Headline = append(v.Headline, h)
		v.Charts = append(v.Charts, chartView{Arm: a, SVG: template.HTML(chartSVG(r, sc, a))})
		if v.IsOneBigNet {
			pr := predRow{Arm: a, Predicted: optI(s.PredictedKnee), Measured: strconv.Itoa(s.MedianN), Ratio: "-"}
			if s.PredictedKnee != nil && *s.PredictedKnee > 0 {
				pr.Ratio = num(float64(s.MedianN)/float64(*s.PredictedKnee), 2)
			}
			v.Pred = append(v.Pred, pr)
		}
	}
	for _, x := range r.Ratios {
		if x.Scenario == sc {
			v.Ratios = append(v.Ratios, ratioRow{x.Ratio, strconv.Itoa(x.Legs), optF(x.Median, 2), optF(x.Min, 2),
				optF(x.Max, 2), strconv.Itoa(x.CensoredLegs), x.Topology})
		}
	}
	return v
}

func kneeText(r schema.Run, sc string, s schema.SummaryRow) string {
	t := fmt.Sprintf("%d (%d-%d)", s.MedianN, s.MinN, s.MaxN)
	kinds := map[string]bool{}
	for _, k := range r.Knees {
		if k.Scenario == sc && k.Arm == s.Arm {
			kinds[k.ResultKind] = true
		}
	}
	switch {
	case kinds["generator-bound"]:
		t = ">= " + t + " (generator-bound)"
	case kinds["time-bound"]:
		t = ">= " + t + " (time-bound)"
	case s.Censored:
		t = ">= " + t
	}
	if s.Unconfirmed {
		t += " unconfirmed"
	}
	return t
}

// pinnedCPUs counts the CPUs the arm's server processes are pinned to.
func pinnedCPUs(m schema.Manifest, arm string) int {
	n := 0
	for _, p := range m.Pins {
		if p.Arm != arm {
			continue
		}
		for _, part := range strings.Split(p.CPUs, ",") {
			lo, hi, isRange := strings.Cut(part, "-")
			if !isRange {
				n++
				continue
			}
			a, _ := strconv.Atoi(lo)
			b, _ := strconv.Atoi(hi)
			n += b - a + 1
		}
	}
	return n
}

func fillStepStats(h *headRow, r schema.Run, sc, arm string) {
	h.JoinP99, h.SecretP99, h.CPUSplit, h.Softirq = "-", "-", "-", "-"
	var join, secret int64 = -1, -1
	var best *schema.StepRow
	var deregs uint64
	for i := range r.Steps {
		s := &r.Steps[i]
		if s.Scenario != sc || s.Arm != arm {
			continue
		}
		join = max(join, s.JoinP99Us)
		if s.SecretToAckP99 != nil {
			secret = max(secret, *s.SecretToAckP99)
		}
		deregs += s.NodeDeregs
		if s.Outcome == "pass" && (best == nil || s.N > best.N) {
			best = s
		}
	}
	if join > 0 {
		h.JoinP99 = num(float64(join)/1000, 1)
	}
	if secret >= 0 {
		h.SecretP99 = num(float64(secret)/1000, 1)
	}
	if best != nil {
		if best.ServerCPUByRole != "" {
			h.CPUSplit = strings.ReplaceAll(best.ServerCPUByRole, ";", " ")
		}
		h.Softirq = strconv.FormatUint(best.SoftIRQTicks, 10)
	}
	h.Deregs = strconv.FormatUint(deregs, 10)
}

func genCapacity(r schema.Run, sc string) string {
	legs := map[int]bool{}
	for _, l := range r.Manifest.Legs {
		if l.Scenario == sc {
			legs[l.Leg] = true
		}
	}
	var vals []float64
	lower := false
	for _, c := range r.Calibration {
		if c.Kind == "receive_capacity" && legs[c.Leg] && c.EgressPPS != nil {
			vals = append(vals, *c.EgressPPS)
			lower = lower || c.LowerBound
		}
	}
	if len(vals) == 0 {
		return "-"
	}
	t := num(median(vals), 0)
	if lower {
		t = ">= " + t
	}
	return t
}

func median(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func num(v float64, prec int) string { return strconv.FormatFloat(v, 'f', prec, 64) }
func optF(p *float64, prec int) string {
	if p == nil {
		return "-"
	}
	return num(*p, prec)
}
func optI(p *int) string {
	if p == nil {
		return "-"
	}
	return strconv.Itoa(*p)
}

// chartSVG draws loss (left axis) and one-way p99 (right axis) against N for
// one arm of one scenario, taking the median across legs at each N. Output is
// inline SVG with fixed-precision coordinates, so it is byte-stable.
func chartSVG(r schema.Run, sc, arm string) string {
	type pt struct{ loss, p99 []float64 }
	byN := map[int]*pt{}
	for _, s := range r.Steps {
		if s.Scenario != sc || s.Arm != arm {
			continue
		}
		p := byN[s.N]
		if p == nil {
			p = &pt{}
			byN[s.N] = p
		}
		if s.LossRatio != nil {
			p.loss = append(p.loss, *s.LossRatio*100)
		}
		if s.OneWayP99Ms != nil {
			p.p99 = append(p.p99, *s.OneWayP99Ms)
		}
	}
	var ns []int
	for n := range byN {
		ns = append(ns, n)
	}
	sort.Ints(ns)
	const w, h, ml, mr, mt, mb = 560.0, 280.0, 56.0, 56.0, 16.0, 40.0
	// Defence in depth: chartSVG is returned as template.HTML, which bypasses
	// html/template escaping, so escape every interpolated name here.
	sc, arm = html.EscapeString(sc), html.EscapeString(arm)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %.0f %.0f" width="%.0f" role="img" aria-labelledby="t-%s-%s">`, w, h, w, sc, arm)
	fmt.Fprintf(&b, `<title id="t-%s-%s">%s %s: loss percent and one-way p99 milliseconds against N</title>`, sc, arm, sc, arm)
	if len(ns) == 0 {
		fmt.Fprintf(&b, `<text x="%.0f" y="%.0f">no step data</text></svg>`, ml, h/2)
		return b.String()
	}
	maxN := float64(ns[len(ns)-1])
	var maxLoss, maxP99 float64
	lossAt, p99At := map[int]float64{}, map[int]float64{}
	hasLoss, hasP99 := map[int]bool{}, map[int]bool{}
	for _, n := range ns {
		if p := byN[n]; len(p.loss) > 0 {
			lossAt[n], hasLoss[n] = median(p.loss), true
			maxLoss = math.Max(maxLoss, lossAt[n])
		}
		if p := byN[n]; len(p.p99) > 0 {
			p99At[n], hasP99[n] = median(p.p99), true
			maxP99 = math.Max(maxP99, p99At[n])
		}
	}
	if maxLoss == 0 {
		maxLoss = 1
	}
	if maxP99 == 0 {
		maxP99 = 1
	}
	pw, ph := w-ml-mr, h-mt-mb
	x := func(n float64) float64 { return ml + pw*n/maxN }
	y := func(v, mx float64) float64 { return mt + ph*(1-v/mx) }
	for i := 0; i <= 4; i++ {
		f := float64(i) / 4
		yy := mt + ph*(1-f)
		fmt.Fprintf(&b, `<line class="grid" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`, ml, yy, ml+pw, yy)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="end">%s</text>`, ml-6, yy+4, num(maxLoss*f, 1))
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f">%s</text>`, ml+pw+6, yy+4, num(maxP99*f, 1))
	}
	for _, n := range ns {
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle">%d</text>`, x(float64(n)), mt+ph+16, n)
	}
	fmt.Fprintf(&b, `<line class="axis" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`, ml, mt+ph, ml+pw, mt+ph)
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle">N (bots)</text>`, ml+pw/2, h-6)
	fmt.Fprintf(&b, `<text x="12" y="%.1f" text-anchor="middle" transform="rotate(-90 12 %.1f)">loss %% (solid)</text>`, mt+ph/2, mt+ph/2)
	fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="middle" transform="rotate(90 %.1f %.1f)">one-way p99 ms (dashed)</text>`, w-10, mt+ph/2, w-10, mt+ph/2)
	line := func(class string, has map[int]bool, at map[int]float64, mx float64) {
		var pts []string
		for _, n := range ns {
			if has[n] {
				pts = append(pts, fmt.Sprintf("%.1f,%.1f", x(float64(n)), y(at[n], mx)))
			}
		}
		if len(pts) > 0 {
			fmt.Fprintf(&b, `<polyline class="%s" points="%s"/>`, class, strings.Join(pts, " "))
		}
		for _, n := range ns {
			if has[n] {
				fmt.Fprintf(&b, `<circle class="%sdot" cx="%.1f" cy="%.1f" r="3"/>`, class, x(float64(n)), y(at[n], mx))
			}
		}
	}
	line("loss", hasLoss, lossAt, maxLoss)
	line("p99", hasP99, p99At, maxP99)
	b.WriteString(`</svg>`)
	return b.String()
}
