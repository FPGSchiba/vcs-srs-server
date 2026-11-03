package sweep

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// StepOutcome is the verdict on one measured hold.
type StepOutcome int

const (
	// Pass: the generator was healthy and no server criterion was breached.
	Pass StepOutcome = iota
	// Breach: the generator was healthy and the server crossed a "handled" or
	// "saturated" criterion (design 6.4.4). The step size is above the knee.
	Breach
	// Invalid: the measurement cannot be attributed to the server (generator
	// self-health out of bounds, or the step never settled). It is neither a
	// pass nor a breach.
	Invalid
	// ControlLost: at least one bot lost its control connection during the step.
	// The cause is not attributed (it may be the harness or the server), so the
	// step is neither a pass nor a breach nor a generator-health verdict.
	ControlLost
)

func (o StepOutcome) String() string {
	switch o {
	case Pass:
		return "pass"
	case Breach:
		return "breach"
	case Invalid:
		return "invalid"
	case ControlLost:
		return "control_lost"
	}
	return fmt.Sprintf("StepOutcome(%d)", int(o))
}

// Bounds are the server-side breach thresholds and the kit-measured generator
// CPU bound (design 6.4.4, proposal section 3). The generator's own
// bot-reported bounds (D14) are applied by the load bot and arrive in
// StepData.GeneratorViolations.
type Bounds struct {
	// MaxLossRatio: loss at or above it breaches.
	MaxLossRatio float64
	// MaxLateLossRatio: frames later than the 60 ms jitter target count as
	// lost (proposal section 3), so the same 1 % applies, at or above it.
	// The 1 % is an implementer choice: design 6.4.4 only says "late loss
	// above the threshold". This field and DefaultBounds are the single
	// source that the leg driver and the S5 publishing code must cite.
	MaxLateLossRatio float64
	// MaxOneWayP99: one-way p99 strictly above it breaches.
	MaxOneWayP99 time.Duration
	// MaxServerCPU: the busiest server process, as a fraction of its CPU set,
	// strictly above it breaches.
	MaxServerCPU float64
	// MaxGenCPU: kit-measured generator CPU strictly above it invalidates.
	MaxGenCPU float64
}

// DefaultBounds are the design's values: loss 1 %, one-way p99 50 ms, server
// process CPU 80 %, generator CPU 85 % (design 6.3, 6.4.4).
var DefaultBounds = Bounds{
	MaxLossRatio:     0.01,
	MaxLateLossRatio: 0.01,
	MaxOneWayP99:     50 * time.Millisecond,
	MaxServerCPU:     0.80,
	MaxGenCPU:        0.85,
}

// PhaseCounts is a per-phase counter, as in the load bot's report.
type PhaseCounts struct {
	Ramp, Settle, Hold uint64
}

// StepData is everything Classify needs about one step. The kit fills it from
// the load bot's step report, the samplers and the log files; this package
// does no I/O.
type StepData struct {
	// N is the step size in bots (not used by Classify; it labels the step).
	N int

	// LossRatio and LateLossRatio are fractions in [0, 1]. NaN means the bot
	// did not measure it, which invalidates the step.
	LossRatio, LateLossRatio float64
	// OneWayP99 is the one-way latency p99 over the hold.
	OneWayP99 time.Duration

	// ResourceExhausted is split by phase. Only Hold can breach; Ramp and
	// Settle are reported in their own columns because the O(N^2) join
	// fan-out makes them a ramp artefact (design 6.4.4).
	ResourceExhausted PhaseCounts

	// ServerCPU is the busiest server process's utime+stime over the hold,
	// as a fraction of its CPU set. NaN means unmeasured.
	ServerCPU float64
	// ServerSocketDrops is the increase in the server sockets' drop counter
	// over the hold (ss -uamp).
	ServerSocketDrops uint64

	// Log line counts over the hold, from the server log files.
	ShardQueueFull     uint64 // "Voice shard queue full"
	UpdateStreamBehind uint64 // "Update stream fell behind"
	NodeFeedBehind     uint64 // "Voice node feed fell behind"
	NodeDeregistered   uint64 // "Voice node deregistered" (R13)

	// NodesWant and NodesMin are the distributed arm's node-count reading: the
	// nodes the topology should have (control's distributionStatus lists three:
	// nodeA, nodeB, global) and the fewest the 5 s poller saw during the hold.
	// NodesWant 0 means not measured (S, Seq, or no poll answered): never a breach.
	NodesWant, NodesMin int

	// GeneratorViolations are the load bot's own D14 violations for the hold
	// (loadgen Bounds.Violations). Non-empty invalidates the step.
	GeneratorViolations []string
	// GenCPU is the kit-measured generator CPU fraction over the hold. The leg
	// driver must always supply it: a negative or NaN value means not
	// measured, which invalidates the step.
	GenCPU float64
	// CannotSettle is set when the settle phase hit its bound
	// (Settled reported InvalidCannotSettle).
	CannotSettle bool

	// JoinFailures is the number of bots that failed to join in the ramp that
	// led to this step. Non-zero short-circuits Classify: the step has no hold,
	// so LossRatio, ServerCPU and GenCPU are typically NaN.
	JoinFailures uint64
	// JoinAttempted is the number of bots the ramp tried to add.
	JoinAttempted int
	// JoinStages is the failure count by stage (control, radio, voice_dial,
	// hello).
	JoinStages map[string]uint64
	// HelloTriesMax is the most HELLOs any bot of the ramp sent.
	HelloTriesMax int
	// RampGenCPU is the kit-measured generator CPU fraction over the ramp. A
	// negative or NaN value means not measured. The zero value reads as a
	// healthy generator, so a driver must always set it from the ramp window
	// (NaN or -1 when unmeasured) whenever JoinFailures may be non-zero.
	RampGenCPU float64

	// ControlLost is the number of bots whose control connection died during
	// the step (ramp, settle or hold). Non-zero makes Classify return ControlLost
	// before any other rule, because the step's load was not the load asked for.
	ControlLost uint64
	// ControlLostFirst describes the first death, for example "bot 2 Unavailable:
	// error reading from server: connection reset by peer". Optional.
	ControlLostFirst string
}

// Classify applies the design 6.4.4 criteria to one step. The returned
// reasons name every criterion that decided the outcome, in a fixed order;
// they are empty for Pass.
//
// A lost control connection is checked first: it is neither a server breach nor
// a generator verdict, and every other number of such a step is suspect.
//
// Invalid wins over Breach. A generator that is not healthy cannot attribute
// a server breach to the server (D14), and a step that never settled still
// carries the ramp's load, so neither may produce a knee. The breach
// indicators of such a step are not listed.
//
// A join failure is a server breach unless the kit's own generator CPU over
// the ramp shows a generator limit (accepted proposal, 2026-10-28);
// bot-reported ramp scheduler/GC latency is logged only. Join-failure rules
// run first and return: such a step has no hold to measure.
func Classify(s StepData, b Bounds) (StepOutcome, []string) {
	if s.ControlLost > 0 {
		return ControlLost, []string{controlLostReason(s)}
	}
	if s.JoinFailures > 0 {
		return classifyJoinFailure(s, b)
	}
	if inv := invalidReasons(s, b); len(inv) > 0 {
		return Invalid, inv
	}
	if br := breachReasons(s, b); len(br) > 0 {
		return Breach, br
	}
	return Pass, nil
}

func invalidReasons(s StepData, b Bounds) []string {
	var r []string
	for _, v := range s.GeneratorViolations {
		r = append(r, "generator health: "+v)
	}
	// An unmeasured generator CPU (negative or NaN) is invalid, like the other
	// unmeasured fields: D14 needs CPU headroom evidence, so a failed sampler
	// must not let a step count as valid.
	switch {
	case s.GenCPU < 0 || math.IsNaN(s.GenCPU):
		r = append(r, "invalid: generator cpu not measured")
	case s.GenCPU > b.MaxGenCPU:
		r = append(r, fmt.Sprintf("generator cpu %.0f%% above %.0f%%", s.GenCPU*100, b.MaxGenCPU*100))
	}
	if s.CannotSettle {
		r = append(r, InvalidCannotSettle)
	}
	for _, m := range []struct {
		name string
		v    float64
	}{
		{"loss ratio", s.LossRatio},
		{"late loss ratio", s.LateLossRatio},
		{"server cpu", s.ServerCPU},
	} {
		if math.IsNaN(m.v) {
			r = append(r, "invalid: "+m.name+" not measured")
		}
	}
	return r
}

func breachReasons(s StepData, b Bounds) []string {
	var r []string
	if s.LossRatio >= b.MaxLossRatio {
		r = append(r, fmt.Sprintf("loss %.2f%% at or above %.2f%%", s.LossRatio*100, b.MaxLossRatio*100))
	}
	if s.LateLossRatio >= b.MaxLateLossRatio {
		r = append(r, fmt.Sprintf("late loss %.2f%% at or above %.2f%%", s.LateLossRatio*100, b.MaxLateLossRatio*100))
	}
	if s.OneWayP99 > b.MaxOneWayP99 {
		r = append(r, fmt.Sprintf("one-way p99 %s above %s", s.OneWayP99, b.MaxOneWayP99))
	}
	if s.ResourceExhausted.Hold > 0 {
		r = append(r, fmt.Sprintf("%d ResourceExhausted during the hold", s.ResourceExhausted.Hold))
	}
	if s.ServerCPU > b.MaxServerCPU {
		r = append(r, fmt.Sprintf("server process cpu %.0f%% above %.0f%%", s.ServerCPU*100, b.MaxServerCPU*100))
	}
	if s.ServerSocketDrops > 0 {
		r = append(r, fmt.Sprintf("%d server socket drops during the hold", s.ServerSocketDrops))
	}
	if s.NodesWant > 0 && s.NodesMin < s.NodesWant {
		r = append(r, fmt.Sprintf("node count dropped to %d of %d", s.NodesMin, s.NodesWant))
	}
	for _, l := range []struct {
		line string
		n    uint64
	}{
		{"Voice shard queue full", s.ShardQueueFull},
		{"Update stream fell behind", s.UpdateStreamBehind},
		{"Voice node feed fell behind", s.NodeFeedBehind},
		{"Voice node deregistered", s.NodeDeregistered},
	} {
		if l.n > 0 {
			r = append(r, fmt.Sprintf("%d x %q in the log", l.n, l.line))
		}
	}
	return r
}

// joinStageOrder fixes the order of the stage list in a reason.
var joinStageOrder = []string{"control", "radio", "voice_dial", "hello"}

func classifyJoinFailure(s StepData, b Bounds) (StepOutcome, []string) {
	switch {
	case s.RampGenCPU < 0 || math.IsNaN(s.RampGenCPU):
		return Invalid, []string{fmt.Sprintf("invalid: generator cpu not measured during the ramp (%d join failures)", s.JoinFailures)}
	case s.RampGenCPU > b.MaxGenCPU:
		return Invalid, []string{fmt.Sprintf("generator limit during the ramp: generator cpu %.0f%% above %.0f%% with %d join failures",
			s.RampGenCPU*100, b.MaxGenCPU*100, s.JoinFailures)}
	}
	var stages []string
	for _, k := range joinStageOrder {
		if n := s.JoinStages[k]; n > 0 {
			stages = append(stages, fmt.Sprintf("%s %d", k, n))
		}
	}
	msg := fmt.Sprintf("join failure: %d of %d new bots failed", s.JoinFailures, s.JoinAttempted)
	if len(stages) > 0 {
		msg += " (" + strings.Join(stages, ", ") + ")"
	}
	if s.HelloTriesMax > 0 {
		msg += fmt.Sprintf("; hello tries max %d", s.HelloTriesMax)
	}
	return Breach, []string{msg}
}

func controlLostReason(s StepData) string {
	noun := "bots"
	if s.ControlLost == 1 {
		noun = "bot"
	}
	msg := fmt.Sprintf("control lost: %d %s", s.ControlLost, noun)
	if s.ControlLostFirst != "" {
		msg += " (" + s.ControlLostFirst + ")"
	}
	return msg
}
