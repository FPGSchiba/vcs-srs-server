package leg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
)

// legFakes replaces the processes a leg starts: the feature probe, the
// calibration, each arm's environment and the driver. It counts what ran.
type legFakes struct {
	calls       int // calibrations run
	envs        []string
	driven      []string
	failEnvArms map[string]bool
	feats       Features
	knees       map[string]int
}

type armEnvFake struct {
	arm  string
	fail bool
}

func (e *armEnvFake) Start(ctx context.Context) (System, error) {
	if e.fail {
		return nil, errors.New("start standalone server: boom")
	}
	return nil, errors.New("the fake driver never starts a system")
}

func v4All() Features {
	return Features{Version: 4, Set: map[string]bool{"join_rate": true, "ping": true, "arm_d2": true, "org_op": true}}
}

func installLegFakes(t *testing.T, f *legFakes) {
	t.Helper()
	oldP, oldC, oldE, oldD := probeFeatures, runCalibration, newArmEnv, runDriver
	t.Cleanup(func() { probeFeatures, runCalibration, newArmEnv, runDriver = oldP, oldC, oldE, oldD })
	probeFeatures = func(string, time.Duration) Features { return f.feats }
	runCalibration = func(ctx context.Context, c CalConfig) (*Calibration, error) {
		f.calls++
		return &Calibration{ReceivePPS: 200000, Steps: []CalStep{{N: 64, Valid: true}}}, nil
	}
	newArmEnv = func(plan ArmPlan, base envBase) Env {
		f.envs = append(f.envs, plan.Arm)
		return &armEnvFake{arm: plan.Arm, fail: f.failEnvArms[plan.Arm]}
	}
	runDriver = func(ctx context.Context, env Env, cfg Config) (*Outcome, error) {
		e := env.(*armEnvFake)
		f.driven = append(f.driven, e.arm)
		if _, err := env.Start(ctx); e.fail && err != nil {
			return &Outcome{}, err
		}
		n := f.knees[e.arm]
		step := Step{ArmLabel: e.arm + " label", Payload: "opus speech", Corpus: &CorpusInfo{SHA256: "abc123", Frames: 1566}, Parity: &StepParity{OK: true, ConnectMs: 4}}
		res := sweep.Result{Kind: sweep.KneeFound, N: n}
		if e.arm == "D2" {
			res = sweep.Result{Kind: sweep.GeneratorBound, N: n}
		}
		return &Outcome{Result: res, Starts: 1, Rows: []Row{{Kind: "sweep", Seq: 1, N: n, Held: true, Step: step, Outcome: sweep.Pass}}}, nil
	}
}

func legOpts(t *testing.T, arms string) Options {
	t.Helper()
	o := hostOpts()
	o.OutDir, o.Arms, o.Seed, o.ServerSet, o.GenSet = t.TempDir(), arms, 7, "0,1", "2,3"
	o.Budget = 90 * time.Minute
	o.Label = "relative only"
	o.CalSteps = "64,128"
	o.readFile = fakeReader(map[string]string{"/proc/meminfo": memInfo(64 * 1024), "/proc/sys/net/ipv4/ip_local_port_range": portsFile})
	return o
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func TestLegLayout(t *testing.T) {
	f := &legFakes{feats: v4All(), knees: map[string]int{"S": 2000, "Seq": 2600}}
	installLegFakes(t, f)
	o := legOpts(t, "S,Seq")
	var log bytes.Buffer
	if err := Run(context.Background(), o, &log); err != nil {
		t.Fatalf("Run: %v\n%s", err, log.String())
	}
	for _, p := range []string{"leg.json", "calibration.csv", "results.md", "S/knees.csv", "S/steps.csv", "S/manifest.json", "S/results.md", "Seq/knees.csv", "Seq/steps.csv", "Seq/manifest.json"} {
		if _, err := os.Stat(filepath.Join(o.OutDir, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	for _, p := range []string{"knees.csv", "manifest.json", "steps.csv"} {
		if _, err := os.Stat(filepath.Join(o.OutDir, p)); err == nil {
			t.Errorf("%s at the leg root: per-arm files belong in <arm>/", p)
		}
	}
	if f.calls != 1 {
		t.Errorf("calibrations = %d, want 1 per leg", f.calls)
	}
	var idx LegIndex
	readJSON(t, filepath.Join(o.OutDir, "leg.json"), &idx)
	wantOrder := ArmOrder([]string{"S", "Seq"}, 7)
	if !reflect.DeepEqual(idx.ArmOrder, wantOrder) || !reflect.DeepEqual(f.driven, wantOrder) {
		t.Errorf("arm_order %v, ran %v, want %v", idx.ArmOrder, f.driven, wantOrder)
	}
	if idx.Schema != "leg-index/1" || idx.Scenario != "one-big-net" || idx.Seed != 7 || idx.TopologyLabel != "t" || !strings.Contains(idx.Label, "relative only") {
		t.Errorf("leg.json = %+v", idx)
	}
	if idx.Arms["S"].Kind != "knee" || idx.Arms["S"].N != 2000 || idx.Arms["Seq"].N != 2600 {
		t.Errorf("arms = %+v", idx.Arms)
	}
	if r := idx.Ratios["Seq/S"]; r == nil || *r < 1.299 || *r > 1.301 {
		t.Errorf("Seq/S = %v", r)
	}
	if idx.Ratios["D2/S"] != nil || idx.RatioNotes["D2/S"] != "D2 not run" {
		t.Errorf("D2/S = %v note %q", idx.Ratios["D2/S"], idx.RatioNotes["D2/S"])
	}
	var m Manifest
	readJSON(t, filepath.Join(o.OutDir, "Seq", "manifest.json"), &m)
	if m.Arm != "Seq" || m.ArmLabel != "Seq label" || m.Payload != "opus speech" || m.Corpus == nil || m.Corpus.SHA256 != "abc123" || m.Parity == nil || !m.Parity.OK {
		t.Errorf("manifest arm=%q arm_label=%q payload=%q corpus=%+v parity=%+v", m.Arm, m.ArmLabel, m.Payload, m.Corpus, m.Parity)
	}
	if !reflect.DeepEqual(m.ArmOrder, wantOrder) {
		t.Errorf("manifest arm_order = %v", m.ArmOrder)
	}
	if m.Pins["server"].CPUs != "0,1" || m.Pins["server"].GOMAXPROCS != 2 {
		t.Errorf("Seq pins = %+v: the manifest must record the arm's own pin", m.Pins)
	}
	md, _ := os.ReadFile(filepath.Join(o.OutDir, "results.md"))
	for _, want := range []string{"(arm S)", "(arm Seq)", "Seq/S", "1.30"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("results.md lacks %q:\n%s", want, md)
		}
	}
}

func TestLegArmEnvironmentFailureIsRecordedAndTheRestRun(t *testing.T) {
	order := ArmOrder([]string{"S", "Seq"}, 7)
	bad, good := order[0], order[1]
	f := &legFakes{feats: v4All(), knees: map[string]int{"S": 2000, "Seq": 2600}, failEnvArms: map[string]bool{bad: true}}
	installLegFakes(t, f)
	o := legOpts(t, "S,Seq")
	err := Run(context.Background(), o, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("Run err = %v, want an error naming the failed arm %s", err, bad)
	}
	var idx LegIndex
	readJSON(t, filepath.Join(o.OutDir, "leg.json"), &idx)
	if idx.Arms[bad].Kind != "error" {
		t.Errorf("%s = %+v, want kind error", bad, idx.Arms[bad])
	}
	if idx.Arms[good].Kind != "knee" {
		t.Errorf("%s = %+v: the arm after a failed one must still run", good, idx.Arms[good])
	}
	if idx.Ratios["Seq/S"] != nil || !strings.Contains(idx.RatioNotes["Seq/S"], bad+" error") {
		t.Errorf("Seq/S = %v note %q", idx.Ratios["Seq/S"], idx.RatioNotes["Seq/S"])
	}
	var m Manifest
	readJSON(t, filepath.Join(o.OutDir, bad, "manifest.json"), &m)
	if !strings.Contains(m.Error, "boom") {
		t.Errorf("manifest error = %q", m.Error)
	}
	if _, err := os.Stat(filepath.Join(o.OutDir, good, "knees.csv")); err != nil {
		t.Errorf("good arm's knees.csv: %v", err)
	}
	if _, err := os.Stat(filepath.Join(o.OutDir, bad, "knees.csv")); err == nil {
		t.Errorf("a failed arm has no knees.csv")
	}
	if f.calls != 1 {
		t.Errorf("calibrations = %d", f.calls)
	}
}

func TestLegRefusesD2AndOrgOpOnAV3BotBeforeAnyProcess(t *testing.T) {
	v3 := Features{Version: 3, Set: map[string]bool{"join_rate": true, "ping": true}}
	for _, tc := range []struct{ arms, scenario, missing string }{
		{"S,Seq,D2", "one-big-net", "arm_d2"},
		{"S,Seq", "org-op", "org_op"},
	} {
		f := &legFakes{feats: v3, knees: map[string]int{"S": 1, "Seq": 1, "D2": 1}}
		installLegFakes(t, f)
		o := legOpts(t, tc.arms)
		o.Scenario = tc.scenario
		err := Run(context.Background(), o, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), tc.missing) {
			t.Errorf("%s %s: err = %v, want it to name %s", tc.arms, tc.scenario, err, tc.missing)
		}
		if f.calls != 0 || len(f.envs) != 0 {
			t.Errorf("%s %s: calibrations %d, environments %v: nothing may start", tc.arms, tc.scenario, f.calls, f.envs)
		}
	}
}

func TestLegOnAV3BotStillRunsSAndSeqOnOneBigNet(t *testing.T) {
	v3 := Features{Version: 3, Set: map[string]bool{"join_rate": true, "ping": true}}
	f := &legFakes{feats: v3, knees: map[string]int{"S": 100, "Seq": 150}}
	installLegFakes(t, f)
	o := legOpts(t, "S,Seq")
	if err := Run(context.Background(), o, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(f.driven) != 2 {
		t.Errorf("driven = %v", f.driven)
	}
}

func TestLegPerArmPlanningUsesTheScenariosReceiveRate(t *testing.T) {
	// 200000 pps / 19.25 = 10389 bots for org-op, / 50 = 4000 for one-big-net.
	for _, tc := range []struct {
		scenario string
		calBots  int
	}{{"one-big-net", 4000}, {"org-op", 10389}} {
		f := &legFakes{feats: v4All(), knees: map[string]int{"S": 1}}
		installLegFakes(t, f)
		o := legOpts(t, "S")
		o.Scenario = tc.scenario
		if err := Run(context.Background(), o, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		var m Manifest
		readJSON(t, filepath.Join(o.OutDir, "S", "manifest.json"), &m)
		if m.HostCaps == nil || m.HostCaps.CalBots != tc.calBots {
			t.Errorf("%s: host_caps = %+v, want cal_bots %d", tc.scenario, m.HostCaps, tc.calBots)
		}
	}
}

func TestLegPreArmFailureStillWritesLegJSONWithErrorArms(t *testing.T) {
	f := &legFakes{feats: v4All()}
	installLegFakes(t, f)
	o := legOpts(t, "S,Seq")
	o.readFile = fakeReader(map[string]string{"/proc/meminfo": memInfo(600), "/proc/sys/net/ipv4/ip_local_port_range": portsFile})
	if err := Run(context.Background(), o, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "pre-flight: memory:") {
		t.Fatalf("err = %v", err)
	}
	var idx LegIndex
	readJSON(t, filepath.Join(o.OutDir, "leg.json"), &idx)
	if idx.Arms["S"].Kind != "error" || idx.Arms["Seq"].Kind != "error" {
		t.Errorf("arms = %+v", idx.Arms)
	}
	var m Manifest
	readJSON(t, filepath.Join(o.OutDir, "S", "manifest.json"), &m)
	if !strings.Contains(m.Error, "pre-flight: memory:") {
		t.Errorf("S manifest error = %q", m.Error)
	}
}

func TestLegStopsStartingArmsOnceCancelled(t *testing.T) {
	f := &legFakes{feats: v4All(), knees: map[string]int{"S": 1, "Seq": 1}}
	installLegFakes(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	inner := runDriver
	runDriver = func(c context.Context, e Env, cfg Config) (*Outcome, error) {
		out, err := inner(c, e, cfg)
		cancel()
		return out, err
	}
	o := legOpts(t, "S,Seq")
	err := Run(ctx, o, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a cancelled leg must return an error")
	}
	if len(f.driven) != 1 {
		t.Errorf("driven = %v, want only the first arm", f.driven)
	}
	var idx LegIndex
	readJSON(t, filepath.Join(o.OutDir, "leg.json"), &idx)
	if len(idx.Arms) != 2 {
		t.Fatalf("arms = %+v: every requested arm is listed", idx.Arms)
	}
	if k := idx.Arms[ArmOrder([]string{"S", "Seq"}, 7)[1]].Kind; k != "error" {
		t.Errorf("the unrun arm = %q, want error", k)
	}
}
