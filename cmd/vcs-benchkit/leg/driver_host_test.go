package leg

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
)

func TestDriverStartsAtConfigStart(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30}
	cfg := testConfig()
	cfg.Predicted, cfg.Start, cfg.NMax = 4000, 300, 600
	out, err := NewDriver(w, cfg).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(w.setNs) < 2 || w.setNs[1] != 150 {
		t.Errorf("setNs = %v, want the warm-up ramp to 150 after the pre-flight 2", w.setNs)
	}
	if sw := rowsOfKind(out, "sweep"); len(sw) == 0 || sw[0].N != 300 {
		t.Errorf("first sweep row = %+v, want N 300", sw)
	}
}

// The evidence replay (bench-local-2): a breach at 6027, a pass at 3013, then a
// context deadline in the overload ramp. The leg must end with the bracket.
func TestDriverDeadlineInOverloadKeepsTheBracket(t *testing.T) {
	w := &fakeWorld{kneeN: 4000, setNErrAt: 6026, setNErrFromStart: 2}
	cfg := testConfig()
	cfg.Predicted, cfg.Start, cfg.NMax = 24108, 6027, 7058
	cfg.RateMax, cfg.RateScale = 12, 5000
	cfg.Budget = 80 * time.Minute
	out, err := NewDriver(w, cfg).Run(context.Background())
	if err != nil {
		t.Fatalf("Run = %v, want nil: a late error keeps the result", err)
	}
	want := sweep.Result{Kind: sweep.Bracketed, N: 3013, Hi: 6027}
	if out.Result != want {
		t.Fatalf("result = %+v, want %+v (notes %v)", out.Result, want, out.Notes)
	}
	notes := strings.Join(out.Notes, "\n")
	t.Logf("stopped early %q; notes %v", out.StoppedEarly, out.Notes)
	// Deterministic with the fake world: at this budget the deadline path is
	// taken, not the budget guard.
	if !strings.Contains(out.StoppedEarly, "context deadline exceeded") {
		t.Fatalf("StoppedEarly = %q, want the deadline path", out.StoppedEarly)
	}
	if !strings.Contains(notes, "stopped early") {
		t.Errorf("no stopped-early note: %v", out.Notes)
	}
	cs := rowsOfKind(out, "confirm")
	if len(cs) == 0 || cs[0].Outcome != sweep.Pass {
		t.Errorf("confirm rows = %+v, want a pass", cs)
	}
}

// Without a budget the overload is reached and the deadline hits it: the knee
// the sweep found is kept, not lost.
func TestDriverDeadlineInOverloadKeepsTheKnee(t *testing.T) {
	w := &fakeWorld{kneeN: 4000, setNErrAt: 7000, setNErrFromStart: 2}
	cfg := testConfig()
	cfg.Predicted, cfg.Start, cfg.NMax = 24108, 6027, 7058
	out, err := NewDriver(w, cfg).Run(context.Background())
	if err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if out.Result.Kind != sweep.KneeFound || out.Result.N < 3013 || out.Result.N >= 4000 {
		t.Errorf("result = %+v, want a found knee in [3013, 4000)", out.Result)
	}
	if !strings.Contains(out.StoppedEarly, "context deadline exceeded") {
		t.Errorf("StoppedEarly = %q", out.StoppedEarly)
	}
	if cs := rowsOfKind(out, "confirm"); len(cs) == 0 || cs[0].Outcome != sweep.Pass {
		t.Errorf("confirm rows = %+v", cs)
	}
}

func TestDriverErrorMidSweepIsTimeBoundAtLastPass(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, setNErrAt: 400}
	cfg := testConfig()
	cfg.Predicted, cfg.Start, cfg.NMax = 0, 100, 2000 // 100, 200, then 400 fails
	out, err := NewDriver(w, cfg).Run(context.Background())
	if err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if out.Result.Kind != sweep.TimeBound || out.Result.N != 200 {
		t.Errorf("result = %+v, want time-bound at 200", out.Result)
	}
	if out.StoppedEarly == "" {
		t.Error("StoppedEarly empty")
	}
}

func TestDriverErrorBeforeAnyStepIsFatal(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, setNErrAt: 3}
	cfg := testConfig()
	cfg.Predicted, cfg.Start, cfg.NMax = 0, 100, 2000 // the warm-up ramps to 50
	if _, err := NewDriver(w, cfg).Run(context.Background()); err == nil {
		t.Fatal("Run = nil, want the warm-up error")
	}
}

func TestOverBudgetReservesOverloadRampUnderModel(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	cfg := testConfig()
	cfg.Clock = fixedClock{t0}
	cfg.NMax, cfg.RateMax, cfg.RateScale = 7058, 12, 5000
	d := NewDriver(&fakeWorld{}, cfg)
	d.start = t0
	d.joined = 3013
	cost := sweep.SettleTypical + sweep.HoldDuration
	reserve := cfg.Confirm + cfg.Overload + 2*sweep.SettleTypical + 30*time.Second
	secs := func(from, to int) time.Duration { return time.Duration(d.rampSeconds(from, to) * float64(time.Second)) }
	d.cfg.Budget = cost + reserve + secs(0, 3013) + secs(3013, 6026) + 20*time.Second
	if d.overBudget(3013, 3013) {
		t.Fatal("a budget covering the whole tail was refused")
	}
	// The old reserve priced the overload at 2*R(0,lo): ~908 s, not ~2724 s.
	d.cfg.Budget = cost + reserve + 2*secs(0, 3013) + 20*time.Second
	if !d.overBudget(3013, 3013) {
		t.Fatal("the overload's ramp from the knee to 2x was not reserved")
	}
}

func TestDriverOverloadClampedAtTop(t *testing.T) {
	cfg := testConfig()
	cfg.Predicted, cfg.NMax, cfg.Top = 400, 2000, 900
	out, err := NewDriver(&fakeWorld{kneeN: 700}, cfg).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ov := rowsOfKind(out, "overload")
	if len(ov) != 1 || ov[0].N != 900 {
		t.Fatalf("overload rows = %+v, want one at 900", ov)
	}
	cfg = testConfig()
	cfg.Predicted, cfg.NMax, cfg.Top = 400, 2000, 700
	out, err = NewDriver(&fakeWorld{kneeN: 700}, cfg).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rowsOfKind(out, "overload")) != 0 {
		t.Error("an overload ran although Top does not exceed the knee")
	}
	if !strings.Contains(strings.Join(out.Notes, "\n"), "overload not measured: capped at 700 (N_max by ") {
		t.Errorf("notes = %v", out.Notes)
	}
}

func TestGuardAdmitsTheFirstStepAtHostCap(t *testing.T) {
	models := map[string]struct {
		m     sweep.JoinModel
		floor float64
		rule  bool
	}{
		"const":    {sweep.JoinModel{Const: 27}, 0, false},
		"rule":     {sweep.JoinModel{Max: 12, Scale: 5000}, 0, true},
		"anchored": {sweep.JoinModel{Max: 12, Scale: 5000}, 4, true},
	}
	for _, budget := range []time.Duration{15 * time.Minute, 100 * time.Minute} {
		for name, mc := range models {
			t.Run(fmt.Sprintf("%s/%s", budget, name), func(t *testing.T) {
				c := HostCap(CapInput{Model: mc.m, Floor: mc.floor, HasRule: mc.rule, Budget: budget, Steps: sweepSteps,
					PortRange: [2]int{32768, 60999}, UlimitN: 1 << 20, GenCPUs: 4, BotsPerGenCPU: 1500, CalBots: 4096})
				if c.NMax < 1 {
					t.Skipf("N_max %d: nothing fits", c.NMax)
				}
				t0 := time.Unix(1_700_000_000, 0)
				cfg := testConfig()
				cfg.Clock, cfg.Budget = fixedClock{t0}, budget
				cfg.NMax, cfg.Start, cfg.Top, cfg.NMaxBy = c.NMax, c.Start, c.Top, c.By
				if mc.rule {
					cfg.RateMax, cfg.RateScale = mc.m.Max, c.Scale
				} else {
					cfg.JoinRate = mc.m.Const
				}
				d := NewDriver(&fakeWorld{}, cfg)
				d.start = t0
				if d.overBudget(c.Start, 0) {
					t.Errorf("guard refuses the first step %d at N_max %d (by %s)", c.Start, c.NMax, c.By)
				}
			})
		}
	}
}

func TestDriverTimeBoundCarriesTheLimit(t *testing.T) {
	cfg := testConfig()
	cfg.Predicted, cfg.NMax, cfg.NMaxBy = 400, 800, "memory"
	out, err := NewDriver(&fakeWorld{kneeN: 1 << 30}, cfg).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Result.String(); got != "≥ 800 (N_max: memory)" {
		t.Errorf("result = %q", got)
	}
}
