package srs

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/events"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
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
