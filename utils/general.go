package utils

import (
	"golang.org/x/crypto/bcrypt"
)

// HashPassword TODO: Test this function and make sure the client can do this as well
// HashPassword hashes the given password using bcrypt and returns the hashed password as a string.
func HashPassword(password string) string {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		// In production, handle error properly. For now, return empty string on error.
		return ""
	}
	return string(hash)
}
