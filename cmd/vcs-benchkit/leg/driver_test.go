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
	updates    uint64
}

type fakeHold struct {
	label string
	n     int
	d     time.Duration
}

type fakeSystem struct {
	w *fakeWorld
	n int
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

func (s *fakeSystem) SetN(ctx context.Context, n int) error {
	if n < s.n {
		return errors.New("fake: set_n cannot decrease")
	}
	s.n = n
	return nil
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
	if s.w.deadStart && label == "preflight" {
		st.Unique = 0
	}
	if s.n > s.w.kneeN {
		st.LossRatio = 0.05
	}
	if s.w.genBoundN > 0 && s.n >= s.w.genBoundN {
		st.Violations = []string{"rx lag p99 9ms above 2ms"}
		st.Valid = false
	}
	return st, nil
}

func (s *fakeSystem) LogText() (string, error) { return "", nil }
func (s *fakeSystem) BeginSample() func() Window {
	n := s.n
	return func() Window {
		w := Window{ServerCPU: 0.3, GenCPU: 0.2, Seconds: 40, Err: s.w.samplerErr}
		if s.w.genBoundN > 0 && n >= s.w.genBoundN {
			w.GenCPU = 0.95
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
