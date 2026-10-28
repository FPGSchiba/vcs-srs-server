package topology

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"gopkg.in/yaml.v3"
)

// TestHelperStandalone is the re-exec target that plays a standalone server.
// GO_TOPOLOGY_HELPER=standalone selects it; GO_TOPOLOGY_PLAN is a comma list
// of "stage=<ms>" (stages grpc, voice, http), where a missing stage is never
// brought up. The ports come from the config path after "--".
func TestHelperStandalone(t *testing.T) {
	if os.Getenv("GO_TOPOLOGY_HELPER") != "standalone" {
		return
	}
	cfgPath := os.Args[len(os.Args)-1]
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Servers struct {
			HTTP    struct{ Port int } `yaml:"http"`
			Control struct{ Port int } `yaml:"control"`
		} `yaml:"servers"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	plan := map[string]time.Duration{}
	for _, kv := range strings.Split(os.Getenv("GO_TOPOLOGY_PLAN"), ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		ms, _ := strconv.Atoi(v)
		plan[k] = time.Duration(ms) * time.Millisecond
	}
	if d, ok := plan["grpc"]; ok {
		go func() {
			time.Sleep(d)
			l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Servers.Control.Port)))
			if err == nil {
				_ = grpc.NewServer().Serve(l)
			}
		}()
	}
	if d, ok := plan["voice"]; ok {
		go func() {
			time.Sleep(d)
			fmt.Println(`time=2026-01-01T00:00:00.000Z level=INFO msg="Voice server started" address=127.0.0.1:1`)
		}()
	}
	if d, ok := plan["http"]; ok {
		go func() {
			time.Sleep(d)
			_ = http.ListenAndServe(net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Servers.HTTP.Port)),
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(`{"data":{"__typename":"Query"}}`))
				}))
		}()
	}
	time.Sleep(10 * time.Minute)
}

func standaloneHelperSpec(t *testing.T, plan string, ready time.Duration, stages *[]string) Spec {
	t.Helper()
	t.Setenv("GO_TOPOLOGY_HELPER", "standalone")
	t.Setenv("GO_TOPOLOGY_PLAN", plan)
	return Spec{
		Binary:       os.Args[0],
		Dir:          t.TempDir(),
		ReadyTimeout: ready,
		argvHook: func(_ Role, argv []string) []string {
			for i, a := range argv {
				if a == "--config" {
					return []string{"-test.run=TestHelperStandalone", "--", argv[i+1]}
				}
			}
			t.Errorf("no --config in %q", argv)
			return argv
		},
		stageHook: func(s string) { *stages = append(*stages, s) },
	}
}

// The helper brings the stages up in the reverse of the ladder's order, so
// only a ladder that really waits for each rung in turn reports them as
// graphql, voice, grpc, and only after the slowest one is up.
func TestStandaloneLadderOrder(t *testing.T) {
	var stages []string
	sp := standaloneHelperSpec(t, "grpc=0,voice=300,http=700", 15*time.Second, &stages)
	begin := time.Now()
	top, err := StartStandalone(context.Background(), sp)
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer top.Stop()
	if elapsed := time.Since(begin); elapsed < 700*time.Millisecond {
		t.Errorf("returned after %s, before the last stage came up at 700ms", elapsed)
	}
	want := []string{"graphql", "voice-started", "grpc-ready"}
	if !reflect.DeepEqual(stages, want) {
		t.Errorf("stages = %v, want %v", stages, want)
	}
	if pid := top.PIDs()[Standalone]; pid <= 0 {
		t.Errorf("PIDs()[Standalone] = %d, want a live pid", pid)
	}
	if got := top.Addr(Standalone); !strings.HasPrefix(got, "127.0.0.1:") {
		t.Errorf("Addr(Standalone) = %q, want the loopback UDP voice address", got)
	}
}

func TestStandaloneLadderTimeouts(t *testing.T) {
	cases := []struct {
		name, plan, stuckAt string
	}{
		{"no http", "grpc=0,voice=0", "graphql"},
		{"no voice line", "grpc=0,http=0", "Voice server started"},
		{"no grpc", "voice=0,http=0", "grpc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stages []string
			sp := standaloneHelperSpec(t, tc.plan, 1500*time.Millisecond, &stages)
			var cmd *exec.Cmd
			sp.procHook = func(_ Role, c *exec.Cmd) { cmd = c }
			begin := time.Now()
			top, err := StartStandalone(context.Background(), sp)
			if err == nil {
				_ = top.Stop()
				t.Fatal("StartStandalone succeeded with a stage missing")
			}
			if top != nil {
				t.Error("non-nil topology alongside an error")
			}
			if elapsed := time.Since(begin); elapsed > 4*time.Second {
				t.Errorf("took %s, want the 1.5s ready timeout to bind it", elapsed)
			}
			if !strings.Contains(err.Error(), "not ready") || !strings.Contains(err.Error(), tc.stuckAt) {
				t.Errorf("err = %v, want \"not ready\" naming %q", err, tc.stuckAt)
			}
			if cmd == nil || cmd.ProcessState == nil {
				t.Error("the server process was not reaped after the failed start")
			}
		})
	}
}

func TestStandaloneRefusesEmptySpec(t *testing.T) {
	if _, err := StartStandalone(context.Background(), Spec{}); err == nil {
		t.Fatal("StartStandalone accepted an empty Spec")
	}
}

// The real server only runs control, HTTP and voice together under
// --mode standalone; --mode voice would try to register with a control.
func TestStandaloneStartsInStandaloneMode(t *testing.T) {
	var stages []string
	sp := standaloneHelperSpec(t, "grpc=0,voice=0,http=0", 15*time.Second, &stages)
	inner := sp.argvHook
	var seen []string
	sp.argvHook = func(r Role, argv []string) []string {
		seen = append([]string(nil), argv...)
		return inner(r, argv)
	}
	top, err := StartStandalone(context.Background(), sp)
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	defer top.Stop()
	if len(seen) < 2 || seen[0] != "--mode" || seen[1] != "standalone" {
		t.Errorf("argv = %q, want it to start with --mode standalone", seen)
	}
}
