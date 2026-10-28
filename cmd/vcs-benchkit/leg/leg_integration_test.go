//go:build integration

package leg

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"runtime"
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
