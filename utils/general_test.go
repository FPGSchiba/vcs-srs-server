package utils

import (
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// forgeCost rewrites the cost field of a "$2a$04$..." hash. bcrypt.Cost only
// parses the header, so the forged hash is accepted by it.
func forgeCost(t *testing.T, hash string, prefix string) string {
	t.Helper()
	if !strings.HasPrefix(hash, "$2a$04$") {
		t.Fatalf("unexpected hash prefix: %q", hash)
	}
	return prefix + strings.TrimPrefix(hash, "$2a$04$")
}

func minCostHash(t *testing.T, pw string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	return string(h)
}

func TestCheckHashCost(t *testing.T) {
	base := minCostHash(t, "pw")
	cases := []struct {
		name      string
		hash      string
		wantErr   string
		malformed bool
	}{
		{name: "cost4", hash: base},
		{name: "cost12", hash: forgeCost(t, base, "$2a$12$")},
		{name: "2b-cost10", hash: forgeCost(t, base, "$2b$10$")},
		{name: "2y-cost10", hash: forgeCost(t, base, "$2y$10$")},
		{name: "cost13", hash: forgeCost(t, base, "$2a$13$"), wantErr: "bcrypt cost 13 exceeds the server maximum of 12"},
		{name: "empty", hash: "", malformed: true},
		{name: "not-a-hash", hash: "not-a-hash", malformed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckHashCost(tc.hash)
			switch {
			case tc.malformed:
				if !errors.Is(err, ErrMalformedHash) {
					t.Fatalf("err = %v, want ErrMalformedHash", err)
				}
			case tc.wantErr != "":
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
			}
		})
	}
	if MaxBcryptCost != 12 {
		t.Fatalf("MaxBcryptCost = %d", MaxBcryptCost)
	}
}

func TestCheckPasswordHashRefusesOverCostQuickly(t *testing.T) {
	h := forgeCost(t, minCostHash(t, "pw"), "$2a$14$")
	start := time.Now()
	ok := CheckPasswordHash(h, "pw")
	if ok {
		t.Fatal("over-cost hash must not match")
	}
	if d := time.Since(start); d >= 50*time.Millisecond {
		t.Fatalf("took %v, want < 50ms", d)
	}
}

func TestCheckPasswordHashStillMatches(t *testing.T) {
	h := minCostHash(t, "pw")
	if !CheckPasswordHash(h, "pw") {
		t.Fatal("expected match")
	}
	if CheckPasswordHash(h, "other") {
		t.Fatal("expected mismatch")
	}
}
