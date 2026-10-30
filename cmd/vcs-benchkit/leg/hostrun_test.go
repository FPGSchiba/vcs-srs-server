package leg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
)

// fakeReader serves a map of files; everything else is not found.
func fakeReader(files map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	}
}

func memInfo(mib int) string { return fmt.Sprintf("MemAvailable: %d kB\n", mib*1024) }

const portsFile = "32768 60999\n"

func hostOpts() Options {
	return Options{
		ServerBin: "s", LoadbotBin: "l", OutDir: "o", Scenario: "one-big-net", Arms: "S",
		ServerSet: "0", GenSet: "1-4", Topology: "t", PredictedKnee: 24108,
		JoinRate: 27, JoinRateFloor: 4, MemPerBotMiB: 2, MemMargin: 0.2, MemReserveMiB: 512, BotsPerGenCPU: 1500,
		CalSteps: "64,128,256,512,1024,2048,4096",
		Bot:      DefaultBotParams(),
	}
}

func v3Rule() Features {
	return Features{Version: 3, Set: map[string]bool{"join_rate": true, "ping": true}}
}

func TestMemoryPreflightRefusesTiny(t *testing.T) {
	o := hostOpts()
	for _, mib := range []int{256, 512, 600} { // memTop -1, -1, 54 (< 64)
		mf := ReadMemFacts(fakeReader(map[string]string{"/proc/meminfo": memInfo(mib)}))
		_, _, _, err := memoryPreflight(o, mf)
		if err == nil || !strings.Contains(err.Error(), "pre-flight: memory:") || !strings.Contains(err.Error(), "need at least 64") {
			t.Errorf("%d MiB: err = %v", mib, err)
		}
	}
}

func TestMemoryPreflightUnknownSkips(t *testing.T) {
	o := hostOpts()
	top, steps, note, err := memoryPreflight(o, ReadMemFacts(fakeReader(nil)))
	if err != nil || top != 0 || steps != o.CalSteps || !strings.HasPrefix(note, "memory pre-flight skipped: ") {
		t.Errorf("top=%d steps=%q note=%q err=%v", top, steps, note, err)
	}
}

func TestRunTrimsCalibrationStepsToMemory(t *testing.T) {
	o := hostOpts()
	mf := ReadMemFacts(fakeReader(map[string]string{"/proc/meminfo": memInfo(2048)}))
	top, steps, note, err := memoryPreflight(o, mf)
	if err != nil || top != 614 {
		t.Fatalf("top=%d err=%v", top, err)
	}
	if steps != "64,128,256,512" {
		t.Errorf("steps = %q", steps)
	}
	if !strings.Contains(note, "calibration steps above the memory top 614 dropped") {
		t.Errorf("note = %q", note)
	}
	// Plenty of memory: nothing dropped, no note.
	mf = ReadMemFacts(fakeReader(map[string]string{"/proc/meminfo": memInfo(64 * 1024)}))
	if _, steps, note, err = memoryPreflight(o, mf); err != nil || steps != o.CalSteps || note != "" {
		t.Errorf("steps=%q note=%q err=%v", steps, note, err)
	}
}

// The leg refuses a tiny host before the first calibration ramp: the loadbot
// path does not exist, so a leg that got as far as calibration would fail with
// "calibration:" instead.
func TestRunRefusesTinyMemoryBeforeCalibration(t *testing.T) {
	dir := t.TempDir()
	o := hostOpts()
	o.LoadbotBin, o.OutDir, o.Sysctl = filepath.Join(dir, "no-such-loadbot"), dir, false
	o.readFile = fakeReader(map[string]string{"/proc/meminfo": memInfo(600), "/proc/sys/net/ipv4/ip_local_port_range": portsFile})
	var log bytes.Buffer
	err := Run(context.Background(), o, &log)
	if err == nil || !strings.Contains(err.Error(), "pre-flight: memory:") {
		t.Fatalf("err = %v, want the memory pre-flight refusal", err)
	}
	if strings.Contains(log.String(), "calibration: reflector") {
		t.Errorf("calibration started:\n%s", log.String())
	}
}

func planInput(o Options) (Caps, Options) {
	caps := planHost(&o, v3Rule(), 5529, 2047, 97*time.Minute, [2]int{32768, 60999}, 1048576, 12)
	return caps, o
}

func TestBotArgsCarryAnchoredScale(t *testing.T) {
	o := hostOpts()
	caps, o := planInput(o)
	if caps.Top != 5529 || caps.NMax != 5529 {
		t.Fatalf("caps = %+v", caps)
	}
	want := fmtFloat(max(5000, 4*float64(caps.Top)))
	if o.Bot.JoinRateScale != caps.Scale || fmtFloat(caps.Scale) != want {
		t.Fatalf("scale = %v caps.Scale %v want %s", o.Bot.JoinRateScale, caps.Scale, want)
	}
	m := newManifest(o, nil, nil)
	m.recordRound2(o, v3Rule())
	if got := strings.Join(m.BotFlags, " "); !strings.Contains(got, "--join-rate-scale "+want+" ") {
		t.Errorf("bot_flags = %s", got)
	}
	if m.Ramp.RateFloor != 4 || m.Ramp.RateScale != caps.Scale {
		t.Errorf("ramp = %+v", m.Ramp)
	}
}

func TestFloorZeroIsRound2Rule(t *testing.T) {
	o := hostOpts()
	o.JoinRateFloor = 0
	_, o = planInput(o)
	m := newManifest(o, nil, nil)
	m.recordRound2(o, v3Rule())
	want := BotRunArgsWith(v3Rule(), DefaultBotParams())
	if !reflect.DeepEqual(m.BotFlags, want) {
		t.Errorf("bot_flags = %v\nwant %v", m.BotFlags, want)
	}
	if got := strings.Join(m.BotFlags, " "); !strings.Contains(got, "--join-rate-max 12 --join-rate-scale 5000") {
		t.Errorf("bot_flags = %s", got)
	}
}

func TestPredictionIsContextOnly(t *testing.T) {
	a, b, z := hostOpts(), hostOpts(), hostOpts()
	a.PredictedKnee, b.PredictedKnee, z.PredictedKnee = 24108, 100, 0
	ca, _ := planInput(a)
	cb, _ := planInput(b)
	cz, _ := planInput(z)
	if !reflect.DeepEqual(ca, cb) || !reflect.DeepEqual(ca, cz) {
		t.Errorf("caps depend on the prediction:\n%+v\n%+v\n%+v", ca, cb, cz)
	}
	ma, mb := newManifest(a, nil, nil), newManifest(b, nil, nil)
	ma.recordCaps(ca, 2047)
	mb.recordCaps(cb, 2047)
	if !reflect.DeepEqual(ma.HostCaps, mb.HostCaps) {
		t.Errorf("host_caps differ: %+v vs %+v", ma.HostCaps, mb.HostCaps)
	}
	if ma.PredictedKnee == mb.PredictedKnee {
		t.Errorf("the manifest must keep the prediction as context: %d %d", ma.PredictedKnee, mb.PredictedKnee)
	}
	cfgA, cfgB := driverConfig(a, ca, 90*time.Minute, v3Rule()), driverConfig(b, cb, 90*time.Minute, v3Rule())
	cfgA.Predicted, cfgB.Predicted = 0, 0
	if !reflect.DeepEqual(cfgA, cfgB) {
		t.Errorf("driver config depends on the prediction:\n%+v\n%+v", cfgA, cfgB)
	}
	if cfgA.Start != ca.Start || cfgA.Top != ca.Top || cfgA.NMaxBy != ca.By || cfgA.NMax != ca.NMax {
		t.Errorf("driver config = %+v, caps %+v", cfgA, ca)
	}
}

func TestPlanHostWithoutRuleKeepsScaleAndUsesConstant(t *testing.T) {
	o := hostOpts()
	feats := Features{Version: 2, Set: map[string]bool{}}
	caps := planHost(&o, feats, 0, 0, 97*time.Minute, [2]int{32768, 60999}, 1048576, 2)
	if caps.Scale != 0 || o.Bot.JoinRateScale != 5000 {
		t.Errorf("scale = %v / %v with no join_rate feature", caps.Scale, o.Bot.JoinRateScale)
	}
	if caps.By != "generator-cpu" || caps.NMax != 3000 {
		t.Errorf("caps = %+v, want generator-cpu 3000", caps)
	}
}

func TestRunStoppedEarlyWritesKneesAndExitsZero(t *testing.T) {
	dir := t.TempDir()
	o := hostOpts()
	o.OutDir = dir
	out := &Outcome{
		Result:       sweep.Result{Kind: sweep.Bracketed, N: 3013, Hi: 6027},
		StoppedEarly: "set_n 6026: context deadline exceeded",
	}
	m := newManifest(o, nil, nil)
	m.recordOutcome(out, nil)
	if m.StoppedEarly != out.StoppedEarly || m.Error != "" || !strings.Contains(m.Result, "3013") || !strings.Contains(m.Result, "bracketed") {
		t.Fatalf("manifest = result %q stopped %q error %q", m.Result, m.StoppedEarly, m.Error)
	}
	if err := writeOutcomeFiles(o, out, 5529, nil); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"steps.csv", "knees.csv"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	var buf bytes.Buffer
	if err := WriteResultsMarkdown(&buf, legResultsInput(o, m, nil, out)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "stopped early: set_n 6026: context deadline exceeded") {
		t.Errorf("results.md:\n%s", buf.String())
	}
	// A run error records no result.
	m2 := newManifest(o, nil, nil)
	m2.recordOutcome(out, errors.New("boom"))
	if m2.Result != "" {
		t.Errorf("failed run recorded result %q", m2.Result)
	}
}

func TestManifestDecisionsRound3(t *testing.T) {
	d := decisionsRound3()
	if len(d) != 15 {
		t.Fatalf("%d entries, want 15", len(d))
	}
	for i := 1; i <= 15; i++ {
		if d[fmt.Sprintf("R3-%d", i)] == "" {
			t.Errorf("R3-%d missing", i)
		}
	}
	if !strings.Contains(d["R3-1"], "off -> default") {
		t.Errorf("R3-1 = %q", d["R3-1"])
	}
	o := hostOpts()
	m := newManifest(o, nil, nil)
	m.recordRound2(o, v3Rule())
	if len(m.DecisionsRound3) != 15 {
		t.Errorf("manifest decisions_round3 has %d entries", len(m.DecisionsRound3))
	}
}

var _ = sweep.Pass
