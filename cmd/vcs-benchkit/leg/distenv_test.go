package leg

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
	"github.com/FPGSchiba/vcs-srs-server/internal/topology"
)

// fakeTop is a serverTopology with scripted answers.
type fakeTop struct {
	mu      sync.Mutex
	logs    map[topology.Role]string
	statusF func(n int) (topology.DistributionStatus, error)
	polls   int
	stops   int
}

func (f *fakeTop) Addr(r topology.Role) string {
	return map[topology.Role]string{
		topology.Control: "127.0.0.1:1000", topology.NodeA: "127.0.0.1:2001",
		topology.NodeB: "127.0.0.1:2002", topology.Global: "127.0.0.1:2003",
		topology.Standalone: "127.0.0.1:3000",
	}[r]
}
func (f *fakeTop) PIDs() map[topology.Role]int {
	return map[topology.Role]int{topology.Control: 11, topology.NodeA: 12, topology.NodeB: 13, topology.Global: 14}
}
func (f *fakeTop) Status(context.Context) (topology.DistributionStatus, error) {
	f.mu.Lock()
	f.polls++
	n := f.polls
	f.mu.Unlock()
	return f.statusF(n)
}
func (f *fakeTop) LogText(r topology.Role) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logs[r], nil
}
func (f *fakeTop) Errors() []string { return nil }
func (f *fakeTop) Stop() error      { f.mu.Lock(); f.stops++; f.mu.Unlock(); return nil }

func threeNodes(clientsA, clientsB, clientsG int) topology.DistributionStatus {
	return topology.DistributionStatus{Nodes: []topology.NodeStatus{
		{Address: "127.0.0.1:2001", ConnectedClients: clientsA},
		{Address: "127.0.0.1:2002", ConnectedClients: clientsB},
		{Address: "127.0.0.1:2003", IsGlobal: true, ConnectedClients: clientsG},
	}}
}

func TestNewArmEnvPicksTheEnvironment(t *testing.T) {
	base := envBase{serverBin: "srv", loadbotBin: "bot", outDir: "o", scratchDir: "s", scenario: "one-big-net", seed: 1}
	for _, c := range []struct {
		arm, pin string
		gmp      int
		ncpu     int
	}{{"S", "0", 1, 1}, {"Seq", "0,1", 2, 2}} {
		plan, err := PlanArm(c.arm, "0,1", "2,3")
		if err != nil {
			t.Fatal(err)
		}
		h, ok := NewArmEnv(plan, base).(*hostEnv)
		if !ok {
			t.Fatalf("%s: not a hostEnv", c.arm)
		}
		if h.arm != c.arm || h.serverPin != c.pin || h.serverGOMAXPROCS != c.gmp || h.serverNCPU() != c.ncpu || h.genSet != "2,3" || h.genNCPU != 2 {
			t.Errorf("%s: env = arm %q pin %q gmp %d ncpu %d gen %q/%d", c.arm, h.arm, h.serverPin, h.serverGOMAXPROCS, h.serverNCPU(), h.genSet, h.genNCPU)
		}
	}
	plan, _ := PlanArm("D2", "0,1", "2,3")
	if _, ok := NewArmEnv(plan, base).(*distEnv); !ok {
		t.Error("D2 is not a distEnv")
	}
}

func TestDistEnvStartPinsTheTopologyAndStopsItWhenTheBotFails(t *testing.T) {
	plan, _ := PlanArm("D2", "0,1", "2,3")
	ft := &fakeTop{}
	var got topology.Spec
	old := startTopology
	startTopology = func(_ context.Context, sp topology.Spec) (serverTopology, error) { got = sp; return ft, nil }
	defer func() { startTopology = old }()
	e := NewArmEnv(plan, envBase{serverBin: "srv", loadbotBin: filepath.Join(t.TempDir(), "no-such-bot"), outDir: t.TempDir(), scratchDir: t.TempDir(), scenario: "one-big-net"})
	if _, err := e.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded with no load bot")
	}
	if !reflect.DeepEqual(got.Pin, plan.Server) {
		t.Errorf("Spec.Pin = %v, want the D2 plan %v", got.Pin, plan.Server)
	}
	if got.Binary != "srv" {
		t.Errorf("Spec.Binary = %q", got.Binary)
	}
	if ft.stops != 1 {
		t.Errorf("topology stopped %d times, want 1", ft.stops)
	}
}

func TestDistEnvStartFailureIsReturned(t *testing.T) {
	plan, _ := PlanArm("D2", "0,1", "2,3")
	old := startTopology
	startTopology = func(context.Context, topology.Spec) (serverTopology, error) { return nil, errors.New("no ladder") }
	defer func() { startTopology = old }()
	e := NewArmEnv(plan, envBase{outDir: t.TempDir(), scratchDir: t.TempDir()})
	if _, err := e.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "no ladder") {
		t.Errorf("err = %v", err)
	}
}

func TestDistBotArgvDialsTheNodesNeverGlobal(t *testing.T) {
	plan, _ := PlanArm("D2", "0,1", "2,3")
	e := NewArmEnv(plan, envBase{scenario: "org-op", seed: 9}).(*distEnv)
	got := e.botArgv(&fakeTop{}, "/pw.json")
	want := BotArgv("D2", "org-op", ArmAddrs{Control: "127.0.0.1:1000", Nodes: []string{"127.0.0.1:2001", "127.0.0.1:2002"}}, "/pw.json", 9, e.features, botParamsOr(e.bot))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %v\nwant   %v", got, want)
	}
	j := strings.Join(got, " ")
	if strings.Contains(j, "--voice") || strings.Contains(j, "2003") {
		t.Errorf("D2 argv dials a standalone voice address or global: %s", j)
	}
}

func TestDistServersSampleEveryRole(t *testing.T) {
	plan, _ := PlanArm("D2", "0,1", "2,3")
	e := NewArmEnv(plan, envBase{}).(*distEnv)
	got := e.serverProcs(&fakeTop{})
	want := []serverProc{
		{Role: "control", PID: 11, NCPU: 2},
		{Role: "nodeA", PID: 12, NCPU: 1, VoiceKey: "127.0.0.1:2001"},
		{Role: "nodeB", PID: 13, NCPU: 1, VoiceKey: "127.0.0.1:2002"},
		{Role: "global", PID: 14, NCPU: 2, VoiceKey: "127.0.0.1:2003"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("servers = %+v\nwant     %+v", got, want)
	}
}

func TestDistLogTextConcatenatesTheFourRoleLogs(t *testing.T) {
	ft := &fakeTop{logs: map[topology.Role]string{
		topology.Control: "c\n",
		topology.NodeA:   "Voice shard queue full\nVoice shard queue full\n",
		topology.NodeB:   "Accepted voice HELLO\n",
		topology.Global:  "Voice node deregistered\n",
	}}
	text, err := distLogText(ft)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range distRoles {
		if !strings.Contains(text, "== "+string(r)+" ==\n") {
			t.Errorf("no header for %s in %q", r, text)
		}
	}
	if strings.Index(text, "== control ==") > strings.Index(text, "== nodeA ==") {
		t.Error("roles are not in control, nodeA, nodeB, global order")
	}
	want := LogCounts{ShardQueueFull: 2, HelloAccepted: 1, NodeDeregistered: 1}
	if got := CountLog(text); got != want {
		t.Errorf("CountLog = %+v, want %+v", got, want)
	}
}

func TestSaveRoleLogWritesOneGzPerRole(t *testing.T) {
	dir := t.TempDir()
	p, err := SaveRoleLog(dir, 3, "nodeA", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "server-3-nodeA.log.gz" {
		t.Errorf("name = %s", filepath.Base(p))
	}
	f, _ := os.Open(p)
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(zr)
	if string(b) != "hello" {
		t.Errorf("content = %q", b)
	}
}

func TestNodePollerRecordsTheMinimumAndTheLastClients(t *testing.T) {
	ft := &fakeTop{statusF: func(n int) (topology.DistributionStatus, error) {
		switch n {
		case 1:
			return threeNodes(10, 20, 0), nil
		case 2:
			st := threeNodes(11, 21, 0)
			st.Nodes = st.Nodes[:2] // a node deregistered
			return st, nil
		case 3:
			return topology.DistributionStatus{}, errors.New("boom")
		}
		return threeNodes(12, 22, 0), nil
	}}
	p := &nodePoller{top: ft, every: time.Millisecond}
	stop := p.start()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ft.mu.Lock()
		n := ft.polls
		ft.mu.Unlock()
		if n >= 5 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	w := stop()
	if w.Polls < 4 || w.Min != 2 || w.Errors < 1 {
		t.Fatalf("window = %+v, want >=4 polls, min 2, >=1 error", w)
	}
	if w.Clients["nodeA"] != 12 || w.Clients["nodeB"] != 22 || w.Clients["global"] != 0 {
		t.Errorf("clients = %v, want the last good poll", w.Clients)
	}
}

func TestNodePollerSamplesAShortWindowOnce(t *testing.T) {
	ft := &fakeTop{statusF: func(int) (topology.DistributionStatus, error) { return threeNodes(1, 2, 3), nil }}
	p := &nodePoller{top: ft, every: time.Hour}
	w := p.start()()
	if w.Polls < 1 || w.Min != 3 {
		t.Errorf("window = %+v: a 2 s hold must still get a reading", w)
	}
}

func TestNodeCells(t *testing.T) {
	if got := nodeCells(Row{}); got[0] != "" || got[1] != "" {
		t.Errorf("no polls = %q", got)
	}
	r := Row{Nodes: NodeWindow{Polls: 2, Min: 2, Clients: map[string]int{"nodeB": 5, "nodeA": 4, "global": 0}}}
	got := nodeCells(r)
	if got[0] != "2" || got[1] != "global=0;nodeA=4;nodeB=5" {
		t.Errorf("cells = %q", got)
	}
}

func TestDistSystemCloseKeepsFourRoleLogsAndServerLogTextFallsBack(t *testing.T) {
	t.Setenv("LEG_FAKE_LOADBOT", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := PlanArm("D2", "0,1", "2,3")
	plan.Gen.CPUs = "" // no taskset: this runs on any host
	ft := &fakeTop{logs: map[topology.Role]string{
		topology.Control: "c-line\n", topology.NodeA: "a-line\n", topology.NodeB: "b-line\n", topology.Global: "g-line\n",
	}}
	old := startTopology
	startTopology = func(context.Context, topology.Spec) (serverTopology, error) { return ft, nil }
	defer func() { startTopology = old }()
	out := t.TempDir()
	e := NewArmEnv(plan, envBase{serverBin: "srv", loadbotBin: exe, outDir: out, scratchDir: t.TempDir(), scenario: "one-big-net"})
	sys, err := e.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	live, err := e.(interface{ ServerLogText() (string, error) }).ServerLogText()
	if err != nil || !strings.Contains(live, "== nodeA ==") || !strings.Contains(live, "a-line") {
		t.Fatalf("live ServerLogText = %q, %v", live, err)
	}
	if err := sys.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ft.stops != 1 {
		t.Errorf("topology stopped %d times, want 1", ft.stops)
	}
	for _, r := range distRoles {
		p := filepath.Join(out, "server-1-"+string(r)+".log.gz")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s log not kept: %v", r, err)
		}
	}
	// The topology is gone: the text must now come from the kept copies.
	ft.mu.Lock()
	ft.logs = nil
	ft.mu.Unlock()
	after, err := e.(interface{ ServerLogText() (string, error) }).ServerLogText()
	if err != nil {
		t.Fatalf("ServerLogText after Close: %v", err)
	}
	for _, want := range []string{"== control ==", "c-line", "== nodeA ==", "a-line", "== nodeB ==", "b-line", "== global ==", "g-line"} {
		if !strings.Contains(after, want) {
			t.Errorf("fallback text lacks %q: %q", want, after)
		}
	}
}

func TestNodesUnreachableIsABreachNotAnUnmeasuredHold(t *testing.T) {
	row := Row{Outcome: sweep.Pass}
	noteNodeWindow(&row, NodeWindow{Errors: 3})
	if row.Outcome != sweep.Breach || len(row.Reasons) != 1 || !strings.Contains(row.Reasons[0], "distributionStatus unreachable") {
		t.Errorf("no answer at all: outcome %v reasons %v", row.Outcome, row.Reasons)
	}
	// Some answers: the count is measured, the failures are not a breach.
	row = Row{Outcome: sweep.Pass}
	noteNodeWindow(&row, NodeWindow{Polls: 2, Errors: 1, Min: 3})
	if row.Outcome != sweep.Pass || len(row.Reasons) != 0 {
		t.Errorf("answered polls: outcome %v reasons %v", row.Outcome, row.Reasons)
	}
	// S and Seq poll nothing: not a breach.
	row = Row{Outcome: sweep.Pass}
	noteNodeWindow(&row, NodeWindow{})
	if row.Outcome != sweep.Pass {
		t.Errorf("no poll: outcome %v", row.Outcome)
	}
	// A worse outcome is kept, the reason still added.
	row = Row{Outcome: sweep.Invalid, Reasons: []string{"invalid: x"}}
	noteNodeWindow(&row, NodeWindow{Errors: 1})
	if row.Outcome != sweep.Invalid || len(row.Reasons) != 2 {
		t.Errorf("invalid kept: outcome %v reasons %v", row.Outcome, row.Reasons)
	}
}
