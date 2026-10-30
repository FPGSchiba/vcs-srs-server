package sweep

import "math"

// JoinModel is the one ramp model shared by the N_max time cap and the leg
// driver's budget guard, so the two can never disagree about how long a ramp
// takes.
//
// Max and Scale are the load bot's join rule: at alive bots the bot joins at
// r = min(Max, Scale/alive), a zero term being left out. Const is the plain
// joins-per-second rate used only when both are zero (a bot without the rule).
// All three zero is an unusable model.
type JoinModel struct {
	Max, Scale, Const float64
}

// ruled reports whether the bot rule (Max and/or Scale) applies.
func (m JoinModel) ruled() bool { return m.Max > 0 || m.Scale > 0 }

// Valid reports whether the model can price a ramp.
func (m JoinModel) Valid() bool { return m.ruled() || m.Const > 0 }

// Rate is the join rate in joins per second with n bots alive. It is 0 for an
// unusable model.
func (m JoinModel) Rate(n int) float64 {
	if !m.ruled() {
		return math.Max(m.Const, 0)
	}
	a := float64(max(n, 1))
	switch {
	case m.Scale <= 0:
		return m.Max
	case m.Max <= 0:
		return m.Scale / a
	}
	return math.Min(m.Max, m.Scale/a)
}

// Seconds is the time to grow the bot count from `from` to `to` joins. Under
// the rule the closed form integrates 1/rate over alive: Max per second up to
// the crossover alive = Scale/Max, and alive/Scale per join after it. Without
// the rule it is the constant rate.
func (m JoinModel) Seconds(from, to int) float64 {
	if to <= from || !m.Valid() {
		return 0
	}
	if !m.ruled() {
		return float64(to-from) / m.Const
	}
	a, b := float64(max(from, 1)), float64(to)
	mx, sc := m.Max, m.Scale
	switch {
	case sc <= 0:
		return (b - a) / mx
	case mx <= 0:
		return (b*b - a*a) / (2 * sc)
	}
	c := sc / mx
	var secs float64
	if a < c {
		secs += (math.Min(b, c) - a) / mx
	}
	if b > c {
		lo := math.Max(a, c)
		secs += (b*b - lo*lo) / (2 * sc)
	}
	return secs
}
