package srs

import (
	"regexp"
	"strings"

	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"golang.org/x/mod/semver"
)

// MinClientVersion is the oldest client this server admits. Clients report
// their version in ClientCapabilities.version on InitAuth.
//
// Exported so the value is greppable from the client repo, which has to keep
// its own version at or above it.
const MinClientVersion = "v0.1.0"

// checkVersion reports whether a client's reported version is new enough.
//
// This used to be `version == "0.1.0"` -- an exact string equality, despite a
// comment claiming all versions were accepted. That made the two repositories
// silently co-dependent: bumping the client's version.Client constant by a
// single patch release would have made EVERY login fail with "Unsupported
// version", and nothing on either side said so. It was found by running the
// client's integration tests against a real server.
//
// A floor comparison is what the check was evidently meant to be: new clients
// keep working, genuinely ancient ones are still refused, and raising the bar
// becomes a deliberate edit of MinClientVersion rather than an accident of
// release numbering.
//
// The leading "v" that semver requires is supplied here rather than demanded
// of the client, since the wire format has always carried a bare "0.1.0".
func checkVersion(version string) bool {
	v := strings.TrimSpace(version)
	if v == "" {
		return false
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return false
	}
	return semver.Compare(v, MinClientVersion) >= 0
}

func checkUsername(username string) bool {
	// Check if the username is valid
	// For now, we assume all usernames are valid
	return len(username) > 0 && len(username) <= 32
}

var unitIDRegex = regexp.MustCompile(`^[A-Z0-9]{2,4}$`)

func checkUnitId(unitId string) bool {
	return unitIDRegex.MatchString(unitId)
}

func getSelectedUnit(authClient *AuthenticatingClient, unitId string) *pb.UnitSelection {
	var selectedUnit *pb.UnitSelection
	for _, unit := range authClient.AvailableUnits {
		if unit.UnitId == unitId {
			selectedUnit = unit
			break
		}
	}
	return selectedUnit
}

func isRoleAvailable(authClient *AuthenticatingClient, selectedRole uint8) bool {
	var roleAvailable bool
	for _, role := range authClient.AvailableRoles {
		if role == selectedRole {
			roleAvailable = true
			break
		}
	}
	return roleAvailable
}

func convertRadios(radio []state.Radio) []*pb.Radio {
	var pbRadios []*pb.Radio
	for _, r := range radio {
		pbRadios = append(pbRadios, convertSingleRadio(&r))
	}
	return pbRadios
}

func convertSingleRadio(r *state.Radio) *pb.Radio {
	return &pb.Radio{
		Id:         r.ID,
		Name:       r.Name,
		Frequency:  r.Frequency,
		Enabled:    r.Enabled,
		IsIntercom: r.IsIntercom,
	}
}

func ptrInt64(v int64) *int64 {
	return &v
}

func canSwapRoles(client *state.ClientState, roleId uint8) bool {
	// Guests cannot swap roles (Maybe this should be configurable in the future, so guests can swap roles)
	if roleId == utils.GuestRole {
		return false
	}

	// If the client has a higher role than the requested role, they can swap
	if client.Role > roleId {
		return true
	}

	// If the client has the requested role, they can swap
	if client.Role == roleId {
		return true
	}

	// If the client has a lower role than the requested role, they cannot swap
	return false
}

func convertRadioInfo(radio *pb.RadioInfo) *state.RadioState {
	var radios []state.Radio
	for _, r := range radio.Radios {
		radios = append(radios, convertSingleRadioState(r))
	}
	return &state.RadioState{
		Radios: radios,
		Muted:  radio.Muted,
	}
}

func convertSingleRadioState(r *pb.Radio) state.Radio {
	return state.Radio{
		ID:         r.Id,
		Name:       r.Name,
		Frequency:  r.Frequency,
		Enabled:    r.Enabled,
		IsIntercom: r.IsIntercom,
	}
}
