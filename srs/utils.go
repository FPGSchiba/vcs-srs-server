package srs

import (
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
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
