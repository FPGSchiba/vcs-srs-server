package voice

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/logging"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
	slogmulti "github.com/samber/slog-multi"
)

// Benchmark fixture (W6 B-0). The existing fixtures newFanoutFixture and
// newTestPeer take *testing.T; benchmarks need testing.TB, so this file
// carries its own.

const (
	benchFreqMHz      float32 = 243.0
	benchOtherFreqMHz float32 = 251.0
	benchFreqKHz              = 243000
)

// loggerArm selects the logger shape a benchmark runs with.
type loggerArm string

const (
	// armText is a text handler on io.Discard at Info level.
	armText loggerArm = "text"
	// armBus is the production shape from shared.go: a Fanout of a text
	// handler and the event-bus handler. BusHandler.Enabled is always true,
	// so every Debug record is built and published.
	armBus loggerArm = "bus"
)

// benchLogger returns the logger for arm. The bus arm stops its event bus
// when the benchmark finishes.
func benchLogger(tb testing.TB, arm loggerArm) *slog.Logger {
	tb.Helper()
	switch arm {
	case armText:
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	case armBus:
		bus := events.NewEventBus()
		tb.Cleanup(bus.Stop)
		return slog.New(slogmulti.Fanout(
			slog.NewTextHandler(io.Discard, nil),
			logging.NewBusHandler(bus),
		))
	default:
		tb.Fatalf("unknown logger arm %q", arm)
		return nil
	}
}

// benchRoster is a bound voice server with one talker, l listeners on
// 243.0 (real UDP sockets) and n-1-l idle clients on 251.0 (addresses only).
type benchRoster struct {
	s         *Server
	talker    uuid.UUID
	talkAddr  *net.UDPAddr
	listeners []*net.UDPConn
	pkt       *VCSPacket
}

// newBenchRoster builds a roster of n clients in total, l of them listeners.
func newBenchRoster(tb testing.TB, n, l int, arm loggerArm) *benchRoster {
	tb.Helper()
	if l < 0 || n < 1+l {
		tb.Fatalf("bad roster shape n=%d l=%d", n, l)
	}
	ss := &state.ServerState{}
	s := NewServer(ss, benchLogger(tb, arm), &state.DistributionState{}, &state.SettingsState{})
	udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("resolve: %v", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		tb.Fatalf("listen: %v", err)
	}
	tb.Cleanup(func() { _ = conn.Close() })
	s.conn = conn
	s.running = true

	r := &benchRoster{s: s}
	bind := func(id uuid.UUID, addr *net.UDPAddr, freq float32) {
		ss.AddClient(id, &state.ClientState{Name: "Pilot", Coalition: "Blue"})
		ss.Lock()
		ss.RadioClients[id] = &state.RadioState{Radios: []state.Radio{{ID: 1, Frequency: freq, Enabled: true}}}
		ss.Unlock()
		s.Lock()
		s.clients[id] = &Client{Addr: addr, LastSeen: time.Now()}
		s.Unlock()
	}
	peer := func() *net.UDPConn {
		a, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
		if err != nil {
			tb.Fatalf("resolve: %v", err)
		}
		c, err := net.ListenUDP("udp", a)
		if err != nil {
			tb.Fatalf("listen: %v", err)
		}
		tb.Cleanup(func() { _ = c.Close() })
		_ = c.SetReadBuffer(8 << 20)
		return c
	}

	r.talker = uuid.New()
	tp := peer()
	r.talkAddr = tp.LocalAddr().(*net.UDPAddr)
	bind(r.talker, r.talkAddr, benchFreqMHz)
	for i := 0; i < l; i++ {
		p := peer()
		bind(uuid.New(), p.LocalAddr().(*net.UDPAddr), benchFreqMHz)
		r.listeners = append(r.listeners, p)
	}
	for i := 0; i < n-1-l; i++ {
		bind(uuid.New(), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1 + i}, benchOtherFreqMHz)
	}
	r.pkt = NewVCSVoicePacket(r.talker, 1, benchFreqKHz, make([]byte, 120))
	return r
}

// effectiveListeners is the number of listeners a grid cell really has. The
// grid label L=100 at N=100 cannot hold 100 listeners beside the talker, so
// that cell runs with N-1 = 99 and divides its per-send metrics by 99. The
// sub-benchmark name keeps the grid label so the cells stay aligned.
func effectiveListeners(n, l int) int {
	if l > n-1 {
		return n - 1
	}
	return l
}

func TestBenchRosterShape(t *testing.T) {
	for _, c := range []struct{ n, l int }{{100, 10}, {1000, 100}} {
		r := newBenchRoster(t, c.n, c.l, armText)
		if got := len(r.s.GetListeningClients(r.pkt, r.talker)); got != c.l {
			t.Errorf("n=%d l=%d: listening clients = %d, want %d", c.n, c.l, got, c.l)
		}
		if got := r.s.GetClientCount(); got != c.n {
			t.Errorf("n=%d l=%d: client count = %d, want %d", c.n, c.l, got, c.n)
		}
		if got := len(r.listeners); got != c.l {
			t.Errorf("n=%d l=%d: listener sockets = %d, want %d", c.n, c.l, got, c.l)
		}
	}
}
