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
