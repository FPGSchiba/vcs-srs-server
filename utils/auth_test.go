package utils

import (
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	testPrivKeyFile string
	testPubKeyFile  string
)

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "utils_test_*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)
	testPrivKeyFile = tmp + "/priv.pem"
	testPubKeyFile = tmp + "/pub.pem"
	// Initialize keys once for all tests
	if _, _, err := getKeys(testPrivKeyFile, testPubKeyFile); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestGetJWTClaimsMissingClientGuid(t *testing.T) {
	key, _, err := getKeys(testPrivKeyFile, testPubKeyFile)
	if err != nil {
		t.Fatalf("getKeys: %v", err)
	}
	claims := jwt.MapClaims{
		"role_id": float64(0),
		"exp":     float64(time.Now().Add(time.Hour).Unix()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tokenStr, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	result, err := getJWTClaims(tokenStr, testPrivKeyFile, testPubKeyFile)
	if err == nil {
		t.Fatal("expected error for missing client_guid, got nil")
	}
	if result != nil {
		t.Fatal("expected nil result for missing client_guid")
	}
}

func TestGetJWTClaimsWrongType(t *testing.T) {
	key, _, err := getKeys(testPrivKeyFile, testPubKeyFile)
	if err != nil {
		t.Fatalf("getKeys: %v", err)
	}
	claims := jwt.MapClaims{
		"client_guid": "some-guid",
		"role_id":     "not-a-number",
		"exp":         float64(time.Now().Add(time.Hour).Unix()),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tokenStr, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	result, err := getJWTClaims(tokenStr, testPrivKeyFile, testPubKeyFile)
	if err == nil {
		t.Fatal("expected error for wrong role_id type, got nil")
	}
	if result != nil {
		t.Fatal("expected nil result for wrong role_id type")
	}
}

func TestGetJWTClaimsValid(t *testing.T) {
	tokenStr, err := GenerateToken("test-guid", GuestRole, "issuer", "subject", time.Hour, testPrivKeyFile, testPubKeyFile)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	result, err := getJWTClaims(tokenStr, testPrivKeyFile, testPubKeyFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ClientGuid != "test-guid" {
		t.Fatalf("expected client_guid=test-guid, got %s", result.ClientGuid)
	}
}
