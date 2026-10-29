package leg

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
)

// stepClock advances one second per Now call, so the settle predicate's time
// rules (5 s floor, 2 s quiet, 60 s bound) play out without sleeping.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.t.IsZero() {
		c.t = time.Unix(1_700_000_000, 0)
	}
	c.t = c.t.Add(time.Second)
	return c.t
}

type fakeWorld struct {
	mu         sync.Mutex
	starts     int
	closes     int
	holds      []fakeHold // every hold, in order, across systems
	kneeN      int        // loss >= 1% for n > kneeN
	genBoundN  int        // generator CPU out of bounds for n >= genBoundN (0 = never)
	noisy      bool       // update counters grow during every hold
	samplerErr string     // Window.Err of every window
	deadStart  bool       // pre-flight hold delivers no frames
	startErr   error
	joinFailN  int     // SetN reports the run-40752831443 handshake failure for n >= joinFailN (0 = never)
	joinsV2    bool    // report the failure as a v2 joins line (2 failures) instead of a v1 error
	rampGenCPU float64 // generator CPU of a ramp window whose SetN had join failures (0 = the usual 0.2)
	failedGUID string  // GUID carried by the v2 failed entries
	logs       string  // server log text returned by LogText
	logErr     error   // LogText fails with this error when set
	updates    uint64
	// stepHook and winHook let a test shape one report or one sampler window
	// independently of the other (n is the bot count of the system).
	stepHook func(n int, label string, st *Step)
	winHook  func(n int, w *Window)
	// pendingPreflightFail models a v2 bot whose pre-flight ramp (fire-and-forget,
	// before the first hold) lost bot 1: SetN says pending, the pre-flight hold
	// sees no frame, and the joins line is only visible through LastJoins.
	pendingPreflightFail bool
}

type fakeHold struct {
	label string
	n     int
	d     time.Duration
}

type fakeSystem struct {
	w          *fakeWorld
	n          int
	failedRamp bool // the last SetN had join failures
}

func (w *fakeWorld) Start(ctx context.Context) (System, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.startErr != nil {
		return nil, w.startErr
	}
	w.starts++
	return &fakeSystem{w: w}, nil
}

func (s *fakeSystem) SetN(ctx context.Context, n int) (Joins, error) {
	s.failedRamp = false
	if s.w.pendingPreflightFail && s.n == 0 {
		s.n = n
		return Joins{Source: JoinsSourcePending, Requested: n}, nil
	}
	if s.w.joinFailN > 0 && n >= s.w.joinFailN {
		s.failedRamp = true
		if s.w.joinsV2 {
			by := map[string]int{"control": 0, "radio": 0, "voice_dial": 0, "hello": 2}
			return Joins{
				Schema: JoinsSchema, Requested: n, Had: s.n, N: s.n + (n - s.n - 2), Joined: n - s.n - 2,
				JoinFailures: 2, FailuresByStage: by, HelloTriesHist: make([]int, helloHistLen), HelloTriesMax: 5,
				Failed: []JoinFailure{
					{Bot: 1553, GUID: s.w.failedGUID, Stage: "hello", Tries: 5, Error: "no HELLO_ACK after 5 tries"},
					{Bot: 1669, GUID: "ffffffff-0000-0000-0000-000000000000", Stage: "hello", Tries: 5, Error: "no HELLO_ACK after 5 tries"},
				},
				Source: JoinsSourceV2,
			}, nil
		}
		return JoinsFromV1Error(n, s.n, "loadbot: bot 1669: hello: no HELLO_ACK after 5 tries"), nil
	}
	if n < s.n {
		return Joins{}, errors.New("fake: set_n cannot decrease")
	}
	s.n = n
	return Joins{}, nil
}

func (s *fakeSystem) Hold(ctx context.Context, d time.Duration, label string) (Step, error) {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	s.w.holds = append(s.w.holds, fakeHold{label, s.n, d})
	if s.w.noisy {
		s.w.updates += 7
	}
	st := Step{Type: "step", Step: label, N: s.n, Valid: true,
		Unique: 990, Expected: 1000, Updates: map[string]uint64{"CLIENT_JOIN": 3 + s.w.updates}}
	if (s.w.deadStart || s.w.pendingPreflightFail) && label == "preflight" {
		st.Unique = 0
	}
	if s.n > s.w.kneeN {
		st.LossRatio = 0.05
	}
	if s.w.genBoundN > 0 && s.n >= s.w.genBoundN {
		st.Violations = []string{"rx lag p99 9ms above 2ms"}
		st.Valid = false
	}
	if s.w.stepHook != nil {
		s.w.stepHook(s.n, label, &st)
	}
	return st, nil
}

// LastJoins reports the pre-flight ramp's joins line of pendingPreflightFail.
func (s *fakeSystem) LastJoins() (Joins, bool) {
	if !s.w.pendingPreflightFail {
		return Joins{}, false
	}
	return JoinsFromV1Error(2, 0, "loadbot: bot 1: hello: no HELLO_ACK after 5 tries"), true
}

func (s *fakeSystem) LogText() (string, error) {
	if s.w.logErr != nil {
		return "", s.w.logErr
	}
	return s.w.logs, nil
}
func (s *fakeSystem) BeginSample() func() Window {
	n := s.n
	return func() Window {
		w := Window{ServerCPU: 0.3, GenCPU: 0.2, Seconds: 40, Err: s.w.samplerErr}
		if s.w.genBoundN > 0 && n >= s.w.genBoundN {
			w.GenCPU = 0.95
		}
		if s.failedRamp && s.w.rampGenCPU > 0 {
			w.GenCPU = s.w.rampGenCPU
		}
		if s.w.winHook != nil {
			s.w.winHook(n, &w)
		}
		return w
	}
}
func (s *fakeSystem) Close() error {
	s.w.mu.Lock()
	s.w.closes++
	s.w.mu.Unlock()
	return nil
}

func testConfig() Config {
	return Config{
		Predicted: 400, NMax: 2000, JoinRate: 27,
		Warmup: 30 * time.Second, Hold: 40 * time.Second,
		Confirm: 120 * time.Second, Overload: 60 * time.Second, ProbeHold: time.Second,
		Bounds: sweep.DefaultBounds, Clock: &stepClock{},
	}
}

func TestDriverFindsKneeConfirmsAndOverloads(t *testing.T) {
	w := &fakeWorld{kneeN: 700}
	d := NewDriver(w, testConfig())
	out, err := d.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Kind != sweep.KneeFound || out.Result.N != 700 {
		t.Fatalf("result = %v", out.Result)
	}
	var sweepNs []int
	for _, r := range out.Rows {
		if r.Kind == "sweep" {
			sweepNs = append(sweepNs, r.N)
		}
	}
	want := []int{100, 200, 400, 800, 600, 700, 750}
	if len(sweepNs) != len(want) {
		t.Fatalf("sweep steps = %v, want %v", sweepNs, want)
	}
	for i := range want {
		if sweepNs[i] != want[i] {
			t.Fatalf("sweep steps = %v, want %v", sweepNs, want)
		}
	}
	// Bots only grow: a step below the joined count (800 -> 600, and the
	// confirm at 700 after 750) needs a fresh server and loadbot.
	if w.starts != 3 || w.closes != 3 {
		t.Errorf("starts = %d closes = %d, want 3 and 3 (1 initial + 2 rebuilds)", w.starts, w.closes)
	}
	last := out.Rows[len(out.Rows)-2:]
	if last[0].Kind != "confirm" || last[0].N != 700 || last[0].Outcome != sweep.Pass || last[0].Step.Step == "" {
		t.Errorf("confirm row = %+v", last[0])
	}
	if last[1].Kind != "overload" || last[1].N != 1400 || last[1].Outcome != sweep.Breach {
		t.Errorf("overload row = %+v", last[1])
	}
	// The confirm hold is 120 s and the overload 60 s.
	var confirmHold, overloadHold time.Duration
	for _, h := range w.holds {
		switch h.label {
		case "confirm":
			confirmHold = h.d
		case "overload":
			overloadHold = h.d
		}
	}
	if confirmHold != 120*time.Second || overloadHold != 60*time.Second {
		t.Errorf("confirm/overload holds = %v / %v", confirmHold, overloadHold)
	}
}

func TestDriverWarmupAndPreflightComeFirst(t *testing.T) {
	w := &fakeWorld{kneeN: 700}
	d := NewDriver(w, testConfig())
	if _, err := d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(w.holds) < 2 || w.holds[0].label != "preflight" || w.holds[0].n != 2 {
		t.Fatalf("first hold = %+v, want the pre-flight login at 2 bots", w.holds[0])
	}
	// Warm-up: 30 s at ~10 % of the predicted knee (400 -> 40), discarded.
	if h := w.holds[1]; h.label != "warmup" || h.n != 40 || h.d != 30*time.Second {
		t.Fatalf("second hold = %+v", h)
	}
}

func TestDriverPreflightNeedsFrames(t *testing.T) {
	w := &fakeWorld{kneeN: 700, deadStart: true}
	_, err := NewDriver(w, testConfig()).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pre-flight") {
		t.Fatalf("err = %v, want a pre-flight failure", err)
	}
	if w.closes != w.starts {
		t.Errorf("a failed pre-flight left %d systems open", w.starts-w.closes)
	}
}

func TestDriverGeneratorBoundIsInvalidNotBreach(t *testing.T) {
	w := &fakeWorld{kneeN: 100000, genBoundN: 400}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Kind != sweep.GeneratorBound || out.Result.N != 200 {
		t.Fatalf("result = %v, want >= 200 (generator-bound)", out.Result)
	}
	var inv *Row
	for i := range out.Rows {
		if out.Rows[i].N == 400 && out.Rows[i].Kind == "sweep" {
			inv = &out.Rows[i]
		}
	}
	if inv == nil || inv.Outcome != sweep.Invalid || len(inv.Reasons) == 0 {
		t.Fatalf("n=400 row = %+v", inv)
	}
}

func TestDriverTimeBoundAtNMax(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30}
	cfg := testConfig()
	cfg.NMax = 400 // 100, 200, 400 all pass
	out, err := NewDriver(w, cfg).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Kind != sweep.TimeBound || out.Result.N != 400 {
		t.Fatalf("result = %v", out.Result)
	}
	// Overload is 2x the knee, but never above 2 x N_max (the port budget).
	ov := out.Rows[len(out.Rows)-1]
	if ov.Kind != "overload" || ov.N != 800 {
		t.Errorf("overload row = %+v", ov)
	}
}

func TestDriverCannotSettleSkipsTheHold(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, noisy: true}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Kind != sweep.GeneratorBound || out.Result.N != 0 {
		t.Fatalf("result = %v", out.Result)
	}
	r := out.Rows[0]
	if r.Outcome != sweep.Invalid || r.Held || !r.Data.CannotSettle {
		t.Fatalf("row = %+v", r)
	}
	if !strings.Contains(strings.Join(r.Reasons, ";"), sweep.InvalidCannotSettle) {
		t.Errorf("reasons = %v", r.Reasons)
	}
	for _, h := range w.holds {
		if h.label == "step-1" {
			t.Errorf("the measured hold ran although the step never settled: %+v", h)
		}
	}
	if !math.IsNaN(r.Data.LossRatio) {
		t.Errorf("an unmeasured step must carry NaN loss, got %v", r.Data.LossRatio)
	}
}

func TestDriverBudgetStopsTheSweepAsTimeBound(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30}
	cfg := testConfig()
	cfg.Budget = 5 * time.Minute // fixed costs alone leave room for only the first steps
	out, err := NewDriver(w, cfg).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Kind != sweep.TimeBound {
		t.Fatalf("result = %v", out.Result)
	}
	if out.Result.N >= 2000 {
		t.Errorf("the budget guard did not stop early: %v", out.Result)
	}
	if !strings.Contains(strings.Join(out.Notes, ";"), "budget") {
		t.Errorf("notes = %v", out.Notes)
	}
}

func TestDriverStartFailure(t *testing.T) {
	w := &fakeWorld{startErr: errors.New("boom")}
	if _, err := NewDriver(w, testConfig()).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestDriverSamplerFailureInvalidatesTheStep(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, samplerErr: "ss: not found"}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := out.Rows[0]
	if r.Outcome != sweep.Invalid || !strings.Contains(strings.Join(r.Reasons, ";"), "ss: not found") {
		t.Fatalf("row = %+v", r)
	}
	if out.Result.Kind != sweep.GeneratorBound {
		t.Errorf("result = %v", out.Result)
	}
}

// sweepRow returns the first sweep row measured at n.
func sweepRow(t *testing.T, out *Outcome, n int) Row {
	t.Helper()
	for _, r := range out.Rows {
		if r.Kind == "sweep" && r.N == n {
			return r
		}
	}
	t.Fatalf("no sweep row at %d bots: %+v", n, out.Rows)
	return Row{}
}

// The fake bot used to set violations and a hot generator CPU together, so
// either wire could be dropped from measure() unnoticed. These tests drive each
// wire alone through the driver (D14, risk R1).

func TestDriverBotViolationsAloneInvalidateTheStep(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30,
		stepHook: func(n int, label string, st *Step) {
			if n >= 400 && strings.HasPrefix(label, "step-") {
				st.Violations = []string{"rx lag p99 9ms above 2ms"}
			}
		}}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := sweepRow(t, out, 400)
	if r.Outcome != sweep.Invalid || !strings.Contains(strings.Join(r.Reasons, ";"), "rx lag p99") {
		t.Fatalf("row = %+v, want invalid with the bot's violation", r)
	}
	if out.Result.Kind != sweep.GeneratorBound || out.Result.N != 200 {
		t.Fatalf("result = %v, want generator-bound at 200", out.Result)
	}
}

func TestDriverHotGeneratorCPUAloneInvalidatesTheStep(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30,
		winHook: func(n int, win *Window) {
			if n >= 400 {
				win.GenCPU = 0.95
			}
		}}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := sweepRow(t, out, 400)
	if r.Outcome != sweep.Invalid || !strings.Contains(strings.Join(r.Reasons, ";"), "generator cpu") {
		t.Fatalf("row = %+v, want invalid on generator cpu", r)
	}
	if out.Result.Kind != sweep.GeneratorBound || out.Result.N != 200 {
		t.Fatalf("result = %v, want generator-bound at 200", out.Result)
	}
}

func TestDriverServerCPUOverBoundIsABreach(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30,
		winHook: func(n int, win *Window) {
			if n >= 400 {
				win.ServerCPU = 0.9
			}
		}}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := sweepRow(t, out, 400)
	if r.Outcome != sweep.Breach || !strings.Contains(strings.Join(r.Reasons, ";"), "server process cpu") {
		t.Fatalf("row = %+v, want a server-cpu breach", r)
	}
	if out.Result.Kind != sweep.KneeFound || out.Result.N >= 400 {
		t.Fatalf("result = %v, want a knee below 400", out.Result)
	}
}

func TestDriverResourceExhaustedInTheHoldIsABreach(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30,
		stepHook: func(n int, label string, st *Step) {
			if n >= 400 && strings.HasPrefix(label, "step-") {
				st.ResourceExhausted.Hold = 3
			}
		}}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := sweepRow(t, out, 400)
	if r.Outcome != sweep.Breach || !strings.Contains(strings.Join(r.Reasons, ";"), "ResourceExhausted") {
		t.Fatalf("row = %+v, want a ResourceExhausted breach", r)
	}
	if out.Result.Kind != sweep.KneeFound || out.Result.N >= 400 {
		t.Fatalf("result = %v, want a knee below 400", out.Result)
	}
}

func TestDriverFailedConfirmIsNotedAndMarksTheKneeUnconfirmed(t *testing.T) {
	w := &fakeWorld{kneeN: 700,
		stepHook: func(n int, label string, st *Step) {
			if label == "confirm" {
				st.LossRatio = 0.05
			}
		}}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Kind != sweep.KneeFound || out.Result.N != 700 {
		t.Fatalf("result = %v", out.Result)
	}
	if !out.Unconfirmed {
		t.Error("a knee that fails its own confirm must be marked unconfirmed")
	}
	notes := strings.Join(out.Notes, ";")
	if !strings.Contains(notes, "confirm") || !strings.Contains(notes, "700") || !strings.Contains(notes, "loss") {
		t.Errorf("notes = %v, want the failed confirm and its reason", out.Notes)
	}
}

func TestDriverInvalidConfirmIsNotedToo(t *testing.T) {
	w := &fakeWorld{kneeN: 700,
		stepHook: func(n int, label string, st *Step) {
			if label == "confirm" {
				st.Violations = []string{"rx lag p99 9ms above 2ms"}
			}
		}}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !out.Unconfirmed || !strings.Contains(strings.Join(out.Notes, ";"), "confirm") {
		t.Fatalf("unconfirmed = %v notes = %v", out.Unconfirmed, out.Notes)
	}
}

func TestDriverPassingConfirmLeavesTheKneeConfirmed(t *testing.T) {
	out, err := NewDriver(&fakeWorld{kneeN: 700}, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Unconfirmed {
		t.Errorf("notes = %v", out.Notes)
	}
	for _, n := range out.Notes {
		if strings.Contains(n, "confirm step") {
			t.Errorf("unexpected confirm note %q", n)
		}
	}
}

// Settle runs on 1 s probe holds, which the bot counts as hold phases, so their
// ResourceExhausted counts must be folded into the settle column.
func TestDriverProbeHoldsFeedTheSettleColumns(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30,
		stepHook: func(n int, label string, st *Step) {
			if strings.HasPrefix(label, "probe-") && n == 400 {
				st.ResourceExhausted.Hold = 2
				st.ResourceExhausted.Ramp = 1
			}
		}}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := sweepRow(t, out, 400)
	if r.Step.ResourceExhausted.Settle < 2 {
		t.Errorf("rex settle = %d, want the probes' counts (>= 2)", r.Step.ResourceExhausted.Settle)
	}
	if r.Step.ResourceExhausted.Ramp < 1 {
		t.Errorf("rex ramp = %d, want the probes' ramp counts (>= 1)", r.Step.ResourceExhausted.Ramp)
	}
	// Probe counts never reach the hold column, so they cannot cause a breach.
	if r.Step.ResourceExhausted.Hold != 0 || r.Outcome != sweep.Pass {
		t.Errorf("row = %+v, want a pass with no hold-phase rex", r)
	}
}

// TestDriverBudgetStopAfterABreachIsAKnee: whatever budget makes the guard
// stop the sweep after a breach was already measured, the result must be a
// knee at the last pass, never "time-bound" (which reads as censored and would
// turn the D13 go/no-go into a no-go for a knee the leg actually bracketed).
func TestDriverBudgetStopAfterABreachIsAKnee(t *testing.T) {
	found := false
	for b := 2 * time.Minute; b <= 40*time.Minute; b += 5 * time.Second {
		w := &fakeWorld{kneeN: 150} // 100 passes, 200 breaches, then bisect
		cfg := testConfig()
		cfg.Budget = b
		out, err := NewDriver(w, cfg).Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		stopped := strings.Contains(strings.Join(out.Notes, ";"), "budget guard")
		breached := false
		for _, r := range out.Rows {
			if r.Kind == "sweep" && r.Outcome == sweep.Breach {
				breached = true
			}
		}
		if !stopped || !breached {
			continue
		}
		found = true
		if out.Result.Kind != sweep.KneeFound || out.Result.N < 100 || out.Result.N >= 200 {
			t.Fatalf("budget %s: result = %v, want a knee in [100, 200)", b, out.Result)
		}
		if !strings.Contains(strings.Join(out.Notes, ";"), "bisection") {
			t.Errorf("budget %s: notes do not say the bisection was cut short: %v", b, out.Notes)
		}
	}
	if !found {
		t.Fatal("no budget stopped the sweep after a breach; the test scenario needs retuning")
	}
}

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// TestOverBudgetReservesBothTailRamps: the confirm may rebuild and ramp to the
// knee, and the overload then ramps another knee's worth of bots, so the
// reserve holds two ramps and two settles, not one.
func TestOverBudgetReservesBothTailRamps(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	cfg := testConfig()
	cfg.Clock = fixedClock{t0}
	cfg.Budget = 400 * time.Second
	d := NewDriver(&fakeWorld{}, cfg)
	d.start = t0
	// Step 100 costs ~54 s; one ramp of 2700 bots at 27/s is 100 s. With one
	// tail ramp the total is ~364 s (fits 400 s); with both it is ~484 s.
	if !d.overBudget(100, 2700) {
		t.Fatal("overBudget ignored the overload's own ramp after a rebuilt confirm")
	}
}

// Run 40752831443: a voice handshake failure inside a ramp aborted the whole
// arm. It must instead be that step's recorded outcome (a server breach).
func TestDriverJoinFailureIsAStepOutcome(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, joinFailN: 150}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatalf("Run aborted on a join failure: %v", err)
	}
	var failed *Row
	sweeps := 0
	for i := range out.Rows {
		r := &out.Rows[i]
		if r.Kind != "sweep" {
			continue
		}
		sweeps++
		if r.N >= 150 && failed == nil {
			failed = r
		}
	}
	if failed == nil {
		t.Fatalf("no sweep row at or above the failing N: %+v", out.Rows)
	}
	if failed.Outcome != sweep.Breach {
		t.Errorf("row at %d: outcome = %v, want Breach", failed.N, failed.Outcome)
	}
	if len(failed.Reasons) == 0 || !strings.HasPrefix(failed.Reasons[0], "join failure") {
		t.Errorf("reasons = %v, want a reason with the prefix \"join failure\"", failed.Reasons)
	}
	if sweeps < 3 {
		t.Errorf("sweep rows = %d: the bisection did not run after the join failure", sweeps)
	}
	if out.Result.Kind != sweep.KneeFound {
		t.Errorf("result = %v, want a knee", out.Result)
	}
}

func sweepRowAtOrAbove(out *Outcome, n int) *Row {
	for i := range out.Rows {
		if r := &out.Rows[i]; r.Kind == "sweep" && r.N >= n {
			return r
		}
	}
	return nil
}

func TestDriverJoinFailureV2KeepsCounts(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, joinFailN: 150, joinsV2: true, failedGUID: "0b6f3c1e-8f0a-4c1d-9a51-1f2d3c4b5a69"}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := sweepRowAtOrAbove(out, 150)
	if r == nil {
		t.Fatalf("no failing row: %+v", out.Rows)
	}
	if r.Held {
		t.Error("a join-failure step must not reach its hold")
	}
	if r.Joins.JoinFailures != 2 || r.Joins.FailuresByStage["hello"] != 2 {
		t.Errorf("Joins = %+v", r.Joins)
	}
	if r.Outcome != sweep.Breach || len(r.Reasons) == 0 || !strings.Contains(r.Reasons[0], "join failure: 2 of") {
		t.Errorf("outcome %v reasons %v", r.Outcome, r.Reasons)
	}
	if r.FailedAckedByServer != 0 {
		t.Errorf("FailedAckedByServer = %d, want 0 (GUIDs present, empty log)", r.FailedAckedByServer)
	}
}

func TestDriverJoinFailureCountsFailedGUIDsAckedByServer(t *testing.T) {
	g := "0b6f3c1e-8f0a-4c1d-9a51-1f2d3c4b5a69"
	w := &fakeWorld{kneeN: 1 << 30, joinFailN: 150, joinsV2: true, failedGUID: g,
		logs: "level=INFO msg=\"Accepted voice HELLO\" sender_id=" + g + " addr=1.2.3.4:5\n"}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := sweepRowAtOrAbove(out, 150)
	if r == nil {
		t.Fatal("no failing row")
	}
	// The fake's log never grows, so the ramp delta is empty: 0 of 2 acked.
	if r.FailedAckedByServer != 0 {
		t.Errorf("FailedAckedByServer = %d, want 0", r.FailedAckedByServer)
	}
}

func TestDriverJoinFailureWithHotGeneratorIsInvalid(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, joinFailN: 150, rampGenCPU: 0.95}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := sweepRowAtOrAbove(out, 150)
	if r == nil || r.Outcome != sweep.Invalid {
		t.Fatalf("row = %+v, want Invalid", r)
	}
	if out.Result.Kind != sweep.GeneratorBound {
		t.Errorf("result = %v, want generator-bound", out.Result)
	}
}

func TestDriverRebuildsAfterJoinFailure(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, joinFailN: 150}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Starts < 2 {
		t.Errorf("starts = %d, want a rebuild", out.Starts)
	}
	found := false
	for _, n := range out.Notes {
		if strings.Contains(n, "rebuilt the server and load bot after join failures at") {
			found = true
		}
	}
	if !found {
		t.Errorf("notes = %v", out.Notes)
	}
}

func TestDriverJoinFailureAtOverloadIsRecorded(t *testing.T) {
	// The sweep passes everywhere below 1500; the 2x overload step fails to join.
	w := &fakeWorld{kneeN: 1 << 30, joinFailN: 1500}
	cfg := testConfig()
	cfg.NMax = 1000
	out, err := NewDriver(w, cfg).Run(context.Background())
	if err != nil {
		t.Fatalf("Run aborted: %v", err)
	}
	last := out.Rows[len(out.Rows)-1]
	if last.Kind != "overload" || last.Outcome != sweep.Breach {
		t.Fatalf("last row = %s %v, want overload breach", last.Kind, last.Outcome)
	}
}

func TestDriverJoinFailureInWarmupIsFatal(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, joinFailN: 40}
	_, err := NewDriver(w, testConfig()).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "warm-up: join failures") || !strings.Contains(err.Error(), "no HELLO_ACK after 5 tries") {
		t.Fatalf("err = %v", err)
	}
}

func TestDriverPreflightJoinFailureIsFatal(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, joinFailN: 2}
	_, err := NewDriver(w, testConfig()).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pre-flight: join failures") {
		t.Fatalf("err = %v", err)
	}
}

// A v2 bot's pre-flight ramp is fire-and-forget, so its join failure arrives
// as a joins line during the pre-flight hold. The error must carry the bot's
// words, not only "no voice frame reached the listener".
func TestDriverPreflightPendingJoinFailureIsFatal(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, pendingPreflightFail: true}
	_, err := NewDriver(w, testConfig()).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pre-flight: join failures") || !strings.Contains(err.Error(), "no HELLO_ACK after 5 tries") {
		t.Fatalf("err = %v", err)
	}
}

// After a join failure the system is dead and the next step boots fresh at 2
// bots, so the next step's cost is a rebuild plus a ramp from 2, not a ramp
// from the stale pre-failure count.
func TestOverBudgetPricesPendingRebuild(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	cfg := testConfig()
	cfg.Clock = fixedClock{t0}
	d := NewDriver(&fakeWorld{}, cfg)
	d.start = t0
	d.joined = 1000 // stale: the system that held them failed a join
	// Step 1100 from the stale count is a 100-bot ramp (~4 s); the real cost
	// is a rebuild plus ~1098 bots at 27/s (~41 s + 30 s). Some budget fits
	// the first and not the second.
	found := false
	for b := time.Second; b < 2000*time.Second; b += time.Second {
		d.cfg.Budget = b
		d.rebuild = false
		stale := d.overBudget(1100, 0)
		d.rebuild = true
		pending := d.overBudget(1100, 0)
		if !stale && pending {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("overBudget ignored the pending rebuild: priced the ramp from the stale count")
	}
}

// A failed log read must not make the whole server log the ramp's delta.
func TestEnsureUnreadableLogIsNotARampDelta(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, joinFailN: 150, joinsV2: true,
		failedGUID: "0b6f3c1e-8f0a-4c1d-9a51-1f2d3c4b5a69",
		logs:       strings.Repeat("Accepted voice HELLO 0b6f3c1e-8f0a-4c1d-9a51-1f2d3c4b5a69\n", 50)}
	d := NewDriver(w, testConfig())
	ctx := context.Background()
	if err := d.boot(ctx); err != nil {
		t.Fatal(err)
	}
	w.logErr = errors.New("read server log: file closed")
	r, err := d.ensure(ctx, 150)
	if err != nil {
		t.Fatal(err)
	}
	if r.Logs != (LogCounts{}) || r.FailedAcked != -1 {
		t.Fatalf("logs = %+v acked = %d, want zero and unknown", r.Logs, r.FailedAcked)
	}
	if !strings.Contains(strings.Join(d.out.Notes, ";"), "ramp log unreadable") {
		t.Fatalf("notes = %v, want a note that the ramp log was unreadable", d.out.Notes)
	}
}
