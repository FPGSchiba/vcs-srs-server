package sweep

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// healthy is a step that passes every criterion with margin.
func healthy() StepData {
	return StepData{
		N:         1000,
		LossRatio: 0.0005, LateLossRatio: 0.0002,
		OneWayP99: 5 * time.Millisecond,
		ServerCPU: 0.40,
		GenCPU:    0.30,
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		mut      func(*StepData)
		want     StepOutcome
		contains []string // substrings that must each appear in some reason
	}{
		{"healthy step passes", func(*StepData) {}, Pass, nil},

		// Loss: >= 1 % breaches.
		{"loss just under 1 percent passes", func(s *StepData) { s.LossRatio = 0.0099 }, Pass, nil},
		{"loss exactly 1 percent breaches", func(s *StepData) { s.LossRatio = 0.01 }, Breach, []string{"loss"}},
		{"late loss exactly 1 percent breaches", func(s *StepData) { s.LateLossRatio = 0.01 }, Breach, []string{"late"}},
		{"late loss under threshold passes", func(s *StepData) { s.LateLossRatio = 0.0099 }, Pass, nil},

		// One-way p99: > 50 ms breaches.
		{"one-way p99 exactly 50ms passes", func(s *StepData) { s.OneWayP99 = 50 * time.Millisecond }, Pass, nil},
		{"one-way p99 above 50ms breaches", func(s *StepData) { s.OneWayP99 = 50*time.Millisecond + time.Microsecond }, Breach, []string{"one-way"}},

		// Server process CPU: > 80 % breaches.
		{"cpu exactly 80 percent passes", func(s *StepData) { s.ServerCPU = 0.80 }, Pass, nil},
		{"cpu above 80 percent breaches", func(s *StepData) { s.ServerCPU = 0.81 }, Breach, []string{"cpu"}},

		// Server socket drops: any increase breaches.
		{"one server socket drop breaches", func(s *StepData) { s.ServerSocketDrops = 1 }, Breach, []string{"socket drop"}},

		// ResourceExhausted: hold breaches, ramp and settle are only reported.
		{"hold ResourceExhausted breaches", func(s *StepData) { s.ResourceExhausted.Hold = 1 }, Breach, []string{"ResourceExhausted"}},
		{"ramp ResourceExhausted is not a breach", func(s *StepData) { s.ResourceExhausted.Ramp = 900 }, Pass, nil},
		{"settle ResourceExhausted is not a breach", func(s *StepData) { s.ResourceExhausted.Settle = 12 }, Pass, nil},

		// Log lines.
		{"shard queue full breaches", func(s *StepData) { s.ShardQueueFull = 1 }, Breach, []string{"Voice shard queue full"}},
		{"update stream behind breaches", func(s *StepData) { s.UpdateStreamBehind = 1 }, Breach, []string{"Update stream fell behind"}},
		{"node feed behind breaches", func(s *StepData) { s.NodeFeedBehind = 1 }, Breach, []string{"Voice node feed fell behind"}},
		{"node deregistration breaches (R13)", func(s *StepData) { s.NodeDeregistered = 1 }, Breach, []string{"Voice node deregistered"}},

		// Generator self-health: invalid, never a breach.
		{"generator violation invalidates", func(s *StepData) { s.GeneratorViolations = []string{"rx_lag_p99"} }, Invalid, []string{"rx_lag_p99"}},
		{"several generator violations all named", func(s *StepData) { s.GeneratorViolations = []string{"socket_drops", "gen_cpu"} }, Invalid, []string{"socket_drops", "gen_cpu"}},
		{"kit-measured generator cpu above 85 percent invalidates", func(s *StepData) { s.GenCPU = 0.86 }, Invalid, []string{"generator cpu"}},
		{"generator cpu exactly 85 percent is valid", func(s *StepData) { s.GenCPU = 0.85 }, Pass, nil},
		{"unmeasured generator cpu is invalid", func(s *StepData) { s.GenCPU = -1 }, Invalid, []string{"generator cpu not measured"}},
		{"nan generator cpu is unmeasured and invalid", func(s *StepData) { s.GenCPU = math.NaN() }, Invalid, []string{"generator cpu not measured"}},
		{"zero generator cpu is a measurement", func(s *StepData) { s.GenCPU = 0 }, Pass, nil},
		{"cannot settle invalidates", func(s *StepData) { s.CannotSettle = true }, Invalid, []string{"settle"}},
		{"nan loss is unmeasured and invalid", func(s *StepData) { s.LossRatio = math.NaN() }, Invalid, []string{"loss"}},
		{"nan cpu is unmeasured and invalid", func(s *StepData) { s.ServerCPU = math.NaN() }, Invalid, []string{"cpu"}},

		// A generator that is not healthy cannot attribute a breach to the
		// server, so invalid wins over breach.
		{"invalid masks a simultaneous breach", func(s *StepData) {
			s.GeneratorViolations = []string{"socket_drops"}
			s.LossRatio = 0.5
			s.ServerCPU = 0.99
		}, Invalid, []string{"socket_drops"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := healthy()
			c.mut(&s)
			got, reasons := Classify(s, DefaultBounds)
			if got != c.want {
				t.Fatalf("outcome = %v (%v), want %v", got, reasons, c.want)
			}
			if c.want == Pass && len(reasons) != 0 {
				t.Fatalf("a passing step has no reasons, got %v", reasons)
			}
			if c.want != Pass && len(reasons) == 0 {
				t.Fatalf("a %v step must say why", c.want)
			}
			for _, sub := range c.contains {
				found := false
				for _, r := range reasons {
					if strings.Contains(r, sub) {
						found = true
					}
				}
				if !found {
					t.Errorf("no reason contains %q: %v", sub, reasons)
				}
			}
		})
	}
}

func TestClassifyListsEveryBreachInFixedOrder(t *testing.T) {
	s := healthy()
	s.LossRatio = 0.2
	s.OneWayP99 = time.Second
	s.ServerCPU = 0.95
	s.ShardQueueFull = 3
	_, reasons := Classify(s, DefaultBounds)
	if len(reasons) != 4 {
		t.Fatalf("want 4 reasons, got %v", reasons)
	}
	want := []string{"loss", "one-way", "cpu", "Voice shard queue full"}
	for i, w := range want {
		if !strings.Contains(reasons[i], w) {
			t.Errorf("reason %d = %q, want it to mention %q", i, reasons[i], w)
		}
	}
	// Deterministic: same input, same output.
	_, again := Classify(s, DefaultBounds)
	if !reflect.DeepEqual(reasons, again) {
		t.Fatalf("not deterministic: %v vs %v", reasons, again)
	}
}

func TestClassifyHonoursCustomBounds(t *testing.T) {
	b := DefaultBounds
	b.MaxServerCPU = 0.5
	s := healthy() // cpu 0.40
	if got, _ := Classify(s, b); got != Pass {
		t.Fatalf("0.40 under 0.5 should pass, got %v", got)
	}
	s.ServerCPU = 0.55
	if got, _ := Classify(s, b); got != Breach {
		t.Fatalf("0.55 over 0.5 should breach, got %v", got)
	}
}

func TestStepOutcomeString(t *testing.T) {
	for o, want := range map[StepOutcome]string{Pass: "pass", Breach: "breach", Invalid: "invalid"} {
		if o.String() != want {
			t.Errorf("%d.String() = %q, want %q", int(o), o.String(), want)
		}
	}
	if StepOutcome(99).String() == "" {
		t.Error("unknown outcome must still render")
	}
}

// TestDefaultLateLossBound pins the 1 % late-loss threshold, an implementer
// choice (design 6.4.4 gives no number), so a change is deliberate.
func TestDefaultLateLossBound(t *testing.T) {
	if DefaultBounds.MaxLateLossRatio != 0.01 {
		t.Fatalf("MaxLateLossRatio = %v, want 0.01", DefaultBounds.MaxLateLossRatio)
	}
}

func joinFailStep(cpu float64) StepData {
	return StepData{
		N:             2074,
		LossRatio:     math.NaN(),
		LateLossRatio: math.NaN(),
		ServerCPU:     math.NaN(),
		GenCPU:        math.NaN(),
		JoinFailures:  2,
		JoinAttempted: 1038,
		JoinStages:    map[string]uint64{"hello": 2},
		HelloTriesMax: 5,
		RampGenCPU:    cpu,
	}
}

func TestClassifyJoinFailure(t *testing.T) {
	cases := []struct {
		name string
		cpu  float64
		want StepOutcome
		r0   string
	}{
		{"healthy generator is a breach", 0.40, Breach,
			"join failure: 2 of 1038 new bots failed (hello 2); hello tries max 5"},
		{"hot generator is invalid", 0.95, Invalid,
			"generator limit during the ramp: generator cpu 95% above 85% with 2 join failures"},
		{"unmeasured generator is invalid", math.NaN(), Invalid,
			"invalid: generator cpu not measured during the ramp (2 join failures)"},
		{"negative generator cpu is unmeasured", -1, Invalid,
			"invalid: generator cpu not measured during the ramp (2 join failures)"},
		{"exactly at the bound is a breach", 0.85, Breach,
			"join failure: 2 of 1038 new bots failed (hello 2); hello tries max 5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reasons := Classify(joinFailStep(c.cpu), DefaultBounds)
			if got != c.want {
				t.Fatalf("outcome = %v (%v), want %v", got, reasons, c.want)
			}
			if len(reasons) != 1 || reasons[0] != c.r0 {
				t.Fatalf("reasons = %q, want [%q]", reasons, c.r0)
			}
		})
	}
}

func TestClassifyJoinFailureStagesOrdered(t *testing.T) {
	s := joinFailStep(0.2)
	s.JoinStages = map[string]uint64{"hello": 1, "control": 3, "radio": 0}
	_, reasons := Classify(s, DefaultBounds)
	want := "join failure: 2 of 1038 new bots failed (control 3, hello 1); hello tries max 5"
	if len(reasons) != 1 || reasons[0] != want {
		t.Fatalf("reasons = %q, want [%q]", reasons, want)
	}
}

func TestClassifyJoinFailureWithoutHelloTries(t *testing.T) {
	s := joinFailStep(0.2)
	s.HelloTriesMax = 0
	s.JoinStages = nil
	_, reasons := Classify(s, DefaultBounds)
	want := "join failure: 2 of 1038 new bots failed"
	if len(reasons) != 1 || reasons[0] != want {
		t.Fatalf("reasons = %q, want [%q]", reasons, want)
	}
}
