package sweep

import (
	"math"
	"testing"
)

func TestJoinModelSecondsMatchesDriver(t *testing.T) {
	m := JoinModel{Max: 12, Scale: 5000}
	if got := m.Seconds(0, 6026); got < 3648 || got > 3650 {
		t.Errorf("Seconds(0,6026) = %.1f, want about 3649", got)
	}
	if got := m.Seconds(3013, 6026); got < 2722 || got > 2724 {
		t.Errorf("Seconds(3013,6026) = %.1f, want about 2723", got)
	}
	if got := (JoinModel{Const: 27}).Seconds(0, 2700); got != 100 {
		t.Errorf("const Seconds(0,2700) = %v, want 100", got)
	}
	if got := (JoinModel{Max: 12}).Seconds(1, 121); math.Abs(got-10) > 1e-9 {
		t.Errorf("max-only Seconds(1,121) = %v, want 10", got)
	}
	if got := (JoinModel{Scale: 5000}).Seconds(100, 200); math.Abs(got-3) > 1e-9 {
		t.Errorf("scale-only Seconds(100,200) = %v, want (200^2-100^2)/10000 = 3", got)
	}
	if got := m.Seconds(10, 10); got != 0 {
		t.Errorf("empty ramp = %v, want 0", got)
	}
	if got := (JoinModel{}).Seconds(0, 10); got != 0 {
		t.Errorf("unusable model = %v, want 0", got)
	}
}

func TestJoinModelRate(t *testing.T) {
	if got := (JoinModel{Max: 12, Scale: 5000}).Rate(4400); math.Abs(got-1.1364) > 0.001 {
		t.Errorf("Rate(4400) = %v, want about 1.136", got)
	}
	if got := (JoinModel{Max: 12, Scale: 4 * 12000}).Rate(12000); got != 4 {
		t.Errorf("Rate(12000) = %v, want exactly 4", got)
	}
	if got := (JoinModel{Max: 12, Scale: 5000}).Rate(10); got != 12 {
		t.Errorf("Rate(10) = %v, want the Max 12", got)
	}
	if got := (JoinModel{Const: 27}).Rate(999); got != 27 {
		t.Errorf("const Rate = %v, want 27", got)
	}
	if (JoinModel{}).Valid() || !(JoinModel{Const: 1}).Valid() || !(JoinModel{Max: 1}).Valid() {
		t.Error("Valid wrong")
	}
}
