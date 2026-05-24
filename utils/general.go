package utils

import (
	"golang.org/x/crypto/bcrypt"
)

// CheckPasswordHash compares a bcrypt hash with a plaintext password.
// The first argument is the stored hash; the second is the plaintext to verify.
func CheckPasswordHash(hash, plaintext string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext))
	return err == nil
}
