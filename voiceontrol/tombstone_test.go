package voiceontrol

import (
	"slices"
	"sync"
	"testing"
	"time"

	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
)

// fakeClock is a settable clock for the tombstone grace.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// deregisterViaStream attaches a stream for id and ends it, so the control
// node deregisters id through its stream-end path.
func deregisterViaStream(t *testing.T, s *VoiceControlServer, id string) {
	t.Helper()
	st := newCtlStream(id)
	errc := runStream(s, st)
	eventually(t, 2*time.Second, func() bool { return nodeStreamIs(s, id, st) }, "stream attached")
	st.cancel()
	waitErr(t, errc, 2*time.Second, "handler to return")
	if nodeCoalitions(s, id) != nil {
		t.Fatalf("%s still registered after its stream ended", id)
	}
}

func tombstoneServer(t *testing.T) (*VoiceControlServer, *fakeClock) {
	t.Helper()
	s := threeNodes(t)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	s.mu.Lock()
	s.now = clk.now
	s.mu.Unlock()
	return s, clk
}

func TestReRegistrationAfterDeregistrationKeepsCoalitionsWithinGrace(t *testing.T) {
	s, clk := tombstoneServer(t)
	deregisterViaStream(t, s, "n")
	clk.add(60 * time.Second)

	resp := register(t, s, &pb.RegisterVoiceServerRequest{ServerId: "n", ServerAddress: "10.0.0.1", UdpPort: 5002})
	want := []string{"Blue", "Green"}
	if got := sortedCopy(resp.AssignedCoalitions); !slices.Equal(got, want) {
		t.Fatalf("returning node assigned %v, want its old %v", got, want)
	}
	if got := nodeCoalitions(s, "a"); !slices.Equal(got, []string{"Red"}) {
		t.Fatalf("node a owns %v, want [Red]", got)
	}
}

func TestTombstoneSkipsCoalitionsNowOwnedElsewhere(t *testing.T) {
	s, clk := tombstoneServer(t)
	deregisterViaStream(t, s, "n")
	// Another node took Green meanwhile.
	s.mu.Lock()
	s.nodes["a"].coalitions = []string{"Red", "Green"}
	s.mu.Unlock()
	clk.add(30 * time.Second)

	resp := register(t, s, &pb.RegisterVoiceServerRequest{ServerId: "n", ServerAddress: "10.0.0.1", UdpPort: 5002})
	if got := sortedCopy(resp.AssignedCoalitions); !slices.Equal(got, []string{"Blue"}) {
		t.Fatalf("returning node assigned %v, want [Blue] (Green is owned elsewhere now)", got)
	}
	if got := nodeCoalitions(s, "a"); !slices.Equal(got, []string{"Green", "Red"}) {
		t.Fatalf("node a owns %v, want [Green Red]", got)
	}
}

func TestTombstoneExpires(t *testing.T) {
	s, clk := tombstoneServer(t)
	deregisterViaStream(t, s, "n")
	clk.add(121 * time.Second)

	resp := register(t, s, &pb.RegisterVoiceServerRequest{ServerId: "n", ServerAddress: "10.0.0.1", UdpPort: 5002})
	if got := sortedCopy(resp.AssignedCoalitions); slices.Equal(got, []string{"Blue", "Green"}) {
		t.Fatalf("a node returning after the grace got its old coalitions back (%v)", got)
	}
}

// Expired tombstones of other node IDs are swept on any registration, so the
// map stays bounded even when a gone node never comes back.
func TestExpiredTombstoneOfAnotherNodeIsSwept(t *testing.T) {
	s, clk := tombstoneServer(t)
	deregisterViaStream(t, s, "a")
	clk.add(121 * time.Second)

	register(t, s, &pb.RegisterVoiceServerRequest{ServerId: "c", ServerAddress: "10.0.0.2", UdpPort: 5002})
	s.mu.RLock()
	_, left := s.recentlyGone["a"]
	s.mu.RUnlock()
	if left {
		t.Fatal("node a's expired tombstone survived another node's registration (lazy expiry)")
	}
}
