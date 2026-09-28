package control

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/state"
)

func TestClientTransportCredentials(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()

	t.Run("unconfigured means plaintext", func(t *testing.T) {
		creds, err := clientTransportCredentials(state.ClientTLSSettings{}, logger)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if creds != nil {
			t.Fatal("expected nil credentials so the listener stays plaintext")
		}
	})

	// Review Focus 3: a half-configured block must be a loud failure. Falling
	// through to plaintext here would give an operator who thought they
	// enabled TLS a server that quietly did not.
	t.Run("certificate without key is an error", func(t *testing.T) {
		_, err := clientTransportCredentials(state.ClientTLSSettings{
			CertificateFile: filepath.Join(dir, "cert.pem"),
		}, logger)
		if err == nil {
			t.Fatal("expected half-configured TLS to fail, not fall back to plaintext")
		}
		if !strings.Contains(err.Error(), "privateKeyFile is empty") {
			t.Fatalf("error should name the missing field, got: %v", err)
		}
	})

	t.Run("key without certificate is an error", func(t *testing.T) {
		_, err := clientTransportCredentials(state.ClientTLSSettings{
			PrivateKeyFile: filepath.Join(dir, "key.pem"),
		}, logger)
		if err == nil {
			t.Fatal("expected half-configured TLS to fail, not fall back to plaintext")
		}
		if !strings.Contains(err.Error(), "certificateFile is empty") {
			t.Fatalf("error should name the missing field, got: %v", err)
		}
	})

	t.Run("fully configured generates and returns credentials", func(t *testing.T) {
		certPath := filepath.Join(dir, "gen-cert.pem")
		keyPath := filepath.Join(dir, "gen-key.pem")

		creds, err := clientTransportCredentials(state.ClientTLSSettings{
			CertificateFile: certPath,
			PrivateKeyFile:  keyPath,
			ServerName:      "vcs.test",
		}, logger)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if creds == nil {
			t.Fatal("expected credentials")
		}
		if creds.Info().SecurityProtocol != "tls" {
			t.Fatalf("expected tls, got %q", creds.Info().SecurityProtocol)
		}
		// The pair is generated on first use, matching the VoiceControl
		// channel's behaviour, so a self-hoster gets something to copy.
		if _, err := os.Stat(certPath); err != nil {
			t.Fatalf("expected the certificate to be generated at %s: %v", certPath, err)
		}
		if _, err := os.Stat(keyPath); err != nil {
			t.Fatalf("expected the key to be generated at %s: %v", keyPath, err)
		}
		// LoadOrGenerateKeyPair takes (privateKeyPath, certPath, ...); a
		// swapped call would still create both files and still yield working
		// in-process credentials (the same swapped paths get read back), so
		// os.Stat alone would not catch it. Check the PEM content lands at
		// the path it claims to.
		certPEM, err := os.ReadFile(certPath)
		if err != nil {
			t.Fatalf("read cert: %v", err)
		}
		if !strings.Contains(string(certPEM), "-----BEGIN CERTIFICATE-----") {
			t.Fatalf("expected a certificate PEM at %s, got: %s", certPath, certPEM)
		}
	})

	t.Run("a second call reuses the generated pair", func(t *testing.T) {
		certPath := filepath.Join(dir, "reuse-cert.pem")
		keyPath := filepath.Join(dir, "reuse-key.pem")
		cfg := state.ClientTLSSettings{CertificateFile: certPath, PrivateKeyFile: keyPath, ServerName: "vcs.test"}

		if _, err := clientTransportCredentials(cfg, logger); err != nil {
			t.Fatalf("first call: %v", err)
		}
		first, err := os.ReadFile(certPath)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if _, err := clientTransportCredentials(cfg, logger); err != nil {
			t.Fatalf("second call: %v", err)
		}
		second, err := os.ReadFile(certPath)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(first) != string(second) {
			t.Fatal("a restart must not mint a new certificate -- clients pinning the old one would break")
		}
	})

	// That WARN is the only signal an operator gets that client credentials
	// are crossing the network in the clear -- pin its presence so a wording
	// edit that drops the meaning is caught.
	t.Run("unconfigured logs a plaintext warning", func(t *testing.T) {
		var buf bytes.Buffer
		capturing := slog.New(slog.NewTextHandler(&buf, nil))

		if _, err := clientTransportCredentials(state.ClientTLSSettings{}, capturing); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(buf.String(), "PLAINTEXT") {
			t.Fatalf("expected a PLAINTEXT warning, got log output: %s", buf.String())
		}
	})
}
