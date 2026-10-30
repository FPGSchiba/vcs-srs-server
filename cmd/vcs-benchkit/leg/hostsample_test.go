package leg

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// multiProc builds a procSampler over a fake /proc and a fake ss. ticks holds
// each pid's utime; drops holds each voice socket's d<n>; a key missing from
// drops is absent from the ss output.
type multiProc struct {
	ticks map[int]uint64
	drops map[string]uint64
}

func (m *multiProc) sampler(servers []serverProc) *procSampler {
	return &procSampler{
		servers: servers, genPID: 99, genNCPU: 1, serverCPUs: []int{0},
		read: func(path string) ([]byte, error) {
			files := map[string]string{
				"/proc/stat":             "cpu  1 2 3 4 5 6 7 8\ncpu0 1 2 3 4 5 6 7 8\n",
				"/proc/net/softnet_stat": "00000001 00000002 00000003\n",
				"/proc/net/snmp":         "Udp: RcvbufErrors SndbufErrors\nUdp: 0 0\nTcp: AttemptFails EstabResets OutRsts\nTcp: 1 2 3\n",
				"/proc/net/netstat":      "TcpExt: TCPTimeouts\nTcpExt: 1\n",
				"/proc/99/stat":          "99 (bot) S 1 1 1 0 0 0 0 0 0 0 0 0 0 0 0 0 1 0 0",
			}
			for pid, tk := range m.ticks {
				files[fmt.Sprintf("/proc/%d/stat", pid)] = fmt.Sprintf("%d (srv) S 1 1 1 0 0 0 0 0 0 0 %d 0 0 0 0 0 1 0 0", pid, tk)
			}
			return fakeProc(files)(path)
		},
		ss: func() ([]byte, error) {
			var b strings.Builder
			for key, d := range m.drops {
				fmt.Fprintf(&b, "UNCONN 0 0 %s 0.0.0.0:*\n\t skmem:(r0,rb1,t0,tb1,f0,w0,o0,bl0,d%d)\n", key, d)
			}
			return []byte(b.String()), nil
		},
	}
}

func twoReadings(t *testing.T, p *procSampler, m *multiProc, step func()) []snapshot {
	t.Helper()
	a, e := p.take(time.Unix(1000, 0))
	if e != "" {
		t.Fatalf("first take: %s", e)
	}
	step()
	b, e := p.take(time.Unix(1010, 0))
	if e != "" {
		t.Fatalf("second take: %s", e)
	}
	return []snapshot{a, b}
}

var distServers = []serverProc{
	{Role: "control", PID: 10, NCPU: 2},
	{Role: "nodeA", PID: 11, NCPU: 1, VoiceKey: "10.0.0.1:5002"},
	{Role: "nodeB", PID: 12, NCPU: 1, VoiceKey: "10.0.0.2:5002"},
	{Role: "global", PID: 13, NCPU: 2, VoiceKey: "10.0.0.3:5002"},
}

func TestServerCPUIsBusiestProcessWithRoleMap(t *testing.T) {
	m := &multiProc{
		ticks: map[int]uint64{10: 0, 11: 0, 12: 0, 13: 0},
		drops: map[string]uint64{"10.0.0.1:5002": 0, "10.0.0.2:5002": 0, "10.0.0.3:5002": 0},
	}
	p := m.sampler(distServers)
	snaps := twoReadings(t, p, m, func() {
		// 10 s at 100 ticks/s: nodeA 900 of 1000 = 0.9; nodeB 300 = 0.3;
		// control 400 over 2 cpus = 0.2; global 100 over 2 cpus = 0.05.
		m.ticks[11], m.ticks[12], m.ticks[10], m.ticks[13] = 900, 300, 400, 100
	})
	w := windowFromServers(snaps, p.servers, p.serverCPUs, p.genNCPU, nil)
	if w.Err != "" {
		t.Fatalf("Err = %q", w.Err)
	}
	if math.Abs(w.ServerCPU-0.9) > 1e-9 {
		t.Errorf("ServerCPU = %v, want 0.9", w.ServerCPU)
	}
	want := map[string]float64{"nodeA": 0.9, "nodeB": 0.3, "control": 0.2, "global": 0.05}
	for role, f := range want {
		if got, ok := w.ServerCPUByRole[role]; !ok || math.Abs(got-f) > 1e-9 {
			t.Errorf("ServerCPUByRole[%s] = %v (present=%v), want %v", role, got, ok, f)
		}
	}
	if len(w.ServerCPUByRole) != len(want) {
		t.Errorf("ServerCPUByRole = %v, want exactly %d roles", w.ServerCPUByRole, len(want))
	}
}

func TestServerDropsSumOverNodeSockets(t *testing.T) {
	m := &multiProc{
		ticks: map[int]uint64{10: 0, 11: 0, 12: 0, 13: 0},
		drops: map[string]uint64{"10.0.0.1:5002": 5, "10.0.0.2:5002": 7, "10.0.0.3:5002": 0},
	}
	p := m.sampler(distServers)
	snaps := twoReadings(t, p, m, func() {
		m.drops["10.0.0.1:5002"], m.drops["10.0.0.2:5002"] = 15, 8 // +10, +1
	})
	w := windowFromServers(snaps, p.servers, p.serverCPUs, p.genNCPU, nil)
	if w.ServerDrops != 11 {
		t.Errorf("ServerDrops = %d, want 11 (nodeA +10, nodeB +1, global +0)", w.ServerDrops)
	}
}

func TestGlobalWithNoTrafficStillNeedsItsSocket(t *testing.T) {
	m := &multiProc{
		ticks: map[int]uint64{10: 0, 11: 0, 12: 0, 13: 0},
		drops: map[string]uint64{"10.0.0.1:5002": 0, "10.0.0.2:5002": 0}, // global absent
	}
	p := m.sampler(distServers)
	if _, e := p.take(time.Unix(1000, 0)); !strings.Contains(e, "global") {
		t.Errorf("take err = %q, want one naming global", e)
	}
}

func TestMissingNodeBSocketNamesNodeB(t *testing.T) {
	m := &multiProc{
		ticks: map[int]uint64{10: 0, 11: 0, 12: 0, 13: 0},
		drops: map[string]uint64{"10.0.0.1:5002": 0, "10.0.0.3:5002": 0},
	}
	p := m.sampler(distServers)
	_, e := p.take(time.Unix(1000, 0))
	if !strings.Contains(e, "nodeB") || !strings.Contains(e, "10.0.0.2:5002") {
		t.Errorf("take err = %q, want one naming nodeB and its socket", e)
	}
}

func TestMissingServerPIDNamesItsRole(t *testing.T) {
	m := &multiProc{
		ticks: map[int]uint64{10: 0, 11: 0, 13: 0}, // nodeB's pid has no /proc entry
		drops: map[string]uint64{"10.0.0.1:5002": 0, "10.0.0.2:5002": 0, "10.0.0.3:5002": 0},
	}
	p := m.sampler(distServers)
	if _, e := p.take(time.Unix(1000, 0)); !strings.Contains(e, "nodeB") {
		t.Errorf("take err = %q, want one naming nodeB", e)
	}
}

// A single standalone process keeps today's output: the same CPU, the same
// drops, no by-role map, and the same error wording.
func TestSingleProcessIsUnchanged(t *testing.T) {
	m := &multiProc{ticks: map[int]uint64{10: 0}, drops: map[string]uint64{"127.0.0.1:5002": 3}}
	p := m.sampler([]serverProc{{Role: "standalone", PID: 10, NCPU: 1, VoiceKey: "127.0.0.1:5002"}})
	snaps := twoReadings(t, p, m, func() { m.ticks[10] = 500; m.drops["127.0.0.1:5002"] = 9 })
	w := windowFromServers(snaps, p.servers, p.serverCPUs, p.genNCPU, nil)
	if math.Abs(w.ServerCPU-0.5) > 1e-9 || w.ServerDrops != 6 {
		t.Errorf("ServerCPU=%v ServerDrops=%d, want 0.5 and 6", w.ServerCPU, w.ServerDrops)
	}
	if w.ServerCPUByRole != nil {
		t.Errorf("ServerCPUByRole = %v, want nil for one process", w.ServerCPUByRole)
	}
	delete(m.drops, "127.0.0.1:5002")
	if _, e := p.take(time.Unix(1020, 0)); e != "ss -uamp: server socket 127.0.0.1:5002 not in the output" {
		t.Errorf("err = %q, want the unchanged single-process wording", e)
	}
	delete(m.ticks, 10)
	if _, e := p.take(time.Unix(1020, 0)); !strings.HasPrefix(e, "server /proc stat: ") {
		t.Errorf("err = %q, want the unchanged single-process prefix", e)
	}
}

// A counter that goes backwards makes the busiest figure NaN, not zero.
func TestBackwardsCounterMakesBusiestNaN(t *testing.T) {
	m := &multiProc{
		ticks: map[int]uint64{10: 500, 11: 500, 12: 500, 13: 500},
		drops: map[string]uint64{"10.0.0.1:5002": 0, "10.0.0.2:5002": 0, "10.0.0.3:5002": 0},
	}
	p := m.sampler(distServers)
	snaps := twoReadings(t, p, m, func() { m.ticks[12] = 100 }) // went backwards
	w := windowFromServers(snaps, p.servers, p.serverCPUs, p.genNCPU, nil)
	if !math.IsNaN(w.ServerCPU) {
		t.Errorf("ServerCPU = %v, want NaN when any process's counter went backwards", w.ServerCPU)
	}
}

// A snapshot whose Procs does not match the server list is an error, never a
// silent zero from the legacy single-process fields.
func TestMismatchedProcsIsAnErrorNotZero(t *testing.T) {
	one := []serverProc{{Role: "a", NCPU: 1}}
	at := time.Unix(1000, 0)
	snaps := []snapshot{
		{At: at, Procs: []procSnap{{Tk: 0}, {Tk: 0}}},
		{At: at.Add(time.Second), Procs: []procSnap{{Tk: 50}, {Tk: 50}}},
	}
	w := windowFromServers(snaps, one, nil, 1, nil)
	if w.Err == "" || !math.IsNaN(w.ServerCPU) {
		t.Errorf("Err = %q ServerCPU = %v, want an error and NaN", w.Err, w.ServerCPU)
	}
	ok := []snapshot{
		{At: at, Procs: []procSnap{{Tk: 0}}},
		{At: at.Add(time.Second), Procs: []procSnap{{Tk: 50}}},
	}
	if w = windowFromServers(ok, one, nil, 1, nil); w.Err != "" {
		t.Errorf("matching single process errored: %q", w.Err)
	}
}
