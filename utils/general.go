package utils

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// MaxBcryptCost is the highest bcrypt cost the server will evaluate for a
// client-supplied hash. The cost is attacker-controlled, so an uncapped value
// is a remote CPU-exhaustion vector. The C# peer hashes at exactly 12.
const MaxBcryptCost = 12

// ErrMalformedHash is returned by CheckHashCost when the hash cannot be parsed.
var ErrMalformedHash = errors.New("malformed")

// CheckHashCost returns nil when hash parses as bcrypt and its cost is within
// MaxBcryptCost. It only reads the hash header and never runs bcrypt.
func CheckHashCost(hash string) error {
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		return ErrMalformedHash
	}
	if cost > MaxBcryptCost {
		return fmt.Errorf("bcrypt cost %d exceeds the server maximum of %d", cost, MaxBcryptCost)
	}
	return nil
}

// CheckPasswordHash compares a bcrypt hash with a plaintext password.
// The first argument is the stored hash; the second is the plaintext to verify.
// Hashes that are malformed or above MaxBcryptCost never match.
func CheckPasswordHash(hash, plaintext string) bool {
	if CheckHashCost(hash) != nil {
		return false
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext))
	return err == nil
}
