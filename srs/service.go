package srs

import (
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"sync"
)

type SimpleRadioServer struct {
	pb.UnimplementedSRSServiceServer

	mu            sync.Mutex // protects routeNotes
	clientState   *state.ClientState
	settingsState *state.SettingsState
}
