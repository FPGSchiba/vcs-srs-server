package leg

import (
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sample"
)

// clockTicksPerSec is USER_HZ, the unit of /proc/<pid>/stat and /proc/stat.
// It is 100 on every Linux the runners use (getconf CLK_TCK).
const clockTicksPerSec = 100

// snapshot is one 1 s sampler reading.
type snapshot struct {
	At       time.Time
	ServerTk uint64 // server process utime+stime
	GenTk    uint64 // load bot utime+stime
	CPU      map[string]sample.CPUTimes
	Drops    uint64 // server socket d<n>, cumulative
	// Softnet is the sum of softnet_stat "dropped" over all CPUs; UDP is the
	// snmp Udp: counters. NetOK is false when either file could not be read
	// (NetErr says why). The snapshot is kept anyway: a ramp window must not
	// lose its CPU figures to a missing /proc/net file.
	Softnet uint64
	UDP     sample.UDPCounters
	NetOK   bool
	NetErr  string
	// TCP are the TcpExt abort/timeout and Tcp reset counters and SoftnetPerCPU
	// the per-CPU softnet rows. They are informational: a failed read leaves
	// TCPOK false and TCPErr set and never touches NetOK.
	TCP           sample.TCPCounters
	TCPOK         bool
	TCPErr        string
	SoftnetPerCPU []sample.Softnet
}

// windowFrom reduces the readings of one hold to a Window. serverNCPU and
// genNCPU are the sizes of the processes' CPU sets (the CPU fraction's
// denominator); serverCPUs are the CPUs whose softirq is reported. errs are
// sampler failures collected while the snapshots were taken.
func windowFrom(snaps []snapshot, serverNCPU int, serverCPUs []int, genNCPU int, errs []string) Window {
	w := Window{ServerCPU: math.NaN(), GenCPU: math.NaN()}
	if len(errs) > 0 {
		w.Err = strings.Join(errs, "; ")
	}
	if len(snaps) < 2 {
		if w.Err == "" {
			w.Err = "fewer than two sampler readings"
		}
		return w
	}
	first, last := snaps[0], snaps[len(snaps)-1]
	w.Seconds = last.At.Sub(first.At).Seconds()
	if w.Seconds <= 0 {
		w.Err = strings.TrimPrefix(w.Err+"; sampler window has no duration", "; ")
		return w
	}
	frac := func(a, b uint64, ncpu int) float64 {
		if b < a || ncpu <= 0 {
			return math.NaN()
		}
		return float64(b-a) / (clockTicksPerSec * w.Seconds * float64(ncpu))
	}
	w.ServerCPU = frac(first.ServerTk, last.ServerTk, serverNCPU)
	w.GenCPU = frac(first.GenTk, last.GenTk, genNCPU)
	for _, c := range serverCPUs {
		k := "cpu" + strconv.Itoa(c)
		if f, ok := first.CPU[k]; ok {
			if l, ok := last.CPU[k]; ok && l.SoftIRQ >= f.SoftIRQ {
				w.SoftIRQTicks += l.SoftIRQ - f.SoftIRQ
			}
		}
	}
	if f, l := first.CPU["cpu"], last.CPU["cpu"]; l.Steal >= f.Steal {
		w.StealTicks = l.Steal - f.Steal
	}
	for i := 1; i < len(snaps); i++ {
		w.ServerDrops += sample.DropDelta(snaps[i-1].Drops, snaps[i].Drops)
	}
	if first.NetOK && last.NetOK {
		w.NetOK = true
		// softnet's dropped is a 32-bit counter per CPU, so the sum wraps too.
		w.SoftnetDropped = sample.DropDelta(first.Softnet, last.Softnet)
		w.UDPRcvbufErrors = counterDelta(first.UDP.RcvbufErrors, last.UDP.RcvbufErrors)
		w.UDPSndbufErrors = counterDelta(first.UDP.SndbufErrors, last.UDP.SndbufErrors)
	} else {
		w.NetErr = first.NetErr
		if w.NetErr == "" {
			w.NetErr = last.NetErr
		}
		if w.NetErr == "" {
			w.NetErr = "network counters not read"
		}
	}
	w.softnetPerCPU(first.SoftnetPerCPU, last.SoftnetPerCPU)
	w.tcpDeltas(first, last)
	return w
}

// tcpDeltas fills the informational TCP counters from the first and last
// snapshot. A decrease is 0 (counterDelta), and a missing file is TCPErr.
func (w *Window) tcpDeltas(first, last snapshot) {
	if !first.TCPOK || !last.TCPOK {
		w.TCPErr = first.TCPErr
		if w.TCPErr == "" {
			w.TCPErr = last.TCPErr
		}
		if w.TCPErr == "" {
			w.TCPErr = "tcp counters not read"
		}
		return
	}
	w.TCPOK = true
	f, l := first.TCP, last.TCP
	w.TCPAbortOnData = counterDelta(f.AbortOnData, l.AbortOnData)
	w.TCPAbortOnClose = counterDelta(f.AbortOnClose, l.AbortOnClose)
	w.TCPAbortOnMemory = counterDelta(f.AbortOnMemory, l.AbortOnMemory)
	w.TCPAbortOnTimeout = counterDelta(f.AbortOnTimeout, l.AbortOnTimeout)
	w.TCPAbortOnLinger = counterDelta(f.AbortOnLinger, l.AbortOnLinger)
	w.TCPTimeouts = counterDelta(f.Timeouts, l.Timeouts)
	w.EstabResets = counterDelta(f.EstabResets, l.EstabResets)
	w.OutRsts = counterDelta(f.OutRsts, l.OutRsts)
	w.TCPMissing = append([]string(nil), first.TCP.Missing...)
}

// softnetPerCPU fills the per-CPU softnet increases. Both counters are 32-bit
// kernel values, so a wrap is not a decrease. A CPU present in only one reading
// (hotplug) is skipped.
func (w *Window) softnetPerCPU(first, last []sample.Softnet) {
	for i := 0; i < len(first) && i < len(last); i++ {
		c := CPUSoftnet{
			CPU:      i,
			Dropped:  sample.DropDelta(first[i].Dropped, last[i].Dropped),
			Squeezed: sample.DropDelta(first[i].TimeSqueeze, last[i].TimeSqueeze),
		}
		w.SoftnetCPUs = append(w.SoftnetCPUs, c)
		w.SoftnetSqueezed += c.Squeezed
	}
}

// counterDelta is cur - prev of a 64-bit kernel counter; a decrease (a reset)
// is 0, never an underflow that would fake a breach.
func counterDelta(prev, cur uint64) uint64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}

// procSampler reads the counters of one running system.
type procSampler struct {
	serverPID, genPID int
	serverNCPU        int
	serverCPUs        []int
	genNCPU           int
	// voiceKey is the server voice socket's local "addr:port" in ss output.
	voiceKey string
	read     func(path string) ([]byte, error)
	ss       func() ([]byte, error)
}

// take reads one snapshot. A failed read is returned as an error string and
// the snapshot is dropped, so a missing figure is never taken for zero.
func (p *procSampler) take(now time.Time) (snapshot, string) {
	s := snapshot{At: now}
	pidTicks := func(pid int) (uint64, error) {
		b, err := p.read(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			return 0, err
		}
		u, st, err := sample.ParsePIDStat(b)
		return u + st, err
	}
	var err error
	if s.ServerTk, err = pidTicks(p.serverPID); err != nil {
		return s, "server /proc stat: " + err.Error()
	}
	if s.GenTk, err = pidTicks(p.genPID); err != nil {
		return s, "load bot /proc stat: " + err.Error()
	}
	b, err := p.read("/proc/stat")
	if err != nil {
		return s, "/proc/stat: " + err.Error()
	}
	if s.CPU, err = sample.ParseProcStat(b); err != nil {
		return s, err.Error()
	}
	out, err := p.ss()
	if err != nil {
		return s, "ss -uamp: " + err.Error()
	}
	drops, ok := sample.ParseSSDrops(out)[p.voiceKey]
	if !ok {
		return s, fmt.Sprintf("ss -uamp: server socket %s not in the output", p.voiceKey)
	}
	s.Drops = drops
	s.NetOK, s.NetErr = p.takeNet(&s)
	p.takeTCP(&s)
	return s, ""
}

// takeNet reads the host-wide softnet and UDP counters into s. They are
// world-readable. Failure is returned as text, never as zero counters; the
// driver treats it as a sampler error only for a hold.
func (p *procSampler) takeNet(s *snapshot) (bool, string) {
	b, err := p.read("/proc/net/softnet_stat")
	if err != nil {
		return false, "/proc/net/softnet_stat: " + err.Error()
	}
	if s.Softnet, err = sample.ParseSoftnetStat(b); err != nil {
		return false, err.Error()
	}
	if b, err = p.read("/proc/net/snmp"); err != nil {
		return false, "/proc/net/snmp: " + err.Error()
	}
	if s.UDP, err = sample.ParseSNMPUdp(b); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// takeTCP reads the informational TCP counters and the per-CPU softnet rows
// into s. A failure is TCPErr and never an error of the snapshot: these
// counters attribute a lost control connection, they never decide a step.
func (p *procSampler) takeTCP(s *snapshot) {
	if b, err := p.read("/proc/net/softnet_stat"); err == nil {
		s.SoftnetPerCPU, _ = sample.ParseSoftnetPerCPU(b)
	}
	b, err := p.read("/proc/net/netstat")
	if err != nil {
		s.TCPErr = "/proc/net/netstat: " + err.Error()
		return
	}
	ext, err := sample.ParseNetstatTcpExt(b)
	if err != nil {
		s.TCPErr = err.Error()
		return
	}
	if b, err = p.read("/proc/net/snmp"); err != nil {
		s.TCPErr = "/proc/net/snmp: " + err.Error()
		return
	}
	tcp, err := sample.ParseSNMPTcp(b)
	if err != nil {
		s.TCPErr = err.Error()
		return
	}
	tcp.AbortOnData, tcp.AbortOnClose, tcp.AbortOnMemory = ext.AbortOnData, ext.AbortOnClose, ext.AbortOnMemory
	tcp.AbortOnTimeout, tcp.AbortOnLinger, tcp.Timeouts = ext.AbortOnTimeout, ext.AbortOnLinger, ext.Timeouts
	tcp.Missing = ext.Missing
	s.TCP, s.TCPOK = tcp, true
}

// begin starts the 1 s sampler and returns the func that stops it.
func (p *procSampler) begin(interval time.Duration) func() Window {
	var (
		mu    sync.Mutex
		snaps []snapshot
		errs  []string
		seen  = map[string]bool{}
	)
	record := func() {
		s, e := p.take(time.Now())
		mu.Lock()
		defer mu.Unlock()
		if e != "" {
			if !seen[e] {
				seen[e] = true
				errs = append(errs, e)
			}
			return
		}
		snaps = append(snaps, s)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	record()
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				record()
			}
		}
	}()
	return func() Window {
		close(stop)
		<-done
		record() // the window ends at the stop, not at the last tick
		mu.Lock()
		defer mu.Unlock()
		return windowFrom(snaps, p.serverNCPU, p.serverCPUs, p.genNCPU, errs)
	}
}

// RaiseSocketBuffers raises net.core.rmem_max and wmem_max to at least target
// so the generator can enlarge its own sockets. It never lowers a value and
// never touches rmem_default or wmem_default: the server never sets socket
// buffers, so a changed default would change its effective buffers (design
// 6.3). A failure is recorded in the returned map, not returned as an error:
// the leg still runs, and the manifest says what the buffers were.
func RaiseSocketBuffers(read func(string) ([]byte, error), run func(string, ...string) ([]byte, error), target int) map[string]string {
	rec := map[string]string{"target": strconv.Itoa(target)}
	for _, name := range []string{"rmem_max", "wmem_max"} {
		path := "/proc/sys/net/core/" + name
		b, err := read(path)
		if err != nil {
			rec[name+"_error"] = err.Error()
			continue
		}
		cur := strings.TrimSpace(string(b))
		rec[name+"_before"] = cur
		rec[name+"_after"] = cur
		if n, err := strconv.Atoi(cur); err == nil && n >= target {
			continue
		}
		out, err := run("sudo", "-n", "sysctl", "-w", "net.core."+name+"="+strconv.Itoa(target))
		if err != nil {
			rec[name+"_error"] = strings.TrimSpace(err.Error() + ": " + string(out))
			continue
		}
		rec[name+"_after"] = strconv.Itoa(target)
	}
	return rec
}

// runCmd runs a short command and returns its combined output.
func runCmd(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}
