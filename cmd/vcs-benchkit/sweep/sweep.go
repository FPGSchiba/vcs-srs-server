package sweep

import (
	"errors"
	"fmt"
)

// bisections is the number of bisection steps after the first breach
// (design 6.4.3: "then bisect 3 times").
const bisections = 3

// ResultKind says how a sweep ended.
type ResultKind int

const (
	// KneeFound: N is the largest step size that passed, with a breach above.
	KneeFound ResultKind = iota
	// GeneratorBound: the generator's health failed before any server breach
	// bracketed the knee. N is the last valid passing step (0 if none): the
	// knee is at least N but was not reached (design 6.4, D14).
	GeneratorBound
	// TimeBound: every step up to N_max passed. N is N_max (0 when N_max
	// allows no step): the knee is at least N_max.
	TimeBound
	// BelowRange: even the smallest bisected size breached. N is that
	// smallest breached size; the knee is below it. This is not in the
	// design's three result kinds; it only arises when the very first step
	// (predicted knee / 4) already breaches.
	BelowRange
)

// Result is a sweep's final answer.
type Result struct {
	Kind ResultKind
	N    int
}

// String renders the result as the design names it: "knee N",
// "≥ N (generator-bound)", "≥ N_max (time-bound)".
func (r Result) String() string {
	switch r.Kind {
	case KneeFound:
		return fmt.Sprintf("knee %d", r.N)
	case GeneratorBound:
		return fmt.Sprintf("≥ %d (generator-bound)", r.N)
	case TimeBound:
		return fmt.Sprintf("≥ %d (time-bound)", r.N)
	case BelowRange:
		return fmt.Sprintf("< %d (below range)", r.N)
	}
	return fmt.Sprintf("Result(%d, %d)", int(r.Kind), r.N)
}

// StepRecord is one executed step and its outcome.
type StepRecord struct {
	N       int
	Outcome StepOutcome
}

type phase int

const (
	phaseDoubling phase = iota
	phaseBisecting
	phaseDone
)

// Sweep is the doubling-then-bisect step controller (design 6.4.3). It is a
// pure state machine: the caller asks Next for a size, runs the step,
// classifies it and reports the outcome with Record, until Next says done.
//
//	start = predicted knee / 4 (at least 1, at most N_max)
//	double until the first Breach, capped at N_max
//	then bisect (last pass, first breach) three times
//
// An Invalid step ends the sweep as generator-bound at the last passing step.
// During bisection that is deliberately conservative: the knee lies in
// [last pass, first breach), but with an unhealthy generator in between it
// cannot be narrowed, so it is reported as a lower bound.
type Sweep struct {
	nMax int

	phase   phase
	next    int // size of the step handed out by Next
	pending bool

	lo, hi    int // lo: largest passing size (0 = none); hi: smallest breached size
	bisected  int
	result    Result
	history   []StepRecord
	smallestB int // smallest breached size seen (for BelowRange)
}

// NewSweep starts a sweep from the B-0 predicted knee, capped at nMax. A
// non-positive prediction starts at 1. A non-positive nMax runs no step and
// ends as TimeBound with N 0.
func NewSweep(predictedKnee, nMax int) *Sweep {
	s := &Sweep{nMax: nMax}
	if nMax < 1 {
		s.phase = phaseDone
		s.result = Result{Kind: TimeBound, N: 0}
		return s
	}
	start := predictedKnee / 4
	if start < 1 {
		start = 1
	}
	if start > nMax {
		start = nMax
	}
	s.next = start
	return s
}

// Next returns the size of the step to run. It returns false when the sweep
// is finished; Result is then final. Calling Next again before Record
// returns the same pending size.
func (s *Sweep) Next() (int, bool) {
	if s.phase == phaseDone {
		return 0, false
	}
	s.pending = true
	return s.next, true
}

// Record reports the outcome of the step Next returned. It fails when no
// step is pending, the sweep is finished, or the outcome is not one of
// Pass, Breach and Invalid; a failed Record leaves the pending step intact.
func (s *Sweep) Record(o StepOutcome) error {
	if s.phase == phaseDone {
		return errors.New("sweep: finished, nothing to record")
	}
	if !s.pending {
		return errors.New("sweep: Record without a pending Next")
	}
	if o != Pass && o != Breach && o != Invalid {
		return fmt.Errorf("sweep: unknown outcome %d", int(o))
	}
	s.pending = false
	n := s.next
	s.history = append(s.history, StepRecord{N: n, Outcome: o})

	if o == Invalid {
		s.finish(Result{Kind: GeneratorBound, N: s.lo})
		return nil
	}
	if o == Breach && (s.smallestB == 0 || n < s.smallestB) {
		s.smallestB = n
	}

	switch s.phase {
	case phaseDoubling:
		s.recordDoubling(n, o)
	case phaseBisecting:
		s.recordBisecting(n, o)
	}
	return nil
}

func (s *Sweep) recordDoubling(n int, o StepOutcome) {
	if o == Pass {
		s.lo = n
		if n >= s.nMax {
			s.finish(Result{Kind: TimeBound, N: s.nMax})
			return
		}
		s.next = min(2*n, s.nMax)
		return
	}
	// First breach: bisect (lo, n). lo may be 0 when the first step breached.
	s.hi = n
	s.phase = phaseBisecting
	s.advanceBisect()
}

func (s *Sweep) recordBisecting(n int, o StepOutcome) {
	if o == Pass {
		s.lo = n
	} else {
		s.hi = n
	}
	s.bisected++
	s.advanceBisect()
}

// advanceBisect schedules the next midpoint or finishes the sweep.
func (s *Sweep) advanceBisect() {
	if s.bisected < bisections {
		if mid := (s.lo + s.hi) / 2; mid > s.lo && mid < s.hi {
			s.next = mid
			return
		}
	}
	if s.lo == 0 {
		s.finish(Result{Kind: BelowRange, N: s.smallestB})
		return
	}
	s.finish(Result{Kind: KneeFound, N: s.lo})
}

func (s *Sweep) finish(r Result) {
	s.phase = phaseDone
	s.result = r
}

// Bracket returns the largest passing size (0 = none) and the smallest
// breached size (0 = no breach yet) seen so far.
func (s *Sweep) Bracket() (lo, hi int) { return s.lo, s.hi }

// Stop ends the sweep early (the leg's budget guard) and returns its result.
// A sweep still doubling has seen no breach, so it is time-bound at its last
// pass. Once a breach brackets the knee, the last pass is a knee, coarser
// than three bisections would give but not censored; with no pass at all it
// is below range. A pending step is discarded. A finished sweep keeps its
// result.
func (s *Sweep) Stop() Result {
	if s.phase == phaseDone {
		return s.result
	}
	s.pending = false
	switch {
	case s.phase == phaseDoubling:
		s.finish(Result{Kind: TimeBound, N: s.lo})
	case s.lo == 0:
		s.finish(Result{Kind: BelowRange, N: s.smallestB})
	default:
		s.finish(Result{Kind: KneeFound, N: s.lo})
	}
	return s.result
}

// Result is the sweep's answer. It is only meaningful once Next has returned
// false.
func (s *Sweep) Result() Result { return s.result }

// History returns the executed steps in order.
func (s *Sweep) History() []StepRecord {
	return append([]StepRecord(nil), s.history...)
}
