package leg

import (
	"encoding/json"
	"math"
	"reflect"
	"sort"
	"testing"
	"time"
)

func knee(n int) ArmResult { return ArmResult{Result: "knee", Kind: "knee", N: n} }

func TestRatiosOnlyBetweenKnees(t *testing.T) {
	arms := map[string]ArmResult{
		"S":   knee(2000),
		"Seq": knee(2600),
		"D2":  {Result: "≥ 3000 (generator-bound)", Kind: "generator-bound", N: 3000},
	}
	r, notes := Ratios(arms)
	if r["Seq/S"] == nil || math.Abs(*r["Seq/S"]-1.3) > 1e-9 {
		t.Errorf("Seq/S = %v, want 1.3", r["Seq/S"])
	}
	if r["D2/S"] != nil || r["D2/Seq"] != nil {
		t.Errorf("a censored arm has no ratio: D2/S=%v D2/Seq=%v", r["D2/S"], r["D2/Seq"])
	}
	if notes["D2/S"] != "D2 censored" || notes["D2/Seq"] != "D2 censored" {
		t.Errorf("notes = %v", notes)
	}
	if _, ok := notes["Seq/S"]; ok {
		t.Errorf("a computed ratio has no note: %v", notes)
	}
	for _, k := range []string{"Seq/S", "D2/S", "D2/Seq"} {
		if _, ok := r[k]; !ok {
			t.Errorf("ratio key %s missing: the three keys are always present", k)
		}
	}
}

func TestRatiosNameBothCensoredSidesAndMissingAndFailedArms(t *testing.T) {
	r, notes := Ratios(map[string]ArmResult{
		"S":   {Result: "≥ 100 (N_max: memory)", Kind: "memory-bound", N: 100},
		"Seq": {Result: "error", Kind: "error"},
	})
	for k, v := range r {
		if v != nil {
			t.Errorf("%s = %v, want null", k, *v)
		}
	}
	if notes["Seq/S"] != "S censored, Seq error" {
		t.Errorf("Seq/S note = %q", notes["Seq/S"])
	}
	if notes["D2/S"] != "S censored, D2 not run" || notes["D2/Seq"] != "Seq error, D2 not run" {
		t.Errorf("notes = %v", notes)
	}
}

func TestRatiosAllKnees(t *testing.T) {
	r, notes := Ratios(map[string]ArmResult{"S": knee(1000), "Seq": knee(1500), "D2": knee(2500)})
	want := map[string]float64{"Seq/S": 1.5, "D2/S": 2.5, "D2/Seq": 2500.0 / 1500.0}
	for k, w := range want {
		if r[k] == nil || math.Abs(*r[k]-w) > 1e-9 {
			t.Errorf("%s = %v, want %v", k, r[k], w)
		}
	}
	if len(notes) != 0 {
		t.Errorf("notes = %v, want none", notes)
	}
}

func TestRatiosZeroKneeIsNotADivisor(t *testing.T) {
	r, notes := Ratios(map[string]ArmResult{"S": knee(0), "Seq": knee(10)})
	if r["Seq/S"] != nil || notes["Seq/S"] == "" {
		t.Errorf("Seq/S = %v note %q: a zero knee must give null with a note, never Inf", r["Seq/S"], notes["Seq/S"])
	}
}

func TestLegIndexJSONHasExactlyTheLegJSONKeys(t *testing.T) {
	x := 1.3
	b, err := json.Marshal(LegIndex{
		Schema: "leg-index/1", Scenario: "one-big-net", Seed: 1, ArmOrder: []string{"Seq", "S"},
		Arms:       map[string]ArmResult{"S": knee(2000), "Seq": knee(2600)},
		Ratios:     map[string]*float64{"Seq/S": &x, "D2/S": nil, "D2/Seq": nil},
		RatioNotes: map[string]string{"D2/S": "D2 not run"},
		Label:      "relative only", TopologyLabel: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"arm_order", "arms", "label", "ratio_notes", "ratios", "scenario", "schema", "seed", "topology_label"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	var arms map[string]map[string]any
	if err := json.Unmarshal(top["arms"], &arms); err != nil {
		t.Fatal(err)
	}
	var ak []string
	for k := range arms["S"] {
		ak = append(ak, k)
	}
	sort.Strings(ak)
	if !reflect.DeepEqual(ak, []string{"kind", "n", "result"}) {
		t.Errorf("arm keys = %v", ak)
	}
	var ratios map[string]*float64
	if err := json.Unmarshal(top["ratios"], &ratios); err != nil {
		t.Fatal(err)
	}
	if _, ok := ratios["D2/S"]; !ok || ratios["D2/S"] != nil {
		t.Errorf("ratios = %v: a null ratio must be present as null", ratios)
	}
}

func TestHardStopScalesWithTheArms(t *testing.T) {
	if got := HardStop(90*time.Minute, 3); got != 3*(105*time.Minute) {
		t.Errorf("HardStop(90m, 3) = %v, want 5h15m", got)
	}
	if got := HardStop(90*time.Minute, 0); got != 105*time.Minute {
		t.Errorf("HardStop with no arms = %v, want one arm's worth", got)
	}
}
