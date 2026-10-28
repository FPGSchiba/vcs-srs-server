package topology

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHelperProcess is the re-exec target for the not-ready test. With
// GO_TOPOLOGY_HELPER=sleep the test binary sleeps and logs nothing, so a
// topology started with it never becomes ready.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_TOPOLOGY_HELPER") != "sleep" {
		return
	}
	time.Sleep(10 * time.Minute)
}

func TestStartKillsProcessesWhenNotReady(t *testing.T) {
	t.Setenv("GO_TOPOLOGY_HELPER", "sleep")
	var mu sync.Mutex
	var cmds []*exec.Cmd
	sp := Spec{
		Binary:       os.Args[0],
		Dir:          t.TempDir(),
		ReadyTimeout: 1 * time.Second,
		argvHook: func(Role, []string) []string {
			return []string{"-test.run=TestHelperProcess"}
		},
		procHook: func(_ Role, c *exec.Cmd) {
			mu.Lock()
			cmds = append(cmds, c)
			mu.Unlock()
		},
	}
	begin := time.Now()
	top, err := Start(context.Background(), sp)
	elapsed := time.Since(begin)
	if err == nil {
		_ = top.Stop()
		t.Fatal("Start succeeded against a process that never logs readiness")
	}
	if top != nil {
		t.Errorf("Start returned a non-nil topology alongside an error")
	}
	if elapsed > 3*time.Second {
		t.Errorf("Start took %s, want < 3s", elapsed)
	}
	if !strings.Contains(err.Error(), "not ready") {
		t.Errorf("error = %v, want it to say \"not ready\"", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(cmds) == 0 {
		t.Fatal("procHook never saw a started process")
	}
	for _, c := range cmds {
		if c.ProcessState == nil {
			t.Errorf("pid %d was not reaped after a failed Start", c.Process.Pid)
		}
	}
}

func TestStopIsIdempotent(t *testing.T) {
	top := &Topology{procs: map[Role]*proc{}}
	if err := top.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := top.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// helperTopology is a Topology that spawns the sleeping helper process.
func helperTopology(t *testing.T, spawned *int) *Topology {
	t.Helper()
	t.Setenv("GO_TOPOLOGY_HELPER", "sleep")
	return &Topology{
		sp: Spec{
			Binary: os.Args[0],
			Dir:    t.TempDir(),
			argvHook: func(Role, []string) []string {
				return []string{"-test.run=TestHelperProcess"}
			},
			procHook: func(Role, *exec.Cmd) { *spawned++ },
		},
		procs: map[Role]*proc{},
		extra: map[string]*proc{},
	}
}

func TestStopIsIdempotentWithLiveProcess(t *testing.T) {
	var spawned int
	top := helperTopology(t, &spawned)
	stop, _, err := top.StartExtraNode(context.Background(), "extra", "")
	if err != nil {
		t.Fatalf("StartExtraNode: %v", err)
	}
	p := top.extra["extra"]
	if err := top.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if p.cmd.ProcessState == nil {
		t.Fatal("process not reaped by first Stop")
	}
	if err := top.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	stop() // the returned stop func must also be safe after Stop
	stop()
}

func TestStartExtraNodeAfterStopSpawnsNothing(t *testing.T) {
	var spawned int
	top := helperTopology(t, &spawned)
	_ = top.Stop()
	if _, _, err := top.StartExtraNode(context.Background(), "late", ""); err == nil {
		t.Fatal("StartExtraNode succeeded after Stop")
	}
	if spawned != 0 {
		t.Fatalf("spawned %d processes after Stop, want 0", spawned)
	}
}

func TestStartExtraNodeHonoursCancelledContext(t *testing.T) {
	var spawned int
	top := helperTopology(t, &spawned)
	defer top.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := top.StartExtraNode(ctx, "x", ""); err == nil {
		t.Fatal("StartExtraNode ignored a cancelled context")
	}
	if spawned != 0 {
		t.Fatalf("spawned %d processes, want 0", spawned)
	}
}
