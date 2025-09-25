package voiceontrol

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

const (
	DefaultVoiceControlPort = 14448
)

type VoiceControlClient struct {
	client pb.VoiceControlServiceClient
	// GetServerState returns the current state of the voice control server.
	conn                *grpc.ClientConn
	serverId            string
	assignedFrequencies []*pb.FrequencyRange
	logger              *slog.Logger
	stream              grpc.BidiStreamingClient[pb.ControlResponse, pb.ControlMessage]
	stopc               chan struct{}
	connectionFailed    bool
}

func NewVoiceControlClient(serverId string, logger *slog.Logger) *VoiceControlClient {
	return &VoiceControlClient{
		serverId: serverId,
		logger:   logger,
	}
}

func (v *VoiceControlClient) ConnectControlServer(addr string) error {
	address := fmt.Sprintf("%s:%d", addr, DefaultVoiceControlPort)
	v.logger.Info("Connecting to Control node", "address", address)

	cert, err := LoadCertificateOnly()
	if err != nil {
		return err
	}
	clientTLSConfig, err := CreateClientTLSConfig(cert)
	if err != nil {
		return err
	}

	opts := []grpc.DialOption{
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
	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		return err
	}

	v.conn = conn
	client := pb.NewVoiceControlServiceClient(v.conn)
	v.client = client
	if err := v.establishConnection(); err != nil {
		v.logger.Error("Failed to establish connection to Control Server", "error", err)
		if v.conn != nil {
			v.conn.Close()
		}
	}

	return nil
}

func (v *VoiceControlClient) establishConnection() error {
	go func() {
		lastState := v.conn.GetState()
		for {
			if !v.conn.WaitForStateChange(context.Background(), lastState) {
				return
			}
			newState := v.conn.GetState()
			if newState == connectivity.Idle {
				v.logger.Warn("Voicecontrol connection idle, stopping voice services")
				// TODO: Implement logic to stop voice services
				go v.handleReconnection()
			}
			lastState = newState
		}
	}()

	if err := v.registerSelf(); err != nil {
		return err
	}
	return v.establishStream()
}

func (v *VoiceControlClient) registerSelf() error {
	resp, err := v.client.RegisterVoiceServer(context.Background(), &pb.RegisterVoiceServerRequest{
		ServerId: v.serverId,
		Capabilities: &pb.ServerCapabilities{
			Version: "0.1.0",
		},
		ServerAddress: v.conn.Target(),
		UdpPort:       5002,
	})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("failed to register voice server: %s", resp.Message)
	}
	v.assignedFrequencies = resp.AssignedFrequencies
	return nil
}

func (v *VoiceControlClient) establishStream() error {
	stream, err := v.client.EstablishStream(context.Background())
	v.stream = stream
	if err != nil {
		st, ok := status.FromError(err)
		if ok && (st.Code() == codes.Unavailable || st.Code() == codes.DeadlineExceeded) {
			go v.handleReconnection()
			return fmt.Errorf("temporary connection issue: %v", err)
		}
		return fmt.Errorf("failed to establish stream: %v", err)
	}
	if v.stopc == nil {
		v.stopc = make(chan struct{})
	}
	go func() {
		for {
			select {
			case <-v.stopc:
				return
			default:
				_, err := v.stream.Recv()
				if err == io.EOF {
					return
				}
				if err != nil {
					go v.handleReconnection()
					break
				}
			}
		}
	}()

	v.connectionFailed = false
	v.logger.Info("Connected to Control Server")
	return nil
}

func (v *VoiceControlClient) handleReconnection() {
	currentBackoff := 1
	reconnectionAttempts := 0
	maxBackoff := 128
	maxReconnectionAttempts := 20
	v.logger.Warn("Attempting to reconnect to Voice Control Server")

	for {
		select {
		case <-v.stopc:
			return
		default:
			if reconnectionAttempts >= maxReconnectionAttempts {
				v.logger.Error("Max reconnection attempts reached, giving up")
				v.Close()
				return
			}
			err := v.establishConnection()
			if err == nil {
				return
			}
			time.Sleep(time.Duration(currentBackoff) * time.Second)
			if currentBackoff < maxBackoff {
				currentBackoff *= 2
			}
			reconnectionAttempts++
		}
	}
}

func (v *VoiceControlClient) Close() error {
	if v.stopc != nil {
		close(v.stopc)
	}
	if v.conn != nil {
		return v.conn.Close()
	}
	return nil
}
