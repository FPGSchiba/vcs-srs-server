package utils

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
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

// A token key path whose directory does not exist -- the shipped
// "/path/to/ecdsa_key.pem" placeholder, on Windows D:\path\to\ -- must fail
// with an error that names the path and the OS cause, not a bare
// "could not be written".
func TestGenerateKeyUnwritablePathNamesTheCause(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	priv := filepath.Join(dir, "ecdsa_key.pem")
	pub := filepath.Join(dir, "ecdsa_pubkey.pem")

	_, _, err := generateKey(priv, pub)
	if err == nil {
		t.Fatal("expected an error writing into a missing directory")
	}
	if !strings.Contains(err.Error(), priv) {
		t.Fatalf("error %q does not name the private key path %q", err, priv)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error %q does not wrap the underlying os.ErrNotExist", err)
	}
}

func TestGetTokenClaimsInsufficientRoleIsSentinel(t *testing.T) {
	tok, err := GenerateToken(uuid.NewString(), GuestRole, "i", "s", time.Hour, testPrivKeyFile, testPubKeyFile)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	_, err = GetTokenClaims(tok, AdminRole, testPrivKeyFile, testPubKeyFile)
	if !errors.Is(err, ErrInsufficientRole) {
		t.Fatalf("expected errors.Is(err, ErrInsufficientRole), got %v", err)
	}
}

func TestGetTokenClaimsExpiredIsNotRoleError(t *testing.T) {
	tok, err := GenerateToken(uuid.NewString(), GuestRole, "i", "s", -time.Hour, testPrivKeyFile, testPubKeyFile)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	_, err = GetTokenClaims(tok, GuestRole, testPrivKeyFile, testPubKeyFile)
	if err == nil {
		t.Fatal("expected an error for an expired token")
	}
	if errors.Is(err, ErrInsufficientRole) {
		t.Fatalf("expired token must not be reported as insufficient role: %v", err)
	}
	if errors.Is(err, ErrTokenKeys) {
		t.Fatalf("expired token must not be reported as a key failure: %v", err)
	}
}
