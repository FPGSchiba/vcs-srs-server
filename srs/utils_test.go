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

// TestCheckVersionAcceptsNewerClients pins the fix for a cross-repo landmine:
// checkVersion used to be `version == "0.1.0"`, an exact string equality, so
// bumping the CLIENT's version.Client constant by a single patch release
// would have made every login fail with "Unsupported version" and nothing in
// either repository would have hinted why.
//
// Expected values are written out literally rather than derived from
// MinClientVersion, so that raising the floor is a deliberate, visible edit
// to this table instead of something the test silently absorbs.
func TestCheckVersionAcceptsNewerClients(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
		why     string
	}{
		{"0.1.0", true, "the current client, and the floor itself"},
		{"v0.1.0", true, "a leading v is tolerated, not required"},
		{"0.1.1", true, "patch bump -- the exact case the old check broke"},
		{"0.2.0", true, "minor bump"},
		{"1.0.0", true, "major bump"},
		{"10.0.0", true, "numeric, not lexicographic: 10 > 1"},
		{"0.0.9", false, "genuinely older than the floor"},
		{"", false, "empty"},
		{"   ", false, "whitespace only"},
		{"not-a-version", false, "unparseable"},
		{"0.1", true, "x/mod/semver reads a two-component version as 0.1.0, so this equals the floor -- deliberate leniency, verified against the library rather than assumed"},
	} {
		if got := checkVersion(tc.version); got != tc.want {
			t.Errorf("checkVersion(%q) = %v, want %v -- %s", tc.version, got, tc.want, tc.why)
		}
	}
}
