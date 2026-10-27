package control

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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

		// Review Focus 5: cfg.ServerName is passed to LoadOrGenerateKeyPair as
		// extraHosts (server.go:200). That is the argument that makes remote
		// pinning work at all -- prove it actually lands in the generated
		// certificate's SANs, not merely that a certificate was produced.
		block, _ := pem.Decode(certPEM)
		if block == nil {
			t.Fatalf("expected a decodable PEM block at %s", certPath)
		}
		parsed, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("parse generated certificate: %v", err)
		}
		const wantServerName = "vcs.test"
		found := false
		for _, name := range parsed.DNSNames {
			if name == wantServerName {
				found = true
				break
			}
		}
		if !found {
			if ip := net.ParseIP(wantServerName); ip != nil {
				for _, addr := range parsed.IPAddresses {
					if addr.Equal(ip) {
						found = true
						break
					}
				}
			}
		}
		if !found {
			t.Fatalf("expected %q in DNSNames %v or IPAddresses %v", wantServerName, parsed.DNSNames, parsed.IPAddresses)
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

// grpc-go reports a failed security handshake only through grpclog, which is
// silent by default, so a plaintext client hitting a TLS-only port left no
// trace in the server log at all. The client-port credentials must log it.
func TestClientTransportCredentialsLogsFailedHandshake(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	creds, err := clientTransportCredentials(state.ClientTLSSettings{
		CertificateFile: filepath.Join(dir, "cert.pem"),
		PrivateKeyFile:  filepath.Join(dir, "key.pem"),
		ServerName:      "localhost",
	}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	serverSide, clientSide := net.Pipe()
	defer clientSide.Close()
	go func() {
		// What a plaintext gRPC client sends first: the HTTP/2 preface.
		_, _ = clientSide.Write([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"))
	}()
	if _, _, err := creds.ServerHandshake(serverSide); err == nil {
		t.Fatal("expected the handshake to fail on a plaintext preface")
	}
	out := buf.String()
	if !strings.Contains(out, "handshake failed") || !strings.Contains(out, "plaintext") {
		t.Fatalf("expected a handshake-failure warning naming plaintext, got log output: %s", out)
	}
}

// newInterceptorTestServer builds a Server whose token key files live in a
// temp dir. utils caches the keys process-wide on first use, so every test in
// this package signs and verifies with the same pair.
func newInterceptorTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	settings := &state.SettingsState{}
	settings.Security.Token = state.TokenSettings{
		PrivateKeyFile: filepath.Join(dir, "priv.pem"),
		PublicKeyFile:  filepath.Join(dir, "pub.pem"),
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(&state.ServerState{}, settings, logger, &state.DistributionState{}, nil, nil)
}

// mintToken returns a signed token for a fresh GUID, and that GUID.
func mintToken(t *testing.T, s *Server, role uint8, ttl time.Duration) (string, string) {
	t.Helper()
	guid := uuid.NewString()
	tok, err := utils.GenerateToken(guid, role, "i", "s", ttl,
		s.settingsState.Security.Token.PrivateKeyFile, s.settingsState.Security.Token.PublicKeyFile)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	return tok, guid
}

type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }

// runAuth drives either interceptor with ctx and reports whether the handler
// ran, the client GUID it saw, and the interceptor's error.
func runAuth(s *Server, stream bool, method string, ctx context.Context) (called bool, gotID interface{}, err error) {
	if stream {
		err = s.authStreamInterceptor(nil, &fakeServerStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: method},
			func(srv interface{}, ss grpc.ServerStream) error {
				called = true
				gotID = ss.Context().Value(utils.ClientIDKey)
				return nil
			})
		return
	}
	_, err = s.authInterceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			called = true
			gotID = ctx.Value(utils.ClientIDKey)
			return "ok", nil
		})
	return
}

func TestAuthInterceptorCodes(t *testing.T) {
	s := newInterceptorTestServer(t)
	expired, _ := mintToken(t, s, utils.GuestRole, -time.Hour)
	valid, validGUID := mintToken(t, s, utils.GuestRole, time.Hour)
	s.serverState.AddClient(uuid.MustParse(validGUID), &state.ClientState{Name: "t"})
	unknown, _ := mintToken(t, s, utils.GuestRole, time.Hour) // GUID never added
	malformed, err := utils.GenerateToken("not-a-uuid", utils.GuestRole, "i", "s", time.Hour,
		s.settingsState.Security.Token.PrivateKeyFile, s.settingsState.Security.Token.PublicKeyFile)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	withAuth := func(v string) context.Context {
		return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", v))
	}

	for _, kind := range []struct {
		name   string
		stream bool
		method string
	}{
		{"unary", false, "/srs.SRSService/SyncClient"},
		{"stream", true, "/srs.SRSService/SubscribeToUpdates"},
	} {
		t.Run(kind.name, func(t *testing.T) {
			rejects := []struct {
				name string
				ctx  context.Context
				msg  string // substring the status message must contain, if set
			}{
				{"no metadata", context.Background(), ""},
				{"no token", metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-other", "1")), ""},
				{"garbage token", withAuth("Bearer x.y.z"), ""},
				{"expired token", withAuth("Bearer " + expired), ""},
				// SRV-06: a valid token whose session is gone.
				{"unknown GUID", withAuth("Bearer " + unknown), "session not found for " + kind.method},
				{"malformed GUID claim", withAuth("Bearer " + malformed), "session not found for " + kind.method},
			}
			for _, tc := range rejects {
				t.Run(tc.name, func(t *testing.T) {
					called, _, err := runAuth(s, kind.stream, kind.method, tc.ctx)
					if called {
						t.Fatal("handler must not run")
					}
					if got := status.Code(err); got != codes.Unauthenticated {
						t.Fatalf("code = %v, want Unauthenticated (err %v)", got, err)
					}
					if tc.msg != "" && !strings.Contains(status.Convert(err).Message(), tc.msg) {
						t.Fatalf("message = %q, want it to contain %q", status.Convert(err).Message(), tc.msg)
					}
				})
			}

			t.Run("valid token", func(t *testing.T) {
				called, gotID, err := runAuth(s, kind.stream, kind.method, withAuth("Bearer "+valid))
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !called {
					t.Fatal("handler not called")
				}
				if gotID != validGUID {
					t.Fatalf("ClientIDKey = %v, want %s", gotID, validGUID)
				}
			})

			t.Run("AuthService is exempt", func(t *testing.T) {
				called, _, err := runAuth(s, kind.stream, "/srs.AuthService/InitAuth", context.Background())
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !called {
					t.Fatal("handler not called for an AuthService method")
				}
			})
		})
	}
}

func TestAuthErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code codes.Code
		msg  string
	}{
		{"insufficient role", fmt.Errorf("%w: 0, required: 3", utils.ErrInsufficientRole), codes.PermissionDenied, ""},
		{"key load failure", fmt.Errorf("%w: boom", utils.ErrTokenKeys), codes.Internal, "token verification unavailable"},
		{"expired", errors.New("token is expired"), codes.Unauthenticated, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := authError("/m", tc.err)
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("not a status error: %v", err)
			}
			if st.Code() != tc.code {
				t.Fatalf("code = %v, want %v", st.Code(), tc.code)
			}
			if tc.msg != "" && st.Message() != tc.msg {
				t.Fatalf("message = %q, want %q", st.Message(), tc.msg)
			}
		})
	}
}

// --- SRV-05: Start fails cleanly ---

// newStartTestServer builds a Server ready for Start: token keys in a temp
// dir, the VoiceControl listener on 127.0.0.1:vcPort with its keypair under
// keyDir, and a real event bus.
func newStartTestServer(t *testing.T, mode uint8, vcPort int, keyDir string) *Server {
	t.Helper()
	tokenDir := t.TempDir()
	settings := &state.SettingsState{}
	settings.Security.Token = state.TokenSettings{
		Expiration:     3600,
		PrivateKeyFile: filepath.Join(tokenDir, "priv.pem"),
		PublicKeyFile:  filepath.Join(tokenDir, "pub.pem"),
	}
	settings.VoiceControl = state.VoiceControlSettings{
		ListenHost:      "127.0.0.1",
		Port:            vcPort,
		PrivateKeyFile:  filepath.Join(keyDir, "vc-key.pem"),
		CertificateFile: filepath.Join(keyDir, "vc-cert.pem"),
		ServerName:      "localhost",
	}
	bus := events.NewEventBus()
	t.Cleanup(bus.Stop)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(&state.ServerState{}, settings, logger, &state.DistributionState{DistributionMode: mode}, bus, nil)
}

// occupy holds a TCP listener on a free loopback port until the test ends.
func occupy(t *testing.T) (net.Listener, string, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, l.Addr().String(), l.Addr().(*net.TCPAddr).Port
}

// freeAddr returns a loopback address that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// isRunningWithin fails the test if IsRunning blocks (the SRV-05 deadlock:
// Start returned with s.mu still locked).
func isRunningWithin(t *testing.T, s *Server, d time.Duration) bool {
	t.Helper()
	got := make(chan bool, 1)
	go func() { got <- s.IsRunning() }()
	select {
	case v := <-got:
		return v
	case <-time.After(d):
		t.Fatal("IsRunning blocked: Start left s.mu locked")
		return false
	}
}

func assertRebindable(t *testing.T, addr string) {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("address %s still held after Start/Stop: %v", addr, err)
	}
	_ = l.Close()
}

func TestStartClientPortOccupied(t *testing.T) {
	_, addr, _ := occupy(t)
	s := newStartTestServer(t, state.DistributionModeStandalone, 0, t.TempDir())
	if err := s.Start(addr, make(chan struct{})); err == nil {
		t.Fatal("Start on an occupied port succeeded")
	}
	if isRunningWithin(t, s, time.Second) {
		t.Fatal("IsRunning true after a failed Start")
	}
}

func TestStartControlPortOccupiedReleasesClientPort(t *testing.T) {
	_, _, vcPort := occupy(t)
	clientAddr := freeAddr(t)
	s := newStartTestServer(t, state.DistributionModeControl, vcPort, t.TempDir())
	if err := s.Start(clientAddr, make(chan struct{})); err == nil {
		t.Fatal("Start with the VoiceControl port occupied succeeded")
	}
	if isRunningWithin(t, s, time.Second) {
		t.Fatal("IsRunning true after a failed Start")
	}
	assertRebindable(t, clientAddr)
}

func TestStartControlKeypairFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyDir := filepath.Join(file, "sub") // a directory under a regular file cannot exist
	vcAddr := freeAddr(t)
	vcPort := 0
	if _, p, err := net.SplitHostPort(vcAddr); err == nil {
		_, _ = fmt.Sscanf(p, "%d", &vcPort)
	}
	clientAddr := freeAddr(t)
	s := newStartTestServer(t, state.DistributionModeControl, vcPort, keyDir)

	err := s.Start(clientAddr, make(chan struct{}))
	if err == nil {
		t.Fatal("Start with an unloadable VoiceControl keypair succeeded")
	}
	if !strings.Contains(err.Error(), "control server TLS") {
		t.Fatalf("error should name the control server TLS step, got: %v", err)
	}
	if isRunningWithin(t, s, time.Second) {
		t.Fatal("IsRunning true after a failed Start")
	}
	assertRebindable(t, clientAddr)
	assertRebindable(t, vcAddr)
}

// Review focus: the admin UI retries Start after a port clash; the retry must
// neither hang nor fail to bind.
func TestStartRetryAfterFailure(t *testing.T) {
	occupier, addr, _ := occupy(t)
	s := newStartTestServer(t, state.DistributionModeStandalone, 0, t.TempDir())
	if err := s.Start(addr, make(chan struct{})); err == nil {
		t.Fatal("Start on an occupied port succeeded")
	}
	_ = occupier.Close()

	if err := s.Start(addr, make(chan struct{})); err != nil {
		t.Fatalf("retry Start: %v", err)
	}
	if !isRunningWithin(t, s, time.Second) {
		t.Fatal("IsRunning false after a successful retry")
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	assertRebindable(t, addr)
}

func TestStartStopReleasesPort(t *testing.T) {
	addr := freeAddr(t)
	s := newStartTestServer(t, state.DistributionModeStandalone, 0, t.TempDir())
	if err := s.Start(addr, make(chan struct{})); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !isRunningWithin(t, s, time.Second) {
		t.Fatal("IsRunning false after Start")
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	assertRebindable(t, addr)
}

// Review follow-up: when Serve fails with a non-ErrServerStopped error, the
// serve goroutine clears isRunning. A later Stop must still tear everything
// down (gRPC servers, listeners, the SRS server and the AuthServer's pending-
// login sweeper) instead of returning early and using up stopOnce.
func TestStopAfterServeFailureStillTearsDown(t *testing.T) {
	addr := freeAddr(t)
	s := newStartTestServer(t, state.DistributionModeStandalone, 0, t.TempDir())
	if err := s.Start(addr, make(chan struct{})); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Simulate the serve goroutine's error path.
	s.mu.Lock()
	s.isRunning = false
	s.mu.Unlock()

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	assertRebindable(t, addr)

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.clientGrpcServer != nil || s.clientListener != nil {
		t.Fatal("Stop left the client gRPC server or listener in place")
	}
}
