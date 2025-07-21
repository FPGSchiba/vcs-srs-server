package srs

import (
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"regexp"
)

func checkVersion(version string) bool {
	// Check if the version is supported
	// For now, we assume all versions are supported
	return version == "0.1.0"
}

func checkUsername(username string) bool {
	// Check if the username is valid
	// For now, we assume all usernames are valid
	return len(username) > 0 && len(username) <= 32
}

func checkUnitId(unitId string) bool {
	re := `^[A-Z0-9]{2,4}$`
	matched, _ := regexp.MatchString(re, unitId)
	return matched
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
		Id:        r.ID,
		Name:      r.Name,
		Frequency: r.Frequency,
		Enabled:   r.Enabled,
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
		ID:        r.Id,
		Name:      r.Name,
		Frequency: r.Frequency,
		Enabled:   r.Enabled,
	}
}
