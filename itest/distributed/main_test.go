//go:build integration

package distributed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/internal/topology"
)

// top is the shared distributed topology (control, nodeA, nodeB, global).
var top *topology.Topology

// allowlist is the union of the per-test ERROR allowlists. It starts empty:
// any level=ERROR line or race report in a server log fails the run. A line
// is added here only with a comment citing its server source and why it is
// expected.
var allowlist = topology.Allow{}

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	tmp, err := os.MkdirTemp("", "vcs-itest-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "itest: mkdtemp:", err)
		return 1
	}
	defer os.RemoveAll(tmp)

	bin := os.Getenv("VCS_SERVER_BIN")
	if bin == "" {
		name := "vcs-server"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		bin = filepath.Join(tmp, name)
		bctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		err := topology.BuildServer(bctx, bin)
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "itest: build server:", err)
			return 1
		}
	}

	t, err := topology.Start(context.Background(), topology.Spec{
		Binary: bin,
		Dir:    filepath.Join(tmp, "topo"),
		Allow:  allowlist,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "itest: start topology:", err)
		if t != nil {
			_ = t.Stop()
		}
		return 1
	}
	top = t
	// The deferred kill also covers a panicking test.
	defer func() { _ = top.Stop() }()

	code := m.Run()

	// Errors applies the allowlist; in a -race build a "WARNING: DATA RACE"
	// is never allowable.
	if errs := top.Errors(); len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "itest: server logs contain disallowed ERROR or race lines:")
		for _, l := range errs {
			fmt.Fprintln(os.Stderr, "  ", l)
		}
		if code == 0 {
			code = 1
		}
	}
	return code
}
