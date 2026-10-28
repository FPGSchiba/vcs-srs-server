package sweep

import (
	"strings"
	"testing"
	"time"
)

func TestPreflight(t *testing.T) {
	cases := []struct {
		name    string
		rng     [2]int
		ulimit  int
		upper   int
		wantErr string
	}{
		// Default Linux range 32768..60999 is 28232 ports; 2*15000 = 30000 > 28232.
		{"default range, upper 15000 fails on ports", [2]int{32768, 60999}, 1 << 20, 15000, "port range"},
		// Usable ceiling on the default range is 28232/2 = 14116 bots; the plan's
		// "15000 passes" example contradicts the design 6.4.1 rule and is wrong.
		{"default range, ceiling 14116 passes", [2]int{32768, 60999}, 1 << 20, 14116, ""},
		{"default range, 14117 fails", [2]int{32768, 60999}, 1 << 20, 14117, "port range"},
		{"default range, upper 14000 passes", [2]int{32768, 60999}, 1 << 20, 14000, ""},
		{"exactly at the port bound passes", [2]int{1, 2000}, 1 << 20, 1000, ""},
		{"one over the port bound fails", [2]int{1, 1999}, 1 << 20, 1000, "port range"},
		// 2*upper*2 + 256 = 4*1000+256 = 4256
		{"fd exactly at bound passes", [2]int{1, 60000}, 4256, 1000, ""},
		{"fd one under bound fails", [2]int{1, 60000}, 4255, 1000, "ulimit -n"},
		{"ulimit 1024 with 15000 fails (wide range)", [2]int{1024, 65535}, 1024, 15000, "ulimit -n"},
		{"inverted range", [2]int{60999, 32768}, 1 << 20, 10, "port range"},
		{"zero upper bound", [2]int{32768, 60999}, 1 << 20, 0, "upper bound"},
		{"negative ulimit", [2]int{32768, 60999}, -1, 10, "ulimit -n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Preflight(c.rng, c.ulimit, c.upper)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, c.wantErr)
			}
		})
	}
}

func TestEstimateLegMatchesDesign(t *testing.T) {
	// Design 6.4: knee 3000 at 27 joins/s, 7 steps -> about 12 min.
	got := EstimateLeg(3000, 27, 7)
	if got < 11*time.Minute || got > 14*time.Minute {
		t.Fatalf("EstimateLeg(3000,27,7) = %v, want about 12 min", got)
	}
	// knee 10k -> about 22 min.
	got = EstimateLeg(10000, 27, 7)
	if got < 20*time.Minute || got > 26*time.Minute {
		t.Fatalf("EstimateLeg(10000,27,7) = %v, want about 22 min", got)
	}
	// knee 1500 -> ramps about 2 min.
	ramps := EstimateLeg(1500, 27, 0) - 210*time.Second
	if ramps < 100*time.Second || ramps > 130*time.Second {
		t.Fatalf("ramps for knee 1500 = %v, want about 2 min", ramps)
	}
}

func TestNMaxFitsBudget(t *testing.T) {
	budget := 120 * time.Minute
	n := NMax(3000, 27, budget, 7)
	if n <= 0 {
		t.Fatalf("NMax = %d, want positive", n)
	}
	if EstimateLeg(n, 27, 7) > budget {
		t.Fatalf("EstimateLeg(NMax=%d) = %v exceeds budget %v", n, EstimateLeg(n, 27, 7), budget)
	}
	if EstimateLeg(n+100, 27, 7) <= budget {
		t.Fatalf("NMax=%d is not tight: n+100 still fits", n)
	}
}

func TestNMaxDegenerate(t *testing.T) {
	cases := []struct {
		name   string
		knee   int
		rate   float64
		budget time.Duration
		steps  int
	}{
		{"budget below fixed cost", 3000, 27, 3 * time.Minute, 7},
		{"zero join rate", 3000, 0, time.Hour, 7},
		{"negative join rate", 3000, -1, time.Hour, 7},
		{"non-positive knee", 0, 27, time.Hour, 7},
		{"steps eat the budget", 3000, 27, 10 * time.Minute, 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NMax(c.knee, c.rate, c.budget, c.steps); got != 0 {
				t.Fatalf("NMax = %d, want 0", got)
			}
		})
	}
}

func TestNMaxCapsBelowPrediction(t *testing.T) {
	// A short budget caps N_max below the predicted knee: the result must be
	// reportable as time-bound by the caller.
	n := NMax(10000, 27, 15*time.Minute, 7)
	if n >= 10000 {
		t.Fatalf("NMax = %d, want below the predicted knee 10000", n)
	}
}

func samples(rampEnd time.Time, specs ...sp) []SettleSample {
	out := make([]SettleSample, len(specs))
	for i, s := range specs {
		out[i] = SettleSample{At: rampEnd.Add(s.at), RampEnd: rampEnd, ResyncsInFlight: s.resync, UpdatesRx: s.rx}
	}
	return out
}

type sp struct {
	at     time.Duration
	resync int
	rx     []uint64
}

func TestSettled(t *testing.T) {
	re := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s := time.Second
	cases := []struct {
		name       string
		samples    []SettleSample
		nowOffset  time.Duration
		want       bool
		wantReason string
	}{
		{"no samples", nil, 10 * s, false, "no samples"},
		{"too early: under 5 s since ramp end", samples(re, sp{0, 0, []uint64{5}}, sp{3 * s, 0, []uint64{5}}), 3 * s, false, "5s"},
		{"resync in flight", samples(re, sp{4 * s, 0, []uint64{5}}, sp{6 * s, 1, []uint64{5}}, sp{8 * s, 0, []uint64{5}}, sp{9 * s, 1, []uint64{5}}), 9 * s, false, "resync"},
		{"counters still growing", samples(re, sp{5 * s, 0, []uint64{5, 5}}, sp{6 * s, 0, []uint64{5, 6}}, sp{7 * s, 0, []uint64{5, 6}}), 7 * s, false, "growing"},
		{"quiescent for exactly 2 s", samples(re, sp{4 * s, 0, []uint64{5, 5}}, sp{5 * s, 0, []uint64{5, 5}}, sp{6 * s, 0, []uint64{5, 5}}), 6 * s, true, ""},
		{"quiescent 2 s after the 5 s floor", samples(re, sp{4 * s, 0, []uint64{5, 5}}, sp{5 * s, 0, []uint64{5, 5}}, sp{7 * s, 0, []uint64{5, 5}}), 7 * s, true, ""},
		{"growth older than the window is ignored", samples(re, sp{5 * s, 0, []uint64{1}}, sp{6 * s, 0, []uint64{9}}, sp{8 * s, 0, []uint64{9}}, sp{9 * s, 0, []uint64{9}}), 9 * s, true, ""},
		{"not enough history for the 2 s window", samples(re, sp{6 * s, 0, []uint64{9}}, sp{7 * s, 0, []uint64{9}}), 7 * s, false, "history"},
		{"no full-store bots sampled is not quiescent", samples(re, sp{5 * s, 0, nil}, sp{6 * s, 0, nil}, sp{8 * s, 0, nil}), 8 * s, false, "no full-store bots sampled"},
		{"bot set changed is growth", samples(re, sp{5 * s, 0, []uint64{9}}, sp{8 * s, 0, []uint64{9, 0}}), 8 * s, false, "growing"},
		{"cannot settle after 60 s", samples(re, sp{58 * s, 0, []uint64{1}}, sp{61 * s, 0, []uint64{2}}), 61 * s, false, "invalid: cannot settle"},
		{"settled just before the bound still passes", samples(re, sp{56 * s, 0, []uint64{3}}, sp{59 * s, 0, []uint64{3}}), 59 * s, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := Settled(c.samples, re.Add(c.nowOffset))
			if got != c.want {
				t.Fatalf("Settled = %v (%q), want %v", got, reason, c.want)
			}
			if !c.want && !strings.Contains(reason, c.wantReason) {
				t.Fatalf("reason = %q, want containing %q", reason, c.wantReason)
			}
			if c.want && reason != "" {
				t.Fatalf("settled but reason = %q", reason)
			}
		})
	}
}

func TestSettledInvalidIsDistinct(t *testing.T) {
	re := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	_, reason := Settled(samples(re, sp{10 * time.Second, 0, []uint64{1}}), re.Add(10*time.Second))
	if strings.HasPrefix(reason, "invalid:") {
		t.Fatalf("a still-settling step inside the bound must not be invalid: %q", reason)
	}
}
