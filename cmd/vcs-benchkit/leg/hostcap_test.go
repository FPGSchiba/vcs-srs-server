package leg

import (
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
)

func TestAnchoredScaleFloorHoldsAtTop(t *testing.T) {
	for _, top := range []int{500, 6026, 11058} {
		m := sweep.JoinModel{Max: 12, Scale: AnchoredScale(5000, 4, top)}
		if r := m.Rate(top); r < 4 {
			t.Errorf("top %d: rate %v < 4", top, r)
		}
	}
	if got := AnchoredScale(5000, 0, 11058); got != 5000 {
		t.Errorf("floor 0 = %v, want 5000", got)
	}
}

func evidenceInput() CapInput {
	return CapInput{
		Model: sweep.JoinModel{Max: 12, Scale: 5000}, Floor: 4, HasRule: true,
		Budget: 97 * time.Minute, Steps: 7,
		PortRange: [2]int{32768, 60999}, UlimitN: 1048576,
		GenCPUs: 12, BotsPerGenCPU: 1500, MemTop: 5529, CalBots: 2047,
	}
}

func TestHostCapEvidenceHost(t *testing.T) {
	c := HostCap(evidenceInput())
	if c.NMax != 5529 || c.By != "memory" || c.Top != 5529 || c.Start != 2047 || c.Scale != 22116 {
		t.Fatalf("caps = %+v", c)
	}
	if c.Limits["time"] < 5529 {
		t.Errorf("time limit %d < 5529", c.Limits["time"])
	}
}

func TestHostCapActionsRunner(t *testing.T) {
	in := evidenceInput()
	in.GenCPUs, in.MemTop = 2, 0
	c := HostCap(in)
	if c.By != "generator-cpu" || c.NMax != 3000 {
		t.Fatalf("caps = %+v", c)
	}
	if _, ok := c.Limits["memory"]; ok {
		t.Error("unknown memory must not be a limit")
	}
}

func TestHostCapOldRuleIsTimeBound(t *testing.T) {
	in := evidenceInput()
	in.Floor = 0
	c := HostCap(in)
	if c.By != "time" || c.NMax >= 5529 || c.Scale != 5000 {
		t.Fatalf("caps = %+v", c)
	}
}

func TestHostCapExplicitIsBinding(t *testing.T) {
	c := HostCap(CapInput{Model: sweep.JoinModel{Const: 27}, Budget: 100 * time.Minute, Steps: 7,
		PortRange: [2]int{32768, 60999}, UlimitN: 65536, Explicit: 32})
	if c.By != "explicit" || c.NMax != 32 {
		t.Errorf("caps = %+v", c)
	}
}
