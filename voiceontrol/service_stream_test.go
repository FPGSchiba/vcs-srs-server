package voiceontrol

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/google/uuid"
)

// ctlStream is a controllable EstablishStream server stream: Recv returns
// the init message once and then blocks until the context ends; Send can be
// made to fail, or to block until released.
type ctlStream struct {
	pb.VoiceControlService_EstablishStreamServer

	ctx    context.Context
	cancel context.CancelFunc
	init   *pb.ControlResponse

	recvOnce  sync.Once
	failSends atomic.Bool
	gate      atomic.Pointer[chan struct{}] // non-nil: Send blocks until closed

	mu   sync.Mutex
	sent []*pb.ControlMessage
}

func newCtlStream(serverID string) *ctlStream {
	ctx, cancel := context.WithCancel(context.Background())
	return &ctlStream{ctx: ctx, cancel: cancel, init: &pb.ControlResponse{ServerId: serverID, EventId: "init"}}
}

func (c *ctlStream) Context() context.Context { return c.ctx }

func (c *ctlStream) Recv() (*pb.ControlResponse, error) {
	var msg *pb.ControlResponse
	c.recvOnce.Do(func() { msg = c.init })
	if msg != nil {
		return msg, nil
	}
	<-c.ctx.Done()
	return nil, c.ctx.Err()
}

func (c *ctlStream) Send(m *pb.ControlMessage) error {
	if g := c.gate.Load(); g != nil {
		select {
		case <-*g:
		case <-c.ctx.Done():
			return c.ctx.Err()
		}
	}
	if c.failSends.Load() {
		return errors.New("send failed")
	}
	c.mu.Lock()
	c.sent = append(c.sent, m)
	c.mu.Unlock()
	return nil
}

// block makes every following Send wait until the returned func is called.
func (c *ctlStream) block() (release func()) {
	ch := make(chan struct{})
	c.gate.Store(&ch)
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

func (c *ctlStream) sentCopy() []*pb.ControlMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*pb.ControlMessage(nil), c.sent...)
}

func runStream(s *VoiceControlServer, st *ctlStream) chan error {
	errc := make(chan error, 1)
	go func() { errc <- s.EstablishStream(st) }()
	return errc
}

func nodeStreamIs(s *VoiceControlServer, id string, st *ctlStream) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[id]
	return ok && n.stream == pb.VoiceControlService_EstablishStreamServer(st)
}

func register(t *testing.T, s *VoiceControlServer, req *pb.RegisterVoiceServerRequest) *pb.RegisterVoiceServerResponse {
	t.Helper()
	resp, err := s.RegisterVoiceServer(context.Background(), req)
	if err != nil || !resp.Success {
		t.Fatalf("register %s: %v %v", req.ServerId, err, resp)
	}
	return resp
}

func waitErr(t *testing.T, errc chan error, within time.Duration, what string) error {
	t.Helper()
	select {
	case err := <-errc:
		return err
	case <-time.After(within):
		t.Fatalf("timed out waiting for: %s", what)
		return nil
	}
}

func joinedEvent(id uuid.UUID, coalition string) events.Event {
	return events.Event{Name: events.ClientsChanged, Data: events.ClientChangeEvent{
		Type:     events.ClientJoined,
		ClientID: id,
		Clients:  map[uuid.UUID]*state.ClientState{id: {Name: "Pilot", Coalition: coalition}},
	}}
}

// --- R-2: deregistration is ownership-checked ---

func TestStaleStreamEndDoesNotDeregisterNewRegistration(t *testing.T) {
	s := newTestVoiceControlServer([]string{"Blue"})
	req := &pb.RegisterVoiceServerRequest{ServerId: "n1", ServerAddress: "10.0.0.1", UdpPort: 5002}
	register(t, s, req)
	st1 := newCtlStream("n1")
	errc1 := runStream(s, st1)
	eventually(t, 2*time.Second, func() bool { return nodeStreamIs(s, "n1", st1) }, "stream 1 attached")

	register(t, s, req)
	st2 := newCtlStream("n1")
	defer st2.cancel()
	runStream(s, st2)
	eventually(t, 2*time.Second, func() bool { return nodeStreamIs(s, "n1", st2) }, "stream 2 attached")

	st1.cancel()
	waitErr(t, errc1, 2*time.Second, "stale handler to return")
	if !nodeStreamIs(s, "n1", st2) {
		t.Fatal("the stale stream's end deregistered (or detached) the new registration")
	}
}

func TestStaleSendErrorDoesNotDeregisterNewRegistration(t *testing.T) {
	s := newTestVoiceControlServer([]string{"Blue"})
	req := &pb.RegisterVoiceServerRequest{ServerId: "n1", ServerAddress: "10.0.0.1", UdpPort: 5002}
	register(t, s, req)
	st1 := newCtlStream("n1")
	defer st1.cancel()
	errc1 := runStream(s, st1)
	eventually(t, 2*time.Second, func() bool { return len(st1.sentCopy()) >= 1 }, "stream 1 snapshot")

	register(t, s, req)
	st2 := newCtlStream("n1")
	defer st2.cancel()
	runStream(s, st2)
	eventually(t, 2*time.Second, func() bool { return nodeStreamIs(s, "n1", st2) }, "stream 2 attached")

	st1.failSends.Store(true)
	id := uuid.New()
	deadline := time.Now().Add(2 * time.Second)
	var err error
	for done := false; !done; {
		s.eventBus.Publish(joinedEvent(id, "Blue"))
		select {
		case err = <-errc1:
			done = true
		case <-time.After(20 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("stale handler did not return on its send error")
			}
		}
	}
	if err == nil {
		t.Fatal("stale handler returned nil on a send error")
	}
	if !nodeStreamIs(s, "n1", st2) {
		t.Fatal("the stale stream's send error deregistered the new registration")
	}
}

func TestStreamEndDeregistersOwnRecord(t *testing.T) {
	s := newTestVoiceControlServer([]string{"Blue"})
	register(t, s, &pb.RegisterVoiceServerRequest{ServerId: "n1", ServerAddress: "10.0.0.1", UdpPort: 5002})
	st := newCtlStream("n1")
	errc := runStream(s, st)
	eventually(t, 2*time.Second, func() bool { return nodeStreamIs(s, "n1", st) }, "stream attached")
	st.cancel()
	waitErr(t, errc, 2*time.Second, "handler to return")
	s.mu.RLock()
	_, ok := s.nodes["n1"]
	s.mu.RUnlock()
	if ok {
		t.Fatal("a node whose own stream ended is still registered")
	}
}

func TestRegisterFormatsIPv6Address(t *testing.T) {
	s := newTestVoiceControlServer([]string{"Blue"})
	register(t, s, &pb.RegisterVoiceServerRequest{ServerId: "v6", ServerAddress: "2001:db8::1", UdpPort: 5002})
	if addr, _ := s.GetVoiceAddressForCoalition("Blue"); addr != "[2001:db8::1]:5002" {
		t.Fatalf("IPv6 node address %q, want [2001:db8::1]:5002", addr)
	}
	s4 := newTestVoiceControlServer([]string{"Blue"})
	register(t, s4, &pb.RegisterVoiceServerRequest{ServerId: "v4", ServerAddress: "10.0.0.1", UdpPort: 5002})
	if addr, _ := s4.GetVoiceAddressForCoalition("Blue"); addr != "10.0.0.1:5002" {
		t.Fatalf("IPv4 node address %q, want 10.0.0.1:5002", addr)
	}
}
