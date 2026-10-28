//go:build integration

package topology

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestStandaloneStartsAndStops runs the real headless server in standalone
// mode through the whole ladder and checks it logs nothing at ERROR.
func TestStandaloneStartsAndStops(t *testing.T) {
	bin := os.Getenv("VCS_SERVER_BIN")
	if bin == "" {
		name := "vcs-server"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		bin = filepath.Join(t.TempDir(), name)
		bctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		err := BuildServer(bctx, bin)
		cancel()
		if err != nil {
			t.Fatalf("BuildServer: %v", err)
		}
	}
	top, err := StartStandalone(context.Background(), Spec{Binary: bin, Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("StartStandalone: %v", err)
	}
	if pid := top.PIDs()[Standalone]; pid <= 0 {
		t.Errorf("PIDs()[Standalone] = %d", pid)
	}
	if errs := top.Errors(); len(errs) > 0 {
		t.Errorf("server logged errors: %v", errs)
	}
	if err := top.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := top.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}
