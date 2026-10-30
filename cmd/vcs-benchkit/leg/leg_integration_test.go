//go:build integration

package leg

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestLegSmoke runs the whole S-arm leg against the real headless server and a
// real vcs-loadbot with a tiny sweep (N <= 32, holds of 2 s). It needs Linux
// (taskset, /proc, ss) and skips unless VCS_SERVER_BIN and LOADBOT_BIN are set.
func TestLegSmoke(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the leg pins with taskset and samples /proc and ss: linux only")
	}
	server, loadbot := os.Getenv("VCS_SERVER_BIN"), os.Getenv("LOADBOT_BIN")
	if server == "" || loadbot == "" {
		t.Skip("set VCS_SERVER_BIN and LOADBOT_BIN to run the leg smoke test")
	}
	if runtime.NumCPU() < 2 {
		t.Skip("needs two cpus: one for the server, one for the generator")
	}
	out := t.TempDir()
	if d := os.Getenv("LEG_SMOKE_OUT"); d != "" {
		out = d // keep the files for inspection
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	err := Run(ctx, Options{
		ServerBin: server, LoadbotBin: loadbot, OutDir: out,
		Scenario: "one-big-net", Arms: "S", Seed: 1, Q6: "smoke",
		ServerSet: "0", GenSet: "1", Topology: "smoke (2 cpus)",
		PredictedKnee: 16, NMaxCap: 32,
		Budget: 0, JoinRate: 27,
		Warmup: 2 * time.Second, Hold: 2 * time.Second, Confirm: 2 * time.Second, Overload: 2 * time.Second,
		CalSteps: "16,32", CalHold: time.Second, Sysctl: false,
	}, os.Stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, f := range []string{"steps.csv", "knees.csv", "calibration.csv", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	// The results directory is uploaded as a public artifact: no passwords and
	// no server data directories may be in it.
	if _, err := os.Stat(filepath.Join(out, "passwords.json")); err == nil {
		t.Error("passwords.json is in the results directory")
	}
	// server-<k>.log.gz is the kept server log (S-T7); a server-<k> directory
	// would be a data directory.
	m, _ := filepath.Glob(filepath.Join(out, "server-*"))
	logs := 0
	for _, f := range m {
		if strings.HasSuffix(f, ".log.gz") {
			logs++
			continue
		}
		t.Errorf("server data directory in the results directory: %v", f)
	}
	if logs == 0 {
		t.Error("no server-<k>.log.gz in the results directory")
	}
	mb, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man struct {
		Pins map[string]PinRecord `json:"pins"`
	}
	if err := json.Unmarshal(mb, &man); err != nil {
		t.Fatal(err)
	}
	if got := man.Pins["server"]; got.CPUs != "0" || got.GOMAXPROCS != 1 || got.Shards != 2 {
		t.Errorf("manifest server pin = %+v, want cpus 0 (the first of the server set), gomaxprocs 1, 2 shards", got)
	}
	fh, err := os.Open(filepath.Join(out, "knees.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	recs, err := csv.NewReader(fh).ReadAll()
	if err != nil || len(recs) != 2 {
		t.Fatalf("knees.csv: %v rows=%d", err, len(recs))
	}
	t.Logf("knees.csv: %v", recs[1])
}

// TestLegSmokeArms starts each arm's real environment (S, Seq and the D2
// topology) with the real headless server and vcs-loadbot, joins a few bots,
// holds 2 s and checks what the environment promises: the logs, the per-role
// samplers and, for D2, the node poller. It needs Linux, four cpus (server set
// 0,1 and generator set 2,3) and a v4 bot; an older bot skips it with
// "bot lacks arm_d2". The multi-arm Run itself is covered by the leg tests of
// the multi-arm leg task.
func TestLegSmokeArms(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the arms pin with taskset and sample /proc and ss: linux only")
	}
	server, loadbot := os.Getenv("VCS_SERVER_BIN"), os.Getenv("LOADBOT_BIN")
	if server == "" || loadbot == "" {
		t.Skip("set VCS_SERVER_BIN and LOADBOT_BIN to run the arms smoke test")
	}
	if runtime.NumCPU() < 4 {
		t.Skip("needs four cpus: server set 0,1 and generator set 2,3")
	}
	feats := ProbeFeatures(loadbot, 10*time.Second)
	if err := RequireFeatures([]string{"D2"}, "one-big-net", feats); err != nil {
		t.Skip(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, arm := range CanonicalArms {
		t.Run(arm, func(t *testing.T) {
			plan, err := PlanArm(arm, "0,1", "2,3")
			if err != nil {
				t.Fatal(err)
			}
			out := t.TempDir()
			env := NewArmEnv(plan, envBase{
				serverBin: server, loadbotBin: loadbot, outDir: out, scratchDir: t.TempDir(),
				scenario: "one-big-net", seed: 1, serverCPUs: []int{0, 1}, features: feats,
			})
			sys, err := env.Start(ctx)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			closed := false
			defer func() {
				if !closed {
					_ = sys.Close()
				}
			}()
			if _, err := sys.SetN(ctx, 16); err != nil {
				t.Fatalf("SetN: %v", err)
			}
			end := sys.BeginSample()
			if _, err := sys.Hold(ctx, 2*time.Second, "smoke"); err != nil {
				t.Fatalf("Hold: %v", err)
			}
			w := end()
			if w.Err != "" {
				t.Errorf("sampler: %s", w.Err)
			}
			text, err := sys.LogText()
			if err != nil || text == "" {
				t.Errorf("LogText = %d bytes, err %v", len(text), err)
			}
			if arm == "D2" {
				if w.Nodes.Polls < 1 || w.Nodes.Min != expectedNodes {
					t.Errorf("node window = %+v, want >=1 poll and %d nodes", w.Nodes, expectedNodes)
				}
				for _, r := range distRoles {
					if !strings.Contains(text, "== "+string(r)+" ==") {
						t.Errorf("no %s section in the D2 log text", r)
					}
				}
				if len(w.ServerCPUByRole) != len(distRoles) {
					t.Errorf("cpu by role = %v, want every role", w.ServerCPUByRole)
				}
			}
			closed = true
			if err := sys.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
			want := "server-1.log.gz"
			if arm == "D2" {
				want = "server-1-nodeA.log.gz"
			}
			if _, err := os.Stat(filepath.Join(out, want)); err != nil {
				t.Errorf("missing %s: %v", want, err)
			}
		})
	}
}
