package utils

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// keysOnce ensures keys are loaded from disk exactly once per process lifetime.
// Changing the key file paths in settings requires a server restart to take effect.
// A future improvement would replace this package-level singleton with a per-instance
// key cache to allow runtime key rotation without a restart.
var (
	keysOnce   sync.Once
	cachedPriv *ecdsa.PrivateKey
	cachedPub  *ecdsa.PublicKey
	keysErr    error
)

const (
	GuestRole uint8 = iota
	MemberRole
	OfficerRole
	AdminRole
)

var (
	srsServiceMinimumRoleMap = map[string]uint8{
		"UpdateClientInfo":   GuestRole,
		"UpdateRadioInfo":    GuestRole,
		"SyncClient":         GuestRole,
		"Disconnect":         GuestRole,
		"GetServerSettings":  GuestRole,
		"SubscribeToUpdates": GuestRole,
	}
	SrsRoleNameMap = map[uint8]string{
		GuestRole:   "Guest",
		MemberRole:  "Member",
		OfficerRole: "Officer",
		AdminRole:   "Admin",
	}
)

// GetMinimumRoleForMethod returns the minimum required role for the given gRPC
// method name, and whether the method is in the role map at all.
func GetMinimumRoleForMethod(methodName string) (uint8, bool) {
	role, ok := srsServiceMinimumRoleMap[methodName]
	return role, ok
}

type TokenClaims struct {
	ClientGuid string `json:"client_guid"`
	RoleId     uint8  `json:"role_id"`
	jwt.RegisteredClaims
}

func getKeys(privateKeyFile, publicKeyFile string) (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	keysOnce.Do(func() {
		cachedPriv, cachedPub, keysErr = generateKey(privateKeyFile, publicKeyFile)
	})
	return cachedPriv, cachedPub, keysErr
}

func generateKey(privateKeyFile, publicKeyFile string) (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	loadedPrivateKey, loadedPublicKey, err := loadKeyFromFile(privateKeyFile, publicKeyFile)
	if err == nil {
		return loadedPrivateKey, loadedPublicKey, nil
	}

	// If the privateKey files do not exist, generate a new privateKey
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	publicKey := privateKey.Public().(*ecdsa.PublicKey)
	if publicKey == nil {
		return nil, nil, errors.New("public key is nil")
	}
	// Encode the privateKey and publicKey to PEM format
	pemEncoded, pemEncodedPub, err := encode(privateKey, publicKey)
	if err != nil {
		return nil, nil, err
	}
	privErr := os.WriteFile(privateKeyFile, []byte(pemEncoded), 0600)
	pubErr := os.WriteFile(publicKeyFile, []byte(pemEncodedPub), 0644)
	if privErr != nil || pubErr != nil {
		return nil, nil, errors.New("private key or public key could not be written to file")
	}
	return privateKey, publicKey, nil
}

func loadKeyFromFile(privateKeyFile, publicKeyFile string) (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	if _, err := os.Stat(privateKeyFile); !errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(publicKeyFile); !errors.Is(err, os.ErrNotExist) {
			pemEncoded, err := os.ReadFile(privateKeyFile)
			if err != nil {
				return nil, nil, err
			}
			pemEncodedPub, err := os.ReadFile(publicKeyFile)
			if err != nil {
				return nil, nil, err
			}

			privateKey, publicKey, err := decode(string(pemEncoded), string(pemEncodedPub))
			if err != nil {
				return nil, nil, err
			}
			return privateKey, publicKey, nil
		}
	}
	return nil, nil, errors.New("private key or public key file does not exist")
}

func encode(privateKey *ecdsa.PrivateKey, publicKey *ecdsa.PublicKey) (string, string, error) {
	x509Encoded, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return "", "", err
	}
	pemEncoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509Encoded})

	x509EncodedPub, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", "", err
	}
	pemEncodedPub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: x509EncodedPub})

	return string(pemEncoded), string(pemEncodedPub), nil
}

func decode(pemEncoded string, pemEncodedPub string) (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemEncoded))
	if block == nil {
		return nil, nil, fmt.Errorf("failed to decode private key PEM")
	}
	x509Encoded := block.Bytes
	privateKey, err := x509.ParseECPrivateKey(x509Encoded)
	if err != nil {
		return nil, nil, err
	}

	blockPub, _ := pem.Decode([]byte(pemEncodedPub))
	if blockPub == nil {
		return nil, nil, fmt.Errorf("failed to decode public key PEM")
	}
	x509EncodedPub := blockPub.Bytes
	genericPublicKey, err := x509.ParsePKIXPublicKey(x509EncodedPub)
	if err != nil {
		return nil, nil, err
	}
	publicKey := genericPublicKey.(*ecdsa.PublicKey)

	return privateKey, publicKey, nil
}

func GenerateToken(clientGuid string, roleId uint8, issuer, subject string, expiration time.Duration, privateKeyFile, publicKeyFile string) (string, error) {
	key, _, err := getKeys(privateKeyFile, publicKeyFile)
	if err != nil {
		return "", err
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256,
		TokenClaims{
			ClientGuid: clientGuid,
			RoleId:     roleId,
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiration)), // Token valid for 24 hours
				IssuedAt:  jwt.NewNumericDate(time.Now()),
				Issuer:    issuer,
				Subject:   subject,
				ID:        clientGuid,
			},
		})
	tokenS, err := token.SignedString(key)
	if err != nil {
		return "", err
	}

	return tokenS, nil
}

func getJWTClaims(tokenString, privateKeyFile, publicKeyFile string) (*TokenClaims, error) {
	_, publicKey, err := getKeys(privateKeyFile, publicKeyFile)
	if err != nil {
		return nil, err
	}
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodECDSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return publicKey, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}

	guidRaw, ok := claims["client_guid"].(string)
	if !ok || guidRaw == "" {
		return nil, errors.New("missing or invalid client_guid claim")
	}
	roleRaw, ok := claims["role_id"].(float64)
	if !ok {
		return nil, errors.New("missing or invalid role_id claim")
	}
	return &TokenClaims{
		ClientGuid: guidRaw,
		RoleId:     uint8(roleRaw),
	}, nil
}

func GetTokenClaims(tokenString string, minRole uint8, privateKeyFile, publicKeyFile string) (*TokenClaims, error) {
	claims, err := getJWTClaims(tokenString, privateKeyFile, publicKeyFile)
	if err != nil {
		return nil, err
	}
	if claims.RoleId < minRole {
		return nil, fmt.Errorf("insufficient role: %d, required: %d", claims.RoleId, minRole)
	}
	return claims, nil
}
