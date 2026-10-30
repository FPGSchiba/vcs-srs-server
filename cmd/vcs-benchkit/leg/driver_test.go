package leg

import (
	"context"
	"errors"
	"fmt"
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
	// dieAtSettleN: the first probe hold at this N of the confirm step returns the
	// verbatim v2 hold-death text of run 40859021110 (0 = never).
	dieAtSettleN int
	dieFired     bool
	// deaths schedules v3-style control_lost events: the nth occurrence (1-based,
	// across systems) of a phase at a bot count kills bot 2 (see fakeDeath).
	deaths    []*fakeDeath
	phaseSeen map[string]int
	// setNs records every SetN target; chunkJoins makes SetN return a populated
	// joins line (Joined = the chunk) instead of the zero Joins.
	setNs      []int
	chunkJoins bool
	// noBurst models a v2 bot; burstFailLive is the join failures of a live burst.
	noBurst       bool
	burstFailLive int
	bursts        []fakeBurst
	// gapDeathN kills bot 2 in the gap after the first measured hold at that
	// bot count: on the second ControlLost read after the hold (the first is
	// the driver's post-hold check, the second its next step's baseline).
	gapDeathN int
	gapArmed  bool
	gapReads  int
	// warmupDiesV2 makes the first warm-up hold return the v2 hold-death text;
	// warmupDiesV3 records a v3 control_lost event during it instead.
	warmupDiesV2, warmupDiesV3 bool
	// setNErrAt makes SetN fail with a wrapped context.DeadlineExceeded for any
	// target >= it, on systems started at or after setNErrFromStart (0 = all).
	setNErrAt, setNErrFromStart int
}

// fakeDeath kills a bot when the nth (1-based) occurrence of phase (ramp, settle
// or hold) at bot count n happens. A hold counts measured holds only.
type fakeDeath struct {
	n     int
	phase string
	nth   int
}

type fakeBurst struct {
	add, inFlight int
	talker        string
	joinedBefore  int
}

type fakeHold struct {
	label string
	n     int
	d     time.Duration
}

type fakeSystem struct {
	w          *fakeWorld
	id         int // 1-based start ordinal
	n          int
	failedRamp bool // the last SetN had join failures
	lost       []ControlLostEvent
}

// die records a scheduled death for this occurrence of phase at s.n. w.mu is held.
func (s *fakeSystem) die(phase string) {
	if len(s.w.deaths) == 0 {
		return
	}
	if s.w.phaseSeen == nil {
		s.w.phaseSeen = map[string]int{}
	}
	key := fmt.Sprintf("%d/%s", s.n, phase)
	s.w.phaseSeen[key]++
	for _, d := range s.w.deaths {
		if d.n == s.n && d.phase == phase && d.nth == s.w.phaseSeen[key] {
			s.lost = append(s.lost, ControlLostEvent{Bot: 2, Name: "LDBT-0002", Cause: "stream", Code: "Unavailable",
				Message: "error reading from server: connection reset by peer", Phase: phase, SinceLastOKPingS: 3.2, DeadTotal: len(s.lost) + 1})
		}
	}
}

func (s *fakeSystem) ControlLost() []ControlLostEvent {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if s.w.gapArmed {
		if s.w.gapReads++; s.w.gapReads == 2 {
			s.w.gapArmed = false
			s.lost = append(s.lost, ControlLostEvent{Bot: 2, Name: "LDBT-0002", Cause: "stream", Code: "Unavailable",
				Message: "error reading from server: connection reset by peer", Phase: "gap", DeadTotal: len(s.lost) + 1})
		}
	}
	return append([]ControlLostEvent(nil), s.lost...)
}

func (s *fakeSystem) SupportsBurst() bool { return !s.w.noBurst }

func (s *fakeSystem) Burst(ctx context.Context, add, inFlight int, talker string) (Joins, error) {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if s.w.noBurst {
		return Joins{}, fmt.Errorf("%w: burst", ErrNotSupported)
	}
	s.w.bursts = append(s.w.bursts, fakeBurst{add, inFlight, talker, s.n})
	had := s.n
	fails := 0
	if talker == "live" {
		fails = s.w.burstFailLive
	}
	by := map[string]int{"control": 0, "radio": 0, "voice_dial": 0, "hello": fails}
	hist := make([]int, helloHistLen)
	hist[1] = add - fails
	s.n = had + add - fails
	j := Joins{Schema: JoinsSchema, Requested: had + add, Had: had, N: s.n, Joined: add - fails, JoinFailures: fails,
		FailuresByStage: by, HelloTriesHist: hist, Mode: "burst", Talker: talker, InFlightMax: inFlight, Source: JoinsSourceV2}
	if fails > 0 {
		j.Failed = []JoinFailure{{Bot: had + 1, Stage: "hello", Tries: 5, Error: "no HELLO_ACK after 5 tries"}}
	}
	return j, nil
}

func (w *fakeWorld) Start(ctx context.Context) (System, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.startErr != nil {
		return nil, w.startErr
	}
	w.starts++
	return &fakeSystem{w: w, id: w.starts}, nil
}

// measuredAt reports whether a measured (non-probe, non-preflight) hold ran at
// n already, which makes the next settle at n the confirm step's. w.mu is held.
func (w *fakeWorld) measuredAt(n int) bool {
	for _, h := range w.holds {
		if h.n == n && !strings.HasPrefix(h.label, "probe-") && h.label != "preflight" {
			return true
		}
	}
	return false
}

func (s *fakeSystem) SetN(ctx context.Context, n int) (Joins, error) {
	if s.w.setNErrAt > 0 && n >= s.w.setNErrAt && s.id >= s.w.setNErrFromStart {
		return Joins{}, fmt.Errorf("loadbot: %w", context.DeadlineExceeded)
	}
	s.failedRamp = false
	s.w.mu.Lock()
	s.w.setNs = append(s.w.setNs, n)
	chunk := s.w.chunkJoins
	had := s.n
	s.w.mu.Unlock()
	joinsOf := func(joined int) Joins {
		if !chunk {
			return Joins{}
		}
		hist := make([]int, helloHistLen)
		hist[1] = joined
		return Joins{Schema: JoinsSchema, Requested: n, Had: had, N: had + joined, Joined: joined,
			FailuresByStage: map[string]int{"control": 0, "radio": 0, "voice_dial": 0, "hello": 0}, HelloTriesHist: hist, Source: JoinsSourceV2}
	}
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
	s.w.mu.Lock()
	s.die("ramp")
	s.w.mu.Unlock()
	return joinsOf(n - had), nil
}

func (s *fakeSystem) Hold(ctx context.Context, d time.Duration, label string) (Step, error) {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	s.w.holds = append(s.w.holds, fakeHold{label, s.n, d})
	if s.w.dieAtSettleN > 0 && s.n == s.w.dieAtSettleN && !s.w.dieFired && strings.HasPrefix(label, "probe-") && s.w.measuredAt(s.n) {
		s.w.dieFired = true
		return Step{}, errors.New("loadbot: hold: bot 2 died: rpc error: code = Unavailable desc = error reading from server: connection reset by peer")
	}
	if label == "warmup" && s.w.warmupDiesV2 {
		s.w.warmupDiesV2 = false
		return Step{}, errors.New("loadbot: hold: bot 2 died: rpc error: code = Unavailable desc = error reading from server: connection reset by peer")
	}
	if label == "warmup" && s.w.warmupDiesV3 {
		s.w.warmupDiesV3 = false
		s.lost = append(s.lost, ControlLostEvent{Bot: 2, Name: "LDBT-0002", Cause: "stream", Code: "Unavailable",
			Message: "error reading from server: connection reset by peer", Phase: "hold", DeadTotal: len(s.lost) + 1})
	}
	measured := label != "preflight" && label != "warmup" && !strings.HasPrefix(label, "probe-")
	if strings.HasPrefix(label, "probe-") {
		s.die("settle")
	} else if measured {
		s.die("hold")
		if s.w.gapDeathN == s.n {
			s.w.gapDeathN, s.w.gapArmed, s.w.gapReads = 0, true, 0
		}
	}
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

// TestDriverBudgetStopAfterABreachIsBracketed: whatever budget makes the guard
// stop the sweep after a breach was already measured, the result must be the
// bracket (lo <= knee < hi), never "time-bound" (the knee was bracketed) and
// not a bare knee (three bisections never ran).
func TestDriverBudgetStopAfterABreachIsBracketed(t *testing.T) {
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
		if out.Result.Kind != sweep.Bracketed || out.Result.N < 100 || out.Result.N >= 200 ||
			out.Result.Hi <= out.Result.N || out.Result.Hi > 200 {
			t.Fatalf("budget %s: result = %v, want Bracketed with N in [100, 200) and Hi in (N, 200]", b, out.Result)
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

// S-T1 (red): run 40859021110 legs 2 and 3 crashed at the N=1336 confirm settle
// with this exact text. It must be data: a control_lost row, a rebuild, a retry.
func TestDriverV2HoldDiedIsControlLost(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, dieAtSettleN: 1336}
	cfg := testConfig()
	cfg.Predicted, cfg.NMax = 1336, 1336
	out, err := NewDriver(w, cfg).Run(context.Background())
	if err != nil {
		t.Fatalf("Run returned %v, want nil (a dead bot is a step outcome)", err)
	}
	var confirms []Row
	for _, r := range out.Rows {
		if r.Kind == "confirm" {
			confirms = append(confirms, r)
		}
	}
	if len(confirms) < 2 {
		t.Fatalf("confirm rows = %d, want 2 (one control_lost, one retry)", len(confirms))
	}
	if confirms[0].Outcome != sweep.ControlLost {
		t.Errorf("first confirm outcome = %q, want control_lost", confirms[0].Outcome.String())
	}
	if confirms[1].Outcome != sweep.Pass {
		t.Errorf("second confirm (the retry) outcome = %q, want pass", confirms[1].Outcome.String())
	}
	if len(confirms[0].Reasons) == 0 || !strings.HasPrefix(confirms[0].Reasons[0], "control lost: 1 bot (bot 2 Unavailable: error reading from server: connection reset by peer") {
		t.Errorf("reasons = %v", confirms[0].Reasons)
	}
	if w.starts < 2 {
		t.Errorf("starts = %d, want the system restarted", w.starts)
	}
}

func rowsOfKind(out *Outcome, kind string) []Row {
	var rs []Row
	for _, r := range out.Rows {
		if r.Kind == kind {
			rs = append(rs, r)
		}
	}
	return rs
}

func noteContains(out *Outcome, sub string) bool {
	return strings.Contains(strings.Join(out.Notes, "\n"), sub)
}

// sweepRowsAt returns the sweep rows measured at n bots, in order.
func sweepRowsAt(out *Outcome, n int) []Row {
	var rs []Row
	for _, r := range rowsOfKind(out, "sweep") {
		if r.N == n {
			rs = append(rs, r)
		}
	}
	return rs
}

func oneStepConfig(n int) Config {
	cfg := testConfig()
	cfg.Predicted, cfg.NMax = n, n
	return cfg
}

func TestDriverV3DeathMidHoldIsControlLost(t *testing.T) {
	// The first measured hold at 1336 is the sweep step's.
	w := &fakeWorld{kneeN: 1 << 30, deaths: []*fakeDeath{{n: 1336, phase: "hold", nth: 1}}}
	out, err := NewDriver(w, oneStepConfig(1336)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sw := sweepRowsAt(out, 1336)
	if len(sw) < 2 {
		t.Fatalf("sweep rows = %d, want the lost step and its retry", len(sw))
	}
	first := sw[0]
	if first.Outcome != sweep.ControlLost || !first.Held || first.Step.Step == "" {
		t.Errorf("first sweep row = outcome %v held %v step %q, want control_lost, held, its step kept", first.Outcome, first.Held, first.Step.Step)
	}
	if first.ControlLostPhase != "hold" || first.ControlLostCount != 1 || len(first.ControlLost) != 1 {
		t.Errorf("control lost = %q count %d events %d", first.ControlLostPhase, first.ControlLostCount, len(first.ControlLost))
	}
	if sw[1].Outcome != sweep.Pass {
		t.Errorf("retry outcome = %v, want pass", sw[1].Outcome)
	}
	if w.starts < 2 {
		t.Errorf("starts = %d, want the next step to reboot", w.starts)
	}
	if !noteContains(out, "rebuilt the server and load bot after control lost at 1336 bots") {
		t.Errorf("notes = %v", out.Notes)
	}
}

func TestDriverDeathDuringRampOrSettle(t *testing.T) {
	for _, phase := range []string{"ramp", "settle"} {
		t.Run(phase, func(t *testing.T) {
			w := &fakeWorld{kneeN: 1 << 30, deaths: []*fakeDeath{{n: 1336, phase: phase, nth: 1}}}
			out, err := NewDriver(w, oneStepConfig(1336)).Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			first := sweepRowsAt(out, 1336)[0]
			if first.Outcome != sweep.ControlLost || first.ControlLostPhase != phase {
				t.Fatalf("outcome %v phase %q, want control_lost in %s", first.Outcome, first.ControlLostPhase, phase)
			}
			if first.Held {
				t.Error("a step that lost control before its hold must not be Held")
			}
			for _, h := range w.holds {
				if first.Seq > 0 && h.label == fmt.Sprintf("step-%d", first.Seq) && h.n == 1336 {
					t.Errorf("the measured hold of the lost step ran: %+v", h)
				}
			}
		})
	}
}

func TestDriverControlLostAtConfirmRetriesOnceThenUnconfirmed(t *testing.T) {
	// Occurrence 1 is the sweep step, 2 the confirm, 3 its retry.
	w := &fakeWorld{kneeN: 1 << 30, deaths: []*fakeDeath{{n: 1336, phase: "hold", nth: 2}, {n: 1336, phase: "hold", nth: 3}}}
	out, err := NewDriver(w, oneStepConfig(1336)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cs := rowsOfKind(out, "confirm")
	if len(cs) != 2 || cs[0].Outcome != sweep.ControlLost || cs[1].Outcome != sweep.ControlLost {
		t.Fatalf("confirm rows = %+v, want two control_lost rows (one retry only)", cs)
	}
	if !out.Unconfirmed {
		t.Error("the knee must be unconfirmed")
	}
}

func TestDriverControlLostAtOverloadRecorded(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, deaths: []*fakeDeath{{n: 2672, phase: "hold", nth: 1}}}
	cfg := testConfig()
	cfg.Predicted, cfg.NMax = 668, 1336
	out, err := NewDriver(w, cfg).Run(context.Background())
	if err != nil {
		t.Fatalf("Run = %v, want nil: control loss at the overload is a recorded outcome", err)
	}
	ov := rowsOfKind(out, "overload")
	if len(ov) != 2 || ov[0].Outcome != sweep.ControlLost || ov[1].Outcome != sweep.Pass {
		t.Fatalf("overload rows = %+v, want control_lost then the retry's pass", ov)
	}
	if out.Unconfirmed {
		t.Error("an overload loss must not unconfirm the knee")
	}
}

func TestDriverRampChunks(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, chunkJoins: true}
	cfg := testConfig()
	cfg.RampChunk = 250
	d := NewDriver(w, cfg)
	ctx := context.Background()
	if err := d.boot(ctx); err != nil {
		t.Fatal(err)
	}
	w.setNs = nil
	r, err := d.ensure(ctx, 972)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(w.setNs), "[252 502 752 972]"; got != want {
		t.Fatalf("SetN calls = %s, want %s", got, want)
	}
	if r.Joins.Joined != 970 || r.Joins.N != 972 || r.Joins.Had != 2 || r.FailedChunk != nil {
		t.Errorf("joins = %+v failed chunk %v, want the chunks summed (970 joined)", r.Joins, r.FailedChunk)
	}

	// A failure in the 752 chunk stops the ramp: no 972 call.
	w2 := &fakeWorld{kneeN: 1 << 30, chunkJoins: true, joinFailN: 752, joinsV2: true}
	d2 := NewDriver(w2, cfg)
	if err := d2.boot(ctx); err != nil {
		t.Fatal(err)
	}
	w2.setNs = nil
	r, err = d2.ensure(ctx, 972)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(w2.setNs), "[252 502 752]"; got != want {
		t.Fatalf("SetN calls = %s, want %s", got, want)
	}
	if r.FailedChunk == nil || *r.FailedChunk != (ChunkRange{Had: 502, Requested: 752}) {
		t.Errorf("failed chunk = %v, want {502 752}", r.FailedChunk)
	}
	if r.Joins.JoinFailures != 2 || r.Joins.Joined != 500+248 {
		t.Errorf("joins = %+v, want chunks summed: 748 joined, 2 failed", r.Joins)
	}
	if !d2.rebuild {
		t.Error("a failed chunk must schedule a rebuild")
	}
}

func TestOverBudgetUsesRampModel(t *testing.T) {
	cfg := testConfig()
	cfg.RateMax, cfg.RateScale = 12, 5000
	d := NewDriver(&fakeWorld{}, cfg)
	if got := d.rampSeconds(2, 1336); math.Abs(got-195.6) > 1 {
		t.Errorf("rampSeconds(2, 1336) = %.1f, want about 195.6", got)
	}
	// Below the crossover (416.7 bots) the cap is the constant 12/s.
	if got := d.rampSeconds(2, 202); math.Abs(got-200.0/12) > 0.01 {
		t.Errorf("rampSeconds(2, 202) = %.2f, want %.2f", got, 200.0/12)
	}
	// Without the rule the constant rate applies.
	d2 := NewDriver(&fakeWorld{}, testConfig())
	if got := d2.rampSeconds(0, 270); math.Abs(got-10) > 0.001 {
		t.Errorf("constant-rate rampSeconds(0, 270) = %.2f, want 10", got)
	}
	// The guard uses the model: 195.6 s of ramp does not fit this budget, 27/s would.
	t0 := time.Unix(1_700_000_000, 0)
	cfg.Clock, cfg.Budget = fixedClock{t0}, 400*time.Second
	d3 := NewDriver(&fakeWorld{}, cfg)
	d3.start = t0
	cfg.RateMax, cfg.RateScale = 0, 0
	d4 := NewDriver(&fakeWorld{}, cfg)
	d4.start = t0
	m, c := d3.overBudget(1336, 100), d4.overBudget(1336, 100)
	if !m || c {
		t.Errorf("model guard = %v, constant guard = %v, want true and false", m, c)
	}
}

func burstConfig() Config {
	cfg := oneStepConfig(1336)
	cfg.BurstEnabled, cfg.BurstAdd, cfg.BurstInFlight = true, 150, 16
	return cfg
}

func TestDriverBurstPhase(t *testing.T) {
	w := &fakeWorld{kneeN: 1336}
	out, err := NewDriver(w, burstConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(w.bursts) != 2 {
		t.Fatalf("bursts = %+v, want silent then live", w.bursts)
	}
	b0, b1 := w.bursts[0], w.bursts[1]
	if b0.talker != "silent" || b1.talker != "live" || b0.add != 150 || b0.inFlight != 16 || b0.joinedBefore != 518 || b1.joinedBefore != 668 {
		t.Errorf("bursts = %+v, want +150 at 16 in flight from 518 (silent) then 668 (live)", w.bursts)
	}
	bs := rowsOfKind(out, "burst")
	if len(bs) != 2 || bs[0].BurstTalker != "silent" || bs[1].BurstTalker != "live" || bs[0].BurstResult != "ok" || bs[1].BurstResult != "ok" {
		t.Fatalf("burst rows = %+v", bs)
	}
	if out.Result.N != 1336 {
		t.Errorf("result = %v, want the sweep result unchanged", out.Result)
	}
	if len(rowsOfKind(out, "sweep")) != 3 {
		t.Errorf("sweep rows = %d, bursts must not add sweep rows", len(rowsOfKind(out, "sweep")))
	}
	cs := rowsOfKind(out, "confirm")
	if len(cs) != 1 || cs[0].Outcome != sweep.Pass {
		t.Errorf("confirm rows = %+v, want one pass after the bursts", cs)
	}
	var kinds []string
	for _, r := range out.Rows {
		kinds = append(kinds, r.Kind)
	}
	if got, want := strings.Join(kinds, ","), "sweep,sweep,sweep,burst,burst,confirm,overload"; got != want {
		t.Errorf("row kinds = %s, want %s", got, want)
	}
}

func TestDriverBurstFailuresNeverBreachTheSweep(t *testing.T) {
	w := &fakeWorld{kneeN: 1336, burstFailLive: 30}
	out, err := NewDriver(w, burstConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bs := rowsOfKind(out, "burst")
	if len(bs) != 2 || bs[1].BurstResult != "join failures" || bs[1].Joins.JoinFailures != 30 {
		t.Fatalf("burst rows = %+v, want the live burst to record 30 join failures", bs)
	}
	if bs[1].Outcome != sweep.Pass || len(bs[1].Reasons) == 0 || !strings.HasPrefix(bs[1].Reasons[0], "burst failures: ") {
		t.Errorf("live burst row = outcome %v reasons %v", bs[1].Outcome, bs[1].Reasons)
	}
	if out.Result.N != 1336 || len(rowsOfKind(out, "sweep")) != 3 || out.Unconfirmed {
		t.Errorf("result = %v unconfirmed %v, want the sweep and confirm untouched", out.Result, out.Unconfirmed)
	}
}

func TestDriverBurstSkippedForV2(t *testing.T) {
	w := &fakeWorld{kneeN: 1336, noBurst: true}
	out, err := NewDriver(w, burstConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rowsOfKind(out, "burst")) != 0 {
		t.Error("a v2 bot must produce no burst rows")
	}
	if !noteContains(out, "join burst not measured: the load bot lacks the burst command (v2)") {
		t.Errorf("notes = %v", out.Notes)
	}
	if w.starts != 1 {
		t.Errorf("starts = %d, a skipped burst phase must not rebuild", w.starts)
	}
}

func TestDriverBurstNeedsRebuildWhenAbove(t *testing.T) {
	// After the sweep step 1336 bots are joined, above the burst base 518.
	w := &fakeWorld{kneeN: 1336}
	out, err := NewDriver(w, burstConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !noteContains(out, "rebuilt the server and load bot for the join burst: 1336 bots joined, burst base 518") {
		t.Errorf("notes = %v", out.Notes)
	}
	if w.starts < 2 {
		t.Errorf("starts = %d, want a reboot for the burst", w.starts)
	}
}

// A bot that dies after one step's final check and before the next step reads
// its baseline must be attributed to the next step, not folded into the
// baseline.
func TestDriverDeathInStepGapIsNotFoldedIntoBaseline(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, gapDeathN: 1336}
	out, err := NewDriver(w, oneStepConfig(1336)).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var lost int
	for _, r := range out.Rows {
		if r.Outcome == sweep.ControlLost {
			lost++
		}
	}
	if lost == 0 {
		t.Fatalf("a death in the gap between steps was never reported: rows %d", len(out.Rows))
	}
}

// A bot that dies during the discarded warm-up is not a leg error and is not
// any sweep step's death: the system is rebuilt before the first step.
func TestDriverWarmupControlLostRebuildsNotFatal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		v2, v3 bool
	}{{"v2 hold death", true, false}, {"v3 control_lost event", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			w := &fakeWorld{kneeN: 700, warmupDiesV2: tc.v2, warmupDiesV3: tc.v3}
			out, err := NewDriver(w, testConfig()).Run(context.Background())
			if err != nil {
				t.Fatalf("Run returned %v, want nil (a warm-up death is not a leg error)", err)
			}
			if !noteContains(out, "warm-up lost control") {
				t.Errorf("notes = %q, want a warm-up control-loss note", out.Notes)
			}
			for _, r := range out.Rows {
				if r.Outcome == sweep.ControlLost {
					t.Errorf("%s row at %d is control_lost; the warm-up death must not be charged to a step", r.Kind, r.N)
				}
			}
			if out.Starts < 2 {
				t.Errorf("starts = %d, want a rebuild after the warm-up death", out.Starts)
			}
			if out.Result.Kind != sweep.KneeFound || out.Result.N == 0 {
				t.Errorf("result = %s, want a knee", out.Result)
			}
		})
	}
}

// When the sweep's last step already left the system flagged for a rebuild
// (join failures or a control loss), the burst phase must boot once, not close
// and boot a system it then throws away again in ensure.
func TestDriverBurstPhaseBootsOnceWhenRebuildPending(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30}
	d := NewDriver(w, burstConfig())
	d.start = d.cfg.Clock.Now()
	ctx := context.Background()
	if err := d.boot(ctx); err != nil {
		t.Fatal(err)
	}
	defer d.closeSys()
	if _, err := d.ensure(ctx, 1336); err != nil {
		t.Fatal(err)
	}
	d.rebuild, d.rebuildAt, d.rebuildWhy = true, 1336, ""
	before := w.starts
	if err := d.burstPhase(ctx, 1336); err != nil {
		t.Fatal(err)
	}
	if got := w.starts - before; got != 1 {
		t.Errorf("burst phase booted %d systems, want 1 (notes %q)", got, d.out.Notes)
	}
	if len(w.bursts) != 2 {
		t.Errorf("bursts = %+v, want silent then live", w.bursts)
	}
}

// The "ramp done" line counts the bots the failing chunk did join too, not only
// the chunks before it.
func TestDriverRampDoneLineCountsFailingChunkJoins(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, chunkJoins: true, joinFailN: 752, joinsV2: true}
	cfg := testConfig()
	cfg.RampChunk = 250
	var lines []string
	cfg.Logf = func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	d := NewDriver(w, cfg)
	ctx := context.Background()
	if err := d.boot(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ensure(ctx, 972); err != nil {
		t.Fatal(err)
	}
	// 250 + 250 joined, then the 502->752 chunk joined 248 and failed 2.
	want := "ramp N=2->972 done in"
	for _, l := range lines {
		if strings.HasPrefix(l, want) {
			if !strings.HasSuffix(l, "joined=748 join_failures=2") {
				t.Errorf("ramp done line = %q, want joined=748 join_failures=2", l)
			}
			return
		}
	}
	t.Fatalf("no ramp done line in %q", lines)
}

// --- v4: the bot's own latency decides the late-loss and one-way breaches ---

// withV4Latency makes the fake bot report a v4 step with the given latency.
func withV4Latency(oneWayP99Us int64, lateRatio float64) func(n int, label string, st *Step) {
	return func(n int, label string, st *Step) {
		if label == "preflight" || label == "warmup" || strings.HasPrefix(label, "probe-") {
			return
		}
		st.Schema = "loadbot-step/4"
		st.Payload = "opus-speech-corpus"
		st.OneWayP99Us = &oneWayP99Us
		st.LateLossRatio = &lateRatio
	}
}

func TestMeasureUsesBotLatencyOneWay(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, stepHook: withV4Latency(60000, 0)}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := sweepRowAtOrAbove(out, 1)
	if r == nil {
		t.Fatal("no sweep row")
	}
	if r.Outcome != sweep.Breach || !strings.Contains(strings.Join(r.Reasons, ";"), "one-way p99 60ms above 50ms") {
		t.Fatalf("row = %+v, want a one-way breach naming 60ms above 50ms", r)
	}
	if r.Data.OneWayP99 != 60*time.Millisecond {
		t.Fatalf("Data.OneWayP99 = %v", r.Data.OneWayP99)
	}
}

func TestMeasureUsesBotLatencyLateLoss(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30, stepHook: withV4Latency(900, 0.02)}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := sweepRowAtOrAbove(out, 1)
	if r == nil || r.Outcome != sweep.Breach || !strings.Contains(strings.Join(r.Reasons, ";"), "late loss 2.00%") {
		t.Fatalf("row = %+v, want a late-loss breach", r)
	}
}

func TestMeasureWithV3StepKeepsLatencyUnmeasured(t *testing.T) {
	w := &fakeWorld{kneeN: 1 << 30}
	out, err := NewDriver(w, testConfig()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := sweepRowAtOrAbove(out, 1)
	if r == nil || !r.Held {
		t.Fatalf("row = %+v", r)
	}
	if r.Data.LateLossRatio != 0 || r.Data.OneWayP99 != 0 {
		t.Fatalf("unmeasured values must stay zero: %+v", r.Data)
	}
	if notMeasured(r.Step) != "late_loss;one_way_p99" {
		t.Fatalf("not_measured = %q", notMeasured(r.Step))
	}
}
