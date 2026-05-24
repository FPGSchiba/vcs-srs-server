package srs

import (
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/events"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
)

func TestConvertSingleRadio_PreservesIsIntercom(t *testing.T) {
	r := &state.Radio{
		ID:         1,
		Name:       "Radio 1",
		Frequency:  121.5,
		Enabled:    true,
		IsIntercom: true,
	}
	got := convertSingleRadio(r)
	if !got.IsIntercom {
		t.Fatal("expected IsIntercom=true, got false")
	}
}

func TestConvertSingleRadioState_PreservesIsIntercom(t *testing.T) {
	r := &pb.Radio{
		Id:         1,
		Name:       "Radio 1",
		Frequency:  121.5,
		Enabled:    true,
		IsIntercom: true,
	}
	got := convertSingleRadioState(r)
	if !got.IsIntercom {
		t.Fatal("expected IsIntercom=true, got false")
	}
}

func TestBuildServerUpdate_TypesCompileCheck(t *testing.T) {
	// Ensure events package types are usable in srs package
	_ = events.ClientChangeEvent{Type: events.ClientJoined}
	_ = events.ClientChangeEvent{Type: events.ClientLeft}
	_ = events.ClientChangeEvent{Type: events.ClientInfoUpdated}
	_ = events.RadioChangeEvent{Type: events.RadioUpdated}
}
