// Package sweep holds the pure decision logic of the S4 leg driver: the
// port/descriptor pre-flight, the N_max time cap, the quiescent-settle
// predicate and (in sweep.go) the sweep controller. Nothing here touches the
// network, the filesystem or a process.
package sweep

import (
	"fmt"
	"time"
)

// Fixed costs of the leg protocol (design 6.4). They are named so a change to
// the protocol is a one-line change here and in the tests.
const (
	// SettleTypical is the settle time assumed when estimating a leg: the
	// floor is 5 s and design 6.4 budgets about 10 s per step.
	SettleTypical = 10 * time.Second
	// HoldDuration is the measured hold of every step.
	HoldDuration = 40 * time.Second
	// confirmAndOverload is the 120 s confirm plus the 60 s overload.
	confirmAndOverload = 180 * time.Second
	// startAndWarmup is topology start plus warm-up.
	startAndWarmup = 30 * time.Second

	// fdHeadroom is the descriptor headroom beyond two sockets per bot.
	fdHeadroom = 256
)

// Preflight checks the generator's port and descriptor budget for a sweep
// whose largest step is upperBound bots (design 6.4.1).
//
// Each bot holds one TCP and one UDP socket. TCP and UDP draw ephemeral ports
// from the same range but from separate per-protocol tables, so the port
// limit is about the range size per protocol and each bot needs one port of
// each kind: it fails when 2*upperBound exceeds the range size (the design's
// stated rule, deliberately conservative). The descriptor rule is
// 2*upperBound*2 + 256 > ulimitN.
//
// On the default Linux range 32768..60999 (28232 ports) the usable ceiling is
// 14116 bots, not 15000: callers must not assume a 15000-bot upper bound fits.
func Preflight(portRange [2]int, ulimitN, upperBound int) error {
	if upperBound <= 0 {
		return fmt.Errorf("preflight: upper bound %d must be positive", upperBound)
	}
	size := portRange[1] - portRange[0] + 1
	if size <= 0 {
		return fmt.Errorf("preflight: port range %d-%d is empty or inverted", portRange[0], portRange[1])
	}
	if 2*upperBound > size {
		return fmt.Errorf("preflight: port range %d-%d holds %d ports, but 2 x upper bound %d = %d needs more",
			portRange[0], portRange[1], size, upperBound, 2*upperBound)
	}
	if need := 2*upperBound*2 + fdHeadroom; need > ulimitN {
		return fmt.Errorf("preflight: ulimit -n is %d, but 2 x %d bots x 2 sockets + %d headroom = %d needs more",
			ulimitN, upperBound, fdHeadroom, need)
	}
	return nil
}

// EstimateLeg is the runtime bound for one arm whose sweep reaches at most
// knee bots:
//
//	T = 2*R(0,k) + R(k,2k) + steps*(settle+40s) + 180s + 30s
//
// R is the ramp time under the join model m (the rule the bot actually runs).
// A sweep that doubles up to the knee and bisects back joins about twice the
// knee in total (the first two terms), and the overload step then grows the
// bot count from k to 2k (the third). An unusable model returns the largest
// representable duration.
func EstimateLeg(knee int, m JoinModel, steps int) time.Duration {
	if !m.Valid() {
		return maxDuration
	}
	k := max(knee, 0)
	ramps := 2*m.Seconds(0, k) + m.Seconds(k, 2*k)
	fixed := time.Duration(steps)*(SettleTypical+HoldDuration) + confirmAndOverload + startAndWarmup
	if ramps >= (maxDuration-fixed).Seconds() {
		return maxDuration
	}
	return time.Duration(ramps*float64(time.Second)) + fixed
}

const maxDuration = time.Duration(1<<63 - 1)

// NMax is the largest sweep size whose EstimateLeg still fits legBudget, so a
// leg finishes inside its job timeout. A sweep that reaches it without a
// breach is reported as time-bound by the caller. It returns 0 when the budget
// cannot even cover the fixed costs, or when the inputs are unusable.
func NMax(m JoinModel, legBudget time.Duration, steps int) int {
	if !m.Valid() || steps < 0 {
		return 0
	}
	if EstimateLeg(0, m, steps) > legBudget {
		return 0
	}
	// EstimateLeg is non-decreasing in n: find the last n that fits.
	lo, hi := 0, 1<<22
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		if EstimateLeg(mid, m, steps) <= legBudget {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}
