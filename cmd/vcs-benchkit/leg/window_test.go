package leg

import (
	"math"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sample"
)

func snap(sec int, srv, gen uint64, soft0, soft1, steal uint64, drops uint64) snapshot {
	return snapshot{
		At:       time.Unix(1000+int64(sec), 0),
		ServerTk: srv, GenTk: gen,
		CPU: map[string]sample.CPUTimes{
			"cpu":  {Steal: steal},
			"cpu0": {SoftIRQ: soft0},
			"cpu1": {SoftIRQ: soft1},
			"cpu2": {SoftIRQ: 999},
		},
		Drops: drops,
	}
}

func TestWindowFrom(t *testing.T) {
	// 10 s on a 1-cpu server pin and a 2-cpu generator pin; 100 ticks/s.
	a := snap(0, 1000, 5000, 10, 20, 0, 4)
	b := snap(10, 1500, 7000, 30, 25, 3, 4)
	w := windowFrom([]snapshot{a, b}, 1, []int{0, 1}, 2, nil)
	if w.Err != "" {
		t.Fatalf("Err = %q", w.Err)
	}
	if math.Abs(w.ServerCPU-0.5) > 1e-9 { // 500 ticks over 10 s on 1 cpu
		t.Errorf("ServerCPU = %v", w.ServerCPU)
	}
	if math.Abs(w.GenCPU-1.0) > 1e-9 { // 2000 ticks over 10 s on 2 cpus
		t.Errorf("GenCPU = %v", w.GenCPU)
	}
	if w.SoftIRQTicks != 25 { // cpu0 +20, cpu1 +5; cpu2 is not in the server set
		t.Errorf("SoftIRQTicks = %d", w.SoftIRQTicks)
	}
	if w.StealTicks != 3 || w.Seconds != 10 {
		t.Errorf("steal=%d seconds=%v", w.StealTicks, w.Seconds)
	}
}

func TestWindowFromDropsWrap(t *testing.T) {
	// The kernel's drop counter is 32 bit: a wrap is not a decrease.
	a := snap(0, 0, 0, 0, 0, 0, 0xFFFFFFFE)
	b := snap(1, 0, 0, 0, 0, 0, 0xFFFFFFFE)
	c := snap(2, 0, 0, 0, 0, 0, 3) // wrapped: +5
	w := windowFrom([]snapshot{a, b, c}, 1, []int{0}, 1, nil)
	if w.ServerDrops != 5 {
		t.Errorf("ServerDrops = %d, want 5", w.ServerDrops)
	}
}

func TestWindowFromNeedsTwoSnapshots(t *testing.T) {
	w := windowFrom([]snapshot{snap(0, 1, 1, 0, 0, 0, 0)}, 1, []int{0}, 1, nil)
	if w.Err == "" || !math.IsNaN(w.ServerCPU) || !math.IsNaN(w.GenCPU) {
		t.Errorf("one snapshot must be unmeasured: %+v", w)
	}
}

func TestWindowFromSamplerErrors(t *testing.T) {
	a, b := snap(0, 0, 0, 0, 0, 0, 0), snap(2, 10, 10, 0, 0, 0, 0)
	w := windowFrom([]snapshot{a, b}, 1, []int{0}, 1, []string{"ss: exit status 1"})
	if w.Err != "ss: exit status 1" {
		t.Errorf("Err = %q", w.Err)
	}
	if math.IsNaN(w.ServerCPU) {
		t.Error("a drops-sampler failure must not blank the CPU figures that were read")
	}
}

func netSnap(sec int, soft uint64, udp sample.UDPCounters) snapshot {
	s := snap(sec, 0, 0, 0, 0, 0, 0)
	s.NetOK, s.Softnet, s.UDP = true, soft, udp
	return s
}

func TestWindowFromNetCounters(t *testing.T) {
	a := netSnap(0, 100, sample.UDPCounters{RcvbufErrors: 10, SndbufErrors: 1, InErrors: 12})
	b := netSnap(5, 130, sample.UDPCounters{RcvbufErrors: 40, SndbufErrors: 1, InErrors: 50})
	w := windowFrom([]snapshot{a, b}, 1, []int{0}, 1, nil)
	if !w.NetOK || w.SoftnetDropped != 30 || w.UDPRcvbufErrors != 30 || w.UDPSndbufErrors != 0 {
		t.Errorf("window = %+v", w)
	}
}

func TestWindowFromSoftnetWrap(t *testing.T) {
	// softnet's dropped column is a 32-bit per-CPU counter: the sum wraps like it.
	a := netSnap(0, 0xFFFFFFFE, sample.UDPCounters{})
	b := netSnap(1, 3, sample.UDPCounters{})
	w := windowFrom([]snapshot{a, b}, 1, []int{0}, 1, nil)
	if w.SoftnetDropped != 5 {
		t.Errorf("SoftnetDropped = %d, want 5", w.SoftnetDropped)
	}
}

func TestWindowFromNetUnread(t *testing.T) {
	a, b := snap(0, 0, 0, 0, 0, 0, 0), snap(2, 10, 10, 0, 0, 0, 0)
	b.NetErr = "/proc/net/softnet_stat: no such file"
	w := windowFrom([]snapshot{a, b}, 1, []int{0}, 1, nil)
	if w.NetOK || w.NetErr == "" {
		t.Errorf("an unread counter must not be a zero: %+v", w)
	}
	if w.Err != "" || math.IsNaN(w.ServerCPU) {
		t.Errorf("a net read failure must not invalidate the CPU figures here: %+v", w)
	}
}
