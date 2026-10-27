package voiceontrol

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/state"
	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// Defaults for the control session. Tests in this package override the
// matching fields after NewVoiceControlClient.
const (
	defaultHeartbeatInterval = 15 * time.Second
	defaultBackoffMin        = 1 * time.Second
	defaultBackoffMax        = 30 * time.Second
	// defaultBackoffResetAfter: a session that lasted at least this long was
	// healthy, so the next reconnect starts from the minimum backoff again.
	defaultBackoffResetAfter = 30 * time.Second
	defaultRPCTimeout        = 10 * time.Second
	defaultReportTimeout     = 5 * time.Second
	// defaultWarnEvery bounds the "Control node unreachable" WARN: a node
	// retrying forever logs at most one per window.
	defaultWarnEvery = 60 * time.Second
)

// VoiceControlClient is a distributed voice node's link to the control node.
//
// One supervisor goroutine (run) owns the link for the client's whole life:
// it registers, opens the control stream, heartbeats and applies control
// messages, and when the session fails it waits a capped, jittered backoff
// and starts a new one -- forever, until Close. Only run ever reconnects, so
// there is exactly one session (and one heartbeat loop) at a time.
type VoiceControlClient struct {
	client        pb.VoiceControlServiceClient
	conn          *grpc.ClientConn
	serverId      string
	isGlobal      bool
	version       string
	logger        *slog.Logger
	settingsState *state.SettingsState
	serverState   *state.ServerState

	// advertisedHost/advertisedPort are resolved once, in ConnectControlServer.
	advertisedHost string
	advertisedPort int

	// mu guards assignedCoalitions, written by run and read by anyone.
	mu                 sync.Mutex
	assignedCoalitions []string

	// lifeMu guards the lifecycle fields below.
	lifeMu    sync.Mutex
	started   bool
	closed    bool
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once

	// failures counts consecutive failed sessions. Only run touches it.
	failures int
	warn     warnLimiter

	// kickHandler drops a client's voice binding on this node. Set once
	// (before ConnectControlServer) and kept across reconnects.
	kickMu      sync.Mutex
	kickHandler func(uuid.UUID)

	// Seams. dialTarget/dialOpts, when set, replace the configured address
	// and TLS credentials.
	dialTarget        string
	dialOpts          []grpc.DialOption
	heartbeatInterval time.Duration
	backoffMin        time.Duration
	backoffMax        time.Duration
	backoffResetAfter time.Duration
	rpcTimeout        time.Duration
	reportTimeout     time.Duration
	warnEvery         time.Duration
	jitter            func(time.Duration) time.Duration
}

// warnLimiter lets one log line through per window and counts the rest.
type warnLimiter struct {
	mu         sync.Mutex
	lastLogged time.Time
	suppressed int
}

func (w *warnLimiter) shouldLog(now time.Time, window time.Duration) (bool, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lastLogged.IsZero() || now.Sub(w.lastLogged) >= window {
		n := w.suppressed
		w.suppressed = 0
		w.lastLogged = now
		return true, n
	}
	w.suppressed++
	return false, 0
}

// SetKickHandler registers the function that drops a client's voice binding
// on this node. It is called for a kick_client command and for a LEFT delta,
// always outside the server-state lock.
func (v *VoiceControlClient) SetKickHandler(h func(uuid.UUID)) {
	v.kickMu.Lock()
	v.kickHandler = h
	v.kickMu.Unlock()
}

// kick calls the kick handler, if one is set.
func (v *VoiceControlClient) kick(id uuid.UUID) {
	v.kickMu.Lock()
	h := v.kickHandler
	v.kickMu.Unlock()
	if h != nil {
		h(id)
	}
}

// NewVoiceControlClient builds a client. version is reported to the control
// node in the registration's capabilities.
func NewVoiceControlClient(serverId string, settingsState *state.SettingsState, serverState *state.ServerState, isGlobal bool, version string, logger *slog.Logger) *VoiceControlClient {
	return &VoiceControlClient{
		serverId:          serverId,
		isGlobal:          isGlobal,
		version:           version,
		logger:            logger,
		settingsState:     settingsState,
		serverState:       serverState,
		heartbeatInterval: defaultHeartbeatInterval,
		backoffMin:        defaultBackoffMin,
		backoffMax:        defaultBackoffMax,
		backoffResetAfter: defaultBackoffResetAfter,
		rpcTimeout:        defaultRPCTimeout,
		reportTimeout:     defaultReportTimeout,
		warnEvery:         defaultWarnEvery,
		jitter:            defaultJitter,
	}
}

// UseDialer replaces the configured control address and TLS credentials with
// target and opts. It is for tests and embedding; production uses the TLS
// settings. It must be called before ConnectControlServer.
func (v *VoiceControlClient) UseDialer(target string, opts ...grpc.DialOption) {
	v.dialTarget = target
	v.dialOpts = opts
}

// AssignedCoalitions returns a copy of the coalitions the control node has
// assigned to this node.
func (v *VoiceControlClient) AssignedCoalitions() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.assignedCoalitions...)
}

func (v *VoiceControlClient) setAssignedCoalitions(c []string) {
	v.mu.Lock()
	v.assignedCoalitions = append([]string(nil), c...)
	v.mu.Unlock()
}

// ConnectControlServer validates the configuration, prepares the gRPC
// connection and starts the supervisor that keeps this node registered.
// listenPort is the UDP port the voice socket is bound to; it is advertised
// when voiceControl.publicAddr carries no port.
//
// It returns an error only for a configuration problem (an unusable
// publicAddr, an unreadable certificate). A network problem is never an
// error here: the supervisor retries until the control node answers.
func (v *VoiceControlClient) ConnectControlServer(listenPort int) error {
	v.settingsState.RLock()
	publicAddr := v.settingsState.VoiceControl.PublicAddr
	address := net.JoinHostPort(v.settingsState.VoiceControl.RemoteHost, strconv.Itoa(v.settingsState.VoiceControl.Port))
	certFileName := v.settingsState.VoiceControl.CertificateFile
	serverName := v.settingsState.VoiceControl.ServerName
	v.settingsState.RUnlock()

	host, port, err := resolveAdvertisedAddr(publicAddr, listenPort)
	if err != nil {
		return err
	}

	target, opts := v.dialTarget, v.dialOpts
	if target == "" {
		cert, err := LoadCertificateOnly(certFileName)
		if err != nil {
			return err
		}
		clientTLSConfig, err := CreateClientTLSConfig(cert, serverName)
		if err != nil {
			return err
		}
		target = address
		opts = []grpc.DialOption{
			grpc.WithTransportCredentials(credentials.NewTLS(clientTLSConfig)),
			grpc.WithConnectParams(grpc.ConnectParams{
				Backoff: backoff.Config{
					BaseDelay:  1 * time.Second,
					Multiplier: 1.6,
					MaxDelay:   10 * time.Second,
					Jitter:     0.2,
				},
				MinConnectTimeout: 5 * time.Second,
			}),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{
				Time:                15 * time.Second,
				Timeout:             10 * time.Second,
				PermitWithoutStream: true,
			}),
		}
	}

	v.lifeMu.Lock()
	defer v.lifeMu.Unlock()
	if v.closed {
		return errors.New("voice control client is closed")
	}
	if v.started {
		return errors.New("voice control client is already connected")
	}

	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return err
	}
	v.logger.Info("Connecting to Control node", "address", target, "advertised", net.JoinHostPort(host, strconv.Itoa(port)))

	v.advertisedHost, v.advertisedPort = host, port
	v.conn = conn
	v.client = pb.NewVoiceControlServiceClient(conn)
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	v.done = make(chan struct{})
	v.started = true
	go v.run(ctx)
	return nil
}

// run is the supervisor: one session at a time, a capped jittered backoff
// between sessions, no attempt cap. It returns only when ctx is cancelled.
func (v *VoiceControlClient) run(ctx context.Context) {
	defer close(v.done)
	attempt := 0
	for {
		started := time.Now()
		err := v.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= v.backoffResetAfter {
			attempt = 0
		}
		v.failures++
		wait := nextBackoff(attempt, v.backoffMin, v.backoffMax, v.jitter)
		if ok, n := v.warn.shouldLog(time.Now(), v.warnEvery); ok {
			v.logger.Warn("Control node unreachable, retrying",
				"attempt", v.failures,
				"retry_in", wait,
				"error", err,
				"suppressed_since_last", n)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		attempt++
	}
}

// session runs one control session: register, open the stream, identify,
// heartbeat, and apply control messages until the stream fails. Everything
// it starts runs under a context it cancels before returning, and it waits
// for its heartbeat goroutine, so nothing outlives the session.
func (v *VoiceControlClient) session(ctx context.Context) error {
	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := v.registerSelf(sessCtx); err != nil {
		return err
	}
	if v.failures > 0 {
		v.logger.Info("Reconnected to Control Server", "attempts", v.failures)
		v.failures = 0
	}

	stream, err := v.client.EstablishStream(sessCtx)
	if err != nil {
		return fmt.Errorf("failed to establish stream: %w", err)
	}
	// Identify ourselves so the server can find our registration record.
	if err := stream.Send(&pb.ControlResponse{
		ServerId: v.serverId,
		EventId:  "init",
		Success:  true,
		Message:  "stream established",
	}); err != nil {
		return fmt.Errorf("failed to send stream identification: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		v.heartbeatLoop(sessCtx)
	}()
	defer func() {
		cancel()
		wg.Wait()
	}()

	v.logger.Info("Stream established with Control Server")
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return errors.New("control stream closed by the control node")
		}
		if err != nil {
			return fmt.Errorf("control stream failed: %w", err)
		}
		v.applyControlMessage(msg)
	}
}

func (v *VoiceControlClient) registerSelf(ctx context.Context) error {
	v.settingsState.RLock()
	region := v.settingsState.VoiceControl.Region
	v.settingsState.RUnlock()

	rctx, cancel := context.WithTimeout(ctx, v.rpcTimeout)
	defer cancel()
	resp, err := v.client.RegisterVoiceServer(rctx, &pb.RegisterVoiceServerRequest{
		ServerId:      v.serverId,
		Capabilities:  &pb.ServerCapabilities{Version: v.version},
		ServerAddress: v.advertisedHost,
		UdpPort:       int32(v.advertisedPort),
		IsGlobal:      v.isGlobal,
		Region:        region,
	})
	if err != nil {
		return fmt.Errorf("failed to register voice server: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("registration rejected: %s", resp.Message)
	}
	v.setAssignedCoalitions(resp.AssignedCoalitions)
	v.logger.Info("Registered with Control Server",
		"coalitions", resp.AssignedCoalitions,
		"globalAddr", resp.GlobalVoiceAddress,
	)
	return nil
}

func (v *VoiceControlClient) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(v.heartbeatInterval)
	defer ticker.Stop()
	var lastRttMs int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sent := time.Now()
			hctx, cancel := context.WithTimeout(ctx, v.rpcTimeout)
			_, err := v.client.SendHeartbeat(hctx, &pb.HeartbeatRequest{
				ServerId:  v.serverId,
				Status:    &pb.ServerStatus{IsHealthy: true},
				LastRttMs: lastRttMs,
			})
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					v.logger.Warn("Heartbeat failed", "error", err)
				}
				continue
			}
			lastRttMs = time.Since(sent).Milliseconds()
			v.logger.Debug("Heartbeat sent", "rttMs", lastRttMs)
		}
	}
}

func (v *VoiceControlClient) applyControlMessage(msg *pb.ControlMessage) {
	switch cmd := msg.Command.(type) {
	case *pb.ControlMessage_StateSnapshot:
		v.applySnapshot(cmd.StateSnapshot)
	case *pb.ControlMessage_ClientDelta:
		v.applyDelta(cmd.ClientDelta)
		// A client that left the session must not stay routable here. The
		// handler runs after applyDelta released the state lock.
		if cmd.ClientDelta.Type == pb.ClientDelta_LEFT {
			if id, err := uuid.Parse(cmd.ClientDelta.ClientId); err == nil {
				v.kick(id)
			}
		}
	case *pb.ControlMessage_KickClient:
		id, err := uuid.Parse(cmd.KickClient.GetClientId())
		if err != nil {
			v.logger.Warn("invalid client ID in kick_client", "id", cmd.KickClient.GetClientId())
			return
		}
		v.logger.Info("Control node kicked client", "client_id", id, "reason", cmd.KickClient.GetReason())
		v.kick(id)
	case *pb.ControlMessage_AssignCoalitions:
		v.setAssignedCoalitions(cmd.AssignCoalitions.Coalitions)
		v.logger.Info("Coalition assignment updated", "coalitions", cmd.AssignCoalitions.Coalitions)
	case *pb.ControlMessage_AssignFrequencies:
		ranges := cmd.AssignFrequencies.FrequencyRanges
		v.logger.Info("Frequency band assignment received", "ranges", len(ranges))
	}
}

// resolveAdvertisedAddr turns voiceControl.publicAddr into the host and UDP
// port this node advertises to the control node. publicAddr is either
// "host:port" (the port wins, e.g. a NAT or container port mapping) or a bare
// host (the port is listenPort, the port the voice socket is bound to). A bare
// IPv6 address counts as a bare host; a bracketed "[v6]:port" as host:port.
//
// It refuses an empty value, an unspecified address (0.0.0.0 / ::), a missing
// host and any port outside 1-65535: a node advertising any of those is
// unreachable by clients, so it must not register at all.
func resolveAdvertisedAddr(publicAddr string, listenPort int) (string, int, error) {
	s := strings.TrimSpace(publicAddr)
	if s == "" {
		return "", 0, fmt.Errorf("voiceControl.publicAddr is not set: a distributed voice node must advertise an address clients can reach")
	}

	host, port := s, listenPort
	if net.ParseIP(s) == nil && strings.Contains(s, ":") {
		h, p, err := net.SplitHostPort(s)
		if err != nil {
			return "", 0, fmt.Errorf("voiceControl.publicAddr %q: %w", publicAddr, err)
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return "", 0, fmt.Errorf("voiceControl.publicAddr %q: invalid port", publicAddr)
		}
		host, port = h, n
	}

	if host == "" {
		return "", 0, fmt.Errorf("voiceControl.publicAddr %q: missing host", publicAddr)
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return "", 0, fmt.Errorf("voiceControl.publicAddr %q: unspecified address (0.0.0.0 / ::) is not reachable by clients", publicAddr)
	}
	if port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("voiceControl.publicAddr %q: invalid port", publicAddr)
	}
	return host, port, nil
}

func (v *VoiceControlClient) applySnapshot(snap *pb.ClientStateSnapshot) {
	v.serverState.Lock()
	defer v.serverState.Unlock()

	v.serverState.Clients = make(map[uuid.UUID]*state.ClientState, len(snap.Clients))
	v.serverState.RadioClients = make(map[uuid.UUID]*state.RadioState, len(snap.Radios))

	for idStr, info := range snap.Clients {
		id, err := uuid.Parse(idStr)
		if err != nil {
			v.logger.Warn("invalid client ID in snapshot", "id", idStr)
			continue
		}
		v.serverState.Clients[id] = &state.ClientState{
			Name:        info.Name,
			Coalition:   info.Coalition,
			UnitId:      info.UnitId,
			Role:        uint8(info.Role),
			VoiceSecret: info.VoiceSecret,
		}
	}
	for idStr, radio := range snap.Radios {
		id, err := uuid.Parse(idStr)
		if err != nil {
			v.logger.Warn("invalid radio ID in snapshot", "id", idStr)
			continue
		}
		v.serverState.RadioClients[id] = convertVoiceRadioInfo(radio)
	}
	v.logger.Info("Applied state snapshot", "clients", len(snap.Clients))
}

func (v *VoiceControlClient) applyDelta(delta *pb.ClientDelta) {
	id, err := uuid.Parse(delta.ClientId)
	if err != nil {
		v.logger.Warn("invalid client ID in delta", "id", delta.ClientId)
		return
	}

	v.serverState.Lock()
	defer v.serverState.Unlock()

	switch delta.Type {
	case pb.ClientDelta_JOINED, pb.ClientDelta_INFO_UPDATED:
		if delta.ClientInfo != nil {
			v.serverState.Clients[id] = &state.ClientState{
				Name:        delta.ClientInfo.Name,
				Coalition:   delta.ClientInfo.Coalition,
				UnitId:      delta.ClientInfo.UnitId,
				Role:        uint8(delta.ClientInfo.Role),
				VoiceSecret: delta.ClientInfo.VoiceSecret,
			}
			if _, exists := v.serverState.RadioClients[id]; !exists {
				v.serverState.RadioClients[id] = &state.RadioState{Radios: []state.Radio{}}
			}
		}
	case pb.ClientDelta_LEFT:
		delete(v.serverState.Clients, id)
		delete(v.serverState.RadioClients, id)
	case pb.ClientDelta_RADIO_UPDATED:
		if delta.RadioInfo != nil {
			newRadio := convertVoiceRadioInfo(delta.RadioInfo)
			// Preserve server-owned mute status across radio updates.
			if existing, exists := v.serverState.RadioClients[id]; exists {
				newRadio.Muted = existing.Muted
			}
			v.serverState.RadioClients[id] = newRadio
		}
	case pb.ClientDelta_MUTED:
		if radio, exists := v.serverState.RadioClients[id]; exists {
			radio.Muted = true
		}
	case pb.ClientDelta_UNMUTED:
		if radio, exists := v.serverState.RadioClients[id]; exists {
			radio.Muted = false
		}
	}
}

func convertVoiceRadioInfo(r *pb.VoiceRadioInfo) *state.RadioState {
	radios := make([]state.Radio, len(r.Radios))
	for i, radio := range r.Radios {
		radios[i] = state.Radio{
			ID:         radio.Id,
			Frequency:  radio.Frequency,
			Enabled:    radio.Enabled,
			IsIntercom: radio.IsIntercom,
		}
	}
	return &state.RadioState{Radios: radios, Muted: r.Muted}
}

// ReportClientConnected tells the control node a client bound to this node.
// It is called from its own goroutine and is bounded by reportTimeout.
func (v *VoiceControlClient) ReportClientConnected(clientID uuid.UUID, addr *net.UDPAddr) {
	if v.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), v.reportTimeout)
	defer cancel()
	_, err := v.client.ReportClientConnected(ctx, &pb.ClientConnectedRequest{
		ServerId:      v.serverId,
		ClientId:      clientID.String(),
		ClientAddress: addr.IP.String(),
		ClientPort:    int32(addr.Port),
		ConnectedAt:   time.Now().Unix(),
	})
	if err != nil {
		v.logger.Warn("ReportClientConnected failed", "client", clientID, "error", err)
	}
}

// ReportClientDisconnected tells the control node a client left this node.
// It is called from its own goroutine and is bounded by reportTimeout.
func (v *VoiceControlClient) ReportClientDisconnected(clientID uuid.UUID) {
	if v.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), v.reportTimeout)
	defer cancel()
	_, err := v.client.ReportClientDisconnected(ctx, &pb.ClientDisconnectedRequest{
		ServerId:       v.serverId,
		ClientId:       clientID.String(),
		Reason:         pb.DisconnectReason_CLIENT_DISCONNECT,
		DisconnectedAt: time.Now().Unix(),
	})
	if err != nil {
		v.logger.Warn("ReportClientDisconnected failed", "client", clientID, "error", err)
	}
}

// Close stops the supervisor, waits for it to exit and closes the gRPC
// connection. Ending the stream lets the control node deregister this node.
// It is idempotent and safe to call before (or without) ConnectControlServer.
//
// The supervisor may be inside the kick handler, which takes the voice
// server's lock, so a caller must not hold that lock while calling Close.
func (v *VoiceControlClient) Close() error {
	var err error
	v.closeOnce.Do(func() {
		v.lifeMu.Lock()
		v.closed = true
		cancel, done, conn := v.cancel, v.done, v.conn
		v.lifeMu.Unlock()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
		if conn != nil {
			err = conn.Close()
		}
	})
	return err
}
