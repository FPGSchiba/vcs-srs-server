package voice

import (
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/FPGSchiba/vcs-srs-server/voiceontrol"
	"github.com/google/uuid"
)

// listenAsync runs Listen in a goroutine and returns its result channel.
func listenAsync(s *Server, addr string, stop chan struct{}) chan error {
	errc := make(chan error, 1)
	go func() { errc <- s.Listen(addr, stop) }()
	return errc
}

// SRV-11: a node must not register a port it failed to bind.
func TestListenBindsBeforeRegistering(t *testing.T) {
	taken, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	port := taken.LocalAddr().(*net.UDPAddr).Port

	f := &recordingControl{}
	s := newDistributedServer("127.0.0.1", serveRecordingControl(t, f))
	stop := make(chan struct{})
	defer close(stop)

	select {
	case err := <-listenAsync(s, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), stop):
		if err == nil {
			t.Fatal("Listen on a taken port returned nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Listen on a taken port did not return")
	}
	time.Sleep(100 * time.Millisecond)
	if r, _ := f.counts(); r != 0 {
		t.Fatalf("control node saw %d registrations for a port that failed to bind", r)
	}
}

// SRV-11 / D3: an unusable publicAddr refuses to start and frees the socket.
func TestListenRefusesBadPublicAddr(t *testing.T) {
	f := &recordingControl{}
	s := newDistributedServer("0.0.0.0:5002", serveRecordingControl(t, f))
	stop := make(chan struct{})
	defer close(stop)

	probe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()

	select {
	case err := <-listenAsync(s, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), stop):
		if err == nil || !strings.HasPrefix(err.Error(), "voiceControl.publicAddr") {
			t.Fatalf("Listen error = %v, want a voiceControl.publicAddr error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Listen with a bad publicAddr did not return")
	}
	again, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatalf("the voice socket was left open after the refusal: %v", err)
	}
	again.Close()
	if r, _ := f.counts(); r != 0 {
		t.Fatalf("control node saw %d registrations", r)
	}
}

// SRV-11: the registration carries the bound port and the server version.
func TestListenAdvertisesBoundPort(t *testing.T) {
	f := &recordingControl{}
	s := newDistributedServer("127.0.0.1", serveRecordingControl(t, f))
	s.SetVersion("v9.9.9")
	stop := make(chan struct{})
	errc := listenAsync(s, "127.0.0.1:0", stop)
	defer func() {
		close(stop)
		_ = s.Stop()
		<-errc
	}()

	waitFor(t, 2*time.Second, func() bool { r, _ := f.counts(); return r == 1 }, "one registration")
	req := f.lastRegister()
	s.RLock()
	bound := s.conn.LocalAddr().(*net.UDPAddr).Port
	s.RUnlock()
	if req.UdpPort == 0 || int(req.UdpPort) != bound {
		t.Fatalf("registered UDP port %d, want the bound port %d", req.UdpPort, bound)
	}
	if req.ServerAddress != "127.0.0.1" {
		t.Fatalf("registered address %q, want 127.0.0.1", req.ServerAddress)
	}
	if v := req.GetCapabilities().GetVersion(); v != "v9.9.9" {
		t.Fatalf("registered version %q, want v9.9.9", v)
	}
}

// connectedServer returns a running voice server (no read loop) whose control
// client is connected to f with its stream up.
func connectedServer(t *testing.T, f *recordingControl) *Server {
	t.Helper()
	hook := serveRecordingControl(t, f)
	s := newDistributedServer("127.0.0.1", hook)
	c := voiceontrol.NewVoiceControlClient(s.serverId, s.settingsState, s.serverState, false, "v", slog.New(slog.DiscardHandler))
	c.SetKickHandler(s.DisconnectClient)
	hook(c)
	if err := c.ConnectControlServer(5002); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	s.Lock()
	s.controlClient = c
	s.running = true
	s.Unlock()
	waitFor(t, 2*time.Second, func() bool { r, a := f.counts(); return r == 1 && a == 1 }, "registered with the stream up")
	return s
}

// R-4: stopping the voice server ends its control registration.
func TestStopClosesControlClient(t *testing.T) {
	f := &recordingControl{}
	s := connectedServer(t, f)

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { _, a := f.counts(); return a == 0 }, "control stream ended by Stop")
	time.Sleep(200 * time.Millisecond)
	if r, _ := f.counts(); r != 1 {
		t.Fatalf("%d registrations after Stop, want 1 (no reconnect)", r)
	}
}

// A kick being handled while Stop runs must not deadlock: the kick handler
// takes the server lock, and Stop waits for the control client.
func TestStopDuringKickDoesNotDeadlock(t *testing.T) {
	f := &recordingControl{}
	s := connectedServer(t, f)
	id := uuid.New()
	s.Lock()
	s.clients[id] = &Client{Addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4000}, LastSeen: time.Now()}
	// Hold the lock so the kick handler blocks inside the control client's
	// supervisor, then start Stop so it queues on the same lock.
	f.out <- &pb.ControlMessage{Command: &pb.ControlMessage_KickClient{KickClient: &pb.KickClientRequest{ClientId: id.String()}}}
	time.Sleep(100 * time.Millisecond)
	stopped := make(chan struct{})
	go func() {
		_ = s.Stop()
		close(stopped)
	}()
	time.Sleep(50 * time.Millisecond)
	s.Unlock()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop deadlocked against the kick handler")
	}
	waitFor(t, 2*time.Second, func() bool { _, ok := s.GetClientIPFromId(id); return !ok }, "kick applied")
}

// A LEFT delta can reach the kick handler (DisconnectClient) while Listen is
// still publishing the control client; the handler must read the client
// under the server lock. Run with -race.
func TestEarlyKickDoesNotRaceControlClientPublish(t *testing.T) {
	for i := 0; i < 20; i++ {
		f := &recordingControl{}
		hook := serveRecordingControl(t, f)
		s := newDistributedServer("127.0.0.1", hook)
		id := uuid.New()
		s.clients[id] = &Client{Addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4000}, LastSeen: time.Now()}
		// Queued before the stream opens, so it is the first thing applied.
		f.out <- &pb.ControlMessage{Command: &pb.ControlMessage_KickClient{KickClient: &pb.KickClientRequest{ClientId: id.String()}}}
		stop := make(chan struct{})
		errc := listenAsync(s, "127.0.0.1:0", stop)
		waitFor(t, 2*time.Second, func() bool { _, ok := s.GetClientIPFromId(id); return !ok }, "early kick applied")
		close(stop)
		_ = s.Stop()
		<-errc
	}
}
