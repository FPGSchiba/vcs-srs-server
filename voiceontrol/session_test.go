package voiceontrol

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/state"
	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// --- a fake control node, served over bufconn ---

type fakeStreamHandle struct {
	brk     chan struct{}
	brkOnce sync.Once
	out     chan *pb.ControlMessage
}

type fakeControl struct {
	pb.UnimplementedVoiceControlServiceServer

	down          atomic.Bool // RegisterVoiceServer fails with Unavailable
	blockRegister atomic.Bool // RegisterVoiceServer blocks until its context ends

	mu               sync.Mutex
	registers        []*pb.RegisterVoiceServerRequest // successful registrations
	registerAttempts int
	streams          int // streams opened
	activeStreams    int // streams whose handler is still running
	heartbeats       int
	cur              *fakeStreamHandle
}

func (f *fakeControl) RegisterVoiceServer(ctx context.Context, req *pb.RegisterVoiceServerRequest) (*pb.RegisterVoiceServerResponse, error) {
	f.mu.Lock()
	f.registerAttempts++
	f.mu.Unlock()
	if f.blockRegister.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.down.Load() {
		return nil, status.Error(codes.Unavailable, "down")
	}
	f.mu.Lock()
	f.registers = append(f.registers, req)
	f.mu.Unlock()
	return &pb.RegisterVoiceServerResponse{Success: true, AssignedCoalitions: []string{"Blue"}}, nil
}

func (f *fakeControl) SendHeartbeat(context.Context, *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	f.mu.Lock()
	f.heartbeats++
	f.mu.Unlock()
	return &pb.HeartbeatResponse{Acknowledged: true}, nil
}

func (f *fakeControl) ReportClientConnected(context.Context, *pb.ClientConnectedRequest) (*pb.ClientConnectedResponse, error) {
	return &pb.ClientConnectedResponse{}, nil
}

func (f *fakeControl) ReportClientDisconnected(context.Context, *pb.ClientDisconnectedRequest) (*pb.ClientDisconnectedResponse, error) {
	return &pb.ClientDisconnectedResponse{}, nil
}

func (f *fakeControl) EstablishStream(stream pb.VoiceControlService_EstablishStreamServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if err := stream.Send(&pb.ControlMessage{Command: &pb.ControlMessage_StateSnapshot{StateSnapshot: &pb.ClientStateSnapshot{}}}); err != nil {
		return err
	}
	h := &fakeStreamHandle{brk: make(chan struct{}), out: make(chan *pb.ControlMessage, 16)}
	f.mu.Lock()
	f.cur = h
	f.streams++
	f.activeStreams++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.activeStreams--
		f.mu.Unlock()
	}()
	for {
		select {
		case <-h.brk:
			return status.Error(codes.Unavailable, "broken")
		case msg := <-h.out:
			if err := stream.Send(msg); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// breakStream ends the current stream with Unavailable.
func (f *fakeControl) breakStream() {
	f.mu.Lock()
	h := f.cur
	f.mu.Unlock()
	if h != nil {
		h.brkOnce.Do(func() { close(h.brk) })
	}
}

// send pushes msg onto the current stream.
func (f *fakeControl) send(msg *pb.ControlMessage) {
	f.mu.Lock()
	h := f.cur
	f.mu.Unlock()
	h.out <- msg
}

func (f *fakeControl) counts() (registers, attempts, streams, active, heartbeats int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.registers), f.registerAttempts, f.streams, f.activeStreams, f.heartbeats
}

func (f *fakeControl) lastRegister() *pb.RegisterVoiceServerRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.registers) == 0 {
		return nil
	}
	return f.registers[len(f.registers)-1]
}

func (f *fakeControl) resetHeartbeats() {
	f.mu.Lock()
	f.heartbeats = 0
	f.mu.Unlock()
}

func serveFake(t *testing.T, f *fakeControl) *bufconn.Listener {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterVoiceControlServiceServer(srv, f)
	go srv.Serve(lis) //nolint:errcheck
	t.Cleanup(srv.Stop)
	return lis
}

func newFakeClient(t *testing.T, lis *bufconn.Listener, publicAddr string, logger *slog.Logger) *VoiceControlClient {
	t.Helper()
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	settings := &state.SettingsState{VoiceControl: state.VoiceControlSettings{PublicAddr: publicAddr}}
	c := NewVoiceControlClient("node-1", settings, emptyState(), false, "v-test", logger)
	c.UseDialer("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	c.heartbeatInterval = 20 * time.Millisecond
	c.backoffMin = 10 * time.Millisecond
	c.backoffMax = 50 * time.Millisecond
	c.jitter = func(d time.Duration) time.Duration { return d }
	t.Cleanup(func() { c.Close() })
	return c
}

func eventually(t *testing.T, within time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// --- a log capture ---

type capturedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

type captureHandler struct {
	mu   *sync.Mutex
	recs *[]capturedRecord
}

func newCapture() (*slog.Logger, *captureHandler) {
	h := &captureHandler{mu: &sync.Mutex{}, recs: &[]capturedRecord{}}
	return slog.New(h), h
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	rec := capturedRecord{level: r.Level, msg: r.Message, attrs: map[string]any{}}
	r.Attrs(func(a slog.Attr) bool { rec.attrs[a.Key] = a.Value.Any(); return true })
	h.mu.Lock()
	*h.recs = append(*h.recs, rec)
	h.mu.Unlock()
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) find(msg string) []capturedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []capturedRecord
	for _, r := range *h.recs {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

// --- SRV-11: the registration advertises the configured address ---

func TestRegisterAdvertisesConfiguredAddress(t *testing.T) {
	f := &fakeControl{}
	lis := serveFake(t, f)

	c := newFakeClient(t, lis, "127.0.0.1:5002", nil)
	if err := c.ConnectControlServer(5002); err != nil {
		t.Fatalf("ConnectControlServer: %v", err)
	}
	eventually(t, 2*time.Second, func() bool { r, _, _, _, _ := f.counts(); return r == 1 }, "one registration")
	req := f.lastRegister()
	if req.ServerAddress != "127.0.0.1" || req.UdpPort != 5002 || req.GetCapabilities().GetVersion() != "v-test" {
		t.Fatalf("registered %q:%d version %q, want 127.0.0.1:5002 version v-test", req.ServerAddress, req.UdpPort, req.GetCapabilities().GetVersion())
	}

	f2 := &fakeControl{}
	lis2 := serveFake(t, f2)
	c2 := newFakeClient(t, lis2, "vcs.example", nil)
	if err := c2.ConnectControlServer(5010); err != nil {
		t.Fatalf("ConnectControlServer: %v", err)
	}
	eventually(t, 2*time.Second, func() bool { r, _, _, _, _ := f2.counts(); return r == 1 }, "one registration")
	req = f2.lastRegister()
	if req.ServerAddress != "vcs.example" || req.UdpPort != 5010 {
		t.Fatalf("registered %q:%d, want vcs.example:5010", req.ServerAddress, req.UdpPort)
	}
}

func TestConnectRefusesWithoutPublicAddr(t *testing.T) {
	f := &fakeControl{}
	lis := serveFake(t, f)
	c := newFakeClient(t, lis, "", nil)

	err := c.ConnectControlServer(5002)
	if err == nil || !strings.HasPrefix(err.Error(), "voiceControl.publicAddr") {
		t.Fatalf("ConnectControlServer error = %v, want a voiceControl.publicAddr error", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, attempts, streams, _, _ := f.counts(); attempts != 0 || streams != 0 {
		t.Fatalf("fake saw %d register attempts and %d streams, want none", attempts, streams)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// --- SRV-12: one reconnect loop ---

func TestReconnectIsSingleFlight(t *testing.T) {
	f := &fakeControl{}
	lis := serveFake(t, f)
	c := newFakeClient(t, lis, "127.0.0.1:5002", nil)
	if err := c.ConnectControlServer(5002); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, func() bool { _, _, s, a, _ := f.counts(); return s == 1 && a == 1 }, "first stream")

	for i := 1; i <= 5; i++ {
		f.breakStream()
		want := i + 1
		eventually(t, 2*time.Second, func() bool { _, _, s, a, _ := f.counts(); return s == want && a == 1 }, "next stream")
	}
	eventually(t, 2*time.Second, func() bool { r, _, s, _, _ := f.counts(); return r == 6 && s == 6 }, "6 registrations and 6 streams")
	time.Sleep(200 * time.Millisecond)
	if r, _, s, _, _ := f.counts(); r != 6 || s != 6 {
		t.Fatalf("after settling: %d registrations, %d streams, want exactly 6 each", r, s)
	}
}

func TestHeartbeatGoroutinesDoNotAccumulate(t *testing.T) {
	f := &fakeControl{}
	lis := serveFake(t, f)
	c := newFakeClient(t, lis, "127.0.0.1:5002", nil)
	if err := c.ConnectControlServer(5002); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, func() bool { _, _, s, _, _ := f.counts(); return s == 1 }, "first stream")
	for i := 1; i <= 5; i++ {
		f.breakStream()
		want := i + 1
		eventually(t, 2*time.Second, func() bool { _, _, s, a, _ := f.counts(); return s == want && a == 1 }, "next stream")
	}

	f.resetHeartbeats()
	time.Sleep(400 * time.Millisecond)
	if _, _, _, _, hb := f.counts(); hb > 400/20+3 {
		t.Fatalf("%d heartbeats in 400ms at a 20ms interval: more than one heartbeat loop is running", hb)
	}
}

func TestReconnectKeepsKickHandler(t *testing.T) {
	f := &fakeControl{}
	lis := serveFake(t, f)
	c := newFakeClient(t, lis, "127.0.0.1:5002", nil)
	rec := &kickRecorder{}
	c.SetKickHandler(rec.handle)
	if err := c.ConnectControlServer(5002); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, func() bool { _, _, s, _, _ := f.counts(); return s == 1 }, "first stream")
	for i := 1; i <= 3; i++ {
		f.breakStream()
		want := i + 1
		eventually(t, 2*time.Second, func() bool { _, _, s, a, _ := f.counts(); return s == want && a == 1 }, "next stream")
	}

	id := uuid.New()
	f.send(kickMsg(id.String()))
	eventually(t, 2*time.Second, func() bool { g := rec.got(); return len(g) == 1 && g[0] == id }, "kick handler called on the newest stream")
}

func TestFirstConnectFailureIsRetried(t *testing.T) {
	f := &fakeControl{}
	f.down.Store(true)
	lis := serveFake(t, f)
	c := newFakeClient(t, lis, "127.0.0.1:5002", nil)
	if err := c.ConnectControlServer(5002); err != nil {
		t.Fatalf("a network failure must not be a ConnectControlServer error: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	f.down.Store(false)
	eventually(t, 2*time.Second, func() bool { r, _, s, _, _ := f.counts(); return r == 1 && s == 1 }, "registration after the control node came back")
}

func TestRetriesForeverAndWarnsRateLimited(t *testing.T) {
	f := &fakeControl{}
	f.down.Store(true)
	lis := serveFake(t, f)
	logger, logs := newCapture()
	c := newFakeClient(t, lis, "127.0.0.1:5002", logger)
	c.backoffMax = time.Millisecond
	if err := c.ConnectControlServer(5002); err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, func() bool { _, a, _, _, _ := f.counts(); return a >= 50 }, "50 register attempts")
	if n := len(logs.find("Control node unreachable, retrying")); n != 1 {
		t.Fatalf("%d retry WARNs within the window, want exactly 1", n)
	}

	c.warn.mu.Lock()
	c.warn.lastLogged = c.warn.lastLogged.Add(-c.warnEvery)
	c.warn.mu.Unlock()
	eventually(t, 2*time.Second, func() bool { return len(logs.find("Control node unreachable, retrying")) == 2 }, "a second WARN after the window")
	second := logs.find("Control node unreachable, retrying")[1]
	if n, _ := second.attrs["suppressed_since_last"].(int64); n <= 0 {
		t.Fatalf("second WARN suppressed_since_last = %v, want > 0", second.attrs["suppressed_since_last"])
	}
	if second.level != slog.LevelWarn {
		t.Fatalf("retry log level %v, want WARN", second.level)
	}

	f.down.Store(false)
	eventually(t, 2*time.Second, func() bool { return len(logs.find("Reconnected to Control Server")) == 1 }, "one Reconnected INFO")
}

func TestCloseStopsReconnectPromptly(t *testing.T) {
	f := &fakeControl{}
	f.down.Store(true)
	lis := serveFake(t, f)
	c := newFakeClient(t, lis, "127.0.0.1:5002", nil)
	c.backoffMin = 30 * time.Second
	c.backoffMax = 30 * time.Second
	if err := c.ConnectControlServer(5002); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, func() bool { _, a, _, _, _ := f.counts(); return a >= 1 }, "first failed attempt")

	start := time.Now()
	c.Close()
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("Close took %v during a backoff, want < 200ms", d)
	}
	_, before, _, _, _ := f.counts()
	time.Sleep(200 * time.Millisecond)
	if _, after, _, _, _ := f.counts(); after != before {
		t.Fatalf("register attempts went %d -> %d after Close", before, after)
	}
}

func TestCloseDuringRegister(t *testing.T) {
	f := &fakeControl{}
	f.blockRegister.Store(true)
	lis := serveFake(t, f)
	c := newFakeClient(t, lis, "127.0.0.1:5002", nil)
	if err := c.ConnectControlServer(5002); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, func() bool { _, a, _, _, _ := f.counts(); return a >= 1 }, "register in flight")

	start := time.Now()
	c.Close()
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("Close took %v during a blocked register, want < 200ms", d)
	}
}

func TestCloseIsIdempotentAndSafeBeforeConnect(t *testing.T) {
	c := NewVoiceControlClient("n", &state.SettingsState{}, emptyState(), false, "v", slog.New(slog.DiscardHandler))
	if err := c.Close(); err != nil {
		t.Fatalf("Close before connect: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestAssignCoalitionsRace(t *testing.T) {
	c := newTestClient(emptyState())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			c.applyControlMessage(&pb.ControlMessage{Command: &pb.ControlMessage_AssignCoalitions{AssignCoalitions: &pb.AssignCoalitionsRequest{Coalitions: []string{"Blue", "Red"}}}})
		}
	}()
	for i := 0; i < 200; i++ {
		_ = c.AssignedCoalitions()
	}
	<-done
	if got := c.AssignedCoalitions(); len(got) != 2 {
		t.Fatalf("AssignedCoalitions = %v, want [Blue Red]", got)
	}
}
