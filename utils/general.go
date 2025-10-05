package utils

import (
	"golang.org/x/crypto/bcrypt"
)

// CheckPasswordHash compares a plaintext password with a hashed password and returns true if they match.
func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}
