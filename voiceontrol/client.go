package voiceontrol

import (
	"context"
	"errors"
	"fmt"
	pb "github.com/FPGSchiba/vcs-srs-server/voicecontrolpb"
	"google.golang.org/grpc"
	"io"
	"log/slog"
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
}

func NewVoiceControlClient(serverId string, logger *slog.Logger) *VoiceControlClient {
	return &VoiceControlClient{
		serverId: serverId,
		logger:   logger,
	}
}

func (v *VoiceControlClient) ConnectControlServer(addr string, opts []grpc.DialOption) error {
	v.logger.Info("Connecting to Control node", "address", addr)
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return err
	}
	v.conn = conn
	client := pb.NewVoiceControlServiceClient(v.conn)
	v.client = client

	// Registering and establishing stream
	err = v.registerSelf()
	err = v.establishStream()
	return err
}

func (v *VoiceControlClient) registerSelf() error {
	v.logger.Info("Registering self", "serverId", v.serverId, "address", v.conn.Target())
	resp, err := v.client.RegisterVoiceServer(context.Background(), &pb.RegisterVoiceServerRequest{
		ServerId: v.serverId,
		Capabilities: &pb.ServerCapabilities{
			Version: "0.1.0",
		},
		// TODO: Both of these should be configurable
		ServerAddress: v.conn.Target(),
		UdpPort:       5002,
	})
	if err != nil {
		return err
	}
	if !resp.Success {
		return errors.New(fmt.Sprintf("Failed to register voice server: %s", resp.Message))
	}
	v.assignedFrequencies = resp.AssignedFrequencies
	return nil
}

func (v *VoiceControlClient) establishStream() error {
	v.logger.Info("Establishing stream", "serverId", v.serverId, "address", v.conn.Target())
	stream, err := v.client.EstablishStream(context.Background())
	v.stream = stream
	if err != nil {
		return fmt.Errorf("failed to establish stream: %v", err)
	}
	if v.stopc == nil {
		v.stopc = make(chan struct{})
	}
	go func() {
		for {
			select {
			case <-v.stopc:
				v.logger.Info("Stopping stream listener goroutine")
				return
			default:
				in, err := v.stream.Recv()
				if err == io.EOF {
					return
				}
				if err != nil {
					v.logger.Warn("Failed to read from stream, ignoring...", "error", err)
					continue
				}
				v.logger.Info(fmt.Sprintf("Received stream: %s", in))
			}
		}
	}()
	return nil
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
