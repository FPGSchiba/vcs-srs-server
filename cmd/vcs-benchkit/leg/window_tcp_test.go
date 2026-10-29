package leg

import (
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sample"
)

func tcpSnap(sec int, tcp sample.TCPCounters, ok bool, soft []sample.Softnet) snapshot {
	s := netSnap(sec, 0, sample.UDPCounters{})
	s.TCP, s.TCPOK, s.SoftnetPerCPU = tcp, ok, soft
	return s
}

func TestWindowFromTCPCounters(t *testing.T) {
	a := tcpSnap(0, sample.TCPCounters{AbortOnData: 1, AbortOnClose: 2, AbortOnMemory: 3, AbortOnTimeout: 4, AbortOnLinger: 5, Timeouts: 6, EstabResets: 7, OutRsts: 8, Missing: []string{"TCPAbortOnLinger"}}, true,
		[]sample.Softnet{{Dropped: 1, TimeSqueeze: 10}, {Dropped: 0, TimeSqueeze: 0}})
	b := tcpSnap(9, sample.TCPCounters{AbortOnData: 2, AbortOnClose: 4, AbortOnMemory: 6, AbortOnTimeout: 8, AbortOnLinger: 10, Timeouts: 12, EstabResets: 14, OutRsts: 16}, true,
		[]sample.Softnet{{Dropped: 4, TimeSqueeze: 15}, {Dropped: 0, TimeSqueeze: 2}})
	w := windowFrom([]snapshot{a, b}, 1, []int{0}, 1, nil)
	if !w.TCPOK || w.TCPErr != "" {
		t.Fatalf("TCPOK=%v err=%q", w.TCPOK, w.TCPErr)
	}
	got := []uint64{w.TCPAbortOnData, w.TCPAbortOnClose, w.TCPAbortOnMemory, w.TCPAbortOnTimeout, w.TCPAbortOnLinger, w.TCPTimeouts, w.EstabResets, w.OutRsts}
	for i, want := range []uint64{1, 2, 3, 4, 5, 6, 7, 8} {
		if got[i] != want {
			t.Errorf("counter %d = %d, want %d (all %v)", i, got[i], want, got)
		}
	}
	if len(w.TCPMissing) != 1 || w.TCPMissing[0] != "TCPAbortOnLinger" {
		t.Errorf("TCPMissing = %v", w.TCPMissing)
	}
	if w.SoftnetSqueezed != 7 {
		t.Errorf("SoftnetSqueezed = %d, want 7", w.SoftnetSqueezed)
	}
	if len(w.SoftnetCPUs) != 2 || w.SoftnetCPUs[0] != (CPUSoftnet{CPU: 0, Dropped: 3, Squeezed: 5}) || w.SoftnetCPUs[1] != (CPUSoftnet{CPU: 1, Dropped: 0, Squeezed: 2}) {
		t.Errorf("SoftnetCPUs = %+v", w.SoftnetCPUs)
	}
}

// A counter that reads lower at the end (a counter reset) is 0, never an
// underflow that would print as 1.8e19 aborts.
func TestWindowFromTCPCountersResetToZero(t *testing.T) {
	a := tcpSnap(0, sample.TCPCounters{AbortOnData: 100, EstabResets: 50}, true, nil)
	b := tcpSnap(5, sample.TCPCounters{AbortOnData: 3, EstabResets: 60}, true, nil)
	w := windowFrom([]snapshot{a, b}, 1, []int{0}, 1, nil)
	if w.TCPAbortOnData != 0 || w.EstabResets != 10 {
		t.Errorf("AbortOnData=%d EstabResets=%d, want 0 and 10", w.TCPAbortOnData, w.EstabResets)
	}
}

// The TCP counters are informational: an unread file is shown as unread, never as
// zero, and never invalidates the step (Err stays empty, NetOK is unaffected).
func TestWindowFromTCPUnread(t *testing.T) {
	a := tcpSnap(0, sample.TCPCounters{}, false, nil)
	a.TCPErr = "/proc/net/netstat: no such file"
	b := tcpSnap(5, sample.TCPCounters{}, false, nil)
	w := windowFrom([]snapshot{a, b}, 1, []int{0}, 1, nil)
	if w.TCPOK || w.TCPErr == "" {
		t.Errorf("an unread counter must not be a zero: %+v", w)
	}
	if w.Err != "" || w.NetErr != "" || !w.NetOK {
		t.Errorf("TCP read failure must not invalidate or blank the net counters: %+v", w)
	}
}
