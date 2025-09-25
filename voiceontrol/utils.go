package voiceontrol

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

const (
	certFileName       = "voicecontrol-cert.pem"
	privateKeyFileName = "voicecontrol-private-key.pem"
)

// LoadOrGenerateKeyPair ensures both private key and certificate exist, generating them if needed, and returns both.
func LoadOrGenerateKeyPair() (*tls.Certificate, *rsa.PrivateKey, error) {
	if _, err := os.Stat(privateKeyFileName); os.IsNotExist(err) {
		cert, privateKey, err := generateSelfSignedCert()
		if err != nil {
			return nil, nil, err
		}
		encodedKey, encodedCert, err := encode(cert, privateKey)
		if err != nil {
			return nil, nil, err
		}
		if err := os.WriteFile(certFileName, encodedCert, 0600); err != nil {
			return nil, nil, err
		}
		if err := os.WriteFile(privateKeyFileName, encodedKey, 0600); err != nil {
			return nil, nil, err
		}
		return cert, privateKey, nil
	}

	cert, err := tls.LoadX509KeyPair(certFileName, privateKeyFileName)
	if err != nil {
		return nil, nil, err
	}
	// Load private key from file
	keyData, err := os.ReadFile(privateKeyFileName)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(keyData)
	if block == nil {
		return nil, nil, fmt.Errorf("failed to decode private key PEM")
	}
	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return &cert, privateKey, nil
}

// LoadCertificateOnly loads and returns the certificate without the private key.
func LoadCertificateOnly() (*x509.Certificate, error) {
	certData, err := os.ReadFile(certFileName)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(certData)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("failed to decode certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	return cert, nil
}

func CreateClientTLSConfig(cert *x509.Certificate) (*tls.Config, error) {
	certPool := x509.NewCertPool()

	// Add the self-signed certificate to the cert pool
	certPool.AddCert(cert)

	// Create TLS configuration
	tlsConfig := &tls.Config{
		RootCAs:            certPool,
		InsecureSkipVerify: false, // Ensure we're still verifying
	}

	return tlsConfig, nil
}

func generateSelfSignedCert() (*tls.Certificate, *rsa.PrivateKey, error) {
	cert := &tls.Certificate{}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	// Create certificate template
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Vanguard Communication System"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}

	cert.PrivateKey = privateKey
	cert.Certificate = make([][]byte, 1)
	cert.Certificate[0], err = x509.CreateCertificate(
		rand.Reader,
		template,
		template, // Self-signed, so template is both subject and issuer
		&privateKey.PublicKey,
		privateKey,
	)
	if err != nil {
		return nil, nil, err
	}

	return cert, privateKey, nil
}

func encode(cert *tls.Certificate, privateKey *rsa.PrivateKey) ([]byte, []byte, error) {
	x509Encoded := x509.MarshalPKCS1PrivateKey(privateKey)
	pemEncoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509Encoded})

	pemEncodedCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})

	return pemEncoded, pemEncodedCert, nil
}

func decode(pemEncoded string, pemEncodedPub string) (*rsa.PrivateKey, *rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemEncoded))
	x509Encoded := block.Bytes
	privateKey, err := x509.ParsePKCS1PrivateKey(x509Encoded)
	if err != nil {
		return nil, nil, err
	}

	blockPub, _ := pem.Decode([]byte(pemEncodedPub))
	x509EncodedPub := blockPub.Bytes
	genericPublicKey, err := x509.ParsePKIXPublicKey(x509EncodedPub)
	if err != nil {
		return nil, nil, err
	}
	publicKey := genericPublicKey.(*rsa.PublicKey)

	return privateKey, publicKey, nil
}
