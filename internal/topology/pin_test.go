package topology

import (
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
