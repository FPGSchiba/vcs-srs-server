package utils

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"os"
	"time"
)

var (
	privateKey *ecdsa.PrivateKey
	publicKey  *ecdsa.PublicKey

	privateKeyFile = "ecdsa_key.pem"
	publicKeyFile  = "ecdsa_pubkey.pem"

	maxTokenExpiration = 24 * time.Hour // Maximum token expiration time
)

var (
	SrsServiceRoleMap = map[string]int32{
		"UpdateClientInfo":   0,
		"UpdateRadioInfo":    0,
		"SyncClient":         0,
		"Disconnect":         0,
		"GetServerSettings":  0,
		"SubscribeToUpdates": 0,
	}
)

type TokenClaims struct {
	ClientGuid string `json:"client_guid"`
	RoleId     int32  `json:"role_id"`
	jwt.RegisteredClaims
}

func getKeys() (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	if privateKey == nil {
		var err error
		privateKey, publicKey, err = generateKey()
		if err != nil {
			return nil, nil, err
		}
	}
	return privateKey, publicKey, nil
}

func generateKey() (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	loadedPublicKey, loadedPrivateKey, err := loadKeyFromFile()
	if err == nil {
		return loadedPublicKey, loadedPrivateKey, nil
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

func loadKeyFromFile() (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
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
	x509Encoded := block.Bytes
	privateKey, err := x509.ParseECPrivateKey(x509Encoded)
	if err != nil {
		return nil, nil, err
	}

	blockPub, _ := pem.Decode([]byte(pemEncodedPub))
	x509EncodedPub := blockPub.Bytes
	genericPublicKey, err := x509.ParsePKIXPublicKey(x509EncodedPub)
	if err != nil {
		return nil, nil, err
	}
	publicKey := genericPublicKey.(*ecdsa.PublicKey)

	return privateKey, publicKey, nil
}

func GenerateToken(clientGuid string, roleId int32) (string, error) {
	key, _, err := getKeys()
	if err != nil {
		return "", err
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256,
		TokenClaims{
			ClientGuid: clientGuid,
			RoleId:     roleId,
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(maxTokenExpiration)), // Token valid for 24 hours
				IssuedAt:  jwt.NewNumericDate(time.Now()),
				Issuer:    "vcs.vngd.net",
				Subject:   "ClientToken",
				ID:        clientGuid,
			},
		})
	tokenS, err := token.SignedString(key)
	if err != nil {
		return "", err
	}

	return tokenS, nil
}

func getJWTClaims(tokenString string) (*TokenClaims, error) {
	_, publicKey, err := getKeys()
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
	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		tokenClaims := &TokenClaims{
			ClientGuid: claims["client_guid"].(string),
			RoleId:     claims["role_id"].(int32),
		}
		return tokenClaims, nil
	}
	return nil, errors.New("invalid token")
}

func GetTokenClaims(tokenString string, minRole int32) (*TokenClaims, error) {
	claims, err := getJWTClaims(tokenString)
	if err != nil {
		return nil, err
	}
	if claims.RoleId < minRole {
		return nil, fmt.Errorf("insufficient role: %d, required: %d", claims.RoleId, minRole)
	}
	return claims, nil
}
