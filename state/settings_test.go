package state_test

import (
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/state"
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
