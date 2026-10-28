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

// EstimateLeg is the design 6.4 runtime bound for one arm whose sweep reaches
// at most knee bots:
//
//	T = 2*knee/joinRate + steps*(settle+40s) + 180s + 30s
//
// The first term is the ramps (a sweep that doubles up to the knee and
// bisects back joins about twice the knee in total). A non-positive joinRate
// returns the largest representable duration.
func EstimateLeg(knee int, joinRate float64, steps int) time.Duration {
	if joinRate <= 0 {
		return time.Duration(1<<63 - 1)
	}
	ramps := time.Duration(2 * float64(knee) / joinRate * float64(time.Second))
	return ramps + time.Duration(steps)*(SettleTypical+HoldDuration) + confirmAndOverload + startAndWarmup
}

// NMax is the largest sweep size whose EstimateLeg still fits legBudget, so a
// leg finishes inside its job timeout. A sweep that reaches it without a
// breach is reported as "ge N_max (time-bound)" by the caller.
//
// predictedKnee must be positive (a prediction of zero means the B-0 model
// produced nothing and there is nothing to cap); it does not otherwise change
// the result, because the time cap depends only on the join rate and the
// fixed per-step costs. It returns 0 when the budget cannot even cover the
// fixed costs, or when the inputs are unusable.
func NMax(predictedKnee int, joinRate float64, legBudget time.Duration, steps int) int {
	if predictedKnee <= 0 || joinRate <= 0 || steps < 0 {
		return 0
	}
	fixed := time.Duration(steps)*(SettleTypical+HoldDuration) + confirmAndOverload + startAndWarmup
	avail := legBudget - fixed
	if avail <= 0 {
		return 0
	}
	n := int(avail.Seconds() * joinRate / 2)
	// Guard float rounding so the returned N always fits the budget.
	for n > 0 && EstimateLeg(n, joinRate, steps) > legBudget {
		n--
	}
	return n
}
