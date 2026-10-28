package topology

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

// voiceStartedMsg is the line voice/server.go logs once the UDP socket is
// bound and the receive path is about to start (its shards start right after).
const voiceStartedMsg = "Voice server started"

// StartStandalone launches one standalone server process (control, HTTP and
// voice in one) and returns once its readiness ladder holds, in order:
//
//  1. GraphQL answers 200;
//  2. the "Voice server started" line is in the log;
//  3. the client gRPC port reaches connectivity.Ready.
//
// Rung 3 is a bare connect and a handshake, never an RPC: InitAuth would hold
// a pending-auth slot for minutes (srs/auth_service.go). A failure kills and
// reaps the process. A bind error retries once. The only main role is
// Standalone; Addr(Standalone) is the UDP voice address and Addr(Control) the
// client gRPC one.
func StartStandalone(ctx context.Context, sp Spec) (*Topology, error) {
	return startWithRetry(ctx, sp, startStandaloneOnce)
}

func startStandaloneOnce(ctx context.Context, sp Spec) (top *Topology, err error) {
	if err := os.MkdirAll(sp.Dir, 0o755); err != nil {
		return nil, err
	}
	t := &Topology{
		sp:         sp,
		voicePorts: map[Role]int{},
		procs:      map[Role]*proc{},
		extra:      map[string]*proc{},
		apiKey:     uuid.NewString(),
	}
	for _, c := range sp.Coalitions {
		t.coalitionNames = append(t.coalitionNames, c.Name)
	}
	defer func() {
		if err != nil {
			_ = t.Stop()
			top = nil
		}
	}()

	for _, p := range []*int{&t.httpPort, &t.controlPort, &t.vcPort} {
		if *p, err = FreeTCPPort(); err != nil {
			return nil, err
		}
	}
	if t.voicePorts[Standalone], err = FreeUDPPort(); err != nil {
		return nil, err
	}

	lctx, cancel := context.WithTimeout(ctx, sp.ReadyTimeout)
	defer cancel()

	// vcPort is rendered but never bound: only a control process listens on it.
	if err = t.launch(Standalone, ProcConfig{
		Role: Standalone, HTTPPort: t.httpPort, ControlPort: t.controlPort,
		VoicePort: t.voicePorts[Standalone], VoiceControlPort: t.vcPort,
		Coalitions:        sp.Coalitions,
		GlobalFrequencies: []float32{249.0}, TestFrequencies: []float32{247.2},
		APIKey:     t.apiKey,
		RemoteHost: loopback,
		PublicAddr: net.JoinHostPort(loopback, strconv.Itoa(t.voicePorts[Standalone])),
	}); err != nil {
		return nil, err
	}

	if err = t.rung(lctx, "graphql", "graphql", func(c context.Context) error {
		return t.GraphQL(c, "{__typename}", nil, nil)
	}); err != nil {
		return nil, err
	}
	if err = t.rung(lctx, "voice-started", voiceStartedMsg, func(context.Context) error {
		log, err := t.LogText(Standalone)
		if err != nil {
			return err
		}
		if !strings.Contains(log, voiceStartedMsg) {
			return fmt.Errorf("no %q line yet", voiceStartedMsg)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Plaintext: the topology renders no clientTLS block. A handshake-only
	// connect holds no server-side slot.
	conn, err := grpc.NewClient(t.Addr(Control), grpc.WithTransportCredentials(insecure.NewCredentials())) // NOSONAR go:S5332 loopback-only
	if err != nil {
		return nil, fmt.Errorf("topology: grpc client: %w", err)
	}
	defer conn.Close()
	if err = t.rung(lctx, "grpc-ready", "grpc Ready on the client port", func(c context.Context) error {
		conn.ResetConnectBackoff()
		conn.Connect()
		st := conn.GetState()
		if st == connectivity.Ready {
			return nil
		}
		wctx, wcancel := context.WithTimeout(c, 200*time.Millisecond)
		defer wcancel()
		conn.WaitForStateChange(wctx, st)
		if st = conn.GetState(); st != connectivity.Ready {
			return fmt.Errorf("client port is %s", st)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return t, nil
}

// rung polls fn like the distributed ladder and reports stage on success.
// waiting names the rung in a timeout error.
func (t *Topology) rung(ctx context.Context, stage, waiting string, fn func(context.Context) error) error {
	if err := t.poll(ctx, waiting, fn); err != nil {
		return err
	}
	if t.sp.stageHook != nil {
		t.sp.stageHook(stage)
	}
	return nil
}
