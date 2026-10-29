package sample

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// Fixtures are SYNTHETIC: hand-written from proc_pid_stat(5), proc_stat(5) and
// ss(8), not captured from a runner. These tests prove the parsers match the
// man-page formats, not what ubuntu-24.04 actually emits. ss is the risky one:
// its column spacing, skmem line placement and Netid column vary by version.
// TODO: replace with captures from a probe run (the first S-arm run should save
// raw /proc/<pid>/stat, /proc/stat and `ss -uamp` output as an artifact) before
// the D13 gate. One comm field contains ") (".

const pidStatPlain = "4242 (vcs-server) S 1 4242 4242 0 -1 4194560 12000 0 0 0 1500 320 0 0 20 0 9 0 777 123456789 5000 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 1 0 0 0 0 0\n"

const pidStatTrap = "4243 (a) (b c) R 1 4243 4243 0 -1 4194560 12000 0 0 0 98765 4321 0 0 20 0 9 0 777 123456789 5000 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 1 0 0 0 0 0\n"

const procStatFixture = `cpu  10132153 290696 3084719 46828483 16683 0 25195 0 0 0
cpu0 1393280 64239 381338 5843021 2245 0 5893 7 0 0
cpu1 1380045 62100 379912 5850333 2100 0 6102 9 0 0
intr 1462898
ctxt 9186541
btime 1700000000
processes 26442
procs_running 1
procs_blocked 0
softirq 5124234 0 1100 2 3 4 5 6 7 8 9
`

const ssFixture = `State  Recv-Q Send-Q Local Address:Port  Peer Address:PortProcess
UNCONN 0      0          127.0.0.1:5002       0.0.0.0:*    users:(("vcs-server (x)",pid=4242,fd=7))
	 skmem:(r0,rb1048576,t0,tb212992,f0,w0,o0,bl0,d12)
UNCONN 0      0          127.0.0.1:41000      0.0.0.0:*    users:(("vcs-loadbot",pid=5000,fd=9))
	 skmem:(r4096,rb1048576,t0,tb212992,f0,w0,o0,bl0,d0)
ESTAB  0      0              [::1]:41001        [::1]:5002 users:(("vcs-loadbot",pid=5000,fd=10))
	 skmem:(r0,rb212992,t0,tb212992,f0,w0,o0,bl0,d7)
`

func TestParsePIDStat(t *testing.T) {
	cases := []struct {
		name         string
		in           string
		utime, stime uint64
		wantErr      bool
	}{
		{"plain", pidStatPlain, 1500, 320, false},
		{"comm with spaces and parens", pidStatTrap, 98765, 4321, false},
		{"empty", "", 0, 0, true},
		{"no paren", "1 foo S 1 2 3", 0, 0, true},
		{"truncated after comm", "1 (x) S 1 2 3", 0, 0, true},
		{"non numeric utime", strings.Replace(pidStatPlain, " 1500 ", " zz ", 1), 0, 0, true},
	}
	for _, c := range cases {
		u, s, err := ParsePIDStat([]byte(c.in))
		if (err != nil) != c.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", c.name, err, c.wantErr)
		}
		if err == nil && (u != c.utime || s != c.stime) {
			t.Fatalf("%s: got %d/%d want %d/%d", c.name, u, s, c.utime, c.stime)
		}
	}
}

func TestParseProcStat(t *testing.T) {
	m, err := ParseProcStat([]byte(procStatFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 3 {
		t.Fatalf("want cpu, cpu0, cpu1, got %v", m)
	}
	want := CPUTimes{User: 1393280, Nice: 64239, System: 381338, Idle: 5843021, IOWait: 2245, IRQ: 0, SoftIRQ: 5893, Steal: 7}
	if m["cpu0"] != want {
		t.Fatalf("cpu0 = %+v want %+v", m["cpu0"], want)
	}
	if m["cpu"].SoftIRQ != 25195 || m["cpu"].Steal != 0 {
		t.Fatalf("aggregate = %+v", m["cpu"])
	}
	if m["cpu1"].Steal != 9 {
		t.Fatalf("cpu1 steal = %d", m["cpu1"].Steal)
	}
}

func TestParseProcStatOldKernelShortLine(t *testing.T) {
	// A kernel without the steal column: 7 numbers. Steal is 0, not an error.
	m, err := ParseProcStat([]byte("cpu0 1 2 3 4 5 6 7\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m["cpu0"].SoftIRQ != 7 || m["cpu0"].Steal != 0 {
		t.Fatalf("%+v", m["cpu0"])
	}
}

func TestParseProcStatErrors(t *testing.T) {
	for name, in := range map[string]string{
		"no cpu lines": "intr 1\nctxt 2\n",
		"too short":    "cpu0 1 2 3\n",
		"non numeric":  "cpu0 1 2 3 4 5 6 x 8\n",
	} {
		if _, err := ParseProcStat([]byte(in)); err == nil {
			t.Fatalf("%s: want error", name)
		}
	}
}

func TestParseSSDrops(t *testing.T) {
	got := ParseSSDrops([]byte(ssFixture))
	want := map[string]uint64{
		"127.0.0.1:5002":  12,
		"127.0.0.1:41000": 0,
		"[::1]:41001":     7,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseSSDropsNetidColumnAndNoHeader(t *testing.T) {
	in := "udp UNCONN 0 0 *:9000 *:*\n\t skmem:(r0,rb1,t0,tb1,f0,w0,o0,bl0,d3)\n" +
		"udp UNCONN 0 0 *:9000 *:*\n\t skmem:(r0,rb1,t0,tb1,f0,w0,o0,bl0,d4)\n"
	got := ParseSSDrops([]byte(in))
	if got["*:9000"] != 7 || len(got) != 1 {
		t.Fatalf("duplicate keys must sum: %v", got)
	}
}

func TestParseSSDropsMissingDropField(t *testing.T) {
	// Older ss omits d<n> when zero: the socket is still listed, as 0.
	in := "UNCONN 0 0 127.0.0.1:7 0.0.0.0:*\n\t skmem:(r0,rb1,t0,tb1,f0,w0,o0,bl0)\n"
	got := ParseSSDrops([]byte(in))
	if v, ok := got["127.0.0.1:7"]; !ok || v != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestParseSSDropsGarbage(t *testing.T) {
	if got := ParseSSDrops([]byte("not ss output\n\n")); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestParseSSDropsSingleLine(t *testing.T) {
	// Some ss versions print skmem on the socket line itself.
	in := "UNCONN 0 0 127.0.0.1:41000 0.0.0.0:* users:((\"b\",pid=1,fd=3)) skmem:(r0,rb1,t0,tb1,f0,w0,o0,bl0,d5)\n" +
		"UNCONN 0 0 127.0.0.1:41001 0.0.0.0:* skmem:(r0,rb1,t0,tb1,f0,w0,o0,bl0,d0)\n"
	got := ParseSSDrops([]byte(in))
	want := map[string]uint64{"127.0.0.1:41000": 5, "127.0.0.1:41001": 0}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestDropDelta(t *testing.T) {
	cases := []struct{ prev, cur, want uint64 }{
		{10, 25, 15},
		{7, 7, 0},
		{math.MaxUint32 - 2, 3, 6}, // d<n> is a 32-bit counter and wrapped
		{math.MaxUint32, 0, 1},
	}
	for _, c := range cases {
		if got := DropDelta(c.prev, c.cur); got != c.want {
			t.Fatalf("DropDelta(%d,%d)=%d want %d", c.prev, c.cur, got, c.want)
		}
	}
}

// Synthetic, hand-written from the kernel's softnet_seq_show (net/core/net-procfs.c)
// and snmp(7)-style layout: not captured from a runner. Columns are hex; the
// second column is the packets dropped because a CPU's backlog was full.
const softnetFixture = `0000a1b2 00000010 00000002 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000
0000ffff 000000ff 00000001 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000
`

func TestParseSoftnetStat(t *testing.T) {
	got, err := ParseSoftnetStat([]byte(softnetFixture))
	if err != nil {
		t.Fatal(err)
	}
	if got != 0x10+0xff {
		t.Errorf("dropped = %d, want %d", got, 0x10+0xff)
	}
	for _, bad := range []string{"", "\n", "0000a1b2\n", "0000a1b2 zz 00\n"} {
		if _, err := ParseSoftnetStat([]byte(bad)); err == nil {
			t.Errorf("ParseSoftnetStat(%q) = nil error, want one", bad)
		}
	}
}

const snmpFixture = `Ip: Forwarding DefaultTTL InReceives
Ip: 1 64 999
Udp: InDatagrams NoPorts InErrors OutDatagrams RcvbufErrors SndbufErrors InCsumErrors IgnoredMulti MemErrors
Udp: 5000 6 17 4000 11 3 0 0 0
UdpLite: InDatagrams NoPorts InErrors OutDatagrams RcvbufErrors SndbufErrors InCsumErrors IgnoredMulti MemErrors
UdpLite: 0 0 0 0 0 0 0 0 0
`

func TestParseSNMPUdp(t *testing.T) {
	got, err := ParseSNMPUdp([]byte(snmpFixture))
	if err != nil {
		t.Fatal(err)
	}
	if got != (UDPCounters{RcvbufErrors: 11, SndbufErrors: 3, InErrors: 17}) {
		t.Errorf("got %+v", got)
	}
	for name, bad := range map[string]string{
		"empty":          "",
		"no Udp lines":   "Ip: A B\nIp: 1 2\n",
		"header only":    "Udp: InErrors RcvbufErrors SndbufErrors\n",
		"missing column": "Udp: InErrors RcvbufErrors\nUdp: 1 2\n",
		"bad number":     "Udp: InErrors RcvbufErrors SndbufErrors\nUdp: 1 x 3\n",
		"short values":   "Udp: InErrors RcvbufErrors SndbufErrors\nUdp: 1 2\n",
	} {
		if _, err := ParseSNMPUdp([]byte(bad)); err == nil {
			t.Errorf("%s: nil error, want one", name)
		}
	}
}
