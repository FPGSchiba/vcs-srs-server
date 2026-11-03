package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/leg"
	"github.com/FPGSchiba/vcs-srs-server/tools/benchsite/schema"
)

// armOrder is the canonical arm order of every published table.
var armOrder = []string{"S", "Seq", "D2"}

// topologyEnum maps a free-form topology string (the Actions probe's label, or
// bench-host's "<host>: server ..., server pair <pair>") to the schema enum.
// The host label inside such a string is dropped: only the pair survives.
func topologyEnum(s string) string {
	if strings.HasPrefix(s, "mixed:") {
		return schema.TopoMixed
	}
	ht, cores := strings.Contains(s, schema.Topo2HT), strings.Contains(s, schema.Topo2Cores)
	switch {
	case ht && cores:
		return schema.TopoMixed
	case ht:
		return schema.Topo2HT
	case cores:
		return schema.Topo2Cores
	}
	return schema.TopoUnspecific
}

// cols is one CSV data row keyed by header name; an absent column reads as "".
type cols map[string]string

func (c cols) f(name string) *float64 {
	v, err := strconv.ParseFloat(c[name], 64)
	if c[name] == "" || err != nil {
		return nil
	}
	return &v
}

func (c cols) i64(name string) *int64 {
	v, err := strconv.ParseInt(c[name], 10, 64)
	if c[name] == "" || err != nil {
		return nil
	}
	return &v
}

func (c cols) i(name string) *int {
	v, err := strconv.Atoi(c[name])
	if c[name] == "" || err != nil {
		return nil
	}
	return &v
}

func (c cols) int(name string) int {
	if p := c.i(name); p != nil {
		return *p
	}
	return 0
}

func (c cols) i64v(name string) int64 {
	if p := c.i64(name); p != nil {
		return *p
	}
	return 0
}

func (c cols) u(name string) uint64 {
	v, _ := strconv.ParseUint(c[name], 10, 64)
	return v
}

func (c cols) b(name string) bool { return c[name] == "true" }

// readRows reads a CSV into rows keyed by header; a missing file is an error.
func readRows(path string) ([]cols, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("%s: empty", path)
	}
	var rows []cols
	for _, r := range recs[1:] {
		m := cols{}
		for i, h := range recs[0] {
			if i < len(r) {
				m[h] = r[i]
			}
		}
		rows = append(rows, m)
	}
	return rows, nil
}

func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// probeFile is the allowlisted part of env-probe.json. Every other key of the
// probe (its label, any host name, the cpu sets) is ignored on purpose.
type probeFile struct {
	Kernel   string   `json:"kernel"`
	CPUModel string   `json:"cpu_model"`
	VCPUs    int      `json:"vcpus"`
	Siblings []string `json:"siblings"`
	SMT      bool     `json:"smt"`
	Topology string   `json:"topology_label"`
	IRQ      string   `json:"irq_time_accounting"`
	Sysctl   struct {
		RmemMax   string `json:"rmem_max"`
		WmemMax   string `json:"wmem_max"`
		PortRange string `json:"ip_local_port_range"`
	} `json:"sysctl"`
	StealTicks   int64  `json:"steal_ticks"`
	StealSeconds int    `json:"steal_seconds"`
	ImageOS      string `json:"image_os"`
	ImageVersion string `json:"image_version"`
}

// buildFile is the allowlisted part of build.json.
type buildFile struct {
	ServerSHA   string `json:"server_sha"`
	GoVersion   string `json:"go_version"`
	ClientSHA   string `json:"client_sha"`
	ClientGo    string `json:"client_go_version"`
	ServerDirty bool   `json:"server_dirty"`
}

func buildEnv(p probeFile) (schema.Env, error) {
	rm, err1 := strconv.ParseInt(strings.TrimSpace(p.Sysctl.RmemMax), 10, 64)
	wm, err2 := strconv.ParseInt(strings.TrimSpace(p.Sysctl.WmemMax), 10, 64)
	pr := strings.Fields(p.Sysctl.PortRange)
	if err1 != nil || err2 != nil || len(pr) != 2 {
		return schema.Env{}, errors.New("env probe: sysctl rmem_max, wmem_max or ip_local_port_range unreadable")
	}
	lo, err3 := strconv.Atoi(pr[0])
	hi, err4 := strconv.Atoi(pr[1])
	if err3 != nil || err4 != nil {
		return schema.Env{}, errors.New("env probe: ip_local_port_range is not two numbers")
	}
	return schema.Env{
		ImageOS: p.ImageOS, ImageVersion: p.ImageVersion, Kernel: p.Kernel, CPUModel: p.CPUModel,
		VCPUs: p.VCPUs, SMT: p.SMT, Siblings: p.Siblings, StealTicks: p.StealTicks, StealSeconds: p.StealSeconds,
		IRQTimeAccounting: p.IRQ, TopologyLabel: topologyEnum(p.Topology),
		RmemMax: rm, WmemMax: wm, PortRange: [2]int{lo, hi},
	}, nil
}

// armDir is one arm's directory in a leg and what it holds.
type armDir struct {
	arm, dir string
}

// legDir is one leg of the aggregate.
type legDir struct {
	dir  string
	idx  *leg.LegIndex
	arms []armDir
}

// findLegs lists the leg directories under <agg>/legs in a stable order. A
// leg is a directory holding leg.json, whose arm directories are the
// subdirectories holding knees.csv; the old single-arm layout (a knees.csv
// directly in the directory) is one leg of one arm.
func findLegs(root string) ([]legDir, error) {
	var legs []legDir
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || (d.Name() != "leg.json" && d.Name() != "knees.csv") {
			return err
		}
		dir := filepath.Dir(p)
		if d.Name() == "knees.csv" {
			if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "leg.json")); err == nil {
				return nil // an arm of a leg that has its own index
			}
		}
		if seen[dir] {
			return nil
		}
		seen[dir] = true
		ld := legDir{dir: dir}
		if d.Name() == "leg.json" {
			var idx leg.LegIndex
			if err := readJSONFile(p, &idx); err != nil {
				return err
			}
			ld.idx = &idx
			subs, err := os.ReadDir(dir)
			if err != nil {
				return err
			}
			for _, s := range subs {
				if s.IsDir() {
					if _, err := os.Stat(filepath.Join(dir, s.Name(), "knees.csv")); err == nil {
						ld.arms = append(ld.arms, armDir{arm: s.Name(), dir: filepath.Join(dir, s.Name())})
					}
				}
			}
		} else {
			ld.arms = []armDir{{dir: dir}}
		}
		legs = append(legs, ld)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(legs, func(i, j int) bool { return legs[i].dir < legs[j].dir })
	for i := range legs {
		sort.Slice(legs[i].arms, func(a, b int) bool { return armRank(legs[i].arms[a].arm) < armRank(legs[i].arms[b].arm) })
	}
	return legs, nil
}

func armRank(a string) int {
	for i, v := range armOrder {
		if v == a {
			return i
		}
	}
	return len(armOrder)
}

// runPublishSet builds the publish set: the aggregate's numbers re-written
// through the typed schema, plus a manifest from an allowlist of the build and
// probe records. Anything the schema has no field for is dropped, so no
// reason, error, note, host label, endpoint or log line can reach the output.
func runPublishSet(args []string, stdout io.Writer) error {
	fl := flag.NewFlagSet("publishset", flag.ContinueOnError)
	in := fl.String("in", "", "aggregate directory (required)")
	micro := fl.String("micro", "", "directory holding micro.csv (optional; empty micro table when absent)")
	build := fl.String("build", "", "build.json (required)")
	probe := fl.String("probe", "", "env-probe.json (required)")
	runURL := fl.String("run-url", "", "the Actions run URL (https://github.com/<o>/<r>/actions/runs/<id>)")
	out := fl.String("out", "", "output directory (required)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *in == "" || *build == "" || *probe == "" || *out == "" || fl.NArg() > 0 {
		return errors.New("publishset: --in, --build, --probe and --out are required, and nothing else")
	}
	var bf buildFile
	if err := readJSONFile(*build, &bf); err != nil {
		return err
	}
	var pf probeFile
	if err := readJSONFile(*probe, &pf); err != nil {
		return err
	}
	env, err := buildEnv(pf)
	if err != nil {
		return err
	}
	run := schema.Run{Manifest: schema.Manifest{
		SchemaVersion: schema.Version, Label: schema.Label, JoinLabel: schema.JoinLabel,
		RunURL: *runURL, ServerSHA: bf.ServerSHA, ClientSHA: bf.ClientSHA, ServerDirty: bf.ServerDirty,
		ServerGo: bf.GoVersion, ClientGo: bf.ClientGo, Env: env,
	}}
	if *micro != "" {
		if run.Micro, err = readMicro(filepath.Join(*micro, "micro.csv")); err != nil {
			return err
		}
	}
	legs, err := findLegs(filepath.Join(*in, "legs"))
	if err != nil {
		return err
	}
	if len(legs) == 0 {
		return fmt.Errorf("publishset: no leg under %s", filepath.Join(*in, "legs"))
	}
	for n, ld := range legs {
		if err := addLeg(&run, n+1, ld); err != nil {
			return err
		}
	}
	if run.Summary, err = readSummary(filepath.Join(*in, "aggregate.csv")); err != nil {
		return err
	}
	if run.Ratios, err = readRatios(filepath.Join(*in, "ratios.csv")); err != nil {
		return err
	}
	if err := schema.WriteRun(*out, run); err != nil {
		return fmt.Errorf("publishset: %w", err)
	}
	fmt.Fprintf(stdout, "publishset: %d legs, %d steps, %d knees written to %s\n", len(legs), len(run.Steps), len(run.Knees), *out)
	return nil
}

func readMicro(path string) ([]schema.MicroRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := schema.ReadCSV[schema.MicroRow](f, schema.Header[schema.MicroRow]())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return rows, nil
}

// addLeg appends one leg's metadata, calibration, steps, knees and (for the
// first leg) pins and corpus to run. n is the leg's ordinal; the leg's
// directory name is never published.
func addLeg(run *schema.Run, n int, ld legDir) error {
	m := &run.Manifest
	meta := schema.LegMeta{Leg: n}
	if ld.idx != nil {
		meta.Scenario, meta.Seed, meta.ArmOrder = ld.idx.Scenario, ld.idx.Seed, ld.idx.ArmOrder
	}
	if cal, err := readRows(filepath.Join(ld.dir, "calibration.csv")); err == nil {
		for _, c := range cal {
			run.Calibration = append(run.Calibration, schema.CalRow{
				Leg: n, Kind: c["kind"], N: c.int("n"), EgressPPS: c.f("egress_pps"), Valid: c.b("valid"),
				LowerBound: c.b("lower_bound"), ReflectorErrors: c.u("reflector_send_errors"), ReflectorSenders: c.int("reflector_senders"),
			})
		}
	}
	for _, a := range ld.arms {
		var am leg.Manifest
		if err := readJSONFile(filepath.Join(a.dir, "manifest.json"), &am); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		knees, err := readRows(filepath.Join(a.dir, "knees.csv"))
		if err != nil {
			return err
		}
		if len(knees) != 1 {
			return fmt.Errorf("%s/knees.csv: want one row, got %d", a.dir, len(knees))
		}
		k := knees[0]
		arm, scen := k["arm"], k["scenario"]
		if meta.Scenario == "" {
			meta.Scenario = scen
		}
		run.Knees = append(run.Knees, schema.KneeRow{
			Leg: n, Arm: arm, Scenario: scen, ResultKind: k["result_kind"], N: k.int("n"),
			UpperN: k.i("upper_n"), ConfirmN: k.i("confirm_n"), ConfirmOutcome: k["confirm_outcome"],
			OverloadN: k.i("overload_n"), OverloadOutcome: k["overload_outcome"], PredictedKnee: k.i("predicted_knee"),
			NMax: k.int("n_max"), Topology: topologyEnum(k["topology"]), ArmLabel: am.ArmLabel, Payload: k["payload"],
		})
		if steps, err := readRows(filepath.Join(a.dir, "steps.csv")); err == nil {
			for _, s := range steps {
				run.Steps = append(run.Steps, stepRow(n, arm, scen, am.ArmLabel, s))
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if m.Corpus == nil && am.Corpus != nil {
			m.Corpus = &schema.Corpus{SHA256: am.Corpus.SHA256, Frames: am.Corpus.Frames, SizeMin: am.Corpus.SizeMin,
				SizeMedian: am.Corpus.SizeMedian, SizeP95: am.Corpus.SizeP95, SizeMax: am.Corpus.SizeMax}
		}
		if n == 1 {
			roles := make([]string, 0, len(am.Pins))
			for r := range am.Pins {
				roles = append(roles, r)
			}
			sort.Strings(roles)
			for _, r := range roles {
				p := am.Pins[r]
				m.Pins = append(m.Pins, schema.PinRow{Arm: arm, Role: r, CPUs: p.CPUs, GOMAXPROCS: p.GOMAXPROCS, Shards: p.Shards})
			}
		}
	}
	m.Legs = append(m.Legs, meta)
	return nil
}

// stepRow keeps the schema's columns of one steps.csv row. The reasons, the
// violations, the warnings and the log-derived counts have no field.
func stepRow(n int, arm, scen, armLabel string, s cols) schema.StepRow {
	return schema.StepRow{
		Leg: n, Arm: arm, Scenario: scen, Seq: s.int("seq"), Kind: s["kind"], N: s.int("n"), Outcome: s["outcome"], Held: s.b("held"),
		LossRatio: s.f("loss_ratio"), LateLossRatio: s.f("late_loss_ratio"),
		OneWayP50Ms: s.f("one_way_p50_ms"), OneWayP99Ms: s.f("one_way_p99_ms"), OneWayMaxMs: s.f("one_way_max_ms"),
		JitterP99Ms: s.f("jitter_p99_ms"), JitterMeanMs: s.f("jitter_mean_ms"), GapP99Ms: s.f("gap_p99_ms"),
		Expected: s.u("expected"), Unique: s.u("unique"), Lost: s.u("lost"), Dups: s.u("dups"), Reordered: s.u("reordered"),
		FanoutPPS: s.f("fanout_pps"), Presses: s.int("presses"), DualPresses: s.int("dual_presses"), VoiceEndpoints: s.int("voice_endpoints"),
		JoinP99Us: s.i64v("join_p99_us"), RTTP99Us: s.i64("rtt_p99_us"), RadioUpdateP99: s.i64("radio_update_p99_us"),
		RadioPropP99: s.i64("radio_prop_p99_us"), SecretToAckP99: s.i64("secret_to_ack_p99_us"),
		ServerCPU: s.f("server_cpu"), ServerCPUByRole: s["server_cpu_by_role"], ServerDrops: s.u("server_drops"),
		SoftIRQTicks: s.u("softirq_ticks_server_set"), StealTicks: s.u("steal_ticks"), GenCPU: s.f("gen_cpu"),
		GenSocketDrops: s.u("gen_socket_drops"), NodeDeregs: s.u("node_deregistered"), ControlLost: s.int("control_lost"),
		ArmLabel: armLabel, Payload: s["payload"], CorpusSHA256: s["corpus_sha256"],
	}
}

// readSummary turns aggregate.csv into summary rows (the results text is dropped).
func readSummary(path string) ([]schema.SummaryRow, error) {
	rows, err := readRows(path)
	if err != nil {
		return nil, err
	}
	var out []schema.SummaryRow
	for _, r := range rows {
		out = append(out, schema.SummaryRow{
			Arm: r["arm"], Scenario: r["scenario"], Repeats: r.int("repeats"), MedianN: r.int("median_n"),
			MinN: r.int("min_n"), MaxN: r.int("max_n"), Censored: r.b("censored"), Unconfirmed: r.b("unconfirmed"),
			PredictedKnee: r.i("predicted_knee"), Topology: topologyEnum(r["topology"]), ArmLabel: r["arm_label"],
		})
	}
	return out, nil
}

// readRatios turns ratios.csv into ratio rows.
func readRatios(path string) ([]schema.RatioRow, error) {
	rows, err := readRows(path)
	if err != nil {
		return nil, err
	}
	var out []schema.RatioRow
	for _, r := range rows {
		out = append(out, schema.RatioRow{
			Scenario: r["scenario"], Ratio: r["ratio"], Legs: r.int("legs"), Median: r.f("median"), Min: r.f("min"),
			Max: r.f("max"), CensoredLegs: r.int("censored_legs"), Topology: topologyEnum(r["topology"]),
		})
	}
	return out, nil
}
