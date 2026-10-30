package schema

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/run-ok from sampleRun")

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }
func i(v int) *int           { return &v }

// sampleRun is the fixture run: one leg, two arms, every kind of nullable cell.
func sampleRun() Run {
	return Run{
		Manifest: Manifest{
			SchemaVersion: Version,
			Label:         Label,
			RunURL:        "https://github.com/FPGSchiba/vcs-srs-server/actions/runs/123456789",
			ServerSHA:     "e2fe62b0000000000000000000000000000000aa",
			ClientSHA:     "b55823e",
			ServerGo:      "go1.27.3",
			ClientGo:      "go1.27.3",
			Corpus: &Corpus{
				SHA256: strings.Repeat("ab", 32), Frames: 1566,
				SizeMin: 3, SizeMedian: 120, SizeP95: 160, SizeMax: 210,
			},
			Env: Env{
				ImageOS: "ubuntu24", ImageVersion: "20261020.1.0",
				Kernel: "6.8.0-1017-azure", CPUModel: "AMD EPYC 7763 64-Core Processor",
				VCPUs: 4, SMT: true, Siblings: []string{"0,1", "2,3"},
				StealTicks: 3, StealSeconds: 5, IRQTimeAccounting: "y",
				TopologyLabel: Topo2HT,
				RmemMax:       212992, WmemMax: 212992, PortRange: [2]int{32768, 60999},
			},
			Legs: []LegMeta{{Leg: 1, Scenario: "one-big-net", Seed: 7, ArmOrder: []string{"Seq", "S"}}},
			Pins: []PinRow{
				{Arm: "S", Role: "server", CPUs: "0-1", GOMAXPROCS: 2, Shards: 2},
				{Arm: "Seq", Role: "server", CPUs: "0,1", GOMAXPROCS: 2, Shards: 2},
			},
			JoinLabel: JoinLabel,
		},
		Micro: []MicroRow{{Bench: "BenchmarkSocketSend", N: 100, L: 10, Logger: "bus", Mode: "single", NsPerOp: 90000, NsPerSend: f64(9000), AllocsPerOp: 1}},
		Calibration: []CalRow{
			{Leg: 1, Kind: "step", N: 500, EgressPPS: f64(25000), Valid: true},
			{Leg: 1, Kind: "receive_capacity", EgressPPS: f64(90000), LowerBound: true, ReflectorSenders: 2},
		},
		Steps: []StepRow{
			{Leg: 1, Arm: "S", Scenario: "one-big-net", Seq: 1, Kind: "sweep", N: 1000, Outcome: "pass", Held: true,
				LossRatio: f64(0), LateLossRatio: f64(0.001), OneWayP99Ms: f64(4.5), Payload: "opus-speech-corpus",
				ServerCPU: f64(0.41), CorpusSHA256: strings.Repeat("ab", 32), ServerCPUByRole: "server=0.41"},
			{Leg: 1, Arm: "S", Scenario: "one-big-net", Seq: 2, Kind: "sweep", N: 2000, Outcome: "breach", Held: true,
				Payload: "synthetic-not-opus"},
		},
		Knees: []KneeRow{{Leg: 1, Arm: "S", Scenario: "one-big-net", ResultKind: "knee", N: 1000, UpperN: nil,
			ConfirmN: i(1000), ConfirmOutcome: "pass", PredictedKnee: i(3800), NMax: 8000, Topology: Topo2HT, Payload: "opus-speech-corpus"}},
		Summary: []SummaryRow{{Arm: "S", Scenario: "one-big-net", Repeats: 3, MedianN: 1000, MinN: 900, MaxN: 1100, Censored: false, Topology: Topo2HT}},
		Ratios:  []RatioRow{{Scenario: "one-big-net", Ratio: "Seq/S", Legs: 3, Median: f64(1.02), Min: f64(0.98), Max: f64(1.1), CensoredLegs: 0, Topology: Topo2HT}},
	}
}

func TestRunOKRoundTrip(t *testing.T) {
	dir := filepath.Join("testdata", "run-ok")
	if *update {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := WriteRun(dir, sampleRun()); err != nil {
			t.Fatal(err)
		}
	}
	run, err := ReadRun(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := WriteRun(out, run); err != nil {
		t.Fatal(err)
	}
	for _, name := range RunFiles {
		want, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want, got) {
			t.Errorf("%s does not round-trip", name)
		}
	}
	if len(run.Steps) != 2 || run.Steps[1].LateLossRatio != nil {
		t.Errorf("an empty cell must decode to nil, got %+v", run.Steps)
	}
}

func TestDecodeManifestRejectsUnknownKey(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "run-ok", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeManifest(bytes.NewReader(b)); err != nil {
		t.Fatalf("fixture must decode: %v", err)
	}
	bad := bytes.Replace(b, []byte(`"schema_version"`), []byte(`"note": "x", "schema_version"`), 1)
	if _, err := DecodeManifest(bytes.NewReader(bad)); err == nil {
		t.Fatal("an extra top-level key must fail")
	}
	nested := bytes.Replace(b, []byte(`"vcpus"`), []byte(`"host": "x", "vcpus"`), 1)
	if _, err := DecodeManifest(bytes.NewReader(nested)); err == nil {
		t.Fatal("an extra nested key must fail")
	}
}

func TestReadCSVRejectsExtraColumnAndWrongHeader(t *testing.T) {
	good, err := os.ReadFile(filepath.Join("testdata", "run-ok", "ratios.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCSV[RatioRow](bytes.NewReader(good), Header[RatioRow]()); err != nil {
		t.Fatalf("fixture must decode: %v", err)
	}
	lines := strings.SplitN(string(good), "\n", 2)
	extra := lines[0] + ",reasons\n" + strings.TrimSuffix(lines[1], "\n") + ",ss: 127.0.0.1:41234 missing\n"
	if _, err := ReadCSV[RatioRow](strings.NewReader(extra), Header[RatioRow]()); err == nil {
		t.Fatal("an extra column must fail")
	}
	if _, err := ReadCSV[RatioRow](strings.NewReader(strings.Replace(string(good), "median", "mean", 1)), Header[RatioRow]()); err == nil {
		t.Fatal("a renamed column must fail")
	}
}

func TestWriteRunRejectsFreeText(t *testing.T) {
	cases := map[string]func(r *Run){
		"outcome":  func(r *Run) { r.Steps[0].Outcome = "breach: ss 127.0.0.1:41234 missing" },
		"kind":     func(r *Run) { r.Knees[0].ResultKind = "knee at 4140 on /home/runner" },
		"topology": func(r *Run) { r.Summary[0].Topology = "host-a: server 0,1" },
		"role":     func(r *Run) { r.Manifest.Pins[0].Role = "127.0.0.1:9000" },
		"payload":  func(r *Run) { r.Steps[0].Payload = "whatever the bot said" },
		"label":    func(r *Run) { r.Manifest.Label = "my laptop" },
		"cpu":      func(r *Run) { r.Manifest.Env.CPUModel = "CPU on /home/runner" },
		"sha":      func(r *Run) { r.Manifest.ServerSHA = "not a sha" },
		"runurl":   func(r *Run) { r.Manifest.RunURL = "http://10.1.2.3/x" },
		"bytype":   func(r *Run) { r.Steps[0].ServerCPUByRole = "server=0.4;note=hello world" },
	}
	for name, mut := range cases {
		r := sampleRun()
		mut(&r)
		if err := WriteRun(t.TempDir(), r); err == nil {
			t.Errorf("%s: free text was written", name)
		}
	}
}

func TestVersionConstant(t *testing.T) {
	if Version != "bench-results/1" {
		t.Fatal(Version)
	}
}
