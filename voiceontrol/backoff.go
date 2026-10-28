package voiceontrol

import (
	"math/rand/v2"
	"time"
)

// nextBackoff returns the wait before reconnect attempt number attempt
// (0-based): min doubled per attempt, capped at max, then passed through
// jitter. It never overflows, however large attempt gets: doubling stops as
// soon as the cap is reached.
func nextBackoff(attempt int, min, max time.Duration, jitter func(time.Duration) time.Duration) time.Duration {
	d := min
	for i := 0; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	if jitter != nil {
		d = jitter(d)
	}
	return d
}

// defaultJitter spreads d downwards to a random value in [0.8·d, d], so a
// jittered backoff never exceeds its cap and a fleet of nodes that lost the
// control node together does not reconnect in lockstep.
func defaultJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	spread := d / 5
	if spread <= 0 {
		return d
	}
	return d - time.Duration(rand.Int64N(int64(spread)+1))
}
