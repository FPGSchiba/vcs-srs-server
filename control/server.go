package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/srs"
	"github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/FPGSchiba/vcs-srs-server/voiceontrol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

var (
	sleep = time.Second * 5
)

const (
	healthServiceSystem  = ""        // empty string represents the health of the healthServiceSystem
	healthServiceSRS     = "srs"     // service name for SRS
	healthServiceControl = "control" // service name for Control Server
	healthServiceAuth    = "auth"    // service name for Auth Server
)

type Server struct {
	mu                 sync.RWMutex
	clientGrpcServer   *grpc.Server
	controlGrpcServer  *grpc.Server
	clientListener     net.Listener
	controlListener    net.Listener
	logger             *slog.Logger
	serverState        *state.ServerState
	settingsState      *state.SettingsState
	distributionState  *state.DistributionState
	eventBus           *events.EventBus
	srsServer          *srs.SimpleRadioServer
	authServer         *srs.AuthServer
	voiceControlServer *voiceontrol.VoiceControlServer
	isRunning          bool
	stopOnce           sync.Once
}

func NewServer(serverState *state.ServerState, settingsState *state.SettingsState, logger *slog.Logger, distributionState *state.DistributionState, eventBus *events.EventBus) *Server {
	return &Server{
		serverState:       serverState,
		settingsState:     settingsState,
		eventBus:          eventBus,
		logger:            logger,
		distributionState: distributionState,
	}
}

func (s *Server) isControlServer() bool {
	s.distributionState.RLock()
	defer s.distributionState.RUnlock()
	return s.distributionState.DistributionMode == state.DistributionModeControl
}

func (s *Server) Start(address string, stopChan chan struct{}) error {
	if s.IsRunning() {
		return fmt.Errorf("server is already running")
	}

	var err error

	s.mu.Lock()
	// fail undoes everything Start has done so far and releases the lock.
	// The caller (StartControlServer) is not fatal on an error: it logs,
	// records ControlStatus.Error, and lets the process stay up, so the admin
	// UI can retry. A half-started server must therefore not keep s.mu (every
	// later IsRunning would block forever) or the port(s) it already bound (a
	// corrected retry would hit "address already in use"). Every fallible step
	// runs before anything that starts a goroutine, so there is nothing else
	// to stop here.
	fail := func(err error) error {
		if s.clientListener != nil {
			_ = s.clientListener.Close()
			s.clientListener = nil
		}
		if s.controlListener != nil {
			_ = s.controlListener.Close()
			s.controlListener = nil
		}
		// Neither gRPC server has served yet; Stop just releases their
		// internal resources.
		if s.clientGrpcServer != nil {
			s.clientGrpcServer.Stop()
			s.clientGrpcServer = nil
		}
		if s.controlGrpcServer != nil {
			s.controlGrpcServer.Stop()
			s.controlGrpcServer = nil
		}
		s.mu.Unlock()
		return err
	}

	s.clientListener, err = net.Listen("tcp", address)
	if err != nil {
		return fail(fmt.Errorf("failed to listen: %v", err))
	}

	if s.isControlServer() {
		s.settingsState.RLock()
		voiceControlAddr := fmt.Sprintf("%s:%d", s.settingsState.VoiceControl.ListenHost, s.settingsState.VoiceControl.Port)
		s.settingsState.RUnlock()
		s.controlListener, err = net.Listen("tcp", voiceControlAddr)
		if err != nil {
			return fail(fmt.Errorf("failed to listen on control server address: %v", err))
		}
	}

	s.settingsState.RLock()
	clientTLS := s.settingsState.ClientTLS
	s.settingsState.RUnlock()

	clientCreds, err := clientTransportCredentials(clientTLS, s.logger)
	if err != nil {
		return fail(fmt.Errorf("client TLS: %w", err))
	}

	clientOpts := []grpc.ServerOption{
		// Recovery is first so it is outermost and covers every later interceptor.
		grpc.ChainUnaryInterceptor(s.recoveryUnaryInterceptor, s.loggingInterceptor, s.authInterceptor),
		grpc.ChainStreamInterceptor(s.recoveryStreamInterceptor, s.authStreamInterceptor),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             60 * time.Second, // allow pings every 60s
			PermitWithoutStream: true,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    60 * time.Second, // server pings an idle connection every 60s
			Timeout: 10 * time.Second, // wait 10s for ping ack
		}),
	}
	if clientCreds != nil {
		clientOpts = append(clientOpts, grpc.Creds(clientCreds))
	}

	s.clientGrpcServer = grpc.NewServer(clientOpts...)

	controlServer := voiceontrol.NewVoiceControlServer(s.serverState, s.settingsState, s.eventBus, s.logger)

	if s.isControlServer() {
		if err := s.initControlServer(controlServer); err != nil {
			return fail(fmt.Errorf("control server TLS: %w", err))
		}
	}

	// Nothing below may fail: NewSimpleRadioServer and NewAuthServer start
	// goroutines that fail() would not stop.
	s.voiceControlServer = controlServer

	srsServer := srs.NewSimpleRadioServer(s.serverState, s.settingsState, s.logger, s.eventBus, controlServer)
	s.srsServer = srsServer
	authServer := srs.NewAuthServer(s.serverState, s.settingsState, s.logger, s.distributionState, s.eventBus)
	s.authServer = authServer
	srspb.RegisterSRSServiceServer(s.clientGrpcServer, srsServer)
	srspb.RegisterAuthServiceServer(s.clientGrpcServer, authServer)

	healthServer := NewFullHealthServer()
	healthpb.RegisterHealthServer(s.clientGrpcServer, healthServer)
	if s.isControlServer() {
		healthpb.RegisterHealthServer(s.controlGrpcServer, healthServer)
	}

	go s.monitorHealth(srsServer, authServer, controlServer, healthServer)

	reflection.Register(s.clientGrpcServer)

	s.isRunning = true
	// Hand the serve goroutines their server and listener while the lock is
	// held: Stop nils these fields, and a Stop racing a just-returned Start
	// would otherwise make the goroutine call Serve on nil.
	clientSrv, clientLis := s.clientGrpcServer, s.clientListener
	controlSrv, controlLis := s.controlGrpcServer, s.controlListener
	s.mu.Unlock()

	go s.serveClient(address, clientSrv, clientLis)
	if controlSrv != nil {
		go s.serveControl(controlSrv, controlLis)
	}

	go s.handleShutdown(stopChan)

	return nil
}

// clientTransportCredentials builds transport credentials for the
// client-facing gRPC listener from cfg.
//
// Returns (nil, nil) when TLS is not configured, which leaves the listener
// plaintext -- the behaviour every deployment predating this block already
// has. Returns an error when the block is HALF configured, because an
// operator who set one field of two believes they enabled TLS, and silently
// serving plaintext to that operator is worse than refusing to start.
//
// The keypair is generated at the configured paths if absent, reusing
// LoadOrGenerateKeyPair exactly as the VoiceControl channel does, so a
// self-hoster ends up with a certificate they can copy to their clients.
// Regeneration only happens when the files are missing: a restart must not
// mint a new certificate, or every client pinning the old one breaks.
func clientTransportCredentials(cfg state.ClientTLSSettings, logger *slog.Logger) (credentials.TransportCredentials, error) {
	switch {
	case cfg.CertificateFile == "" && cfg.PrivateKeyFile == "":
		logger.Warn("client-facing gRPC port is PLAINTEXT: no clientTLS block configured, so client credentials cross the network in the clear")
		return nil, nil
	case cfg.PrivateKeyFile == "":
		return nil, fmt.Errorf("clientTLS.certificateFile is set but clientTLS.privateKeyFile is empty: configure both or neither")
	case cfg.CertificateFile == "":
		return nil, fmt.Errorf("clientTLS.privateKeyFile is set but clientTLS.certificateFile is empty: configure both or neither")
	}

	cert, _, err := voiceontrol.LoadOrGenerateKeyPair(cfg.PrivateKeyFile, cfg.CertificateFile, cfg.ServerName)
	if err != nil {
		return nil, fmt.Errorf("load or generate client TLS keypair: %w", err)
	}
	logger.Info("client-facing gRPC port is TLS", "certificateFile", cfg.CertificateFile, "serverName", cfg.ServerName)
	return handshakeLoggingCreds{credentials.NewServerTLSFromCert(cert), logger}, nil
}

// handshakeLoggingCreds logs failed server-side TLS handshakes on the
// client-facing port. grpc-go reports them only through grpclog, which is
// silent by default, so a client dialing plaintext against this TLS-only
// port -- its HTTP/2 preface is not a ClientHello -- otherwise leaves no
// trace here while the client sees only "error reading server preface".
//
// Logged at WARN because the server's log level is fixed at INFO; on a
// public port this also surfaces scanners, which is acceptable noise.
type handshakeLoggingCreds struct {
	credentials.TransportCredentials
	logger *slog.Logger
}

func (c handshakeLoggingCreds) ServerHandshake(conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	out, info, err := c.TransportCredentials.ServerHandshake(conn)
	if err != nil {
		hint := ""
		if strings.Contains(err.Error(), "first record does not look like a TLS handshake") {
			hint = "the client connected in plaintext; it must dial with TLS (VCS client: set tls_ca_file to this server's clientTLS certificate)"
		}
		c.logger.Warn("client TLS handshake failed", "remote", conn.RemoteAddr(), "error", err, "hint", hint)
	}
	return out, info, err
}

func (c handshakeLoggingCreds) Clone() credentials.TransportCredentials {
	return handshakeLoggingCreds{c.TransportCredentials.Clone(), c.logger}
}

// initControlServer builds the VoiceControl gRPC server. It returns the
// keypair load error instead of leaving controlGrpcServer nil, which Start
// and serveControl would then dereference.
func (s *Server) initControlServer(controlServer voicecontrolpb.VoiceControlServiceServer) error {
	s.settingsState.RLock()
	privateKeyFileName := s.settingsState.VoiceControl.PrivateKeyFile
	certificateFileName := s.settingsState.VoiceControl.CertificateFile
	serverName := s.settingsState.VoiceControl.ServerName
	s.settingsState.RUnlock()
	cert, _, err := voiceontrol.LoadOrGenerateKeyPair(privateKeyFileName, certificateFileName, serverName)
	if err != nil {
		return err
	}
	s.controlGrpcServer = grpc.NewServer(
		grpc.Creds(credentials.NewServerTLSFromCert(cert)),
		grpc.ChainUnaryInterceptor(s.recoveryUnaryInterceptor, s.loggingInterceptor),
		grpc.ChainStreamInterceptor(s.recoveryStreamInterceptor),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second, // allow pings every 10s
			PermitWithoutStream: true,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    20 * time.Second, // server sends pings every 20s if idle
			Timeout: 10 * time.Second, // wait 10s for ping ack
		}),
	)
	voicecontrolpb.RegisterVoiceControlServiceServer(s.controlGrpcServer, controlServer)
	reflection.Register(s.controlGrpcServer)
	return nil
}

func (s *Server) monitorHealth(
	srsServer *srs.SimpleRadioServer,
	authServer *srs.AuthServer,
	controlServer *voiceontrol.VoiceControlServer,
	healthServer *FullHealthServer,
) {
	for s.IsRunning() {
		srsStatus := srsServer.GetServerState()
		authStatus := authServer.GetServerState()
		healthServer.SetServingStatus(healthServiceSRS, srsStatus)
		healthServer.SetServingStatus(healthServiceAuth, authStatus)

		controlStatus := controlServer.GetServerState()
		if s.isControlServer() {
			healthServer.SetServingStatus(healthServiceControl, controlStatus)
		}

		if srsStatus == healthpb.HealthCheckResponse_SERVING &&
			controlStatus == healthpb.HealthCheckResponse_SERVING &&
			authStatus == healthpb.HealthCheckResponse_SERVING {
			healthServer.SetServingStatus(healthServiceSystem, healthpb.HealthCheckResponse_SERVING)
		} else {
			healthServer.SetServingStatus(healthServiceSystem, healthpb.HealthCheckResponse_NOT_SERVING)
		}

		time.Sleep(sleep)
	}
}

func (s *Server) serveClient(address string, srv *grpc.Server, lis net.Listener) {
	s.logger.Info("Starting VCS gRPC server", "address", address)
	if err := srv.Serve(lis); err != nil {
		if !errors.Is(err, grpc.ErrServerStopped) {
			s.logger.Error("gRPC server error", "error", err)
		}
		s.mu.Lock()
		s.isRunning = false
		s.mu.Unlock()
	}
}

func (s *Server) serveControl(srv *grpc.Server, lis net.Listener) {
	s.settingsState.RLock()
	addr := fmt.Sprintf("%s:%d", s.settingsState.VoiceControl.ListenHost, s.settingsState.VoiceControl.Port)
	s.settingsState.RUnlock()
	s.logger.Info("Starting Control gRPC server", "address", addr)
	if err := srv.Serve(lis); err != nil {
		if !errors.Is(err, grpc.ErrServerStopped) {
			s.logger.Error("gRPC server error", "error", err)
		}
		s.mu.Lock()
		s.isRunning = false
		s.mu.Unlock()
	}
}

func (s *Server) handleShutdown(stopChan chan struct{}) {
	<-stopChan
	s.logger.Info("Received stop signal for gRPC server")
	err := s.Stop()
	if err != nil {
		s.logger.Error("failed to stop gRPC server", "error", err)
	}
}

func (s *Server) Stop() error {
	var stopErr error

	s.stopOnce.Do(func() {
		if !s.IsRunning() {
			return
		}
		s.mu.Lock()
		s.isRunning = false
		s.mu.Unlock()

		s.logger.Info("Stopping gRPC server")

		if s.srsServer != nil {
			s.srsServer.Stop()
		}
		if s.authServer != nil {
			s.authServer.Stop()
		}

		// GracefulStop will automatically close the clientListener
		if s.clientGrpcServer != nil {
			s.clientGrpcServer.GracefulStop()
			s.clientGrpcServer = nil
		}

		if s.controlGrpcServer != nil {
			s.controlGrpcServer.GracefulStop()
			s.controlGrpcServer = nil
		}

		// GracefulStop only closes a listener that Serve has already
		// registered. If Stop wins the race against a just-started serve
		// goroutine, that goroutine's Serve closes the listener only later,
		// after Stop has returned, so a caller rebinding the port right away
		// fails. Close them here as well; a second Close is harmless.
		if s.clientListener != nil {
			_ = s.clientListener.Close()
			s.clientListener = nil
		}
		if s.controlListener != nil {
			_ = s.controlListener.Close()
			s.controlListener = nil
		}
	})

	return stopErr
}

func (s *Server) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isRunning
}

// GetDistributionStatus returns the current distribution status from the voice control server.
// Returns a zero-value DistributionView when the control server is not running.
func (s *Server) GetDistributionStatus() voiceontrol.DistributionView {
	s.mu.RLock()
	vcs := s.voiceControlServer
	s.mu.RUnlock()
	if vcs == nil {
		return voiceontrol.DistributionView{}
	}
	return vcs.GetDistributionStatus()
}

// Logging interceptor for debugging
func (s *Server) loggingInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	s.logger.Debug("gRPC request", "method", info.FullMethod, "request", req)

	resp, err := handler(ctx, req)

	if err != nil {
		s.logger.Error("gRPC error",
			"method", info.FullMethod,
			"error", err)
	}

	return resp, err
}

// authenticate validates the bearer token for fullMethod and returns a context
// carrying the client GUID, or a gRPC status error. AuthService and
// VoiceControlService are exempt and get ctx back unchanged. Both the unary and
// the stream interceptor go through here so they cannot drift apart again.
// SV1 adds its unknown-GUID check (SRV-06) here.
func (s *Server) authenticate(ctx context.Context, fullMethod string) (context.Context, error) {
	elements := strings.Split(fullMethod, "/")
	if len(elements) < 3 {
		return nil, status.Errorf(codes.Unauthenticated, "malformed method path: %s", fullMethod)
	}
	fullServiceName := elements[1]
	pathName := elements[2]

	parts := strings.Split(fullServiceName, ".")
	if len(parts) < 2 {
		return nil, status.Errorf(codes.Unauthenticated, "malformed service name: %s", fullServiceName)
	}
	serviceName := parts[len(parts)-1]

	if serviceName == "AuthService" || serviceName == "VoiceControlService" {
		return ctx, nil
	}

	s.logger.Debug("Authentication required for service", "service", serviceName, "method", fullMethod)

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Errorf(codes.Unauthenticated, "missing metadata for %s", fullMethod)
	}

	tokens := md.Get("authorization")
	if len(tokens) == 0 {
		return nil, status.Errorf(codes.Unauthenticated, "missing authorization token for %s", fullMethod)
	}
	token := strings.TrimPrefix(tokens[0], "Bearer ")

	minRole, _ := utils.GetMinimumRoleForMethod(pathName)

	s.settingsState.RLock()
	claims, err := utils.GetTokenClaims(token, minRole, s.settingsState.Security.Token.PrivateKeyFile, s.settingsState.Security.Token.PublicKeyFile)
	s.settingsState.RUnlock()
	if err != nil {
		s.logger.Error("Authentication error", "method", fullMethod, "error", err)
		return nil, authError(fullMethod, err)
	}
	if claims == nil {
		return nil, status.Errorf(codes.Unauthenticated, "authentication error for %s: no claims", fullMethod)
	}

	return context.WithValue(ctx, utils.ClientIDKey, claims.ClientGuid), nil
}

// authError maps a utils.GetTokenClaims error to a gRPC status error. This is
// the contract the VCS client reads: Unauthenticated means the session is gone
// (bad, malformed or expired token), PermissionDenied means the token is valid
// but its role is too low, Internal means the server could not verify at all.
func authError(fullMethod string, err error) error {
	switch {
	case errors.Is(err, utils.ErrTokenKeys):
		// The cause is logged by the caller; it names server file paths, so it
		// stays out of the client-facing message.
		return status.Error(codes.Internal, "token verification unavailable")
	case errors.Is(err, utils.ErrInsufficientRole):
		return status.Errorf(codes.PermissionDenied, "insufficient role for %s: %v", fullMethod, err)
	default:
		return status.Errorf(codes.Unauthenticated, "authentication error for %s: %v", fullMethod, err)
	}
}

func (s *Server) authInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	ctx, err := s.authenticate(ctx, info.FullMethod)
	if err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

// wrappedStream overrides the context of a grpc.ServerStream.
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

func (s *Server) authStreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx, err := s.authenticate(ss.Context(), info.FullMethod)
	if err != nil {
		return err
	}
	if ctx == ss.Context() {
		return handler(srv, ss)
	}
	return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
}
