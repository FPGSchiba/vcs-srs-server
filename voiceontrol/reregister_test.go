package voiceontrol

import (
	"slices"
	"testing"

	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
)

func sortedCopy(c []string) []string {
	out := append([]string(nil), c...)
	slices.Sort(out)
	return out
}

func nodeCoalitions(s *VoiceControlServer, id string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n, ok := s.nodes[id]; ok {
		return sortedCopy(n.coalitions)
	}
	return nil
}

// threeNodes registers a, b and n, then pins a=[Red], b=[Gold], n=[Blue Green]
// (n at target, the others under it).
func threeNodes(t *testing.T) *VoiceControlServer {
	t.Helper()
	s := newTestVoiceControlServer([]string{"Blue", "Red", "Green", "Gold"})
	for _, id := range []string{"a", "b", "n"} {
		register(t, s, &pb.RegisterVoiceServerRequest{ServerId: id, ServerAddress: "10.0.0.1", UdpPort: 5002})
	}
	s.mu.Lock()
	s.nodes["a"].coalitions = []string{"Red"}
	s.nodes["b"].coalitions = []string{"Gold"}
	s.nodes["n"].coalitions = []string{"Blue", "Green"}
	s.mu.Unlock()
	return s
}

// R-3: a node re-registering under the same ID keeps its coalitions.
func TestReRegistrationKeepsCoalitions(t *testing.T) {
	s := threeNodes(t)

	resp := register(t, s, &pb.RegisterVoiceServerRequest{ServerId: "n", ServerAddress: "10.0.0.1", UdpPort: 5002})

	want := []string{"Blue", "Green"}
	if got := sortedCopy(resp.AssignedCoalitions); !slices.Equal(got, want) {
		t.Fatalf("re-registration response assigned %v, want %v", got, want)
	}
	if got := nodeCoalitions(s, "n"); !slices.Equal(got, want) {
		t.Fatalf("re-registered node owns %v, want %v", got, want)
	}
	if got := nodeCoalitions(s, "a"); !slices.Equal(got, []string{"Red"}) {
		t.Fatalf("node a owns %v, want [Red]", got)
	}
	if got := nodeCoalitions(s, "b"); !slices.Equal(got, []string{"Gold"}) {
		t.Fatalf("node b owns %v, want [Gold]", got)
	}
}

func TestReRegistrationGlobalUnaffected(t *testing.T) {
	s := newTestVoiceControlServer([]string{"Blue"})
	register(t, s, &pb.RegisterVoiceServerRequest{ServerId: "g", ServerAddress: "10.0.0.10", UdpPort: 5002, IsGlobal: true})
	resp := register(t, s, &pb.RegisterVoiceServerRequest{ServerId: "g", ServerAddress: "10.0.0.11", UdpPort: 5003, IsGlobal: true})
	if len(resp.AssignedCoalitions) != 0 {
		t.Fatalf("global node assigned %v", resp.AssignedCoalitions)
	}
	s.mu.RLock()
	addr := s.globalNodeAddr
	s.mu.RUnlock()
	if addr != "10.0.0.11:5003" {
		t.Fatalf("global addr %q, want 10.0.0.11:5003", addr)
	}
}

func TestReRegistrationDoesNotAliasCoalitions(t *testing.T) {
	s := threeNodes(t)
	s.mu.RLock()
	old := s.nodes["n"]
	s.mu.RUnlock()

	register(t, s, &pb.RegisterVoiceServerRequest{ServerId: "n", ServerAddress: "10.0.0.1", UdpPort: 5002})

	s.mu.Lock()
	if cur := s.nodes["n"]; cur != old && len(cur.coalitions) > 0 {
		cur.coalitions[0] = "Mutated"
	}
	got := append([]string(nil), old.coalitions...)
	s.mu.Unlock()
	if slices.Contains(got, "Mutated") {
		t.Fatal("the new record shares its coalition slice with the old one")
	}
}
