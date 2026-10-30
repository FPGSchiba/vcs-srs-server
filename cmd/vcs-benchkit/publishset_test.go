package main

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/leg"
	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
	"github.com/FPGSchiba/vcs-srs-server/tools/benchsite/schema"
)

// Strings a published file must never contain. They are planted in every
// source file the publish set reads.
var planted = []string{
	"127.0.0.1", "41234", "10.1.2.3", "/home/runner", "my-aws-host", "host-a",
	"fv-az", "internal.cloudapp.net", "missing", "sched lag spike", "dial tcp",
	"secret note", "token-abc",
}

const (
	pTopo      = "host-a: server 0,1 (2 cpu), generator 2-3 (2 cpu), smt true, server pair 2 cores"
	pCorpus    = "ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12"
	pRunURL    = "https://github.com/FPGSchiba/vcs-srs-server/actions/runs/987654321"
	pProbeJSON = `{"schema":"env-probe/1","label":"my-aws-host","kernel":"6.8.0-1017-azure","cpu_model":"AMD EPYC 7763 64-Core Processor","vcpus":4,"siblings":["0,1","2,3"],"smt":true,"server_set":"0,1","generator_set":"2,3","topology_label":"2 hyperthreads of 1 core","irq_time_accounting":"y","sysctl":{"rmem_default":"212992","rmem_max":"212992","wmem_default":"212992","wmem_max":"212992","ip_local_port_range":"32768 60999"},"ulimit_n":"65536","steal_ticks":3,"steal_seconds":5,"image_os":"ubuntu24","image_version":"20261020.1.0","hostname":"fv-az123-456","note":"token-abc"}`
	pBuildJSON = `{"server_sha":"e2fe62b0000000000000000000000000000000aa","go_version":"go1.27.3","client_sha":"b55823e","client_go_version":"go1.27.3","server_dirty":false,"note":"secret note"}`
)

func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildArm writes one arm's directory with the kit's real writers, with
// free text planted in every field the schema does not publish.
func buildArm(t *testing.T, legDir, arm string, knee int) {
	t.Helper()
	d := filepath.Join(legDir, arm)
	step := func(n int, o sweep.StepOutcome, reasons []string) leg.Row {
		lat, one := 0.0005, int64(4500)
		return leg.Row{
			Kind: "sweep", Seq: 1, N: n, Outcome: o, Reasons: reasons, Held: true,
			Step: leg.Step{
				Schema: "step/4", Payload: schema.PayloadCorpus, ArmLabel: armLabelFor(arm), N: n,
				Expected: 1000, Unique: 999, Lost: 1, LateLossRatio: &lat, OneWayP99Us: &one,
				Violations: []string{"sched lag spike on 127.0.0.1"}, Warnings: []string{"dial tcp 10.1.2.3:9000"},
				Corpus: &leg.CorpusInfo{SHA256: pCorpus, Frames: 1566, SizeMin: 3, SizeMedian: 120, SizeP95: 160, SizeMax: 210},
			},
		}
	}
	out := &leg.Outcome{
		Rows: []leg.Row{
			step(knee, sweep.Pass, nil),
			step(knee*2, sweep.Breach, []string{"ss: 127.0.0.1:41234 missing"}),
		},
		Result: sweep.Result{Kind: sweep.KneeFound, N: knee},
		Notes:  []string{"secret note from /home/runner/work"},
	}
	var steps, knees bytes.Buffer
	if err := leg.WriteStepsCSV(&steps, out, pTopo); err != nil {
		t.Fatal(err)
	}
	if err := leg.WriteKneesCSV(&knees, out, leg.KneeMeta{Arm: arm, Scenario: "one-big-net", Topology: pTopo, Predicted: 3800, NMax: 8000}); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(d, "steps.csv"), steps.Bytes())
	mustWrite(t, filepath.Join(d, "knees.csv"), knees.Bytes())
	m := leg.Manifest{
		Schema: "manifest/1", Arm: arm, Scenario: "one-big-net", Seed: 7, Topology: pTopo,
		ArmLabel: armLabelFor(arm), Corpus: &leg.CorpusInfo{SHA256: pCorpus, Frames: 1566, SizeMin: 3, SizeMedian: 120, SizeP95: 160, SizeMax: 210},
		Pins:  map[string]leg.PinRecord{"server": {CPUs: "0,1", GOMAXPROCS: 2, Shards: 2}},
		Notes: []string{"secret note"}, Error: "dial tcp 10.1.2.3:9000: refused", ServerErrors: []string{"level=ERROR addr=127.0.0.1:41234"},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(d, "manifest.json"), b)
}

func armLabelFor(arm string) string {
	if arm == "D2" {
		return schema.ArmLabelD2
	}
	return ""
}

// buildAggregate makes two legs of S, Seq and D2 under <in>, runs the real
// aggregate over them and returns the aggregate directory.
func buildAggregate(t *testing.T) string {
	t.Helper()
	in, agg := t.TempDir(), t.TempDir()
	for _, ld := range []string{"1-one-big-net", "2-one-big-net"} {
		dir := filepath.Join(in, ld)
		knees := map[string]int{"S": 1000, "Seq": 1300, "D2": 1200}
		idx := leg.LegIndex{Schema: leg.LegIndexSchema, Scenario: "one-big-net", Seed: 7, Arms: map[string]leg.ArmResult{},
			ArmOrder: []string{"Seq", "S", "D2"}, Label: schema.Label, TopologyLabel: pTopo}
		for _, a := range []string{"S", "Seq", "D2"} {
			buildArm(t, dir, a, knees[a])
			idx.Arms[a] = leg.ArmResult{Result: "knee", Kind: "knee", N: knees[a]}
		}
		idx.Ratios, idx.RatioNotes = leg.Ratios(idx.Arms)
		b, err := json.Marshal(idx)
		if err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(dir, "leg.json"), b)
		var cal bytes.Buffer
		c := &leg.Calibration{ReceivePPS: 90000, LowerBound: true, LowerBoundReason: "sched lag spike", Senders: 2,
			Steps: []leg.CalStep{{N: 500, EgressPPS: 25000, Valid: false, Violations: []string{"sched lag spike on 127.0.0.1"}}}}
		if err := leg.WriteCalibrationCSV(&cal, c, pTopo); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(dir, "calibration.csv"), cal.Bytes())
	}
	if err := runAggregate([]string{"--in", in, "--out", agg}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	return agg
}

func buildInputs(t *testing.T) (agg, micro, build, probe string) {
	t.Helper()
	agg = buildAggregate(t)
	tmp := t.TempDir()
	micro = filepath.Join(tmp, "micro")
	f, err := os.Open(filepath.Join("testdata", "micro-sample.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := ParseBenchOutput(f)
	if err != nil {
		t.Fatal(err)
	}
	var mb bytes.Buffer
	if err := writeMicroCSV(&mb, rows); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(micro, "micro.csv"), mb.Bytes())
	build, probe = filepath.Join(tmp, "build.json"), filepath.Join(tmp, "env-probe.json")
	mustWrite(t, build, []byte(pBuildJSON))
	mustWrite(t, probe, []byte(pProbeJSON))
	return
}

func publish(t *testing.T) string {
	t.Helper()
	agg, micro, build, probe := buildInputs(t)
	out := filepath.Join(t.TempDir(), "publish")
	err := runPublishSet([]string{"--in", agg, "--micro", micro, "--build", build, "--probe", probe, "--run-url", pRunURL, "--out", out}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPublishSetReadsBackAsSchema(t *testing.T) {
	out := publish(t)
	run, err := schema.ReadRun(out)
	if err != nil {
		t.Fatal(err)
	}
	m := run.Manifest
	if m.RunURL != pRunURL || m.ClientSHA != "b55823e" || m.Env.ImageVersion != "20261020.1.0" || m.Env.TopologyLabel != schema.Topo2HT {
		t.Errorf("manifest = %+v", m)
	}
	if m.Env.RmemMax != 212992 || m.Env.PortRange != [2]int{32768, 60999} || m.Corpus == nil || m.Corpus.Frames != 1566 {
		t.Errorf("env or corpus wrong: %+v", m)
	}
	if len(m.Legs) != 2 || m.Legs[0].Leg != 1 || strings.Join(m.Legs[0].ArmOrder, ",") != "Seq,S,D2" {
		t.Errorf("legs = %+v", m.Legs)
	}
	if len(run.Knees) != 6 || len(run.Steps) != 12 || len(run.Calibration) != 4 || len(run.Summary) != 3 || len(run.Ratios) == 0 || len(run.Micro) == 0 {
		t.Fatalf("rows: knees %d steps %d cal %d summary %d ratios %d micro %d", len(run.Knees), len(run.Steps), len(run.Calibration), len(run.Summary), len(run.Ratios), len(run.Micro))
	}
	for _, k := range run.Knees {
		if k.Topology != schema.Topo2Cores {
			t.Errorf("knee topology %q must be the enum", k.Topology)
		}
	}
	var sawBreach, sawD2Label bool
	for _, s := range run.Steps {
		if s.Outcome == "breach" {
			sawBreach = true
		}
		if s.Arm == "D2" && s.ArmLabel == schema.ArmLabelD2 {
			sawD2Label = true
		}
		if s.LateLossRatio == nil || s.OneWayP99Ms == nil || *s.OneWayP99Ms != 4.5 {
			t.Errorf("latency lost in the publish set: %+v", s)
		}
	}
	if !sawBreach || !sawD2Label {
		t.Error("breach verdict or the D2 label did not survive")
	}
}

func TestPublishSetHasNoFreeText(t *testing.T) {
	out := publish(t)
	files := 0
	err := filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files++
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, s := range planted {
			if bytes.Contains(b, []byte(s)) {
				t.Errorf("%s contains %q", filepath.Base(p), s)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files != len(schema.RunFiles) {
		t.Errorf("publish set has %d files, want exactly %d", files, len(schema.RunFiles))
	}
}

func TestPublishSetRefusesMissingInputs(t *testing.T) {
	agg, micro, build, probe := buildInputs(t)
	out := filepath.Join(t.TempDir(), "o")
	base := []string{"--in", agg, "--micro", micro, "--build", build, "--probe", probe, "--run-url", pRunURL, "--out", out}
	for flagName := range map[string]bool{"--in": true, "--build": true, "--probe": true, "--out": true} {
		args := append([]string{}, base...)
		for i := range args {
			if args[i] == flagName {
				args = append(args[:i], args[i+2:]...)
				break
			}
		}
		if err := runPublishSet(args, io.Discard); err == nil {
			t.Errorf("without %s: want an error", flagName)
		}
	}
	bad := append([]string{}, base...)
	for i := range bad {
		if bad[i] == "--run-url" {
			bad[i+1] = "http://10.1.2.3/x"
		}
	}
	if err := runPublishSet(bad, io.Discard); err == nil {
		t.Error("a run url outside github.com actions must fail")
	}
}
