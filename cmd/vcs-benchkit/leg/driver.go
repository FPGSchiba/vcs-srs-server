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
	// A join failure is data (Joins.JoinFailures > 0), not an error.
	SetN(ctx context.Context, n int) (Joins, error)
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
	// SoftnetDropped is the increase of the summed softnet_stat dropped column
	// (per-CPU backlog full), UDPRcvbufErrors and UDPSndbufErrors the increase
	// of the host's snmp Udp: RcvbufErrors and SndbufErrors. They are only
	// meaningful when NetOK; otherwise NetErr says why they were not read.
	// A hold window folds NetErr into Err; a ramp window just shows "-".
	SoftnetDropped, UDPRcvbufErrors, UDPSndbufErrors uint64
	NetOK                                            bool
	NetErr                                           string
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

// lastJoiner is the optional System method that returns the last `joins` line
// the bot wrote (see Bot.LastJoins).
type lastJoiner interface {
	LastJoins() (Joins, bool)
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

	// OnRow is called with each row right after it is appended to the outcome;
	// Logf receives the driver's own progress lines (a ramp starting and
	// ending). Both are optional and only for the operator's log.
	OnRow func(Row)
	Logf  func(format string, a ...any)
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
	// Joins is the load bot's account of this step's ramp: how many bots it
	// tried to add, how many failed to join and at which stage. The zero value
	// means the ramp added nothing or the bot is v1 and reported no failure.
	Joins Joins
	// RampWindow is what the kit's samplers measured over the ramp (zero when
	// the step did not ramp). RampLogs is the server's log delta over it.
	RampWindow Window
	RampLogs   LogCounts
	// FailedAckedByServer is how many of the failed bots' GUIDs the server
	// logged an "Accepted voice HELLO" for during the ramp: the server saw the
	// HELLO that the bot never saw answered. -1 = unknown (no GUIDs reported).
	FailedAckedByServer int
}

// Outcome is everything one arm produced.
type Outcome struct {
	Rows   []Row
	Result sweep.Result
	// Unconfirmed is set when the confirm step at the found knee did not pass:
	// the knee is then the sweep's finding only.
	Unconfirmed bool
	Starts      int
	Notes       []string
}

// Driver runs the protocol for one arm.
type Driver struct {
	env Env
	cfg Config

	sys    System
	joined int
	// rebuild is set after a join failure: the server and bot are no longer
	// trusted, so the next step boots fresh. rebuildAt is the failing N.
	rebuild   bool
	rebuildAt int
	rate      float64
	start     time.Time
	out       Outcome
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

func (d *Driver) logf(f string, a ...any) {
	if d.cfg.Logf != nil {
		d.cfg.Logf(f, a...)
	}
}

// record appends a finished row and reports it.
func (d *Driver) record(r Row) {
	d.out.Rows = append(d.out.Rows, r)
	if d.cfg.OnRow != nil {
		d.cfg.OnRow(r)
	}
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
	if j, err := sys.SetN(ctx, 2); err != nil {
		return fmt.Errorf("pre-flight: %w", err)
	} else if j.JoinFailures > 0 {
		return fmt.Errorf("pre-flight: join failures: %s", joinFailureSummary(j))
	}
	// The first hold absorbs the ramp and the first HELLOs.
	hctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	st, err := sys.Hold(hctx, 2*time.Second, "preflight")
	if err != nil {
		return fmt.Errorf("pre-flight: %w", err)
	}
	// The pre-flight SetN was fire-and-forget, so a v2 bot's joins line for it
	// arrived during that hold: report its failures in the bot's own words.
	if lj, ok := sys.(lastJoiner); ok {
		if j, ok := lj.LastJoins(); ok && j.JoinFailures > 0 {
			return fmt.Errorf("pre-flight: join failures: %s", joinFailureSummary(j))
		}
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

// ramp is what one ensure call measured around its SetN.
type ramp struct {
	Joins  Joins
	Window Window
	Logs   LogCounts
	// FailedAcked is Row.FailedAckedByServer for this ramp.
	FailedAcked int
	// Ran is false when no SetN was needed.
	Ran bool
}

// joinFailureSummary is the one line a fatal join failure and a log line carry:
// the counts and the first failure in the bot's own words.
func joinFailureSummary(j Joins) string {
	s := fmt.Sprintf("%d of %d new bots failed", j.JoinFailures, max(0, j.Requested-j.Had))
	if len(j.Failed) > 0 {
		f := j.Failed[0]
		s += fmt.Sprintf(": bot %d %s: %s", f.Bot, f.Stage, f.Error)
	}
	return s
}

// ensure makes the bot count exactly n. Bots only grow, so a smaller n needs
// a fresh server and load bot (which also gives the step a server that was not
// just driven past its knee), and so does the step after a join failure. A join
// failure is returned in ramp.Joins, not as an error: the caller decides.
func (d *Driver) ensure(ctx context.Context, n int) (ramp, error) {
	var r ramp
	switch {
	case d.rebuild:
		d.note("rebuilt the server and load bot after join failures at %d bots", d.rebuildAt)
		d.rebuild = false
		d.closeSys()
		if err := d.boot(ctx); err != nil {
			return r, err
		}
	case n < d.joined:
		d.note("rebuilt the server and load bot to go from %d down to %d bots", d.joined, n)
		d.closeSys()
		if err := d.boot(ctx); err != nil {
			return r, err
		}
	}
	if n == d.joined {
		return r, nil
	}
	t0 := d.cfg.Clock.Now()
	add := n - d.joined
	before, errBefore := d.sys.LogText()
	end := d.sys.BeginSample()
	d.logf("ramp N=%d->%d started", d.joined, n)
	j, err := d.sys.SetN(ctx, n)
	win := end()
	if err != nil {
		return r, fmt.Errorf("set_n %d: %w", n, err)
	}
	if j.Source != JoinsSourcePending {
		d.logf("ramp N=%d->%d done in %.0fs: joined=%d join_failures=%d", d.joined, n,
			d.cfg.Clock.Now().Sub(t0).Seconds(), max(0, n-d.joined-j.JoinFailures), j.JoinFailures)
	}
	after, errAfter := d.sys.LogText()
	r = ramp{Joins: j, Window: win, Ran: true, FailedAcked: -1}
	// A failed read must not turn the server's whole log into the ramp's
	// delta: the counts stay zero (unknown) and the note says why.
	logsOK := errBefore == nil && errAfter == nil
	if logsOK {
		r.Logs = CountLog(after).Sub(CountLog(before))
	} else {
		d.note("ramp log unreadable at %d bots (%v): ramp log counts left empty", n, errors.Join(errBefore, errAfter))
	}
	if j.JoinFailures > 0 {
		if logsOK {
			r.FailedAcked = FailedAcked(before, after, j.Failed)
		}
		d.rebuild, d.rebuildAt = true, n
		return r, nil
	}
	d.joined = n
	// Learn the real join rate once the ramp is long enough to trust, so the
	// time guard follows this VM and not the design's estimate.
	if el := d.cfg.Clock.Now().Sub(t0).Seconds(); add >= 50 && el > 0 {
		d.rate = float64(add) / el
	}
	return r, nil
}

// settle probes with short holds until the step is quiescent (design 6.4.3).
// The bot has no live counters, so each probe hold's report supplies them:
// total update-stream receives across its bots, and the resyncs seen during the
// probe. Both are stricter than the design's "sampled full-store bots".
//
// The bot counts a probe as a hold phase, so a probe's ResourceExhausted counts
// are returned for the caller to fold into the step's ramp and settle columns
// (they are ramp artefacts and never part of the measured hold).
func (d *Driver) settle(ctx context.Context, rampEnd time.Time) (ok bool, reason string, rex PhaseCounts, err error) {
	var samples []sweep.SettleSample
	for i := 0; ; i++ {
		st, err := d.sys.Hold(ctx, d.cfg.ProbeHold, fmt.Sprintf("probe-%d", i))
		if err != nil {
			return false, "", rex, err
		}
		rex.Ramp += st.ResourceExhausted.Ramp
		rex.Settle += st.ResourceExhausted.Settle + st.ResourceExhausted.Hold
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
			return true, "", rex, nil
		}
		if strings.HasPrefix(reason, sweep.InvalidCannotSettle) {
			return false, reason, rex, nil
		}
	}
}

// measure runs one step at n bots: ramp, settle, then the measured hold.
func (d *Driver) measure(ctx context.Context, kind string, n int, hold time.Duration, label string) (Row, error) {
	row := Row{Kind: kind, Seq: len(d.out.Rows) + 1, N: n, FailedAckedByServer: -1}
	rp, err := d.ensure(ctx, n)
	if err != nil {
		return row, err
	}
	row.Joins, row.RampWindow, row.RampLogs, row.FailedAckedByServer = rp.Joins, rp.Window, rp.Logs, rp.FailedAcked
	if rp.Joins.JoinFailures > 0 {
		// The ramp is this step's outcome: no settle, no hold. A join failure
		// is a server breach unless the generator itself was the limit.
		nan := math.NaN()
		by := map[string]uint64{}
		for k, v := range rp.Joins.FailuresByStage {
			by[k] = uint64(v)
		}
		row.Data = sweep.StepData{
			N: n, LossRatio: nan, LateLossRatio: nan, ServerCPU: nan, GenCPU: nan,
			JoinFailures: uint64(rp.Joins.JoinFailures), JoinAttempted: max(0, rp.Joins.Requested-rp.Joins.Had),
			JoinStages: by, HelloTriesMax: rp.Joins.HelloTriesMax, RampGenCPU: rp.Window.GenCPU,
		}
		row.Outcome, row.Reasons = sweep.Classify(row.Data, d.cfg.Bounds)
		return row, nil
	}
	rampEnd := d.cfg.Clock.Now()
	ok, reason, probeRex, err := d.settle(ctx, rampEnd)
	if err != nil {
		return row, fmt.Errorf("settle at %d bots: %w", n, err)
	}
	row.SettleSeconds = d.cfg.Clock.Now().Sub(rampEnd).Seconds()
	nan := math.NaN()
	if !ok {
		row.Step.ResourceExhausted = probeRex
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
	if win.NetErr != "" {
		// A hold must not report a counter it never read (a ramp may).
		win.Err = strings.TrimPrefix(win.Err+"; "+win.NetErr, "; ")
	}
	if herr != nil {
		return row, fmt.Errorf("hold at %d bots: %w", n, herr)
	}
	after, err := d.sys.LogText()
	if err != nil {
		return row, fmt.Errorf("read server log: %w", err)
	}
	st.ResourceExhausted.Ramp += probeRex.Ramp
	st.ResourceExhausted.Settle += probeRex.Settle
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
	if d.rebuild {
		// A join failure killed the system: the step boots fresh at 2 bots
		// and ramps from there, whatever d.joined last was.
		cost += secs(max(0, n-2)) + 30*time.Second
	} else if n < d.joined {
		cost += secs(n) + 30*time.Second
	} else {
		cost += secs(n - d.joined)
	}
	// The confirm may rebuild and ramp to the knee, and the overload then
	// ramps another knee's worth of bots: two ramps and two settles.
	reserve := d.cfg.Confirm + d.cfg.Overload + 2*secs(lo) + 2*sweep.SettleTypical + 30*time.Second
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
	if rp, err := d.ensure(ctx, warmN); err != nil {
		return &d.out, err
	} else if rp.Joins.JoinFailures > 0 {
		return &d.out, fmt.Errorf("warm-up: join failures at %d bots: %s", warmN, joinFailureSummary(rp.Joins))
	}
	if _, err := d.sys.Hold(ctx, d.cfg.Warmup, "warmup"); err != nil {
		return &d.out, fmt.Errorf("warm-up: %w", err)
	}

	sw := sweep.NewSweep(d.cfg.Predicted, d.cfg.NMax)
	lo := 0
	for {
		n, more := sw.Next()
		if !more {
			break
		}
		if d.overBudget(n, lo) {
			d.note("budget guard: stopped before the %d-bot step so the confirm and overload still fit", n)
			if blo, bhi := sw.Bracket(); bhi > 0 {
				d.note("the bisection was cut short: the knee lies in [%d, %d)", blo, bhi)
			}
			sw.Stop()
			break
		}
		row, err := d.measure(ctx, "sweep", n, d.cfg.Hold, fmt.Sprintf("step-%d", len(d.out.Rows)+1))
		if err != nil {
			return &d.out, err
		}
		d.record(row)
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
	d.out.Result = sw.Result()

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
	d.record(row)
	if row.Outcome != sweep.Pass {
		d.out.Unconfirmed = true
		d.note("the confirm step at %d bots was %s, so the knee is unconfirmed: %s", k, row.Outcome, strings.Join(row.Reasons, "; "))
	}
	ov := min(2*k, 2*d.cfg.NMax)
	if ov <= k {
		return &d.out, nil
	}
	row, err = d.measure(ctx, "overload", ov, d.cfg.Overload, "overload")
	if err != nil {
		return &d.out, err
	}
	d.record(row)
	return &d.out, nil
}
