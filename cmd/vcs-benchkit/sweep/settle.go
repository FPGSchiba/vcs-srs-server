package sweep

import (
	"fmt"
	"time"
)

// Settle thresholds (design 6.4 step 3).
const (
	// SettleFloor is the minimum time after the ramp ends before a step can
	// be called settled.
	SettleFloor = 5 * time.Second
	// SettleQuiet is how long the sampled full-store bots' update counters
	// must have stopped growing.
	SettleQuiet = 2 * time.Second
	// SettleBound is the point after which a step that has not settled is
	// invalid.
	SettleBound = 60 * time.Second

	// InvalidCannotSettle is the reason prefix for a step that never settled.
	InvalidCannotSettle = "invalid: cannot settle"
)

// SettleSample is one observation taken during the settle phase.
type SettleSample struct {
	// At is when the sample was taken.
	At time.Time
	// RampEnd is when the ramp ended; every sample of a step carries the same
	// value.
	RampEnd time.Time
	// ResyncsInFlight is the number of bots currently running a resync.
	ResyncsInFlight int
	// UpdatesRx is the update-stream receive counter of each sampled
	// full-store bot, in a stable order.
	UpdatesRx []uint64
}

// Settled reports whether a step is quiescent at now. Samples must be in
// ascending At order. It is true iff at least SettleFloor has passed since the
// ramp ended, no resync is in flight in the latest sample, and no sampled
// bot's counter grew over the last SettleQuiet. When it is false the reason
// says why; a step still unsettled SettleBound after the ramp ended gets the
// reason InvalidCannotSettle, which the caller must treat as an invalid step.
func Settled(samples []SettleSample, now time.Time) (bool, string) {
	if len(samples) == 0 {
		return false, "no samples"
	}
	last := samples[len(samples)-1]
	elapsed := now.Sub(last.RampEnd)

	ok, reason := settledReason(samples, last, elapsed, now)
	if ok {
		return true, ""
	}
	if elapsed >= SettleBound {
		return false, fmt.Sprintf("%s after %s (%s)", InvalidCannotSettle, SettleBound, reason)
	}
	return false, reason
}

func settledReason(samples []SettleSample, last SettleSample, elapsed time.Duration, now time.Time) (bool, string) {
	// With nothing observed, "no counter grew" would hold vacuously and a
	// mis-wired sampler would pass the gate, letting the ramp leak into the hold.
	if len(last.UpdatesRx) == 0 {
		return false, "no full-store bots sampled"
	}
	if elapsed < SettleFloor {
		return false, fmt.Sprintf("ramp ended %s ago, need %s", elapsed.Round(time.Millisecond), SettleFloor)
	}
	if last.ResyncsInFlight > 0 {
		return false, fmt.Sprintf("%d resync(s) in flight", last.ResyncsInFlight)
	}
	// Baseline: the latest sample at or before now-SettleQuiet.
	cutoff := now.Add(-SettleQuiet)
	base := -1
	for i, s := range samples {
		if s.At.After(cutoff) {
			break
		}
		base = i
	}
	if base < 0 {
		return false, fmt.Sprintf("insufficient history: no sample from %s ago", SettleQuiet)
	}
	if grew(samples[base].UpdatesRx, last.UpdatesRx) {
		return false, fmt.Sprintf("update counters still growing within the last %s", SettleQuiet)
	}
	return true, ""
}

// grew reports whether any counter increased, or the bot set changed.
func grew(base, cur []uint64) bool {
	if len(base) != len(cur) {
		return true
	}
	for i := range cur {
		if cur[i] != base[i] {
			return true
		}
	}
	return false
}
