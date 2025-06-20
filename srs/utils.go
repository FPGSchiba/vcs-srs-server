package srs

import (
	"regexp"
)

const (
	GuestRole  = 0
	MemberRole = 1
	AdminRole  = 2
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
