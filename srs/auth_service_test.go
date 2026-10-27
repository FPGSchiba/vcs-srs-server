package srs

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/peer"
)

func TestSplitHostForBanCheck(t *testing.T) {
	cases := []struct {
		addr     string
		wantHost string
	}{
		{"192.168.1.1:54321", "192.168.1.1"},
		{"10.0.0.5:1234", "10.0.0.5"},
		{"[::1]:8080", "::1"},
	}
	for _, tc := range cases {
		host, _, err := net.SplitHostPort(tc.addr)
		if err != nil {
			t.Fatalf("SplitHostPort(%q) error: %v", tc.addr, err)
		}
		if host != tc.wantHost {
			t.Fatalf("addr %q: got host %q, want %q", tc.addr, host, tc.wantHost)
		}
	}
}

const guestTestCoalitionPassword = "correct-horse-battery-staple"

// newGuestLoginTestServer builds an AuthServer wired up for exercising the
// guest-login flow end to end: guest auth enabled, one coalition configured,
// and a token signing key rooted in a scratch directory.
func newGuestLoginTestServer(t *testing.T) *AuthServer {
	t.Helper()

	tmp := t.TempDir()
	settingsState := &state.SettingsState{
		Coalitions: []state.Coalition{
			{Name: "Blue", Password: guestTestCoalitionPassword},
		},
		Security: state.SecuritySettings{
			EnableGuestAuth: true,
			Token: state.TokenSettings{
				Expiration:     3600,
				PrivateKeyFile: filepath.Join(tmp, "priv.pem"),
				PublicKeyFile:  filepath.Join(tmp, "pub.pem"),
				Issuer:         "test-issuer",
				Subject:        "test-subject",
			},
		},
	}

	eventBus := events.NewEventBus()
	t.Cleanup(eventBus.Stop)

	authServer := NewAuthServer(
		&state.ServerState{},
		settingsState,
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
		&state.DistributionState{},
		eventBus,
	)
	t.Cleanup(authServer.Stop)

	return authServer
}

func testPeerContext() context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5555}})
}

// TestGuestLoginConsumesAuthenticatingClient proves that a second GuestLogin
// call reusing the same ClientGuid is rejected once the first has succeeded.
// Before the fix, GuestLogin never deleted the authenticatingClients entry,
// so the GUID stayed redeemable for the rest of its 20-minute window --
// letting an attacker who sniffed a victim's GUID off a HELLO packet replay
// it into GuestLogin, rotate the victim's VoiceSecret, and hijack the
// session.
func TestGuestLoginConsumesAuthenticatingClient(t *testing.T) {
	authServer := newGuestLoginTestServer(t)
	ctx := testPeerContext()

	initResp, err := authServer.InitAuth(ctx, &pb.AuthInitRequest{
		Capabilities: &pb.ClientCapabilities{
			Version:                    "0.1.0",
			SupportedDistributionModes: []pb.DistributionMode{pb.DistributionMode_STANDALONE},
		},
	})
	if err != nil {
		t.Fatalf("InitAuth: %v", err)
	}
	if !initResp.Success {
		t.Fatalf("InitAuth did not succeed: %+v", initResp)
	}
	clientGuid := initResp.GetResult().ClientGuid

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(guestTestCoalitionPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt.GenerateFromPassword: %v", err)
	}

	loginReq := &pb.GuestLoginRequest{
		Name:       "Victim",
		Password:   string(hashedPassword),
		UnitId:     "AB1",
		ClientGuid: clientGuid,
	}

	firstResp, err := authServer.GuestLogin(ctx, loginReq)
	if err != nil {
		t.Fatalf("first GuestLogin returned error: %v", err)
	}
	if !firstResp.Success {
		t.Fatalf("first GuestLogin did not succeed: %+v", firstResp)
	}

	secondResp, err := authServer.GuestLogin(ctx, loginReq)
	if err != nil {
		t.Fatalf("second GuestLogin returned unexpected transport error: %v", err)
	}
	if secondResp.Success {
		t.Fatal("second GuestLogin with the same ClientGuid succeeded; the authenticatingClients entry was not consumed by the first login")
	}
}

func TestInitAuthNilCapabilities(t *testing.T) {
	s := newGuestLoginTestServer(t)
	resp, err := s.InitAuth(testPeerContext(), &pb.AuthInitRequest{})
	if err != nil {
		t.Fatalf("InitAuth returned error: %v", err)
	}
	if resp.GetSuccess() {
		t.Fatal("InitAuth without capabilities succeeded")
	}
	if got := resp.GetErrorMessage(); got != "Missing client capabilities" {
		t.Fatalf("error message = %q", got)
	}
}

func TestInitAuthWithoutPeer(t *testing.T) {
	s := newGuestLoginTestServer(t)
	resp, err := s.InitAuth(context.Background(), &pb.AuthInitRequest{})
	if err != nil {
		t.Fatalf("InitAuth returned error: %v", err)
	}
	if resp.GetSuccess() || resp.GetErrorMessage() != "Missing client capabilities" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestInitAuthNilDistributionModes(t *testing.T) {
	s := newGuestLoginTestServer(t)
	resp, err := s.InitAuth(testPeerContext(), &pb.AuthInitRequest{
		Capabilities: &pb.ClientCapabilities{Version: MinClientVersion},
	})
	if err != nil {
		t.Fatalf("InitAuth returned error: %v", err)
	}
	if resp.GetSuccess() {
		t.Fatal("InitAuth without distribution modes succeeded")
	}
	if got := resp.GetErrorMessage(); !strings.HasPrefix(got, "Unsupported distribution capabilities") {
		t.Fatalf("error message = %q", got)
	}
}

// TestInvalidClientGuidReturnsBody pins SRV-07's AuthService rule: a business
// failure is a Success:false body with a nil error. grpc-go drops the body
// when the error is non-nil, so returning both left clients with a bodiless
// codes.Unknown.
func TestInvalidClientGuidReturnsBody(t *testing.T) {
	s := newGuestLoginTestServer(t)
	s.settingsState.Security.EnablePluginAuth = true
	ctx := testPeerContext()
	const bad = "not-a-uuid"
	const want = "Invalid ClientGuid"

	g, err := s.GuestLogin(ctx, &pb.GuestLoginRequest{ClientGuid: bad})
	if err != nil || g.GetSuccess() || g.GetErrorMessage() != want {
		t.Fatalf("GuestLogin: resp %+v, err %v", g, err)
	}
	st, err := s.StartAuth(ctx, &pb.StartAuthRequest{ClientGuid: bad})
	if err != nil || st.GetSuccess() || st.GetErrorMessage() != want {
		t.Fatalf("StartAuth: resp %+v, err %v", st, err)
	}
	c, err := s.ContinueAuth(ctx, &pb.ContinueAuthRequest{ClientGuid: bad})
	if err != nil || c.GetSuccess() || c.GetErrorMessage() != want {
		t.Fatalf("ContinueAuth: resp %+v, err %v", c, err)
	}
	u, err := s.UnitSelect(ctx, &pb.UnitSelectRequest{ClientGuid: bad})
	if err != nil || u.GetSuccess() || u.GetErrorMessage() != want {
		t.Fatalf("UnitSelect: resp %+v, err %v", u, err)
	}
}

// --- SRV-17: bounded, swept pending logins ---

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func withFakeClock(s *AuthServer) *fakeClock {
	c := &fakeClock{t: time.Now()}
	s.mu.Lock()
	s.now = c.Now
	s.mu.Unlock()
	return c
}

func peerCtx(ip string) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 5555}})
}

func validInitReq() *pb.AuthInitRequest {
	return &pb.AuthInitRequest{
		Capabilities: &pb.ClientCapabilities{
			Version:                    MinClientVersion,
			SupportedDistributionModes: []pb.DistributionMode{pb.DistributionMode_STANDALONE},
		},
	}
}

func mustInit(t *testing.T, s *AuthServer, ctx context.Context) uuid.UUID {
	t.Helper()
	resp, err := s.InitAuth(ctx, validInitReq())
	if err != nil {
		t.Fatalf("InitAuth error: %v", err)
	}
	if !resp.GetSuccess() {
		t.Fatalf("InitAuth refused: %q", resp.GetErrorMessage())
	}
	return uuid.MustParse(resp.GetResult().GetClientGuid())
}

func hashPassword(t *testing.T, pw string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	return string(h)
}

func pendingCount(s *AuthServer) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.authenticatingClients)
}

func perIPCount(s *AuthServer, ip string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.pendingPerIP[ip]
	return n, ok
}

func TestPendingLoginConstants(t *testing.T) {
	if pendingLoginTTL != 5*time.Minute {
		t.Errorf("pendingLoginTTL = %v", pendingLoginTTL)
	}
	if maxPendingLogins != 10000 {
		t.Errorf("maxPendingLogins = %d", maxPendingLogins)
	}
	if maxPendingLoginsPerIP != 32 {
		t.Errorf("maxPendingLoginsPerIP = %d", maxPendingLoginsPerIP)
	}
	if pendingSweepInterval != 60*time.Second {
		t.Errorf("pendingSweepInterval = %v", pendingSweepInterval)
	}
	s := newGuestLoginTestServer(t)
	if s.maxPending != 10000 || s.maxPendingPerIP != 32 {
		t.Errorf("fresh server limits = %d/%d, want 10000/32", s.maxPending, s.maxPendingPerIP)
	}
}

func TestInitAuthPerIPCap(t *testing.T) {
	s := newGuestLoginTestServer(t)
	for i := 0; i < 32; i++ {
		mustInit(t, s, peerCtx("10.0.0.1"))
	}
	resp, err := s.InitAuth(peerCtx("10.0.0.1"), validInitReq())
	if err != nil {
		t.Fatalf("InitAuth error: %v", err)
	}
	if resp.GetSuccess() {
		t.Fatal("33rd InitAuth from one address succeeded")
	}
	if got := resp.GetErrorMessage(); got != "Too many pending logins from your address, please try again shortly" {
		t.Fatalf("message = %q", got)
	}
	mustInit(t, s, peerCtx("10.0.0.2"))
	if n := pendingCount(s); n != 33 {
		t.Fatalf("pending = %d, want 33", n)
	}
}

func TestInitAuthGlobalCap(t *testing.T) {
	s := newGuestLoginTestServer(t)
	s.mu.Lock()
	s.maxPending = 3
	s.mu.Unlock()
	var guids []uuid.UUID
	for _, ip := range []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"} {
		guids = append(guids, mustInit(t, s, peerCtx(ip)))
	}
	resp, err := s.InitAuth(peerCtx("10.0.0.4"), validInitReq())
	if err != nil {
		t.Fatalf("InitAuth error: %v", err)
	}
	if resp.GetSuccess() {
		t.Fatal("InitAuth over the global cap succeeded")
	}
	if got := resp.GetErrorMessage(); got != "Server is busy: too many pending logins, please try again shortly" {
		t.Fatalf("message = %q", got)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, g := range guids {
		if _, ok := s.authenticatingClients[g]; !ok {
			t.Fatalf("existing pending login %s was evicted", g)
		}
	}
}

func TestExpiredPendingRejectedWithoutSweep(t *testing.T) {
	s := newGuestLoginTestServer(t)
	clock := withFakeClock(s)
	guid := mustInit(t, s, testPeerContext())
	clock.Advance(pendingLoginTTL + time.Second)

	resp, err := s.GuestLogin(testPeerContext(), &pb.GuestLoginRequest{
		Name: "Pilot", Password: hashPassword(t, guestTestCoalitionPassword), UnitId: "AB1", ClientGuid: guid.String(),
	})
	if err != nil {
		t.Fatalf("GuestLogin error: %v", err)
	}
	if resp.GetSuccess() || resp.GetErrorMessage() != "ClientGuid not found, please initialize first" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if n := pendingCount(s); n != 0 {
		t.Fatalf("expired entry still present (%d pending)", n)
	}
	if _, ok := perIPCount(s, "127.0.0.1"); ok {
		t.Fatal("per-IP count not released for the expired entry")
	}
}

func TestLoginStepRearmsTTL(t *testing.T) {
	s := newGuestLoginTestServer(t)
	clock := withFakeClock(s)
	guid := mustInit(t, s, testPeerContext())

	clock.Advance(4 * time.Minute)
	resp, err := s.GuestLogin(testPeerContext(), &pb.GuestLoginRequest{
		Name: "Pilot", Password: hashPassword(t, "wrong-password"), UnitId: "AB1", ClientGuid: guid.String(),
	})
	if err != nil {
		t.Fatalf("GuestLogin error: %v", err)
	}
	if resp.GetErrorMessage() != "No coalition found with that password" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	clock.Advance(4 * time.Minute)
	resp, err = s.GuestLogin(testPeerContext(), &pb.GuestLoginRequest{
		Name: "Pilot", Password: hashPassword(t, guestTestCoalitionPassword), UnitId: "AB1", ClientGuid: guid.String(),
	})
	if err != nil {
		t.Fatalf("GuestLogin error: %v", err)
	}
	if !resp.GetSuccess() {
		t.Fatalf("login 8 minutes after InitAuth but 4 after the last step failed: %q", resp.GetErrorMessage())
	}
}

func TestSweepExpiredRemovesAndDecrements(t *testing.T) {
	s := newGuestLoginTestServer(t)
	clock := withFakeClock(s)
	mustInit(t, s, peerCtx("10.0.0.1"))
	mustInit(t, s, peerCtx("10.0.0.1"))
	clock.Advance(pendingLoginTTL + time.Second)

	s.sweepExpired()

	if n := pendingCount(s); n != 0 {
		t.Fatalf("pending = %d after sweep, want 0", n)
	}
	if _, ok := perIPCount(s, "10.0.0.1"); ok {
		t.Fatal("pendingPerIP still has a key for 10.0.0.1")
	}
}

func TestSuccessfulLoginDecrementsPerIP(t *testing.T) {
	s := newGuestLoginTestServer(t)
	guid := mustInit(t, s, testPeerContext())
	resp, err := s.GuestLogin(testPeerContext(), &pb.GuestLoginRequest{
		Name: "Pilot", Password: hashPassword(t, guestTestCoalitionPassword), UnitId: "AB1", ClientGuid: guid.String(),
	})
	if err != nil || !resp.GetSuccess() {
		t.Fatalf("GuestLogin: resp %+v, err %v", resp, err)
	}
	if n, _ := perIPCount(s, "127.0.0.1"); n != 0 {
		t.Fatalf("pendingPerIP[127.0.0.1] = %d after a successful login", n)
	}
}

// Review focus: a plugin login whose pending entry was swept mid-flow must get
// the "please initialize first" body, never a nil-pointer panic.
func TestSetPluginSessionMissingGUID(t *testing.T) {
	s := newGuestLoginTestServer(t)
	if s.setPluginSession(uuid.New(), "p", "sid") {
		t.Fatal("setPluginSession on an unknown GUID returned true")
	}
}

func TestCompletePendingMissingGUID(t *testing.T) {
	s := newGuestLoginTestServer(t)
	if s.completePending(uuid.New(), "Pilot", "a-b-c", []uint8{0}, nil) {
		t.Fatal("completePending on an unknown GUID returned true")
	}
}

// Review focus: handleAuthComplete used to replace the entry wholesale; losing
// IP there would leave pendingPerIP permanently incremented and eventually
// lock the address out.
func TestCompletePendingPreservesIP(t *testing.T) {
	s := newGuestLoginTestServer(t)
	guid := mustInit(t, s, peerCtx("10.0.0.9"))
	if !s.completePending(guid, "Pilot", "a-b-c", []uint8{0}, nil) {
		t.Fatal("completePending returned false for a live entry")
	}
	s.mu.RLock()
	entry := s.authenticatingClients[guid]
	ip, secret := entry.IP, entry.Secret
	s.mu.RUnlock()
	if ip != "10.0.0.9" || secret != "a-b-c" {
		t.Fatalf("entry IP=%q Secret=%q", ip, secret)
	}
	if n, _ := perIPCount(s, "10.0.0.9"); n != 1 {
		t.Fatalf("pendingPerIP[10.0.0.9] = %d, want 1", n)
	}

	resp, err := s.UnitSelect(peerCtx("10.0.0.9"), &pb.UnitSelectRequest{
		ClientGuid: guid.String(), Secret: "a-b-c", UnitId: "AB1", Role: 0, Coalition: "Blue",
	})
	if err != nil || !resp.GetSuccess() {
		t.Fatalf("UnitSelect: resp %+v, err %v", resp, err)
	}
	if n, _ := perIPCount(s, "10.0.0.9"); n != 0 {
		t.Fatalf("pendingPerIP[10.0.0.9] = %d after UnitSelect, want 0", n)
	}
}

func TestAuthServerStopEndsSweeper(t *testing.T) {
	s := newGuestLoginTestServer(t)
	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return within 1s")
	}
	select {
	case <-s.sweepDone:
	default:
		t.Fatal("sweepDone not closed after Stop")
	}
	s.Stop() // idempotent; t.Cleanup calls it once more
}
