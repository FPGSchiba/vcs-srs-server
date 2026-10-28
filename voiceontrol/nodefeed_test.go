package voiceontrol

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// feedNode registers n1 owning Blue (the only coalition), adds a Blue client,
// and starts n1's stream with its snapshot sent.
func feedNode(t *testing.T) (*VoiceControlServer, *ctlStream, chan error, uuid.UUID, *captureHandler) {
	t.Helper()
	s := newTestVoiceControlServer([]string{"Blue"})
	logger, logs := newCapture()
	s.logger = logger
	id := uuid.New()
	s.serverState.Lock()
	s.serverState.Clients[id] = &state.ClientState{Name: "Pilot", Coalition: "Blue"}
	s.serverState.RadioClients[id] = &state.RadioState{}
	s.serverState.Unlock()
	register(t, s, &pb.RegisterVoiceServerRequest{ServerId: "n1", ServerAddress: "10.0.0.1", UdpPort: 5002})
	st := newCtlStream("n1")
	t.Cleanup(st.cancel)
	errc := runStream(s, st)
	eventually(t, 2*time.Second, func() bool { return len(st.sentCopy()) >= 1 }, "snapshot sent")
	return s, st, errc, id, logs
}

func radioEvent(id uuid.UUID) events.Event {
	return events.Event{Name: events.RadioClientsChanged, Data: events.RadioChangeEvent{
		Type:     events.RadioUpdated,
		ClientID: id,
		Radios:   map[uuid.UUID]*state.RadioState{id: {Radios: []state.Radio{{ID: 1, Frequency: 243, Enabled: true}}}},
	}}
}

func hasDelta(st *ctlStream, id uuid.UUID, typ pb.ClientDelta_Type) bool {
	for _, m := range st.sentCopy() {
		if d := m.GetClientDelta(); d != nil && d.ClientId == id.String() && d.Type == typ {
			return true
		}
	}
	return false
}

// D7: logs no longer share (and fill) the node feed's buffer.
func TestNodeFeedIgnoresLogFlood(t *testing.T) {
	s, st, errc, id, _ := feedNode(t)
	release := st.block()
	s.eventBus.Publish(radioEvent(id)) // the handler stalls in Send on this one
	time.Sleep(50 * time.Millisecond)
	for i := 0; i < 500; i++ {
		s.eventBus.Publish(events.Event{Name: events.LogEntry, Data: "log line"})
	}
	// Published while Send is still stalled and after the flood: a feed that
	// buffered the logs would have no room left for it.
	other := uuid.New()
	s.eventBus.Publish(joinedEvent(other, "Blue"))
	time.Sleep(50 * time.Millisecond)
	release()

	eventually(t, 2*time.Second, func() bool { return hasDelta(st, other, pb.ClientDelta_JOINED) }, "JOINED delta published during the log flood")
	select {
	case err := <-errc:
		t.Fatalf("the feed ended on a log flood: %v", err)
	default:
	}
	if got := s.eventBus.Stats().Overflows; got != 0 {
		t.Fatalf("event bus recorded %d overflows during a log flood, want 0", got)
	}
}

// D7: a feed that falls behind ends (so the node resyncs) instead of silently
// dropping deltas; the registration and its coalitions survive.
func TestNodeFeedOverflowEndsStreamKeepsRegistration(t *testing.T) {
	s, st, errc, id, logs := feedNode(t)
	// Give n1 a coalition set a fresh registration would not produce (as in
	// threeNodes), so the final assertion can tell carry-over from reassignment.
	s.settingsState.Lock()
	for _, c := range []string{"Red", "Green", "Gold"} {
		s.settingsState.Coalitions = append(s.settingsState.Coalitions, state.Coalition{Name: c})
	}
	s.settingsState.Unlock()
	for _, n := range []string{"a", "b"} {
		register(t, s, &pb.RegisterVoiceServerRequest{ServerId: n, ServerAddress: "10.0.0.2", UdpPort: 5002})
	}
	s.mu.Lock()
	s.nodes["a"].coalitions = []string{"Red"}
	s.nodes["b"].coalitions = []string{"Gold"}
	s.nodes["n1"].coalitions = []string{"Blue", "Green"}
	s.mu.Unlock()

	release := st.block()
	for i := 0; i < 300; i++ {
		s.eventBus.Publish(radioEvent(id))
	}
	time.Sleep(100 * time.Millisecond)
	release()

	err := waitErr(t, errc, 5*time.Second, "the lagging feed to end")
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("feed ended with %v, want ResourceExhausted", err)
	}
	if n := len(logs.find("Voice node feed fell behind, ending stream")); n != 1 {
		t.Fatalf("%d fell-behind WARNs, want 1", n)
	}
	s.mu.RLock()
	node, ok := s.nodes["n1"]
	var attached bool
	if ok {
		attached = node.stream != nil
	}
	s.mu.RUnlock()
	if !ok {
		t.Fatal("the lagging node was deregistered; it must stay registered to keep its coalitions")
	}
	if attached {
		t.Fatal("the lagging node's dead stream is still attached")
	}
	resp := register(t, s, &pb.RegisterVoiceServerRequest{ServerId: "n1", ServerAddress: "10.0.0.1", UdpPort: 5002})
	want := []string{"Blue", "Green"}
	if got := sortedCopy(resp.AssignedCoalitions); !slices.Equal(got, want) {
		t.Fatalf("re-registration after the overflow assigned %v, want %v", got, want)
	}
	if got := nodeCoalitions(s, "n1"); !slices.Equal(got, want) {
		t.Fatalf("n1 owns %v after re-registering, want %v", got, want)
	}
}

func TestNodeFeedStillForwardsAndRebalances(t *testing.T) {
	s, st, _, id, _ := feedNode(t)

	other := uuid.New()
	s.eventBus.Publish(joinedEvent(other, "Blue"))
	s.eventBus.Publish(radioEvent(id))
	s.eventBus.Publish(events.Event{Name: events.ServerAction, Data: events.ServerActionEvent{ActionType: events.ActionMute, TargetClientID: id}})
	eventually(t, 2*time.Second, func() bool {
		return hasDelta(st, other, pb.ClientDelta_JOINED) && hasDelta(st, id, pb.ClientDelta_RADIO_UPDATED) && hasDelta(st, id, pb.ClientDelta_MUTED)
	}, "JOINED, RADIO_UPDATED and MUTED forwarded")

	s.settingsState.Lock()
	s.settingsState.Coalitions = append(s.settingsState.Coalitions, state.Coalition{Name: "Red"})
	s.settingsState.Unlock()
	s.eventBus.Publish(events.Event{Name: events.CoalitionsChanged})
	eventually(t, 2*time.Second, func() bool { return slices.Equal(nodeCoalitions(s, "n1"), []string{"Blue", "Red"}) }, "the new coalition assigned by rebalance")
}

// W11: the builders read node.coalitions and client.Coalition under their locks.
func TestBuildClientDeltaCoalitionRace(t *testing.T) {
	s := newTestVoiceControlServer([]string{"Blue", "Red"})
	id := uuid.New()
	client := &state.ClientState{Name: "Pilot", Coalition: "Blue"}
	s.serverState.Lock()
	s.serverState.Clients[id] = client
	s.serverState.RadioClients[id] = &state.RadioState{}
	s.serverState.Unlock()
	register(t, s, &pb.RegisterVoiceServerRequest{ServerId: "n1", ServerAddress: "10.0.0.1", UdpPort: 5002})
	s.mu.RLock()
	node := s.nodes["n1"]
	s.mu.RUnlock()

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		ev := radioEvent(id)
		for i := 0; i < 1000; i++ {
			s.buildClientDelta(node, ev)
			if i%50 == 0 {
				s.buildStateSnapshot(node)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			s.mu.Lock()
			if i%2 == 0 {
				node.coalitions = []string{"Blue"}
			} else {
				node.coalitions = []string{"Blue", "Red"}
			}
			s.rebalance(true)
			s.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			s.serverState.Lock()
			if i%2 == 0 {
				client.Coalition = "Red"
			} else {
				client.Coalition = "Blue"
			}
			s.serverState.Unlock()
		}
	}()
	wg.Wait()
}
