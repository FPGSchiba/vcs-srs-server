package leg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
)

// slog text, as the standalone log has it (S-T0 facts), plus a JSON line and the
// grpc-go logger's format.
const diagLogFixture = `time=2026-10-29T11:00:00.000Z level=INFO msg=Version version=1
time=2026-10-29T11:00:01.000Z level=WARN msg="Voice shard queue full" shard=1
time=2026-10-29T11:00:01.100Z level=WARN msg="Voice shard queue full" shard=0
time=2026-10-29T11:00:01.200Z level=WARN msg="Voice shard queue full" shard=1
time=2026-10-29T11:00:02.000Z level=ERROR msg=boom err="x level=WARN"
time=2026-10-29T11:00:03.000Z level=WARN msg="Update stream fell behind" client=a
time=2026-10-29T11:00:03.500Z level=WARN msg="Update stream fell behind" client=b
{"time":"2026-10-29T11:00:04Z","level":"WARN","msg":"Update stream fell behind","client":"c"}
{"time":"2026-10-29T11:00:05Z","level":"INFO","msg":"fine"}
2026/10/29 11:00:06 WARNING: [transport] [server-transport 0xc000123456] Closing: keepalive ping not acked
2026/10/29 11:00:06 WARNING: [transport] [server-transport 0xc000999999] Closing: keepalive ping not acked
2026/10/29 11:00:06 INFO: [transport] noise
not a log line level=WARN? no
`

func TestLogSummary(t *testing.T) {
	got := LogSummary(strings.ReplaceAll(diagLogFixture, "\n", "\r\n"), 15)
	want := []SummaryEntry{
		{"WARN", "Update stream fell behind", 3},
		{"WARN", "Voice shard queue full", 3},
		{"WARN", "[transport] [server-transport 0x*] Closing: keepalive ping not acked", 2},
		{"ERROR", "boom", 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("summary = %+v\nwant      %+v", got, want)
	}
	if top := LogSummary(diagLogFixture, 2); len(top) != 2 || top[0] != want[0] || top[1] != want[1] {
		t.Errorf("top 2 = %+v", top)
	}
	var many strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&many, "time=x level=WARN msg=m%02d\n", i)
	}
	if n := len(LogSummary(many.String(), 15)); n != 15 {
		t.Errorf("entries = %d, want the top 15", n)
	}
	if s := LogSummary("", 15); len(s) != 0 {
		t.Errorf("empty log: %+v", s)
	}
}

func TestTail(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 250; i++ {
		fmt.Fprintf(&b, "line %d\r\n", i)
	}
	got := Tail(b.String(), 200)
	if len(got) != 200 || got[0] != "line 51" || got[199] != "line 250" {
		t.Errorf("tail = %d lines, first %q last %q", len(got), got[0], got[len(got)-1])
	}
	if got := Tail("a\nb\n", 200); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("short = %q", got)
	}
	if got := Tail("a\r\nb", 1); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("no trailing newline = %q", got)
	}
	if got := Tail("", 5); len(got) != 0 {
		t.Errorf("empty = %q", got)
	}
}

// The artifact holds the gzipped server log and nothing from the scratch dir.
func TestSaveServerLogGz(t *testing.T) {
	scratch, out := t.TempDir(), t.TempDir()
	for _, f := range []string{"passwords.json", "server-1/standalone/config.yaml", "server-1/standalone/api.key"} {
		p := filepath.Join(scratch, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := SaveServerLog(out, 3, diagLogFixture)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "server-3.log.gz" {
		t.Errorf("path = %s", p)
	}
	got, err := readGz(p)
	if err != nil || got != diagLogFixture {
		t.Fatalf("round trip: err=%v equal=%v", err, got == diagLogFixture)
	}
	ents, _ := os.ReadDir(out)
	if len(ents) != 1 || ents[0].Name() != "server-3.log.gz" {
		t.Errorf("out dir = %v, want only server-3.log.gz", ents)
	}
}

type fakeDiagSource struct {
	server, bot       string
	serverErr, botErr error
}

func (f fakeDiagSource) ServerLogText() (string, error) { return f.server, f.serverErr }
func (f fakeDiagSource) BotStderrText() (string, error) { return f.bot, f.botErr }

func controlLostRow() Row {
	ev := ControlLostEvent{Bot: 2, Code: "Unavailable", Message: "error reading from server: connection reset by peer", Phase: "settle", AgeS: 243.1, SinceLastOKPingS: 3.2}
	data := nanData(1336)
	data.ControlLost, data.ControlLostFirst = 1, describeLost(ev)
	out, reasons := sweep.Classify(data, sweep.DefaultBounds)
	return Row{
		Kind: "confirm", N: 1336, Outcome: out, Reasons: reasons, Held: true,
		ControlLost: []ControlLostEvent{ev}, ControlLostCount: 1, ControlLostPhase: "settle",
		Window: Window{TCPOK: true, TCPAbortOnData: 4, EstabResets: 2, OutRsts: 9, SoftnetSqueezed: 5, NetOK: true, SoftnetDropped: 588},
	}
}

func TestDiagnosticsBlock(t *testing.T) {
	var bot strings.Builder
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&bot, "bot %d\n", i)
	}
	var srv strings.Builder
	srv.WriteString("::error::injected\n")
	srv.WriteString(diagLogFixture)
	r := controlLostRow()
	var buf bytes.Buffer
	WriteDiagnostics(&buf, DiagInput{
		Tag: "[leg 2 S one-big-net]", Title: "control lost at confirm N=1336", Status: strings.Join(r.Reasons, "; "),
		Events: r.ControlLost, Window: &r.Window, WindowName: "hold, this window",
		ServerLog: srv.String(), BotStderr: bot.String(),
	})
	golden := filepath.Join("testdata", "diag-control-lost.txt")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	// A Windows checkout with autocrlf turns the golden into CRLF.
	if strings.ReplaceAll(string(want), "\r\n", "\n") != buf.String() {
		t.Errorf("block differs from %s:\n--- got\n%s\n--- want\n%s", golden, buf.String(), want)
	}
	got := buf.String()
	if !strings.HasPrefix(got, "::group::[leg 2 S one-big-net] diagnostics: control lost at confirm N=1336\n") || !strings.HasSuffix(got, "::endgroup::\n") {
		t.Errorf("group markers wrong:\n%s", got)
	}
	// Only the two markers may start a line with "::": a log line must not
	// become a workflow command.
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "::") && !strings.HasPrefix(l, "::group::") && l != "::endgroup::" {
			t.Errorf("log line reads as a workflow command: %q", l)
		}
	}
	if strings.Contains(got, "bot 10\n") {
		t.Error("the bot tail is longer than 50 lines")
	}
}

func TestDiagnosticsBlockUnavailableSources(t *testing.T) {
	var buf bytes.Buffer
	WriteDiagnostics(&buf, DiagInput{Tag: "[t]", Title: "leg failed", Status: "boom", ServerLogErr: "no server was started", BotStderrErr: "no load bot was started"})
	got := buf.String()
	for _, w := range []string{"status: boom", "(none)", "not sampled", "not available: no server was started", "not available: no load bot was started"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
}

func TestControlLostRowPrintsDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	rep := &diagReporter{w: &buf, tag: "[leg 2 S one-big-net]", src: fakeDiagSource{server: diagLogFixture, bot: "bot stderr line\n"}}
	rep.Row(controlLostRow())
	got := buf.String()
	for _, w := range []string{
		"::group::[leg 2 S one-big-net] diagnostics: control lost at confirm N=1336",
		"bot=2 code=Unavailable", "tcp_abort_on_data=4", "3x WARN Update stream fell behind", "bot stderr line", "::endgroup::",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
	// A pass prints nothing.
	buf.Reset()
	rep.Row(Row{Kind: "sweep", N: 10, Outcome: sweep.Pass})
	if buf.Len() != 0 {
		t.Errorf("a pass printed %q", buf.String())
	}
}

func TestBreachRowPrintsSummaryOnly(t *testing.T) {
	for _, oc := range []sweep.StepOutcome{sweep.Breach, sweep.Invalid} {
		var buf bytes.Buffer
		rep := &diagReporter{w: &buf, tag: "[t]", src: fakeDiagSource{server: diagLogFixture, bot: "bot stderr line\n"}}
		rep.Row(Row{Kind: "sweep", N: 972, Outcome: oc})
		got := buf.String()
		if !strings.HasPrefix(got, "::group::[t] diagnostics: "+oc.String()+" at sweep N=972") || !strings.HasSuffix(got, "::endgroup::\n") {
			t.Errorf("%s: group markers wrong:\n%s", oc, got)
		}
		if !strings.Contains(got, "3x WARN Voice shard queue full") {
			t.Errorf("%s: no summary:\n%s", oc, got)
		}
		for _, no := range []string{"last 200 lines", "load bot stderr", "bot stderr line", "tcp counters", "control_lost events"} {
			if strings.Contains(got, no) {
				t.Errorf("%s: summary-only block contains %q", oc, no)
			}
		}
	}
}

// A failed Run prints the whole block before it returns, and the manifest
// carries the error. The options are valid but the run fails early (no /proc on
// a non-Linux host, no load bot binary on Linux): the block must say the logs
// were not available, not crash.
func TestRunFailurePrintsDiagnostics(t *testing.T) {
	out := t.TempDir()
	var buf bytes.Buffer
	err := Run(t.Context(), Options{
		ServerBin: filepath.Join(out, "no-such-server"), LoadbotBin: filepath.Join(out, "no-such-loadbot"), OutDir: out,
		Scenario: "one-big-net", Arms: "S", Seed: 2, ServerSet: "0,1", GenSet: "2,3", Topology: "t", PredictedKnee: 100,
		Warmup: time.Second, Hold: time.Second, Confirm: time.Second, Overload: time.Second, CalSteps: "16", CalHold: time.Second,
	}, &buf)
	if err == nil {
		t.Fatal("Run succeeded without a server or a load bot")
	}
	got := buf.String()
	if !strings.Contains(got, "::group::[leg 2 S one-big-net] diagnostics: leg failed\nstatus: ") || !strings.Contains(got, "::endgroup::") {
		t.Errorf("no diagnostics block:\n%s", got)
	}
	if !strings.Contains(got, "not available: no server was started") {
		t.Errorf("the block does not say the server log is unavailable:\n%s", got)
	}
	b, rerr := os.ReadFile(filepath.Join(out, "S", "manifest.json"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	var m struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &m) != nil || m.Error == "" {
		t.Errorf("manifest.json error = %q", m.Error)
	}
}

func TestHeartbeatLines(t *testing.T) {
	var mu sync.Mutex
	clock := time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC)
	advance := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(15 * time.Second)
		return clock
	}
	samples := []heartbeatSample{
		{ServerCPU: 0.61, GenCPU: 0.4, HelloAccepted: 100, Dead: 0},
		{ServerCPU: math.NaN(), GenCPU: 0.5, HelloAccepted: 160, Dead: 2},
	}
	var lines []string
	i := 0
	h := &heartbeat{
		phase: "ramp", n: 1336, start: clock, prevHello: 40,
		now:    func() time.Time { mu.Lock(); defer mu.Unlock(); return clock },
		sample: func() heartbeatSample { s := samples[i]; i++; return s },
		emit:   func(l string) { lines = append(lines, l) },
	}
	done1 := make(chan struct{}, 2)
	h.emit = func(l string) { lines = append(lines, l); done1 <- struct{}{} }
	tick, stop, done := make(chan time.Time), make(chan struct{}), make(chan struct{})
	go func() { defer close(done); runHeartbeat(stop, tick, h) }()
	tick <- advance()
	<-done1
	tick <- advance()
	<-done1
	close(stop)
	<-done
	want := []string{
		"heartbeat ramp N=1336: elapsed=15s server_cpu=0.61 gen_cpu=0.40 alive=1336 dead=0 hello_accepted+=60",
		"heartbeat ramp N=1336: elapsed=30s server_cpu=- gen_cpu=0.50 alive=1334 dead=2 hello_accepted+=60",
	}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("lines =\n%q\nwant\n%q", lines, want)
	}
}

func TestPeekReadsCPUBetweenReadings(t *testing.T) {
	files := map[string]string{}
	set := func(server, gen int) {
		files["/proc/10/stat"] = fmt.Sprintf("10 (srv) S 1 1 1 0 0 0 0 0 0 0 %d 0 0 0 0 0 1 0 0", server)
		files["/proc/20/stat"] = fmt.Sprintf("20 (bot) S 1 1 1 0 0 0 0 0 0 0 %d 0 0 0 0 0 1 0 0", gen)
	}
	files["/proc/stat"] = "cpu  1 2 3 4 5 6 7 8\ncpu0 1 2 3 4 5 6 7 8\n"
	files["/proc/net/softnet_stat"] = "00000001 00000002 00000003\n"
	files["/proc/net/snmp"] = "Udp: RcvbufErrors SndbufErrors\nUdp: 0 0\nTcp: AttemptFails EstabResets OutRsts\nTcp: 1 2 3\n"
	files["/proc/net/netstat"] = "TcpExt: TCPTimeouts\nTcpExt: 1\n"
	p := &procSampler{
		servers: []serverProc{{Role: "standalone", PID: 10, NCPU: 1, VoiceKey: "127.0.0.1:5002"}},
		genPID:  20, serverCPUs: []int{0}, genNCPU: 1,
		read: func(path string) ([]byte, error) {
			if s, ok := files[path]; ok {
				return []byte(s), nil
			}
			return nil, errors.New("no such file")
		},
		ss: func() ([]byte, error) {
			return []byte("UNCONN 0 0 127.0.0.1:5002 0.0.0.0:*\n\t skmem:(r0,rb1,t0,tb1,f0,w0,o0,bl0,d0)\n"), nil
		},
	}
	set(100, 100)
	w, prev := p.peek(snapshot{})
	if !math.IsNaN(w.ServerCPU) || prev.At.IsZero() {
		t.Fatalf("first reading: w=%+v prev.At=%v, want no CPU figure and a snapshot to continue from", w, prev.At)
	}
	time.Sleep(50 * time.Millisecond)
	set(105, 101)
	w, next := p.peek(prev)
	if math.IsNaN(w.ServerCPU) || math.IsNaN(w.GenCPU) || w.ServerCPU <= w.GenCPU || !next.At.After(prev.At) {
		t.Errorf("w=%+v: want a server CPU above the generator's from 5 vs 1 ticks", w)
	}
	// A failed read keeps prev and reports why.
	delete(files, "/proc/stat")
	w, kept := p.peek(next)
	if w.Err == "" || !math.IsNaN(w.ServerCPU) || !kept.At.Equal(next.At) {
		t.Errorf("failed read: w=%+v kept=%v", w, kept.At)
	}
}

// The diagnostics read the live log while a system runs and the saved copy after.
func TestHostEnvDiagSourcePrefersLiveThenSaved(t *testing.T) {
	out := t.TempDir()
	e := &hostEnv{outDir: out}
	if _, err := e.ServerLogText(); err == nil {
		t.Error("no log was kept, want an error")
	}
	if _, err := e.BotStderrText(); err == nil {
		t.Error("no bot was started, want an error")
	}
	gz, err := SaveServerLog(out, 1, "saved log\n")
	if err != nil {
		t.Fatal(err)
	}
	bot := filepath.Join(out, "loadbot-1.log")
	if err := os.WriteFile(bot, []byte("bot err\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.lastServerGz, e.lastBotLog = gz, bot
	if got, err := e.ServerLogText(); err != nil || got != "saved log\n" {
		t.Errorf("saved log = %q, %v", got, err)
	}
	if got, err := e.BotStderrText(); err != nil || got != "bot err\n" {
		t.Errorf("bot stderr = %q, %v", got, err)
	}
}
