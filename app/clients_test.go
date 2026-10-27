package app

import (
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/srs"
	"github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
)

type removeCall struct {
	id     uuid.UUID
	reason srs.RemovalReason
	final  *srspb.ServerUpdate
}

type fakeRemover struct {
	mu               sync.Mutex
	calls            []removeCall
	existed, running bool
}

func (f *fakeRemover) RemoveClient(id uuid.UUID, reason srs.RemovalReason, final *srspb.ServerUpdate) (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, removeCall{id, reason, final})
	return f.existed, f.running
}

func (f *fakeRemover) IsRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

func (f *fakeRemover) callsCopy() []removeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]removeCall(nil), f.calls...)
}

func newClientsTestApp(t *testing.T) *VCSApplication {
	t.Helper()
	a := newTestApp()
	a.eventBus = events.NewEventBus()
	t.Cleanup(a.eventBus.Stop)
	a.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return a
}

// eventLog records every bus event from the moment it is created.
type eventLog struct {
	ch chan events.Event
}

func collectEvents(t *testing.T, a *VCSApplication) *eventLog {
	t.Helper()
	ch := a.eventBus.Subscribe("*")
	t.Cleanup(func() { a.eventBus.Unsubscribe(ch) })
	return &eventLog{ch: ch}
}

// drain returns every event delivered within wait.
func (l *eventLog) drain(wait time.Duration) []events.Event {
	var out []events.Event
	timeout := time.After(wait)
	for {
		select {
		case e := <-l.ch:
			out = append(out, e)
		case <-timeout:
			return out
		}
	}
}

func named(evs []events.Event, name string) []events.Event {
	var out []events.Event
	for _, e := range evs {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}

func assertNotification(t *testing.T, evs []events.Event, title, message string) {
	t.Helper()
	for _, e := range named(evs, events.NotificationEvent) {
		if n, ok := e.Data.(events.Notification); ok && n.Title == title && n.Message == message {
			return
		}
	}
	t.Fatalf("no notification %q / %q in %v", title, message, named(evs, events.NotificationEvent))
}

func addClient(a *VCSApplication) uuid.UUID {
	id := uuid.New()
	a.ServerState.AddClient(id, &state.ClientState{Name: "target"})
	return id
}

func TestKickClientRemovesThroughHelper(t *testing.T) {
	a := newClientsTestApp(t)
	r := &fakeRemover{existed: true, running: true}
	a.remover = r
	id := addClient(a)
	log := collectEvents(t, a)

	a.KickClient(id.String(), "rude")

	calls := r.callsCopy()
	if len(calls) != 1 || calls[0].id != id || calls[0].reason != srs.ReasonKicked {
		t.Fatalf("remover calls %+v", calls)
	}
	sa := calls[0].final.GetServerAction()
	if sa.GetType() != srspb.ServerAction_KICK || sa.GetTargetClientGuid() != id.String() || sa.GetReason() != "rude" {
		t.Fatalf("final update %v", calls[0].final)
	}
	evs := log.drain(300 * time.Millisecond)
	if len(named(evs, events.ServerAction)) != 1 {
		t.Fatalf("want one ServerAction event, got %v", evs)
	}
	if n := len(named(evs, events.ClientsChanged)); n != 0 {
		t.Fatalf("app emitted %d ClientsChanged events; the removal helper owns ClientLeft", n)
	}
	assertNotification(t, evs, "Kick succeeded", "Client kicked successfully")
}

func TestKickClientUnknown(t *testing.T) {
	a := newClientsTestApp(t)
	a.remover = &fakeRemover{existed: false, running: true}
	log := collectEvents(t, a)

	a.KickClient(uuid.NewString(), "x")

	evs := log.drain(300 * time.Millisecond)
	assertNotification(t, evs, "Kick failed", "Client not found")
	if len(named(evs, events.ServerAction)) != 0 {
		t.Fatal("no ServerAction may be emitted for an unknown client")
	}
}

func TestKickClientControlNotRunning(t *testing.T) {
	for name, remover := range map[string]sessionRemover{
		"no remover":          nil,
		"remover not running": &fakeRemover{existed: false, running: false},
	} {
		t.Run(name, func(t *testing.T) {
			a := newClientsTestApp(t)
			a.remover = remover
			id := addClient(a)
			log := collectEvents(t, a)

			a.KickClient(id.String(), "x")

			evs := log.drain(300 * time.Millisecond)
			assertNotification(t, evs, "Kick failed", "Control server is not running")
			if len(named(evs, events.ServerAction)) != 0 {
				t.Fatal("no ServerAction may be emitted")
			}
			a.ServerState.RLock()
			_, ok := a.ServerState.Clients[id]
			a.ServerState.RUnlock()
			if !ok {
				t.Fatal("state must be untouched")
			}
		})
	}
}

func TestBanClientControlNotRunning(t *testing.T) {
	a := newClientsTestApp(t)
	id := addClient(a)
	a.voiceIPLookup = func(uuid.UUID) (net.IP, bool) { return net.ParseIP("203.0.113.7"), true }
	log := collectEvents(t, a)

	a.BanClient(id.String(), "x")

	assertNotification(t, log.drain(300*time.Millisecond), "Ban failed", "Control server is not running")
	if n := len(a.ServerState.BannedState.BannedClients); n != 0 {
		t.Fatalf("ban list changed: %d entries", n)
	}
}

func TestBanClientNoVoiceServer(t *testing.T) {
	a := newClientsTestApp(t)
	r := &fakeRemover{existed: true, running: true}
	a.remover = r
	id := addClient(a)
	log := collectEvents(t, a)

	a.BanClient(id.String(), "x") // no voice server: must not panic

	assertNotification(t, log.drain(300*time.Millisecond), "Ban failed", "Client not found in voice server")
	if n := len(a.ServerState.BannedState.BannedClients); n != 0 {
		t.Fatalf("ban list changed: %d entries", n)
	}
	if len(r.callsCopy()) != 0 {
		t.Fatal("nothing may be removed")
	}
}

func TestBanClientRemovesThroughHelper(t *testing.T) {
	a := newClientsTestApp(t)
	bs, err := state.GetBannedState(filepath.Join(t.TempDir(), "banned.json"))
	if err != nil {
		t.Fatal(err)
	}
	a.ServerState.BannedState = *bs
	r := &fakeRemover{existed: true, running: true}
	a.remover = r
	id := addClient(a)
	lookupUnlocked := false
	a.voiceIPLookup = func(uuid.UUID) (net.IP, bool) {
		// The voice lookup must run without ServerState held (D17).
		if a.ServerState.TryLock() {
			lookupUnlocked = true
			a.ServerState.Unlock()
		}
		return net.ParseIP("203.0.113.7"), true
	}
	log := collectEvents(t, a)

	a.BanClient(id.String(), "cheating")

	if !lookupUnlocked {
		t.Fatal("the voice IP lookup ran while ServerState was locked")
	}
	banned := a.ServerState.BannedState.BannedClients
	if len(banned) != 1 || banned[0].ID != id || banned[0].IPAddress != "203.0.113.7" || banned[0].Reason != "cheating" {
		t.Fatalf("ban list %+v", banned)
	}
	calls := r.callsCopy()
	if len(calls) != 1 || calls[0].id != id || calls[0].reason != srs.ReasonBanned ||
		calls[0].final.GetServerAction().GetType() != srspb.ServerAction_BAN {
		t.Fatalf("remover calls %+v", calls)
	}
	evs := log.drain(300 * time.Millisecond)
	if len(named(evs, events.BannedClientsChanged)) != 1 || len(named(evs, events.ServerAction)) != 1 {
		t.Fatalf("events %v", evs)
	}
	if n := len(named(evs, events.ClientsChanged)); n != 0 {
		t.Fatalf("app emitted %d ClientsChanged events; the removal helper owns ClientLeft", n)
	}
	assertNotification(t, evs, "Ban succeeded", "Client banned successfully")
}

func TestLocalVoiceDropperNilSafe(t *testing.T) {
	a := newClientsTestApp(t)
	localVoiceDropper{a}.DropVoiceSession(uuid.New(), "x") // must not panic
}

// TestRemoverAccessSynchronised: startGrpcServer sets the remover while the
// Kick/Ban bindings may read it from other goroutines.
func TestRemoverAccessSynchronised(t *testing.T) {
	a := newClientsTestApp(t)
	id := addClient(a)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			a.setRemover(&fakeRemover{existed: false, running: true})
		}
	}()
	for i := 0; i < 20; i++ {
		a.KickClient(id.String(), "x")
		a.BanClient(id.String(), "x")
	}
	wg.Wait()
}

// The order of Ban's checks is unchanged from before the removal helper:
// an unknown or already-banned client is reported as such even when it is
// not bound to a voice server.
func TestBanClientUnknownUnbound(t *testing.T) {
	a := newClientsTestApp(t)
	a.remover = &fakeRemover{existed: false, running: true}
	log := collectEvents(t, a)

	a.BanClient(uuid.NewString(), "x")

	assertNotification(t, log.drain(300*time.Millisecond), "Ban failed", "Client not found")
}

func TestBanClientAlreadyBannedUnbound(t *testing.T) {
	a := newClientsTestApp(t)
	r := &fakeRemover{existed: true, running: true}
	a.remover = r
	id := addClient(a)
	a.ServerState.BannedState.BannedClients = append(a.ServerState.BannedState.BannedClients, state.BannedClient{ID: id, Name: "target"})
	log := collectEvents(t, a)

	a.BanClient(id.String(), "x")

	assertNotification(t, log.drain(300*time.Millisecond), "Ban failed", "Client is already banned")
	if len(r.callsCopy()) != 0 {
		t.Fatal("nothing may be removed")
	}
}

// SRV-09: the GUI getters return copies that never alias live state.
func TestClientGettersReturnCopies(t *testing.T) {
	a := newClientsTestApp(t)
	id := addClient(a)
	a.ServerState.Lock()
	a.ServerState.RadioClients[id] = &state.RadioState{Radios: []state.Radio{{ID: 1, Frequency: 100}}}
	a.ServerState.Unlock()

	a.GetRadioClientMap()[id].Radios[0].Frequency = 1
	a.GetRadioClients().RadioClients[id.String()].Radios[0].Frequency = 2
	a.GetClientMap()[id].Name = "changed"
	cs := a.GetClients().Clients[id.String()]
	cs.Name = "changed2"

	a.ServerState.RLock()
	defer a.ServerState.RUnlock()
	if f := a.ServerState.RadioClients[id].Radios[0].Frequency; f != 100 {
		t.Fatalf("live Frequency = %v, want 100", f)
	}
	if n := a.ServerState.Clients[id].Name; n != "target" {
		t.Fatalf("live Name = %q, want target", n)
	}
}
