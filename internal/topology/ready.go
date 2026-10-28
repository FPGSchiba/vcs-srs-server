package topology

import (
	"fmt"
	"sort"
)

// NodeStatus mirrors the GraphQL VoiceNodeStatus fields the harness uses.
// isHealthy is deliberately absent: it stays false until the first
// heartbeat (15 s) and must not gate readiness.
type NodeStatus struct {
	ID               string
	Address          string
	IsGlobal         bool
	Coalitions       []string
	ConnectedClients int
}

// DistributionStatus mirrors the GraphQL DistributionStatus shape.
type DistributionStatus struct {
	GlobalAddr        string
	Nodes             []NodeStatus
	ClientAssignments []struct{ ClientID, NodeID string }
}

func nodeByAddr(st DistributionStatus, addr string) (NodeStatus, bool) {
	for _, n := range st.Nodes {
		if n.Address == addr {
			return n, true
		}
	}
	return NodeStatus{}, false
}

// DistributedReady returns nil iff the distribution has settled: both
// non-global wanted nodes are present with disjoint, non-empty coalition
// sets whose union is coalitions, and the global node is present and
// reported as GlobalAddr. want must name both NodeA and NodeB; a partial
// want is an error rather than a silent skip of the coalition clauses.
// Otherwise the error names the first unmet clause.
func DistributedReady(st DistributionStatus, want map[Role]string, coalitions []string) error {
	for _, r := range []Role{NodeA, NodeB} {
		if _, ok := want[r]; !ok {
			return fmt.Errorf("want must name both %s and %s, missing %s", NodeA, NodeB, r)
		}
	}
	var sets [][]string
	for _, r := range []Role{NodeA, NodeB} {
		addr := want[r]
		n, found := nodeByAddr(st, addr)
		if !found {
			return fmt.Errorf("%s (%s) not present in distributionStatus", r, addr)
		}
		if n.IsGlobal {
			return fmt.Errorf("%s (%s) is reported global, want non-global", r, addr)
		}
		sets = append(sets, n.Coalitions)
	}
	for i, s := range sets {
		if len(s) == 0 {
			return fmt.Errorf("non-global node %d has an empty coalition set, want non-empty", i)
		}
	}
	seen := map[string]bool{}
	for _, s := range sets {
		for _, c := range s {
			if seen[c] {
				return fmt.Errorf("coalition sets are not disjoint: %q is on both nodes", c)
			}
			seen[c] = true
		}
	}
	var got []string
	for c := range seen {
		got = append(got, c)
	}
	exp := append([]string(nil), coalitions...)
	sort.Strings(got)
	sort.Strings(exp)
	if fmt.Sprint(got) != fmt.Sprint(exp) {
		return fmt.Errorf("coalition union %v != wanted %v", got, exp)
	}
	if gaddr, ok := want[Global]; ok {
		n, found := nodeByAddr(st, gaddr)
		if !found {
			return fmt.Errorf("global node (%s) not present in distributionStatus", gaddr)
		}
		if !n.IsGlobal {
			return fmt.Errorf("global node (%s) is not reported isGlobal", gaddr)
		}
		if st.GlobalAddr != gaddr {
			return fmt.Errorf("globalAddr = %q, want %q", st.GlobalAddr, gaddr)
		}
	}
	return nil
}

// CoalitionOwner maps each coalition to the wanted role whose node holds
// it. Coalitions on unwanted or global nodes are omitted.
func CoalitionOwner(st DistributionStatus, want map[Role]string) map[string]Role {
	out := map[string]Role{}
	for _, r := range []Role{NodeA, NodeB} {
		addr, ok := want[r]
		if !ok {
			continue
		}
		if n, found := nodeByAddr(st, addr); found {
			for _, c := range n.Coalitions {
				out[c] = r
			}
		}
	}
	return out
}
