//go:build integration

package distributed

import (
	"context"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/internal/topology"
)

// A1: two non-global nodes with disjoint coalitions covering {Blue, Red},
// and a global node.
func TestTopologyAssignsDisjointCoalitions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	st, err := top.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	want := map[topology.Role]string{
		topology.NodeA:  top.Addr(topology.NodeA),
		topology.NodeB:  top.Addr(topology.NodeB),
		topology.Global: top.Addr(topology.Global),
	}
	if err := topology.DistributedReady(st, want, []string{"Blue", "Red"}); err != nil {
		t.Fatalf("DistributedReady: %v", err)
	}

	owner, err := top.CoalitionMap(ctx)
	if err != nil {
		t.Fatalf("CoalitionMap: %v", err)
	}
	blue, red := owner["Blue"], owner["Red"]
	for name, r := range map[string]topology.Role{"Blue": blue, "Red": red} {
		if r != topology.NodeA && r != topology.NodeB {
			t.Errorf("%s owner = %q, want a non-global node role", name, r)
		}
	}
	if blue == red {
		t.Errorf("Blue and Red are both on %q, want different nodes", blue)
	}
}

// A2: SyncClient returns the owning node's address, the global address and a
// 43-character voice secret.
func TestSyncClientReturnsOwningNodeAndGlobal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner, err := top.CoalitionMap(ctx)
	if err != nil {
		t.Fatalf("CoalitionMap: %v", err)
	}
	for _, coalition := range []string{"Blue", "Red"} {
		t.Run(coalition, func(t *testing.T) {
			b := newBot(t, coalition)
			if got, want := b.coalitionAddr, top.Addr(owner[coalition]); got != want {
				t.Errorf("coalitionAddr = %q, want %q (owner %s)", got, want, owner[coalition])
			}
			if got, want := b.globalAddr, top.Addr(topology.Global); got != want {
				t.Errorf("globalAddr = %q, want %q", got, want)
			}
			if len(b.secret) != 43 {
				t.Errorf("len(secret) = %d, want 43", len(b.secret))
			}
		})
	}
}
