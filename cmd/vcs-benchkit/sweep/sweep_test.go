package sweep

import (
	"reflect"
	"testing"
)

// drive runs a Sweep against a scripted outcome per N, returning the sizes it
// asked for and the final result. A size missing from the script fails the
// test: the controller asked for a step the scenario did not expect.
func drive(t *testing.T, s *Sweep, script map[int]StepOutcome) ([]int, Result) {
	t.Helper()
	var asked []int
	for i := 0; i < 64; i++ {
		n, ok := s.Next()
		if !ok {
			return asked, s.Result()
		}
		asked = append(asked, n)
		o, known := script[n]
		if !known {
			t.Fatalf("controller asked for unscripted N=%d (asked so far %v)", n, asked)
		}
		if err := s.Record(o); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatalf("sweep did not terminate: %v", asked)
	return nil, Result{}
}

func TestSweepScripted(t *testing.T) {
	P, B, I := Pass, Breach, Invalid
	cases := []struct {
		name      string
		predicted int
		nMax      int
		script    map[int]StepOutcome
		wantAsked []int
		want      Result
	}{
		{
			name: "knee found by doubling then three bisections", predicted: 4000, nMax: 100000,
			script:    map[int]StepOutcome{1000: P, 2000: P, 4000: B, 3000: P, 3500: B, 3250: P},
			wantAsked: []int{1000, 2000, 4000, 3000, 3500, 3250},
			want:      Result{Kind: KneeFound, N: 3250},
		},
		{
			name: "every bisection passes, knee is just under the breach", predicted: 4000, nMax: 100000,
			script:    map[int]StepOutcome{1000: P, 2000: P, 4000: B, 3000: P, 3500: P, 3750: P},
			wantAsked: []int{1000, 2000, 4000, 3000, 3500, 3750},
			want:      Result{Kind: KneeFound, N: 3750},
		},
		{
			name: "every bisection breaches, knee is the last pass", predicted: 4000, nMax: 100000,
			script:    map[int]StepOutcome{1000: P, 2000: P, 4000: B, 3000: B, 2500: B, 2250: B},
			wantAsked: []int{1000, 2000, 4000, 3000, 2500, 2250},
			want:      Result{Kind: KneeFound, N: 2000},
		},
		{
			name: "generator invalid during doubling is generator-bound at last valid", predicted: 4000, nMax: 100000,
			script:    map[int]StepOutcome{1000: P, 2000: P, 4000: I},
			wantAsked: []int{1000, 2000, 4000},
			want:      Result{Kind: GeneratorBound, N: 2000},
		},
		{
			name: "generator invalid at the first step has no valid step", predicted: 4000, nMax: 100000,
			script:    map[int]StepOutcome{1000: I},
			wantAsked: []int{1000},
			want:      Result{Kind: GeneratorBound, N: 0},
		},
		{
			name: "generator invalid during bisection stops at the last valid step", predicted: 4000, nMax: 100000,
			script:    map[int]StepOutcome{1000: P, 2000: P, 4000: B, 3000: I},
			wantAsked: []int{1000, 2000, 4000, 3000},
			want:      Result{Kind: GeneratorBound, N: 2000},
		},
		{
			name: "no breach up to N_max is time-bound", predicted: 4000, nMax: 3000,
			script:    map[int]StepOutcome{1000: P, 2000: P, 3000: P},
			wantAsked: []int{1000, 2000, 3000},
			want:      Result{Kind: TimeBound, N: 3000},
		},
		{
			name: "breach at N_max is bisected normally", predicted: 4000, nMax: 3000,
			script:    map[int]StepOutcome{1000: P, 2000: P, 3000: B, 2500: P, 2750: B, 2625: P},
			wantAsked: []int{1000, 2000, 3000, 2500, 2750, 2625},
			want:      Result{Kind: KneeFound, N: 2625},
		},
		{
			name: "N_max below the natural start clamps the start", predicted: 4000, nMax: 500,
			script:    map[int]StepOutcome{500: P},
			wantAsked: []int{500},
			want:      Result{Kind: TimeBound, N: 500},
		},
		{
			name: "N_max of zero runs nothing", predicted: 4000, nMax: 0,
			script:    map[int]StepOutcome{},
			wantAsked: nil,
			want:      Result{Kind: TimeBound, N: 0},
		},
		{
			name: "breach at the very first step bisects down from zero", predicted: 400, nMax: 100000,
			script:    map[int]StepOutcome{100: B, 50: P, 75: B, 62: P},
			wantAsked: []int{100, 50, 75, 62},
			want:      Result{Kind: KneeFound, N: 62},
		},
		{
			name: "everything breaches, the knee is below the range", predicted: 400, nMax: 100000,
			script:    map[int]StepOutcome{100: B, 50: B, 25: B, 12: B},
			wantAsked: []int{100, 50, 25, 12},
			want:      Result{Kind: BelowRange, N: 12},
		},
		{
			name: "bisection ends early when integers run out", predicted: 4, nMax: 100000,
			script:    map[int]StepOutcome{1: P, 2: P, 4: B, 3: P},
			wantAsked: []int{1, 2, 4, 3},
			want:      Result{Kind: KneeFound, N: 3},
		},
		{
			name: "predicted knee under 4 still starts at 1", predicted: 2, nMax: 100000,
			script:    map[int]StepOutcome{1: P, 2: B},
			wantAsked: []int{1, 2},
			want:      Result{Kind: KneeFound, N: 1},
		},
		{
			name: "zero prediction still starts at 1", predicted: 0, nMax: 8,
			script:    map[int]StepOutcome{1: P, 2: P, 4: P, 8: P},
			wantAsked: []int{1, 2, 4, 8},
			want:      Result{Kind: TimeBound, N: 8},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := NewSweep(c.predicted, c.nMax)
			asked, got := drive(t, s, c.script)
			if !reflect.DeepEqual(asked, c.wantAsked) {
				t.Errorf("asked %v, want %v", asked, c.wantAsked)
			}
			if got != c.want {
				t.Errorf("result %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestSweepProtocolMisuse(t *testing.T) {
	s := NewSweep(4000, 100000)
	if err := s.Record(Pass); err == nil {
		t.Error("Record before Next must fail")
	}
	n1, _ := s.Next()
	n2, ok := s.Next()
	if !ok || n1 != n2 {
		t.Errorf("Next without Record must repeat the pending step: %d then %d", n1, n2)
	}
	if err := s.Record(StepOutcome(42)); err == nil {
		t.Error("an unknown outcome must be rejected")
	}
	// The rejected outcome must not have consumed the pending step.
	if err := s.Record(Breach); err != nil {
		t.Fatalf("pending step lost after a rejected Record: %v", err)
	}
	// Finish the sweep, then Record must fail again.
	for {
		_, ok := s.Next()
		if !ok {
			break
		}
		_ = s.Record(Breach)
	}
	if err := s.Record(Pass); err == nil {
		t.Error("Record after the sweep finished must fail")
	}
}

func TestSweepHistory(t *testing.T) {
	s := NewSweep(4000, 100000)
	drive(t, s, map[int]StepOutcome{1000: Pass, 2000: Breach, 1500: Pass, 1750: Pass, 1875: Breach})
	want := []StepRecord{
		{1000, Pass}, {2000, Breach}, {1500, Pass}, {1750, Pass}, {1875, Breach},
	}
	if got := s.History(); !reflect.DeepEqual(got, want) {
		t.Errorf("history %v, want %v", got, want)
	}
}

func TestResultString(t *testing.T) {
	for r, want := range map[Result]string{
		{KneeFound, 3250}:      "knee 3250",
		{GeneratorBound, 2000}: "≥ 2000 (generator-bound)",
		{TimeBound, 3000}:      "≥ 3000 (time-bound)",
		{BelowRange, 12}:       "< 12 (below range)",
	} {
		if r.String() != want {
			t.Errorf("%+v.String() = %q, want %q", r, r.String(), want)
		}
	}
}

// TestSweepStop pins how a sweep cut short (the leg's budget guard) is
// reported. Only a sweep still doubling, with no breach seen, is time-bound;
// once a breach brackets the knee the last pass is a knee, coarser than three
// bisections would give but not censored.
func TestSweepStop(t *testing.T) {
	P, B := Pass, Breach
	cases := []struct {
		name    string
		script  []StepOutcome // outcomes of the first steps, from predicted 4000
		want    Result
		wantBrk [2]int
	}{
		{"doubling, no breach yet", []StepOutcome{P, P}, Result{Kind: TimeBound, N: 2000}, [2]int{2000, 0}},
		{"nothing run yet", nil, Result{Kind: TimeBound, N: 0}, [2]int{0, 0}},
		{"breach seen, bisection cut short", []StepOutcome{P, P, B}, Result{Kind: KneeFound, N: 2000}, [2]int{2000, 4000}},
		{"one bisection done", []StepOutcome{P, P, B, P}, Result{Kind: KneeFound, N: 3000}, [2]int{3000, 4000}},
		{"first step breached", []StepOutcome{B}, Result{Kind: BelowRange, N: 1000}, [2]int{0, 1000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSweep(4000, 100000)
			for _, o := range tc.script {
				if _, ok := s.Next(); !ok {
					t.Fatal("sweep finished early")
				}
				if err := s.Record(o); err != nil {
					t.Fatal(err)
				}
			}
			if lo, hi := s.Bracket(); lo != tc.wantBrk[0] || hi != tc.wantBrk[1] {
				t.Errorf("Bracket() = %d, %d; want %v", lo, hi, tc.wantBrk)
			}
			if got := s.Stop(); got != tc.want {
				t.Errorf("Stop() = %v, want %v", got, tc.want)
			}
			if _, ok := s.Next(); ok {
				t.Error("Next after Stop must report done")
			}
			if got := s.Result(); got != tc.want {
				t.Errorf("Result() after Stop = %v, want %v", got, tc.want)
			}
		})
	}
	// Stop on a finished sweep keeps its result.
	s := NewSweep(4000, 1000)
	s.Next()
	_ = s.Record(Pass)
	if got := s.Stop(); got != (Result{Kind: TimeBound, N: 1000}) {
		t.Errorf("Stop on a finished sweep = %v", got)
	}
}
