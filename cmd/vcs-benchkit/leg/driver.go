package leg

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
)

// Generator is the load bot as the driver sees it.
type Generator interface {
	// SetN grows the bot count; it returns once the ramp has finished (after
	// the first hold; before it the first Hold absorbs the ramp).
	SetN(ctx context.Context, n int) error
	// Hold runs one hold and returns its report.
	Hold(ctx context.Context, d time.Duration, label string) (Step, error)
}

// Window is what the kit's own samplers measured over one hold: per-process
// CPU from /proc/<pid>/stat, per-CPU softirq and steal from /proc/stat, and
// the server socket's drops from ss -uamp.
type Window struct {
	// ServerCPU and GenCPU are fractions of the process's CPU set; NaN means
	// the sampler failed (which invalidates the step).
	ServerCPU, GenCPU float64
	// ServerDrops is the increase of the server socket's drop counter.
	ServerDrops uint64
	// SoftIRQTicks is the softirq clock ticks on the server's CPU set; StealTicks
	// is steal over all CPUs. Reported next to the CPU breach, never part of it
	// (design 6.3).
	SoftIRQTicks, StealTicks uint64
	// Seconds is the sampled wall time.
	Seconds float64
	// Err is set when a sampler failed (a missing /proc file, ss not
	// installed, the server socket absent from ss). The step is then invalid:
	// a zero drop count that was never read is not a measurement.
	Err string
}

// System is one running server plus its load bot.
type System interface {
	Generator
	// LogText returns the server's log read from its file.
	LogText() (string, error)
	// BeginSample starts the 1 s samplers; the returned func stops them and
	// returns the window.
	BeginSample() func() Window
	// Close stops the bot, then the server, and reaps both.
	Close() error
}

// Env starts a fresh system: a new server topology and a new load bot with
// no bots joined.
type Env interface {
	Start(ctx context.Context) (System, error)
}

// Clock is injectable so the settle rules are testable without sleeping.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Config is the protocol of one arm (design 6.4).
type Config struct {
	Predicted int     // B-0 predicted knee
	NMax      int     // time/port cap on a sweep step
	JoinRate  float64 // joins per second, for the time guard
	Budget    time.Duration

	Warmup, Hold, Confirm, Overload, ProbeHold time.Duration

	Bounds sweep.Bounds
	Clock  Clock
}

// Row is one measured step.
type Row struct {
	Kind    string // sweep | confirm | overload
	Seq     int
	N       int
	Outcome sweep.StepOutcome
	Reasons []string
	// Held is false when the step never reached its measured hold.
	Held   bool
	Step   Step
	Window Window
	Logs   LogCounts
	Data   sweep.StepData
	// SettleSeconds is the time from the end of the ramp to the measured hold.
	SettleSeconds float64
}

// Outcome is everything one arm produced.
type Outcome struct {
	Rows   []Row
	Result sweep.Result
	Starts int
	Notes  []string
}

// Driver runs the protocol for one arm.
type Driver struct {
	env Env
	cfg Config

	sys    System
	joined int
	rate   float64
	start  time.Time
	out    Outcome
}

// NewDriver fills the defaults a zero Config leaves open.
func NewDriver(env Env, cfg Config) *Driver {
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.JoinRate <= 0 {
		cfg.JoinRate = 27
	}
	if cfg.ProbeHold <= 0 {
		cfg.ProbeHold = time.Second
	}
	if cfg.Bounds == (sweep.Bounds{}) {
		cfg.Bounds = sweep.DefaultBounds
	}
	return &Driver{env: env, cfg: cfg, rate: cfg.JoinRate}
}

func (d *Driver) note(f string, a ...any) { d.out.Notes = append(d.out.Notes, fmt.Sprintf(f, a...)) }

// boot starts a fresh system and runs the pre-flight login: two bots, one
// short hold, and at least one frame must arrive. That proves the shards run
// (design 3.3, standalone step 4) before any measurement.
func (d *Driver) boot(ctx context.Context) error {
	sys, err := d.env.Start(ctx)
	if err != nil {
		return err
	}
	d.sys = sys
	d.out.Starts++
	d.joined = 0
	if err := sys.SetN(ctx, 2); err != nil {
		return fmt.Errorf("pre-flight: %w", err)
	}
	// The first hold absorbs the ramp and the first HELLOs.
	hctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	st, err := sys.Hold(hctx, 2*time.Second, "preflight")
	if err != nil {
		return fmt.Errorf("pre-flight: %w", err)
	}
	if st.Unique == 0 {
		return errors.New("pre-flight: login succeeded but no voice frame reached the listener (the shards are not relaying)")
	}
	d.joined = 2
	return nil
}

func (d *Driver) closeSys() {
	if d.sys != nil {
		_ = d.sys.Close()
		d.sys = nil
	}
}

// ensure makes the bot count exactly n. Bots only grow, so a smaller n needs
// a fresh server and load bot (which also gives the step a server that was not
// just driven past its knee).
func (d *Driver) ensure(ctx context.Context, n int) error {
	if n < d.joined {
		d.note("rebuilt the server and load bot to go from %d down to %d bots", d.joined, n)
		d.closeSys()
		if err := d.boot(ctx); err != nil {
			return err
		}
	}
	if n == d.joined {
		return nil
	}
	t0 := d.cfg.Clock.Now()
	add := n - d.joined
	if err := d.sys.SetN(ctx, n); err != nil {
		return fmt.Errorf("set_n %d: %w", n, err)
	}
	d.joined = n
	// Learn the real join rate once the ramp is long enough to trust, so the
	// time guard follows this VM and not the design's estimate.
	if el := d.cfg.Clock.Now().Sub(t0).Seconds(); add >= 50 && el > 0 {
		d.rate = float64(add) / el
	}
	return nil
}

// settle probes with short holds until the step is quiescent (design 6.4.3).
// The bot has no live counters, so each probe hold's report supplies them:
// total update-stream receives across its bots, and the resyncs seen during the
// probe. Both are stricter than the design's "sampled full-store bots".
func (d *Driver) settle(ctx context.Context, rampEnd time.Time) (ok bool, reason string, err error) {
	var samples []sweep.SettleSample
	for i := 0; ; i++ {
		st, err := d.sys.Hold(ctx, d.cfg.ProbeHold, fmt.Sprintf("probe-%d", i))
		if err != nil {
			return false, "", err
		}
		var total uint64
		for _, v := range st.Updates {
			total += v
		}
		now := d.cfg.Clock.Now()
		samples = append(samples, sweep.SettleSample{
			At: now, RampEnd: rampEnd,
			ResyncsInFlight: int(st.Resyncs.Hold),
			UpdatesRx:       []uint64{total},
		})
		ok, reason := sweep.Settled(samples, now)
		if ok {
			return true, "", nil
		}
		if strings.HasPrefix(reason, sweep.InvalidCannotSettle) {
			return false, reason, nil
		}
	}
}

// measure runs one step at n bots: ramp, settle, then the measured hold.
func (d *Driver) measure(ctx context.Context, kind string, n int, hold time.Duration, label string) (Row, error) {
	row := Row{Kind: kind, Seq: len(d.out.Rows) + 1, N: n}
	if err := d.ensure(ctx, n); err != nil {
		return row, err
	}
	rampEnd := d.cfg.Clock.Now()
	ok, reason, err := d.settle(ctx, rampEnd)
	if err != nil {
		return row, fmt.Errorf("settle at %d bots: %w", n, err)
	}
	row.SettleSeconds = d.cfg.Clock.Now().Sub(rampEnd).Seconds()
	nan := math.NaN()
	if !ok {
		row.Data = sweep.StepData{N: n, LossRatio: nan, LateLossRatio: nan, ServerCPU: nan, GenCPU: nan, CannotSettle: true}
		row.Outcome, row.Reasons = sweep.Classify(row.Data, d.cfg.Bounds)
		row.Reasons = append([]string{reason}, row.Reasons...)
		return row, nil
	}

	before, err := d.sys.LogText()
	if err != nil {
		return row, fmt.Errorf("read server log: %w", err)
	}
	end := d.sys.BeginSample()
	st, herr := d.sys.Hold(ctx, hold, label)
	win := end()
	if herr != nil {
		return row, fmt.Errorf("hold at %d bots: %w", n, herr)
	}
	after, err := d.sys.LogText()
	if err != nil {
		return row, fmt.Errorf("read server log: %w", err)
	}
	row.Held, row.Step, row.Window = true, st, win
	row.Logs = CountLog(after).Sub(CountLog(before))
	row.Data = sweep.StepData{
		N: n, LossRatio: st.LossRatio,
		// The C1 load bot measures loss only: no late-loss and no one-way
		// latency (those arrive with C2). They are recorded as zero here and
		// named in the not_measured column, so a step is never invalidated by
		// a figure the bot cannot give, and never credited with one.
		LateLossRatio: 0, OneWayP99: 0,
		ResourceExhausted: sweep.PhaseCounts{Ramp: st.ResourceExhausted.Ramp, Settle: st.ResourceExhausted.Settle, Hold: st.ResourceExhausted.Hold},
		ServerCPU:         win.ServerCPU, ServerSocketDrops: win.ServerDrops,
		ShardQueueFull: row.Logs.ShardQueueFull, UpdateStreamBehind: row.Logs.UpdateStreamBehind,
		NodeFeedBehind: row.Logs.NodeFeedBehind, NodeDeregistered: row.Logs.NodeDeregistered,
		GeneratorViolations: st.Violations, GenCPU: win.GenCPU,
	}
	row.Outcome, row.Reasons = sweep.Classify(row.Data, d.cfg.Bounds)
	if win.Err != "" {
		row.Outcome = sweep.Invalid
		row.Reasons = append([]string{"invalid: sampler: " + win.Err}, row.Reasons...)
	}
	return row, nil
}

// overBudget reports whether running a step at n would leave too little of the
// budget for the confirm and overload that follow (and the rebuild the confirm
// may need). Budget 0 is unlimited.
func (d *Driver) overBudget(n, lo int) bool {
	if d.cfg.Budget <= 0 {
		return false
	}
	rate := d.rate
	secs := func(bots int) time.Duration { return time.Duration(float64(bots) / rate * float64(time.Second)) }
	cost := sweep.SettleTypical + sweep.HoldDuration
	if n < d.joined {
		cost += secs(n) + 30*time.Second
	} else {
		cost += secs(n - d.joined)
	}
	reserve := d.cfg.Confirm + d.cfg.Overload + secs(lo) + 30*time.Second
	return d.cfg.Clock.Now().Sub(d.start)+cost+reserve > d.cfg.Budget
}

// Run executes the arm: boot, warm-up, sweep, confirm, overload.
func (d *Driver) Run(ctx context.Context) (*Outcome, error) {
	d.start = d.cfg.Clock.Now()
	defer d.closeSys()
	if err := d.boot(ctx); err != nil {
		return &d.out, err
	}

	// Warm-up: ~10 % of the predicted knee, discarded.
	warmN := max(2, d.cfg.Predicted/10)
	if err := d.ensure(ctx, warmN); err != nil {
		return &d.out, err
	}
	if _, err := d.sys.Hold(ctx, d.cfg.Warmup, "warmup"); err != nil {
		return &d.out, fmt.Errorf("warm-up: %w", err)
	}

	sw := sweep.NewSweep(d.cfg.Predicted, d.cfg.NMax)
	lo := 0
	budgetStop := false
	for {
		n, more := sw.Next()
		if !more {
			break
		}
		if d.overBudget(n, lo) {
			d.note("budget guard: stopped before the %d-bot step so the confirm and overload still fit", n)
			budgetStop = true
			break
		}
		row, err := d.measure(ctx, "sweep", n, d.cfg.Hold, fmt.Sprintf("step-%d", len(d.out.Rows)+1))
		if err != nil {
			return &d.out, err
		}
		d.out.Rows = append(d.out.Rows, row)
		if row.Outcome == sweep.Pass && n > lo {
			lo = n
		}
		if err := sw.Record(row.Outcome); err != nil {
			return &d.out, err
		}
	}
	for _, r := range d.out.Rows {
		if r.Outcome == sweep.Invalid {
			d.note("%s step at %d bots was invalid, not a breach: %s", r.Kind, r.N, strings.Join(r.Reasons, "; "))
		}
	}
	if budgetStop {
		d.out.Result = sweep.Result{Kind: sweep.TimeBound, N: lo}
	} else {
		d.out.Result = sw.Result()
	}

	// Confirm at the found knee, then one overload step at 2x.
	k := d.out.Result.N
	if k <= 0 || d.out.Result.Kind == sweep.BelowRange {
		d.note("no passing step, so no confirm or overload")
		return &d.out, nil
	}
	row, err := d.measure(ctx, "confirm", k, d.cfg.Confirm, "confirm")
	if err != nil {
		return &d.out, err
	}
	d.out.Rows = append(d.out.Rows, row)
	ov := min(2*k, 2*d.cfg.NMax)
	if ov <= k {
		return &d.out, nil
	}
	row, err = d.measure(ctx, "overload", ov, d.cfg.Overload, "overload")
	if err != nil {
		return &d.out, err
	}
	d.out.Rows = append(d.out.Rows, row)
	return &d.out, nil
}
