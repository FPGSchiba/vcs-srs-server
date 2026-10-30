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
	// ServerCPUByRole is each server process's CPU fraction of its own set,
	// keyed by role (W6 S5). nil for a single-process (S) arm until the
	// multi-process sampler fills it.
	ServerCPUByRole map[string]float64
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
	// The kernel TCP abort, timeout and reset counters, and softnet's
	// time_squeeze, over the window (W6 fix 2): they classify the KIND of abort
	// (data, close, memory, timeout, linger). They are host-wide and the bots
	// share the host with the server, so they do not say which side sent a reset;
	// that attribution comes from the grpc transport logs. They are
	// informational and are NEVER classified.
	// Only meaningful when TCPOK; an unread file is TCPErr, shown as unread and
	// never folded into Err (it must not invalidate a step). TCPMissing lists
	// the TcpExt keys this kernel does not print (read as 0).
	TCPAbortOnData, TCPAbortOnClose, TCPAbortOnMemory, TCPAbortOnTimeout, TCPAbortOnLinger, TCPTimeouts uint64
	EstabResets, OutRsts                                                                                uint64
	TCPOK                                                                                               bool
	TCPErr                                                                                              string
	TCPMissing                                                                                          []string
	SoftnetSqueezed                                                                                     uint64
	// SoftnetCPUs is the per-CPU softnet dropped/squeezed increase, in CPU order.
	SoftnetCPUs []CPUSoftnet
	// Err is set when a sampler failed (a missing /proc file, ss not
	// installed, the server socket absent from ss). The step is then invalid:
	// a zero drop count that was never read is not a measurement.
	Err string
}

// CPUSoftnet is one CPU's softnet_stat increase over a window.
type CPUSoftnet struct {
	CPU               int
	Dropped, Squeezed uint64
}

// System is one running server plus its load bot.
type System interface {
	Generator
	// LogText returns the server's log read from its file.
	LogText() (string, error)
	// BeginSample starts the 1 s samplers; the returned func stops them and
	// returns the window.
	BeginSample() func() Window
	// ControlLost returns every control-lost event of this system's load bot so
	// far, in detection order (a v2 bot's hold death is one event too).
	ControlLost() []ControlLostEvent
	// Burst adds add bots with no rate limit at inFlight concurrency and the
	// talker silent or live (I-13). A bot without the burst command returns an
	// error wrapping ErrNotSupported. Join failures are data in the result.
	Burst(ctx context.Context, add, inFlight int, talker string) (Joins, error)
	// Close stops the bot, then the server, and reaps both.
	Close() error
}

// lastJoiner is the optional System method that returns the last `joins` line
// the bot wrote (see Bot.LastJoins).
type lastJoiner interface {
	LastJoins() (Joins, bool)
}

// burstSupporter is the optional System method that says whether the load bot
// has the burst command, so a v2 bot is skipped before any rebuild or ramp.
type burstSupporter interface {
	SupportsBurst() bool
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
	Predicted int // B-0 predicted knee; context only once Start is set
	NMax      int // time/port/memory cap on a sweep step
	// Start is the sweep's first step size, taken from the host (0 = the
	// legacy max(1, Predicted/4)). The warm-up is max(2, Start/2) when it is
	// set.
	Start int
	// Top is the largest bot count any step may request, the overload
	// included (0 = 2*NMax). NMaxBy names what bounded NMax (time, memory,
	// generator-cpu, ports, explicit) and is carried on a time-bound result.
	Top      int
	NMaxBy   string
	JoinRate float64 // joins per second, for the time guard
	Budget   time.Duration

	Warmup, Hold, Confirm, Overload, ProbeHold time.Duration

	Bounds sweep.Bounds
	Clock  Clock

	// RampChunk bounds one set_n of a ramp: a ramp is split into chunks of at
	// most this many bots and stops at the first chunk that fails to join or
	// loses control (0 = one set_n per step). RateMax and RateScale are the bot's
	// join-rate rule min(RateMax, RateScale/alive) (0 terms are left out; both 0
	// = the constant JoinRate), which the time guard prices a ramp with.
	RampChunk          int
	RateMax, RateScale float64
	// BurstEnabled runs the join-burst phase after the sweep; BurstAdd (150) and
	// BurstInFlight (16) shape one burst.
	BurstEnabled            bool
	BurstAdd, BurstInFlight int

	// OnRow is called with each row right after it is appended to the outcome;
	// Logf receives the driver's own progress lines (a ramp starting and
	// ending). Both are optional and only for the operator's log.
	OnRow func(Row)
	Logf  func(format string, a ...any)
}

// Row is one measured step.
type Row struct {
	Kind    string // sweep | confirm | overload | burst
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
	// ControlLost holds the first (at most 8) bots that lost their control
	// connection during the step, ControlLostCount all of them and
	// ControlLostPhase the stage the kit was in when it saw it (ramp, settle or
	// hold). A step with deaths is classified sweep.ControlLost.
	ControlLost      []ControlLostEvent
	ControlLostCount int
	ControlLostPhase string
	// JoinFailedChunk is the ramp chunk that had join failures, when the ramp was
	// chunked (nil otherwise); Joins then sums the chunks up to and including it.
	JoinFailedChunk *ChunkRange
	// BurstTalker (silent | live) and BurstResult (ok | join failures | control
	// lost) are set on Kind "burst" rows only. A burst row never reaches the sweep.
	BurstTalker string
	BurstResult string
}

// ChunkRange is one ramp chunk: it grew the bot count from Had to Requested.
type ChunkRange struct{ Had, Requested int }

// Outcome is everything one arm produced.
type Outcome struct {
	Rows   []Row
	Result sweep.Result
	// Unconfirmed is set when the confirm step at the found knee did not pass:
	// the knee is then the sweep's finding only.
	Unconfirmed bool
	Starts      int
	Notes       []string
	// StoppedEarly is the error that ended the leg after at least one sweep
	// step was recorded; the result is then the best bracket so far.
	StoppedEarly string
}

// Driver runs the protocol for one arm.
type Driver struct {
	env Env
	cfg Config

	sys    System
	joined int
	// seenLost is the count of control-lost events already attributed to a row
	// (reset when the system is rebuilt).
	seenLost int
	// rebuild is set after a join failure: the server and bot are no longer
	// trusted, so the next step boots fresh. rebuildAt is the failing N.
	rebuild   bool
	rebuildAt int
	// rebuildWhy names what killed the system, for the rebuild note.
	rebuildWhy string
	rate       float64
	start      time.Time
	out        Outcome
	sw         *sweep.Sweep
	sweepDone  bool // d.out.Result is the finished sweep's result; Run keeps it on a late error
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
	d.seenLost = 0
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
	// FailedChunk is the chunk with join failures (chunked ramps only).
	FailedChunk *ChunkRange
	// Dead0 is the system's control-lost count when the ramp began (after any
	// rebuild); deaths past it belong to this step. Lost carries a death that
	// came back as an error from a set_n.
	Dead0 int
	Lost  *ControlLostEvent
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

// asLost turns a bot error into a control-lost event: a *ControlLostError, or
// the v2 hold-death text (a v2 bot's raw error line). phase is the kit's stage,
// used when the event carries none.
func asLost(err error, phase string) *ControlLostEvent {
	if err == nil {
		return nil
	}
	var cle *ControlLostError
	if errors.As(err, &cle) {
		ev := cle.Event
		if ev.Phase == "" {
			ev.Phase = phase
		}
		return &ev
	}
	if t := err.Error(); IsControlLostText(t) {
		ev := ControlLostFromV2Error(t, phase)
		return &ev
	}
	return nil
}

// lostSince returns the deaths the system recorded after dead0, or extra alone
// when the bot recorded none (an error that carried the death).
func (d *Driver) lostSince(dead0 int, extra *ControlLostEvent) []ControlLostEvent {
	var evs []ControlLostEvent
	if all := d.sys.ControlLost(); len(all) > dead0 {
		evs = append(evs, all[dead0:]...)
	} else {
		d.seenLost = len(all)
	}
	if len(evs) == 0 && extra != nil {
		evs = append(evs, *extra)
	}
	return evs
}

func describeLost(ev ControlLostEvent) string {
	return fmt.Sprintf("bot %d %s: %s", ev.Bot, ev.Code, ev.Message)
}

// mergeJoins folds the next chunk's joins line into the running total. The
// total's Requested is the last chunk's target, so the I-1 invariants hold for
// the chunks that ran.
func mergeJoins(a, b Joins) Joins {
	m := b
	m.Had = a.Had
	m.Joined = a.Joined + b.Joined
	m.JoinFailures = a.JoinFailures + b.JoinFailures
	m.N = a.Had + m.Joined
	m.FailuresByStage = map[string]int{}
	for _, k := range joinStages {
		m.FailuresByStage[k] = a.FailuresByStage[k] + b.FailuresByStage[k]
	}
	m.HelloTriesHist = make([]int, helloHistLen)
	for i := range m.HelloTriesHist {
		if i < len(a.HelloTriesHist) {
			m.HelloTriesHist[i] += a.HelloTriesHist[i]
		}
		if i < len(b.HelloTriesHist) {
			m.HelloTriesHist[i] += b.HelloTriesHist[i]
		}
	}
	m.HelloTriesMax = max(a.HelloTriesMax, b.HelloTriesMax)
	m.HelloRetried = a.HelloRetried + b.HelloRetried
	m.Failed = append(append([]JoinFailure(nil), a.Failed...), b.Failed...)
	m.FailedTruncated = a.FailedTruncated || b.FailedTruncated
	if len(m.Failed) > maxFailedEntries {
		m.Failed, m.FailedTruncated = m.Failed[:maxFailedEntries], true
	}
	m.RampS = a.RampS + b.RampS
	m.RampHealth.SchedP99us = max(a.RampHealth.SchedP99us, b.RampHealth.SchedP99us)
	m.RampHealth.GCPauseP99us = max(a.RampHealth.GCPauseP99us, b.RampHealth.GCPauseP99us)
	if m.RampS > 0 {
		m.JoinsPerS = float64(m.Joined) / m.RampS
	}
	m.JoinP99Us = max(a.JoinP99Us, b.JoinP99Us)
	return m
}

// ensure makes the bot count exactly n. Bots only grow, so a smaller n needs
// a fresh server and load bot (which also gives the step a server that was not
// just driven past its knee), and so does the step after a join failure or a
// control loss. A ramp runs in chunks of cfg.RampChunk and stops at the first
// chunk with join failures or deaths. A join failure is returned in ramp.Joins,
// not as an error: the caller decides.
func (d *Driver) ensure(ctx context.Context, n int) (ramp, error) {
	var r ramp
	switch {
	case d.rebuild:
		why := d.rebuildWhy
		if why == "" {
			why = "join failures"
		}
		d.note("rebuilt the server and load bot after %s at %d bots", why, d.rebuildAt)
		d.rebuild, d.rebuildWhy = false, ""
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
	// The baseline is the high-water mark of deaths already attributed to a
	// row, not the count now: a bot that died between the previous step's final
	// check and this point belongs to this step.
	r.Dead0 = min(d.seenLost, len(d.sys.ControlLost()))
	if n == d.joined {
		return r, nil
	}
	t0 := d.cfg.Clock.Now()
	start := d.joined
	add := n - start
	before, errBefore := d.sys.LogText()
	end := d.sys.BeginSample()
	d.logf("ramp N=%d->%d started", start, n)
	var total Joins
	chunks := 0
	cur := start
	for cur < n {
		target := n
		if d.cfg.RampChunk > 0 && n-cur > d.cfg.RampChunk {
			target = cur + d.cfg.RampChunk
		}
		j, err := d.sys.SetN(ctx, target)
		chunks++
		if err != nil {
			if ev := asLost(err, "ramp"); ev != nil {
				r.Lost = ev
				break
			}
			end()
			return r, fmt.Errorf("set_n %d: %w", n, err)
		}
		if chunks == 1 {
			total = j
		} else {
			total = mergeJoins(total, j)
		}
		if j.Source == JoinsSourcePending {
			cur = target
			continue
		}
		if j.JoinFailures > 0 {
			if d.cfg.RampChunk > 0 {
				r.FailedChunk = &ChunkRange{Had: cur, Requested: target}
			}
			break
		}
		cur = target
		if len(d.sys.ControlLost()) > r.Dead0 {
			break
		}
	}
	win := end()
	if total.Source != JoinsSourcePending {
		// cur stops before a failing chunk, so the bots that chunk did join
		// are only in the joins line.
		d.logf("ramp N=%d->%d done in %.0fs: joined=%d join_failures=%d", start, n,
			d.cfg.Clock.Now().Sub(t0).Seconds(), max(cur-start, total.Joined), total.JoinFailures)
	}
	after, errAfter := d.sys.LogText()
	r.Joins, r.Window, r.Ran, r.FailedAcked = total, win, true, -1
	// A failed read must not turn the server's whole log into the ramp's
	// delta: the counts stay zero (unknown) and the note says why.
	logsOK := errBefore == nil && errAfter == nil
	if logsOK {
		r.Logs = CountLog(after).Sub(CountLog(before))
	} else {
		d.note("ramp log unreadable at %d bots (%v): ramp log counts left empty", n, errors.Join(errBefore, errAfter))
	}
	if r.Lost != nil || len(d.sys.ControlLost()) > r.Dead0 {
		// The caller records the death; the system is not trusted any more.
		d.rebuild, d.rebuildAt, d.rebuildWhy = true, n, "control lost"
		return r, nil
	}
	if total.JoinFailures > 0 {
		if logsOK {
			r.FailedAcked = FailedAcked(before, after, total.Failed)
		}
		d.rebuild, d.rebuildAt, d.rebuildWhy = true, n, ""
		return r, nil
	}
	d.joined = n
	// Learn the real join rate once the ramp is long enough to trust, so the
	// time guard follows this VM and not the design's estimate. With the bot's
	// rate rule modelled the guard uses the model instead.
	if el := d.cfg.Clock.Now().Sub(t0).Seconds(); add >= 50 && el > 0 && !d.rateModel() {
		d.rate = float64(add) / el
	}
	return r, nil
}

// rateModel reports whether the time guard prices ramps with the bot's rate rule.
func (d *Driver) rateModel() bool { return d.cfg.RateMax > 0 || d.cfg.RateScale > 0 }

// model is the join model the guard prices ramps with: the bot's rate rule
// when it is on, else the constant (learned) rate.
func (d *Driver) model() sweep.JoinModel {
	if d.rateModel() {
		return sweep.JoinModel{Max: d.cfg.RateMax, Scale: d.cfg.RateScale}
	}
	return sweep.JoinModel{Const: d.rate}
}

// rampSeconds is the time to grow the bot count from `from` to `to` joins
// under the driver's join model (see sweep.JoinModel.Seconds).
func (d *Driver) rampSeconds(from, to int) float64 { return d.model().Seconds(from, to) }

// settle probes with short holds until the step is quiescent (design 6.4.3).
// The bot has no live counters, so each probe hold's report supplies them:
// total update-stream receives across its bots, and the resyncs seen during the
// probe. Both are stricter than the design's "sampled full-store bots".
//
// The bot counts a probe as a hold phase, so a probe's ResourceExhausted counts
// are returned for the caller to fold into the step's ramp and settle columns
// (they are ramp artefacts and never part of the measured hold).
//
// A death after dead0, or a probe error that is one, ends the settle with lost
// set; it is the step's outcome, not an error.
func (d *Driver) settle(ctx context.Context, rampEnd time.Time, dead0 int) (ok bool, reason string, rex PhaseCounts, lost []ControlLostEvent, err error) {
	var samples []sweep.SettleSample
	for i := 0; ; i++ {
		st, err := d.sys.Hold(ctx, d.cfg.ProbeHold, fmt.Sprintf("probe-%d", i))
		if err != nil {
			if ev := asLost(err, "settle"); ev != nil {
				return false, "", rex, d.lostSince(dead0, ev), nil
			}
			return false, "", rex, nil, err
		}
		if evs := d.lostSince(dead0, nil); len(evs) > 0 {
			return false, "", rex, evs, nil
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
			return true, "", rex, nil, nil
		}
		if strings.HasPrefix(reason, sweep.InvalidCannotSettle) {
			return false, reason, rex, nil, nil
		}
	}
}

// markControlLost makes the row a control_lost step: it keeps whatever the step
// measured, adds the deaths to its data and classifies (control loss first).
// The system is no longer trusted, so the next step boots fresh.
func (d *Driver) markControlLost(row *Row, evs []ControlLostEvent, phase string) {
	count := len(evs)
	if row.Held && row.Step.ControlLost.DeadSinceLastStep > count {
		count = row.Step.ControlLost.DeadSinceLastStep
	}
	row.ControlLost = append([]ControlLostEvent(nil), evs[:min(8, len(evs))]...)
	row.ControlLostCount, row.ControlLostPhase = count, phase
	row.Data.N = row.N
	row.Data.ControlLost = uint64(count)
	row.Data.ControlLostFirst = describeLost(evs[0])
	row.Outcome, row.Reasons = sweep.Classify(row.Data, d.cfg.Bounds)
	d.rebuild, d.rebuildAt, d.rebuildWhy = true, row.N, "control lost"
}

// nanData is the StepData of a step that never reached a measured hold.
func nanData(n int) sweep.StepData {
	nan := math.NaN()
	return sweep.StepData{N: n, LossRatio: nan, LateLossRatio: nan, ServerCPU: nan, GenCPU: nan}
}

// measure runs one step at n bots: ramp, settle, then the measured hold. A bot
// that loses its control connection at any point ends the step as ControlLost.
func (d *Driver) measure(ctx context.Context, kind string, n int, hold time.Duration, label string) (Row, error) {
	row := Row{Kind: kind, Seq: len(d.out.Rows) + 1, N: n, FailedAckedByServer: -1}
	rp, err := d.ensure(ctx, n)
	if err != nil {
		return row, err
	}
	row.Joins, row.RampWindow, row.RampLogs, row.FailedAckedByServer = rp.Joins, rp.Window, rp.Logs, rp.FailedAcked
	row.JoinFailedChunk = rp.FailedChunk
	if evs := d.lostSince(rp.Dead0, rp.Lost); len(evs) > 0 {
		row.Data = nanData(n)
		d.markControlLost(&row, evs, "ramp")
		return row, nil
	}
	if rp.Joins.JoinFailures > 0 {
		// The ramp is this step's outcome: no settle, no hold. A join failure
		// is a server breach unless the generator itself was the limit.
		by := map[string]uint64{}
		for k, v := range rp.Joins.FailuresByStage {
			by[k] = uint64(v)
		}
		row.Data = nanData(n)
		row.Data.JoinFailures = uint64(rp.Joins.JoinFailures)
		row.Data.JoinAttempted = max(0, rp.Joins.Requested-rp.Joins.Had)
		row.Data.JoinStages = by
		row.Data.HelloTriesMax = rp.Joins.HelloTriesMax
		row.Data.RampGenCPU = rp.Window.GenCPU
		row.Outcome, row.Reasons = sweep.Classify(row.Data, d.cfg.Bounds)
		return row, nil
	}
	rampEnd := d.cfg.Clock.Now()
	ok, reason, probeRex, lost, err := d.settle(ctx, rampEnd, rp.Dead0)
	if err != nil {
		return row, fmt.Errorf("settle at %d bots: %w", n, err)
	}
	row.SettleSeconds = d.cfg.Clock.Now().Sub(rampEnd).Seconds()
	if len(lost) > 0 {
		row.Data = nanData(n)
		d.markControlLost(&row, lost, "settle")
		return row, nil
	}
	if !ok {
		row.Step.ResourceExhausted = probeRex
		row.Data = nanData(n)
		row.Data.CannotSettle = true
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
		if ev := asLost(herr, "hold"); ev != nil {
			row.Data = nanData(n)
			d.markControlLost(&row, d.lostSince(rp.Dead0, ev), "hold")
			return row, nil
		}
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
		// A v4 bot measures late loss and one-way latency; a v3 bot, a
		// non-Linux bot and the D2 arm do not. An unmeasured figure is zero
		// here and named in the not_measured column, so a step is never
		// invalidated by a figure the bot cannot give, and never credited with
		// one (see stepLatency).
		LateLossRatio: stepLateLoss(st), OneWayP99: stepOneWayP99(st),
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
	// A death during the hold (or the second after it) makes the loss of the
	// listeners it took down meaningless: the step is control_lost, with its
	// measurements kept.
	if evs := d.lostSince(rp.Dead0, nil); len(evs) > 0 {
		d.markControlLost(&row, evs, "hold")
	}
	return row, nil
}

// stepLateLoss is the bot's late-loss ratio, or 0 when it did not measure one.
func stepLateLoss(st Step) float64 {
	if st.LateLossRatio == nil {
		return 0
	}
	return *st.LateLossRatio
}

// stepOneWayP99 is the bot's one-way p99, or 0 when it did not measure one.
func stepOneWayP99(st Step) time.Duration {
	if st.OneWayP99Us == nil {
		return 0
	}
	return time.Duration(*st.OneWayP99Us) * time.Microsecond
}

// overBudget reports whether running a step at n would leave too little of the
// budget for the confirm and overload that follow (and the rebuild the confirm
// may need). Budget 0 is unlimited.
func (d *Driver) overBudget(n, lo int) bool {
	if d.cfg.Budget <= 0 {
		return false
	}
	secs := func(from, to int) time.Duration {
		return time.Duration(d.rampSeconds(from, to) * float64(time.Second))
	}
	cost := sweep.SettleTypical + sweep.HoldDuration
	if d.rebuild {
		// A join failure killed the system: the step boots fresh at 2 bots
		// and ramps from there, whatever d.joined last was.
		cost += secs(2, n) + 30*time.Second
	} else if n < d.joined {
		cost += secs(0, n) + 30*time.Second
	} else {
		cost += secs(d.joined, n)
	}
	// The confirm may rebuild and ramp to the knee, and the overload then
	// ramps from the knee to its own size: two ramps and two settles.
	reserve := d.cfg.Confirm + d.cfg.Overload + secs(0, lo) + secs(lo, d.overloadN(lo)) + 2*sweep.SettleTypical + 30*time.Second
	return d.cfg.Clock.Now().Sub(d.start)+cost+reserve > d.cfg.Budget
}

// top is the largest bot count any step may request.
func (d *Driver) top() int {
	if d.cfg.Top > 0 {
		return d.cfg.Top
	}
	return 2 * d.cfg.NMax
}

// overloadN is the overload step's size for a knee of k bots.
func (d *Driver) overloadN(k int) int {
	return min(2*k, 2*d.cfg.NMax, d.top())
}

// tailFits reports whether a confirm or overload step of n bots held for hold
// still fits in the budget, ramping from where the system is now. Budget 0 is
// unlimited.
func (d *Driver) tailFits(n int, hold time.Duration) bool {
	if d.cfg.Budget <= 0 {
		return true
	}
	var ramp time.Duration
	secs := func(from, to int) time.Duration {
		return time.Duration(d.rampSeconds(from, to) * float64(time.Second))
	}
	switch {
	case d.rebuild:
		ramp = secs(2, n) + 30*time.Second
	case n < d.joined:
		ramp = secs(0, n) + 30*time.Second
	default:
		ramp = secs(d.joined, n)
	}
	return d.cfg.Clock.Now().Sub(d.start)+ramp+sweep.SettleTypical+hold <= d.cfg.Budget
}

// measureRetried is a confirm or overload step: a control loss gets exactly one
// retry on a rebuilt system. Both rows are recorded; the last one is returned.
func (d *Driver) measureRetried(ctx context.Context, kind string, n int, hold time.Duration, label string) (Row, error) {
	row, err := d.measure(ctx, kind, n, hold, label)
	if err != nil {
		return row, err
	}
	d.record(row)
	if row.Outcome != sweep.ControlLost {
		return row, nil
	}
	d.note("the %s step at %d bots lost control (%s); retrying once", kind, n, strings.Join(row.Reasons, "; "))
	row, err = d.measure(ctx, kind, n, hold, label)
	if err != nil {
		return row, err
	}
	d.record(row)
	return row, nil
}

// burstPhase measures join behaviour on its own at about knee/2: +BurstAdd bots
// at BurstInFlight concurrency, once with the talker silent and once live. Its
// rows are kind "burst", never reach the sweep, and a failure in here is a note
// at most. The system is rebuilt afterwards so the confirm starts clean.
func (d *Driver) burstPhase(ctx context.Context, k int) error {
	add, inFlight := d.cfg.BurstAdd, d.cfg.BurstInFlight
	if add <= 0 {
		add = 150
	}
	if inFlight <= 0 {
		inFlight = 16
	}
	if k/2 < 300 {
		d.note("join burst not measured: the knee %d is below 600 bots", k)
		return nil
	}
	if bs, ok := d.sys.(burstSupporter); ok && !bs.SupportsBurst() {
		d.note("join burst not measured: the load bot lacks the burst command (v2)")
		return nil
	}
	base := k/2 - add
	if d.overBudget(base, k) {
		d.note("budget guard: join burst not measured so the confirm and overload still fit")
		return nil
	}
	// A pending rebuild (the sweep's last step failed its joins or lost
	// control) already boots fresh in ensure; booting here too would waste one.
	if d.joined > base && !d.rebuild {
		d.note("rebuilt the server and load bot for the join burst: %d bots joined, burst base %d", d.joined, base)
		d.closeSys()
		if err := d.boot(ctx); err != nil {
			return err
		}
	}
	// Whatever happens, the confirm that follows boots fresh.
	defer func() {
		d.rebuild, d.rebuildAt, d.rebuildWhy = true, base+2*add, "the join burst"
	}()
	rp, err := d.ensure(ctx, base)
	if err != nil {
		return err
	}
	if rp.Joins.JoinFailures > 0 || len(d.lostSince(rp.Dead0, rp.Lost)) > 0 {
		d.note("join burst not measured: the ramp to %d bots did not complete cleanly", base)
		return nil
	}
	for _, talker := range []string{"silent", "live"} {
		dead0 := len(d.sys.ControlLost())
		ok, _, _, lost, err := d.settle(ctx, d.cfg.Clock.Now(), dead0)
		if err != nil {
			return fmt.Errorf("settle before the %s join burst: %w", talker, err)
		}
		if len(lost) > 0 || !ok {
			d.note("join burst (%s talker) not measured: the system did not settle at %d bots", talker, d.joined)
			return nil
		}
		j, err := d.sys.Burst(ctx, add, inFlight, talker)
		if err != nil {
			if errors.Is(err, ErrNotSupported) {
				d.note("join burst not measured: the load bot lacks the burst command (v2)")
				return nil
			}
			if ev := asLost(err, "burst"); ev != nil {
				d.note("join burst (%s talker) lost control: %s", talker, describeLost(*ev))
				return nil
			}
			return fmt.Errorf("join burst (%s talker): %w", talker, err)
		}
		n := j.N
		if n == 0 {
			n = d.joined + j.Joined
		}
		row := Row{Kind: "burst", Seq: len(d.out.Rows) + 1, N: n, Joins: j, FailedAckedByServer: -1,
			Outcome: sweep.Pass, BurstTalker: talker, BurstResult: "ok", Data: nanData(n)}
		if j.JoinFailures > 0 {
			row.BurstResult = "join failures"
			row.Reasons = []string{"burst failures: " + joinFailureSummary(j)}
		}
		if evs := d.lostSince(dead0, nil); len(evs) > 0 {
			row.BurstResult = "control lost"
			row.ControlLost, row.ControlLostCount, row.ControlLostPhase = evs[:min(8, len(evs))], len(evs), "burst"
			row.Reasons = append(row.Reasons, "control lost: "+describeLost(evs[0]))
		}
		d.record(row)
		if row.BurstResult != "ok" {
			// The failed joins left the system in an unknown state.
			d.note("join burst (%s talker) ended the burst phase: %s", talker, row.BurstResult)
			return nil
		}
		d.joined = n
	}
	return nil
}

// Run executes the arm: boot, warm-up, sweep, join bursts, confirm, overload.
// An error after at least one sweep row was recorded does not lose the sweep:
// the result is the best bracket so far, the error is Outcome.StoppedEarly and
// Run returns nil. An error before any sweep row is returned.
func (d *Driver) Run(ctx context.Context) (*Outcome, error) {
	d.start = d.cfg.Clock.Now()
	defer d.closeSys()
	out, err := d.run(ctx)
	if err == nil || d.sw == nil || len(rowsOf(d.out.Rows, "sweep")) == 0 {
		return out, err
	}
	if !d.sweepDone {
		d.out.Result = d.sw.Stop()
		if d.out.Result.Kind == sweep.BelowRange {
			d.note("no sweep step passed before the error, so \"below range\" may only mean the leg ended early")
		}
	}
	d.out.StoppedEarly = err.Error()
	d.note("stopped early: %v; the result is the best bracket so far", err)
	return &d.out, nil
}

func rowsOf(rows []Row, kind string) []Row {
	var r []Row
	for _, x := range rows {
		if x.Kind == kind {
			r = append(r, x)
		}
	}
	return r
}

func (d *Driver) run(ctx context.Context) (*Outcome, error) {
	if err := d.boot(ctx); err != nil {
		return &d.out, err
	}

	// Warm-up, discarded: half the host start, or (no start) 10 % of the
	// predicted knee.
	warmN := max(2, d.cfg.Predicted/10)
	if d.cfg.Start > 0 {
		warmN = max(2, d.cfg.Start/2)
	}
	if rp, err := d.ensure(ctx, warmN); err != nil {
		return &d.out, err
	} else if rp.Joins.JoinFailures > 0 {
		return &d.out, fmt.Errorf("warm-up: join failures at %d bots: %s", warmN, joinFailureSummary(rp.Joins))
	}
	// A death in the discarded warm-up is not any step's: note it and rebuild,
	// so the first sweep step starts on a fresh system with a clean baseline.
	_, werr := d.sys.Hold(ctx, d.cfg.Warmup, "warmup")
	if werr != nil && asLost(werr, "warmup") == nil {
		return &d.out, fmt.Errorf("warm-up: %w", werr)
	}
	if evs := d.lostSince(0, asLost(werr, "warmup")); len(evs) > 0 {
		d.note("the warm-up lost control (%d bots; %s); rebuilding before the sweep", len(evs), describeLost(evs[0]))
		d.rebuild, d.rebuildAt, d.rebuildWhy = true, warmN, "control lost in the warm-up"
	}

	start := max(1, d.cfg.Predicted/4)
	if d.cfg.Start > 0 {
		start = d.cfg.Start
	}
	sw := sweep.NewSweepAt(start, d.cfg.NMax)
	d.sw = sw
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
	if r := &d.out.Result; r.Kind == sweep.TimeBound && r.N == d.cfg.NMax && r.Limit == "" {
		r.Limit = d.cfg.NMaxBy
	}
	d.sweepDone = true

	// Confirm at the found knee, then one overload step at 2x.
	k := d.out.Result.N
	if k <= 0 || d.out.Result.Kind == sweep.BelowRange {
		d.note("no passing step, so no confirm or overload")
		return &d.out, nil
	}
	if d.cfg.BurstEnabled {
		if err := d.burstPhase(ctx, k); err != nil {
			return &d.out, err
		}
	}
	if d.tailFits(k, d.cfg.Confirm) {
		row, err := d.measureRetried(ctx, "confirm", k, d.cfg.Confirm, "confirm")
		if err != nil {
			return &d.out, err
		}
		if row.Outcome != sweep.Pass {
			d.out.Unconfirmed = true
			d.note("the confirm step at %d bots was %s, so the knee is unconfirmed: %s", k, row.Outcome, strings.Join(row.Reasons, "; "))
		}
	} else {
		d.note("budget guard: confirm not measured (the %d-bot knee is unconfirmed by time, not by a failed step)", k)
	}
	ov := d.overloadN(k)
	if ov <= k {
		d.note("overload not measured: capped at %d (N_max by %s)", d.top(), d.cfg.NMaxBy)
		return &d.out, nil
	}
	if !d.tailFits(ov, d.cfg.Overload) {
		d.note("budget guard: overload not measured")
		return &d.out, nil
	}
	if _, err := d.measureRetried(ctx, "overload", ov, d.cfg.Overload, "overload"); err != nil {
		return &d.out, err
	}
	return &d.out, nil
}
