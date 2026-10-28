package topology

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPinnedArgv(t *testing.T) {
	argv := []string{"--mode", "standalone", "--config", "c.yaml"}
	cases := []struct {
		name     string
		goos     string
		pin      Pin
		wantName string
		wantArgs []string
		wantEnv  []string
		wantErr  string
	}{
		{
			name: "no pin passes through", goos: "linux", pin: Pin{},
			wantName: "/bin/srv", wantArgs: argv,
		},
		{
			name: "no pin passes through on windows", goos: "windows", pin: Pin{},
			wantName: "/bin/srv", wantArgs: argv,
		},
		{
			name: "pinned linux", goos: "linux", pin: Pin{CPUs: "0,1", GOMAXPROCS: 2},
			wantName: "taskset",
			wantArgs: []string{"-c", "0,1", "/bin/srv", "--mode", "standalone", "--config", "c.yaml"},
			wantEnv:  []string{"GOMAXPROCS=2"},
		},
		{
			name: "pinned range", goos: "linux", pin: Pin{CPUs: "2-3", GOMAXPROCS: 2},
			wantName: "taskset",
			wantArgs: []string{"-c", "2-3", "/bin/srv", "--mode", "standalone", "--config", "c.yaml"},
			wantEnv:  []string{"GOMAXPROCS=2"},
		},
		{name: "darwin refused", goos: "darwin", pin: Pin{CPUs: "0", GOMAXPROCS: 1}, wantErr: "linux"},
		{name: "windows refused", goos: "windows", pin: Pin{CPUs: "0", GOMAXPROCS: 1}, wantErr: "linux"},
		{name: "pin needs GOMAXPROCS", goos: "linux", pin: Pin{CPUs: "0,1"}, wantErr: "GOMAXPROCS"},
		{name: "bad cpu list", goos: "linux", pin: Pin{CPUs: "0; rm -rf /", GOMAXPROCS: 1}, wantErr: "cpu list"},
		{name: "empty element", goos: "linux", pin: Pin{CPUs: "0,,1", GOMAXPROCS: 1}, wantErr: "cpu list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, args, env, err := pinnedCommand(tc.goos, "/bin/srv", argv, tc.pin)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if name != tc.wantName {
				t.Errorf("name = %q, want %q", name, tc.wantName)
			}
			if !reflect.DeepEqual(args, tc.wantArgs) {
				t.Errorf("args = %q, want %q", args, tc.wantArgs)
			}
			if !reflect.DeepEqual(env, tc.wantEnv) {
				t.Errorf("env = %q, want %q", env, tc.wantEnv)
			}
		})
	}
}

// The returned args must not alias the caller's argv.
func TestPinnedArgvDoesNotAliasInput(t *testing.T) {
	argv := make([]string, 2, 8)
	argv[0], argv[1] = "a", "b"
	_, args, _, err := pinnedCommand("linux", "/bin/srv", argv, Pin{CPUs: "0", GOMAXPROCS: 1})
	if err != nil {
		t.Fatal(err)
	}
	args[3] = "changed"
	if argv[0] != "a" || argv[1] != "b" {
		t.Fatalf("input argv mutated: %q", argv)
	}
}

func TestShardCountMirrorsServer(t *testing.T) {
	// voice/shard.go: max(minShards=2, min(GOMAXPROCS, maxShards=8)).
	for in, want := range map[int]int{1: 2, 2: 2, 3: 3, 8: 8, 16: 8} {
		if got := ShardCount(in); got != want {
			t.Errorf("ShardCount(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestSpawnRefusesPinOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("the refusal is for non-Linux hosts")
	}
	var spawned int
	top := helperTopology(t, &spawned)
	top.sp.Pin = map[Role]Pin{"extra": {CPUs: "0", GOMAXPROCS: 1}}
	if _, _, err := top.StartExtraNode(t.Context(), "extra", ""); err == nil || !strings.Contains(err.Error(), "linux") {
		t.Fatalf("err = %v, want a linux-only refusal", err)
	}
	if spawned != 0 {
		t.Fatalf("spawned %d processes, want 0", spawned)
	}
}

// The only place a pin must actually reach a process. pinnedCommand is pure;
// this proves spawn feeds it the role's pin and wires both results into the
// exec.Cmd. Linux-only because only Linux has taskset (and CI runs there).
func TestSpawnAppliesPinOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("taskset exists only on linux")
	}
	var spawned int
	top := helperTopology(t, &spawned)
	var cmd *exec.Cmd
	hook := top.sp.procHook
	top.sp.procHook = func(r Role, c *exec.Cmd) { hook(r, c); cmd = c }
	top.sp.Pin = map[Role]Pin{"extra": {CPUs: "0", GOMAXPROCS: 3}}
	if _, _, err := top.StartExtraNode(t.Context(), "extra", ""); err != nil {
		t.Fatalf("StartExtraNode: %v", err)
	}
	t.Cleanup(func() { _ = top.Stop() })
	if cmd == nil {
		t.Fatal("procHook never saw the process")
	}
	if filepath.Base(cmd.Path) != "taskset" {
		t.Errorf("cmd.Path = %q, want it to end in taskset", cmd.Path)
	}
	if len(cmd.Args) < 4 || cmd.Args[1] != "-c" || cmd.Args[2] != "0" || cmd.Args[3] != os.Args[0] {
		t.Errorf("cmd.Args = %q, want taskset -c 0 <binary> ...", cmd.Args)
	}
	if !slices.Contains(cmd.Env, "GOMAXPROCS=3") {
		t.Errorf("cmd.Env lacks GOMAXPROCS=3")
	}
}

// An unpinned role must be left alone: no taskset, no env override.
func TestSpawnLeavesUnpinnedRoleAlone(t *testing.T) {
	var spawned int
	top := helperTopology(t, &spawned)
	var cmd *exec.Cmd
	hook := top.sp.procHook
	top.sp.procHook = func(r Role, c *exec.Cmd) { hook(r, c); cmd = c }
	top.sp.Pin = map[Role]Pin{"other": {CPUs: "0", GOMAXPROCS: 1}}
	if _, _, err := top.StartExtraNode(t.Context(), "extra", ""); err != nil {
		t.Fatalf("StartExtraNode: %v", err)
	}
	t.Cleanup(func() { _ = top.Stop() })
	if filepath.Base(cmd.Path) == "taskset" || cmd.Env != nil {
		t.Errorf("unpinned spawn was altered: path %q, env set %v", cmd.Path, cmd.Env != nil)
	}
}

func TestPinnedRejectsDescendingRange(t *testing.T) {
	for _, cpus := range []string{"3-2", "0,5-4", "1-1,9-0"} {
		_, _, _, err := pinnedCommand("linux", "/bin/srv", nil, Pin{CPUs: cpus, GOMAXPROCS: 1})
		if err == nil || !strings.Contains(err.Error(), "cpu list") {
			t.Errorf("%q: err = %v, want a cpu list error", cpus, err)
		}
	}
	for _, cpus := range []string{"2-2", "0-3", "0,2-3"} {
		if _, _, _, err := pinnedCommand("linux", "/bin/srv", nil, Pin{CPUs: cpus, GOMAXPROCS: 1}); err != nil {
			t.Errorf("%q: unexpected error %v", cpus, err)
		}
	}
}
