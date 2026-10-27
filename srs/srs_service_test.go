package srs

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/google/uuid"
)

// newTestServer builds a SimpleRadioServer with its own event bus, which
// is stopped when the test ends.
func newTestServer(t *testing.T) *SimpleRadioServer {
	t.Helper()
	bus := events.NewEventBus()
	t.Cleanup(bus.Stop)
	return &SimpleRadioServer{
		serverState: &state.ServerState{
			Clients:      make(map[uuid.UUID]*state.ClientState),
			RadioClients: make(map[uuid.UUID]*state.RadioState),
		},
		settingsState: &state.SettingsState{},
		logger:        slog.New(slog.NewTextHandler(os.Stderr, nil)),
		eventBus:      bus,
		now:           time.Now,
		staleAfter:    10 * time.Minute,
		streams:       make(map[uuid.UUID]*subscription),
		detachedAt:    make(map[uuid.UUID]time.Time),
		stopChan:      make(chan struct{}),
	}
}

// withSRSClock swaps the server's clock for a fake one and returns it.
func withSRSClock(s *SimpleRadioServer) *fakeClock {
	c := &fakeClock{t: time.Now()}
	s.mu.Lock()
	s.now = c.Now
	s.mu.Unlock()
	return c
}

func clientCtx(id uuid.UUID) context.Context {
	return context.WithValue(context.Background(), utils.ClientIDKey, id.String())
}

// SRV-01: every unary RPC a live client makes refreshes its LastUpdate, so
// a client that only pings is never mistaken for a stale one.
func TestRPCsRefreshLastUpdate(t *testing.T) {
	calls := map[string]func(s *SimpleRadioServer, ctx context.Context){
		"Ping": func(s *SimpleRadioServer, ctx context.Context) {
			_, _ = s.Ping(ctx, &pb.PingRequest{})
		},
		"SyncClient": func(s *SimpleRadioServer, ctx context.Context) {
			_, _ = s.SyncClient(ctx, &pb.Empty{})
		},
		"UpdateRadioInfo": func(s *SimpleRadioServer, ctx context.Context) {
			_, _ = s.UpdateRadioInfo(ctx, &pb.RadioInfo{})
		},
		"UpdateClientInfo": func(s *SimpleRadioServer, ctx context.Context) {
			_, _ = s.UpdateClientInfo(ctx, &pb.ClientInfo{Name: "Alice", UnitId: "AB"})
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			s := newTestServer(t)
			clock := withSRSClock(s)
			id := uuid.New()
			s.serverState.AddClient(id, &state.ClientState{Name: "Alice"})
			s.serverState.Lock()
			s.serverState.Clients[id].LastUpdate = clock.Now().Add(-time.Hour)
			s.serverState.Unlock()
			clock.Advance(time.Minute)

			call(s, clientCtx(id))

			s.serverState.RLock()
			got := s.serverState.Clients[id].LastUpdate
			s.serverState.RUnlock()
			if !got.Equal(clock.Now()) {
				t.Fatalf("LastUpdate = %v, want %v", got, clock.Now())
			}
		})
	}
}

func TestBuildServerUpdate_ClientJoined(t *testing.T) {
	s := newTestServer(t)
	id := uuid.New()
	evt := events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{
			Type:     events.ClientJoined,
			ClientID: id,
			Clients:  map[uuid.UUID]*state.ClientState{id: {Name: "Alice", Coalition: "Blue"}},
		},
	}
	update := s.buildServerUpdate(evt)
	if update == nil {
		t.Fatal("expected non-nil update")
	}
	if update.Type != pb.ServerUpdate_CLIENT_JOINED {
		t.Fatalf("expected CLIENT_JOINED, got %v", update.Type)
	}
	cu := update.GetClientUpdate()
	if cu == nil {
		t.Fatal("expected ClientUpdate payload")
	}
	if cu.GetClientGuid() != id.String() {
		t.Fatalf("expected %s, got %s", id.String(), cu.GetClientGuid())
	}
	if cu.GetClientInfo() == nil || cu.GetClientInfo().Name != "Alice" {
		t.Fatal("expected ClientInfo with Name=Alice")
	}
}

func TestBuildServerUpdate_ClientLeft(t *testing.T) {
	s := newTestServer(t)
	id := uuid.New()
	evt := events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{
			Type:     events.ClientLeft,
			ClientID: id,
			Clients:  map[uuid.UUID]*state.ClientState{},
		},
	}
	update := s.buildServerUpdate(evt)
	if update == nil || update.Type != pb.ServerUpdate_CLIENT_LEFT {
		t.Fatalf("expected CLIENT_LEFT, got %v", update)
	}
	cu := update.GetClientUpdate()
	if cu == nil || cu.GetClientGuid() != id.String() {
		t.Fatal("expected ClientUpdate with correct guid")
	}
}

func TestBuildServerUpdate_RadioUpdate(t *testing.T) {
	s := newTestServer(t)
	id := uuid.New()
	evt := events.Event{
		Name: events.RadioClientsChanged,
		Data: events.RadioChangeEvent{
			Type:     events.RadioUpdated,
			ClientID: id,
			Radios:   map[uuid.UUID]*state.RadioState{id: {Muted: true}},
		},
	}
	update := s.buildServerUpdate(evt)
	if update == nil || update.Type != pb.ServerUpdate_CLIENT_RADIO_UPDATE {
		t.Fatalf("expected CLIENT_RADIO_UPDATE, got %v", update)
	}
	cu := update.GetClientUpdate()
	if cu == nil {
		t.Fatal("expected ClientUpdate")
	}
	if cu.GetClientGuid() != id.String() {
		t.Fatalf("expected %s, got %s", id.String(), cu.GetClientGuid())
	}
	if !cu.GetRadioInfo().GetMuted() {
		t.Fatal("expected Muted=true")
	}
}

func TestBuildServerUpdate_ServerAction_Kick(t *testing.T) {
	s := newTestServer(t)
	id := uuid.New()
	evt := events.Event{
		Name: events.ServerAction,
		Data: events.ServerActionEvent{
			ActionType:     events.ActionKick,
			TargetClientID: id,
			Reason:         "rule violation",
		},
	}
	update := s.buildServerUpdate(evt)
	if update == nil || update.Type != pb.ServerUpdate_SERVER_ACTION {
		t.Fatalf("expected SERVER_ACTION, got %v", update)
	}
	sa := update.GetServerAction()
	if sa == nil {
		t.Fatal("expected ServerAction payload")
	}
	if sa.GetType() != pb.ServerAction_KICK {
		t.Fatalf("expected KICK, got %v", sa.GetType())
	}
	if sa.GetTargetClientGuid() != id.String() {
		t.Fatalf("expected %s, got %s", id.String(), sa.GetTargetClientGuid())
	}
	if sa.GetReason() != "rule violation" {
		t.Fatalf("expected 'rule violation', got %q", sa.GetReason())
	}
}

func TestBuildServerUpdate_UnknownEvent_ReturnsNil(t *testing.T) {
	s := newTestServer(t)
	evt := events.Event{Name: "unknown/event", Data: nil}
	if s.buildServerUpdate(evt) != nil {
		t.Fatal("expected nil for unknown event")
	}
}

func TestBuildServerUpdate_BadPayload_ReturnsNil(t *testing.T) {
	s := newTestServer(t)
	evt := events.Event{Name: events.ClientsChanged, Data: "not a ClientChangeEvent"}
	if s.buildServerUpdate(evt) != nil {
		t.Fatal("expected nil when payload is wrong type")
	}
}

func TestBuildServerUpdate_ClientInfoUpdated(t *testing.T) {
	s := newTestServer(t)
	id := uuid.New()
	evt := events.Event{
		Name: events.ClientsChanged,
		Data: events.ClientChangeEvent{
			Type:     events.ClientInfoUpdated,
			ClientID: id,
			Clients:  map[uuid.UUID]*state.ClientState{id: {Name: "Bob", Coalition: "Red"}},
		},
	}
	update := s.buildServerUpdate(evt)
	if update == nil || update.Type != pb.ServerUpdate_CLIENT_INFO_UPDATE {
		t.Fatalf("expected CLIENT_INFO_UPDATE, got %v", update)
	}
	cu := update.GetClientUpdate()
	if cu == nil || cu.GetClientGuid() != id.String() {
		t.Fatal("expected ClientUpdate with correct guid")
	}
	if cu.GetClientInfo() == nil || cu.GetClientInfo().Name != "Bob" {
		t.Fatal("expected ClientInfo with Name=Bob")
	}
}

func TestBuildServerUpdate_SettingsChanged(t *testing.T) {
	s := newTestServer(t)
	evt := events.Event{Name: events.SettingsChanged, Data: nil}
	update := s.buildServerUpdate(evt)
	if update == nil || update.Type != pb.ServerUpdate_SERVER_SETTINGS_CHANGED {
		t.Fatalf("expected SERVER_SETTINGS_CHANGED, got %v", update)
	}
}

func TestVoiceSecretForKnownClient(t *testing.T) {
	s := newTestServer(t)
	id := uuid.New()
	s.serverState.AddClient(id, &state.ClientState{Name: "Alice", Coalition: "Blue"})

	s.serverState.RLock()
	want := s.serverState.Clients[id].VoiceSecret
	s.serverState.RUnlock()

	if want == "" {
		t.Fatal("AddClient should have generated a secret")
	}
	if got := s.voiceSecretFor(id); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestVoiceSecretForUnknownClientIsEmpty(t *testing.T) {
	s := newTestServer(t)
	if got := s.voiceSecretFor(uuid.New()); got != "" {
		t.Fatalf("expected an empty secret for an unknown client, got %q", got)
	}
}

// SRV-09: the RadioClientsChanged snapshot never shares memory with live state.
func TestUpdateRadioInfoSnapshotIndependent(t *testing.T) {
	s := newTestServer(t)
	id := uuid.New()
	s.serverState.AddClient(id, &state.ClientState{Name: "Alice"})
	ch := s.eventBus.Subscribe(events.RadioClientsChanged)

	if _, err := s.UpdateRadioInfo(clientCtx(id), &pb.RadioInfo{Radios: []*pb.Radio{{Id: 1, Frequency: 251}}}); err != nil {
		t.Fatalf("UpdateRadioInfo: %v", err)
	}
	var ev events.RadioChangeEvent
	select {
	case e := <-ch:
		ev = e.Data.(events.RadioChangeEvent)
	case <-time.After(time.Second):
		t.Fatal("no RadioClientsChanged event")
	}

	s.serverState.Lock()
	s.serverState.RadioClients[id].Muted = true
	s.serverState.RadioClients[id].Radios[0].Frequency = 1
	s.serverState.Unlock()

	got := ev.Radios[id]
	if got.Muted || got.Radios[0].Frequency != 251 {
		t.Fatalf("event snapshot changed with live state: muted=%v freq=%v", got.Muted, got.Radios[0].Frequency)
	}
}

// SRV-15: the SyncClient roster carries UnitId and RoleId.
func TestSyncClientRosterHasUnitAndRole(t *testing.T) {
	s := newTestServer(t)
	id := uuid.New()
	s.serverState.AddClient(id, &state.ClientState{Name: "A", UnitId: "AB", Coalition: "Blue", Role: utils.MemberRole})

	resp, err := s.SyncClient(clientCtx(id), &pb.Empty{})
	if err != nil {
		t.Fatalf("SyncClient: %v", err)
	}
	data := resp.GetData()
	c, ok := data.GetClients()[id.String()]
	if !ok {
		t.Fatal("caller missing from the roster")
	}
	if c.GetUnitId() != "AB" || c.GetRoleId() != uint32(utils.MemberRole) {
		t.Fatalf("roster entry UnitId=%q RoleId=%d, want AB/%d", c.GetUnitId(), c.GetRoleId(), utils.MemberRole)
	}
	if _, ok := data.GetRadios()[id.String()]; !ok {
		t.Fatal("caller's radios missing from the sync result")
	}
}

// SRV-15: SyncClient changes no roster field, so it publishes nothing.
func TestSyncClientPublishesNothing(t *testing.T) {
	s := newTestServer(t)
	id := addTestClient(s)
	ch := s.eventBus.Subscribe(events.ClientsChanged)

	if _, err := s.SyncClient(clientCtx(id), &pb.Empty{}); err != nil {
		t.Fatalf("SyncClient: %v", err)
	}
	select {
	case e := <-ch:
		t.Fatalf("SyncClient published %+v", e)
	case <-time.After(300 * time.Millisecond):
	}
}

// SRV-09/SRV-15: SyncClient reads state under the lock, so it is race-free
// against Ping and UpdateClientInfo writes.
func TestSyncClientConcurrentWithPing(t *testing.T) {
	s := newTestServer(t)
	id := uuid.New()
	s.serverState.AddClient(id, &state.ClientState{Name: "Alice", UnitId: "AB", Coalition: "Blue", Role: utils.MemberRole})
	ctx := clientCtx(id)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.SyncClient(ctx, &pb.Empty{})
			_, _ = s.Ping(ctx, &pb.PingRequest{})
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_, _ = s.UpdateClientInfo(ctx, &pb.ClientInfo{Name: "Alice", UnitId: "AB", Coalition: "Blue", RoleId: uint32(utils.MemberRole)})
		}
	}()
	wg.Wait()
}

const guestCoalitionMsg = "Guests cannot change coalition here: log in again with that coalition's password."

// countInfoUpdates counts ClientInfoUpdated events for id arriving on ch
// within wait, checking each carries the caller's GUID and wantName.
func countInfoUpdates(t *testing.T, ch chan events.Event, id uuid.UUID, wantName string, wait time.Duration) int {
	t.Helper()
	n := 0
	timeout := time.After(wait)
	for {
		select {
		case e := <-ch:
			ce, ok := e.Data.(events.ClientChangeEvent)
			if !ok || ce.Type != events.ClientInfoUpdated {
				continue
			}
			if ce.ClientID != id {
				t.Fatalf("ClientInfoUpdated for %s, want caller %s", ce.ClientID, id)
			}
			if c := ce.Clients[id]; c == nil || c.Name != wantName {
				t.Fatalf("ClientInfoUpdated snapshot entry %+v, want Name %q", c, wantName)
			}
			n++
		case <-timeout:
			return n
		}
	}
}

// SRV-16: UpdateClientInfo validates every field, then applies all of them
// atomically; a guest may not change coalition.
func TestUpdateClientInfo(t *testing.T) {
	type fields struct {
		name, unit, coalition string
		role                  uint8
	}
	unchanged := func(role uint8) fields { return fields{"Old", "AB", "Blue", role} }
	cases := []struct {
		name     string
		role     uint8
		req      fields
		success  bool
		contains []string
		after    fields
		events   int
	}{
		{"guest no-op rename", utils.GuestRole, fields{"New", "CD", "Blue", utils.GuestRole}, true, nil,
			fields{"New", "CD", "Blue", utils.GuestRole}, 1},
		{"guest other coalition", utils.GuestRole, fields{"New", "CD", "Red", utils.GuestRole}, false,
			[]string{"log in again with that coalition's password"}, unchanged(utils.GuestRole), 0},
		{"guest other coalition + bad unit", utils.GuestRole, fields{"New", "x", "Red", utils.GuestRole}, false,
			[]string{"Invalid unit ID. It must be 2 to 4 uppercase alphanumeric characters.", guestCoalitionMsg},
			unchanged(utils.GuestRole), 0},
		{"guest unknown coalition", utils.GuestRole, fields{"New", "CD", "Nope", utils.GuestRole}, false,
			[]string{guestCoalitionMsg}, unchanged(utils.GuestRole), 0},
		{"member to Red", utils.MemberRole, fields{"New", "CD", "Red", utils.MemberRole}, true, nil,
			fields{"New", "CD", "Red", utils.MemberRole}, 1},
		{"member unknown coalition", utils.MemberRole, fields{"New", "CD", "Nope", utils.MemberRole}, false,
			[]string{"Coalition not found, please select an existing coalition."}, unchanged(utils.MemberRole), 0},
		{"member upgrade", utils.MemberRole, fields{"New", "CD", "Blue", utils.OfficerRole}, false,
			[]string{"You cannot swap roles."}, unchanged(utils.MemberRole), 0},
		{"admin downgrade", utils.AdminRole, fields{"New", "CD", "Blue", utils.MemberRole}, true, nil,
			fields{"New", "CD", "Blue", utils.MemberRole}, 1},
		{"bad name only", utils.MemberRole, fields{"", "CD", "Blue", utils.MemberRole}, false,
			[]string{"Invalid username."}, unchanged(utils.MemberRole), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			s.settingsState = &state.SettingsState{Coalitions: []state.Coalition{{Name: "Blue"}, {Name: "Red"}}}
			id := uuid.New()
			s.serverState.AddClient(id, &state.ClientState{Name: "Old", UnitId: "AB", Coalition: "Blue", Role: tc.role})
			ch := s.eventBus.Subscribe(events.ClientsChanged)

			resp, err := s.UpdateClientInfo(clientCtx(id), &pb.ClientInfo{
				Name: tc.req.name, UnitId: tc.req.unit, Coalition: tc.req.coalition, RoleId: uint32(tc.req.role),
			})
			if err != nil {
				t.Fatalf("UpdateClientInfo: %v", err)
			}
			if resp.GetSuccess() != tc.success {
				t.Fatalf("Success = %v (%q), want %v", resp.GetSuccess(), resp.GetErrorMessage(), tc.success)
			}
			for _, sub := range tc.contains {
				if !strings.Contains(resp.GetErrorMessage(), sub) {
					t.Fatalf("message %q does not contain %q", resp.GetErrorMessage(), sub)
				}
			}

			s.serverState.RLock()
			c := *s.serverState.Clients[id]
			s.serverState.RUnlock()
			got := fields{c.Name, c.UnitId, c.Coalition, c.Role}
			if got != tc.after {
				t.Fatalf("fields after = %+v, want %+v", got, tc.after)
			}
			if n := countInfoUpdates(t, ch, id, tc.after.name, 300*time.Millisecond); n != tc.events {
				t.Fatalf("%d ClientInfoUpdated events, want %d", n, tc.events)
			}
		})
	}
}

// SRV-16 (D7): an unchanged coalition is accepted even after an admin
// removed it from the settings.
func TestUpdateClientInfoKeepsRemovedCoalition(t *testing.T) {
	s := newTestServer(t)
	s.settingsState = &state.SettingsState{Coalitions: []state.Coalition{{Name: "Blue"}}}
	id := uuid.New()
	s.serverState.AddClient(id, &state.ClientState{Name: "Old", UnitId: "AB", Coalition: "Gone", Role: utils.GuestRole})

	resp, err := s.UpdateClientInfo(clientCtx(id), &pb.ClientInfo{Name: "New", UnitId: "CD", Coalition: "Gone", RoleId: uint32(utils.GuestRole)})
	if err != nil {
		t.Fatalf("UpdateClientInfo: %v", err)
	}
	if !resp.GetSuccess() {
		t.Fatalf("UpdateClientInfo failed: %q", resp.GetErrorMessage())
	}
}
