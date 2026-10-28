//go:build integration

package distributed

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/internal/topology"
)

// binPath returns the server binary used by the shared topology.
func binPath(t *testing.T) string {
	t.Helper()
	if b := os.Getenv("VCS_SERVER_BIN"); b != "" {
		return b
	}
	t.Skip("own-topology subtests need VCS_SERVER_BIN")
	return ""
}

// nodeID returns the node id the control process reports for role's address.
func nodeID(t *testing.T, tp *topology.Topology, role topology.Role) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := tp.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, n := range st.Nodes {
		if n.Address == tp.Addr(role) {
			return n.ID
		}
	}
	t.Fatalf("no node with address %s in status %+v", tp.Addr(role), st.Nodes)
	return ""
}

// globalRelay runs the A4c flow on tp: Blue and Red dial the global node,
// both tune to mhz, Blue sends 25 frames. It returns Red's received count.
func globalRelay(t *testing.T, tp *topology.Topology, sendMHz float32) (blue, red int) {
	t.Helper()
	b1 := newBotOn(t, tp, "Blue")
	r1 := newBotOn(t, tp, "Red")
	for _, b := range []*bot{b1, r1} {
		b.tune(sendMHz)
		if !b.dialGlobal() {
			t.Fatalf("%s bot HELLO to global node %s not ACKed", b.coalition, b.globalAddr)
		}
	}
	f := freqKHz(sendMHz)
	b1.send(b1.globalAddr, f, seqRange(1, 25)...)
	return len(b1.recv(b1.globalAddr, f, 200*time.Millisecond)), len(r1.recv(r1.globalAddr, f, 1500*time.Millisecond))
}

// A4c: whether the global node relays 249.0 across coalitions depends on the
// global node's OWN YAML (finding F2, configuration layer): each node checks
// IsFrequencyGlobal against its own globalFrequencies (voice/server.go:484,
// :674) and frequencies never travel over VoiceControl.
//
// Both subtests assert OBSERVED behaviour. Intended (Q1 = B): control owns
// the global/test lists and pushes them to every node; clients send/receive
// global voice via the global node using globalAddr; when that lands, the
// `false` subtest flips Red to 25/25.
func TestGlobalNodeRelayDependsOnNodeFrequencyConfig_F2(t *testing.T) {
	t.Run("nodeYAMLHasGlobals=false", func(t *testing.T) {
		// Shared topology: node YAML has globalFrequencies: [].
		if _, red := globalRelay(t, top, 249.0); red != 0 {
			t.Errorf("Red received %d frames via global node, want 0 (F2: observed behaviour)", red)
		}
	})
	t.Run("nodeYAMLHasGlobals=true", func(t *testing.T) {
		tmp := t.TempDir()
		tp, err := topology.Start(context.Background(), topology.Spec{
			Binary:                binPath(t),
			Dir:                   filepath.Join(tmp, "topo"),
			NodeGlobalFrequencies: []float32{249.0},
			Allow:                 allowlist,
		})
		if err != nil {
			if tp != nil {
				_ = tp.Stop()
			}
			t.Fatalf("start own topology: %v", err)
		}
		t.Cleanup(func() {
			_ = tp.Stop()
			if errs := tp.Errors(); len(errs) > 0 {
				t.Errorf("own topology logs contain disallowed lines: %v", errs)
			}
		})

		if _, red := globalRelay(t, tp, 249.0); red != 25 {
			t.Errorf("Red received %d frames via global node, want 25", red)
		}
		// A coalition frequency stays isolated even on the global node.
		if _, red := globalRelay(t, tp, 251.0); red != 0 {
			t.Errorf("Red received %d frames of coalition freq 251.0 via global node, want 0", red)
		}
	})
}

// assignments polls the control status until pred holds or 2s pass, and
// returns the final guid -> nodeId map plus per-node connectedClients.
func assignments(t *testing.T, tp *topology.Topology, pred func(map[string]string) bool) (map[string]string, map[string]int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		st, err := tp.Status(ctx)
		cancel()
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		m := map[string]string{}
		for _, a := range st.ClientAssignments {
			m[a.ClientID] = a.NodeID
		}
		cc := map[string]int{}
		for _, n := range st.Nodes {
			cc[n.ID] = n.ConnectedClients
		}
		if pred(m) || time.Now().After(deadline) {
			return m, cc
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A5 (finding F3): clientAssignments holds ONE node per client. The last
// report wins (voiceontrol/service.go:553) and a disconnect reported by
// either node erases the entry (:564). Observed behaviour, not intended.
func TestClientAssignmentsSingleNode_F3(t *testing.T) {
	t.Run("coalition-and-global", func(t *testing.T) {
		owner := owners(t)
		globalID := nodeID(t, top, topology.Global)
		b := newBot(t, "Blue")
		b.tune(251.0)
		if !b.hello(top.Addr(owner["Blue"]), 2*time.Second) {
			t.Fatalf("HELLO to coalition node not ACKed")
		}
		if !b.dialGlobal() {
			t.Fatalf("HELLO to global node not ACKed")
		}
		m, _ := assignments(t, top, func(m map[string]string) bool { return m[b.guid] == globalID })
		if got, ok := m[b.guid]; !ok || got != globalID {
			t.Errorf("assignment for %s = %q (present=%v), want global node %q", b.guid, got, ok, globalID)
		}
	})
	t.Run("coalition-only", func(t *testing.T) {
		owner := owners(t)
		blue, red := newBot(t, "Blue"), newBot(t, "Red")
		for _, b := range []*bot{blue, red} {
			b.tune(251.0)
			if !b.hello(b.coalitionAddr, 2*time.Second) {
				t.Fatalf("%s HELLO to %s not ACKed", b.coalition, b.coalitionAddr)
			}
		}
		want := map[string]string{
			blue.guid: nodeID(t, top, owner["Blue"]),
			red.guid:  nodeID(t, top, owner["Red"]),
		}
		m, cc := assignments(t, top, func(m map[string]string) bool {
			return m[blue.guid] == want[blue.guid] && m[red.guid] == want[red.guid]
		})
		for g, n := range want {
			if m[g] != n {
				t.Errorf("assignment for %s = %q, want owner node %q", g, m[g], n)
			}
		}
		for _, role := range []topology.Role{owner["Blue"], owner["Red"]} {
			id := nodeID(t, top, role)
			rows := 0
			for _, n := range m {
				if n == id {
					rows++
				}
			}
			if cc[id] < 1 || cc[id] != rows {
				t.Errorf("node %s connectedClients = %d, assignment rows = %d; want equal and >= 1", role, cc[id], rows)
			}
		}
	})
}
