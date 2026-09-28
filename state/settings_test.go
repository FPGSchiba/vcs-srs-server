package state_test

import (
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/state"
	"gopkg.in/yaml.v3"
)

func TestSettingsState_Snapshot(t *testing.T) {
	s := &state.SettingsState{
		General:      state.GeneralSettings{MaxRadiosPerUser: 10},
		Security:     state.SecuritySettings{EnableGuestAuth: true},
		VoiceControl: state.VoiceControlSettings{Port: 14448},
		Api:          state.ApiSettings{Key: "secret-key"},
	}

	snap := s.Snapshot()

	if snap.General.MaxRadiosPerUser != 10 {
		t.Fatalf("expected 10, got %d", snap.General.MaxRadiosPerUser)
	}
	if !snap.Security.EnableGuestAuth {
		t.Fatal("expected EnableGuestAuth=true")
	}
	if snap.VoiceControl.Port != 14448 {
		t.Fatalf("expected port 14448, got %d", snap.VoiceControl.Port)
	}
	if snap.Api.Key != "secret-key" {
		t.Fatalf("expected 'secret-key', got %q", snap.Api.Key)
	}
}

func TestSettingsState_ClientTLSIsTopLevel(t *testing.T) {
	// clientTLS is top-level rather than nested under servers.control on
	// purpose. app.SaveServerSettings assigns SettingsState.Servers =
	// *newSettings wholesale, and the GraphQL resolver rebuilds
	// ServerSettings from input carrying only host and port -- so TLS nested
	// inside Servers would be erased by any admin settings save and then
	// persisted as plaintext. This test pins the placement.
	s := &state.SettingsState{
		Servers: state.ServerSettings{
			Control: state.ServerSetting{Host: "0.0.0.0", Port: 5002},
		},
		ClientTLS: state.ClientTLSSettings{
			CertificateFile: "/certs/srs-cert.pem",
			PrivateKeyFile:  "/certs/srs-private-key.pem",
			ServerName:      "vcs.vngd.net",
		},
	}

	// Simulate the admin write path: replace Servers wholesale.
	s.Servers = state.ServerSettings{
		Control: state.ServerSetting{Host: "0.0.0.0", Port: 5002},
	}

	if s.ClientTLS.CertificateFile != "/certs/srs-cert.pem" {
		t.Fatal("clientTLS must survive a wholesale replacement of Servers")
	}
}

func TestSettingsState_ClientTLSSnapshot(t *testing.T) {
	s := &state.SettingsState{
		ClientTLS: state.ClientTLSSettings{
			CertificateFile: "/certs/srs-cert.pem",
			PrivateKeyFile:  "/certs/srs-private-key.pem",
			ServerName:      "vcs.vngd.net",
		},
	}
	snap := s.Snapshot()
	if snap.ClientTLS.CertificateFile != "/certs/srs-cert.pem" {
		t.Fatalf("expected the cert path in the snapshot, got %q", snap.ClientTLS.CertificateFile)
	}
	if snap.ClientTLS.ServerName != "vcs.vngd.net" {
		t.Fatalf("expected the server name in the snapshot, got %q", snap.ClientTLS.ServerName)
	}
}

func TestSettingsState_ClientTLSYAMLRoundTrip(t *testing.T) {
	in := []byte(
		"clientTLS:\n" +
			"  certificateFile: /certs/srs-cert.pem\n" +
			"  privateKeyFile: /certs/srs-private-key.pem\n" +
			"  serverName: vcs.vngd.net\n")

	var s state.SettingsState
	if err := yaml.Unmarshal(in, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.ClientTLS.PrivateKeyFile != "/certs/srs-private-key.pem" {
		t.Fatalf("expected the key path, got %q", s.ClientTLS.PrivateKeyFile)
	}

	// A config with no clientTLS block must load cleanly as the zero value:
	// plaintext, which is what every existing deployment has today.
	var absent state.SettingsState
	if err := yaml.Unmarshal([]byte("general:\n  maxRadiosPerUser: 10\n"), &absent); err != nil {
		t.Fatalf("unmarshal without clientTLS: %v", err)
	}
	if absent.ClientTLS.CertificateFile != "" {
		t.Fatalf("expected empty, got %q", absent.ClientTLS.CertificateFile)
	}
}
