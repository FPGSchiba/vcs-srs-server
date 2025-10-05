package srs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/state"
	pb "github.com/FPGSchiba/vcs-srs-server/vcsauthpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

type PluginClient struct {
	client pb.AuthPluginServiceClient
	// GetServerState returns the current state of the voice control server.
	conn             *grpc.ClientConn
	logger           *slog.Logger
	settingsState    *state.SettingsState
	connectionFailed bool
	address          string
	pluginName       string
	stopc            chan struct{}
	configuredFlows  []string
	config           *state.FlowConfiguration
}

func NewPluginClient(logger *slog.Logger, settingsState *state.SettingsState, name, address string, configuration *state.FlowConfiguration) *PluginClient {
	return &PluginClient{
		logger:        logger,
		settingsState: settingsState,
		pluginName:    name,
		address:       address,
		config:        configuration,
	}
}

func (v *PluginClient) ConnectPlugin() error {
	v.logger.Info("Connecting to plugin", "plugin-name", v.pluginName, "address", v.address)

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
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

	conn, err := grpc.NewClient(v.address, opts...)
	if err != nil || conn == nil {
		v.logger.Error("Failed to connect to Plugin", "plugin-name", v.pluginName, "error", err)
		_ = v.settingsState.SetPluginEnabled(v.pluginName, false)
		return err
	}
	v.conn = conn
	client := pb.NewAuthPluginServiceClient(v.conn)
	v.client = client
	if err := v.establishConnection(); err != nil {
		v.logger.Error("Failed to establish connection to Plugin", "plugin-name", v.pluginName, "error", err)
		v.Close()
		return err
	}
	return nil
}

func (v *PluginClient) establishConnection() error {
	go func() {
		lastState := v.conn.GetState()
		for {
			if !v.conn.WaitForStateChange(context.Background(), lastState) {
				return
			}
			newState := v.conn.GetState()
			if newState == connectivity.Idle {
				v.logger.Warn(fmt.Sprintf("Plugin: '%s' connection idle...", v.pluginName))
				err := v.settingsState.SetPluginEnabled(v.pluginName, false)
				if err != nil {
					return
				}
				go v.handleReconnection()
			}
			lastState = newState
		}
	}()

	err := v.configurePlugin()
	if err != nil {
		return err
	}
	configurableFlows, err := v.discoverPluginFlows()
	if err != nil {
		return err
	}
	return v.configureFlows(configurableFlows)
}

func (v *PluginClient) handleReconnection() {
	currentBackoff := 1
	reconnectionAttempts := 0
	maxBackoff := 128
	maxReconnectionAttempts := 20
	v.logger.Warn("Attempting to reconnect to Plugin Server")

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

func (v *PluginClient) configurePlugin() error {
	v.logger.Info("Configuring plugin", "plugin-name", v.pluginName)
	if v.client == nil {
		return fmt.Errorf("client is not initialized")
	}

	var globalSettings map[string]string
	if v.config != nil && v.config.GlobalSettings != nil {
		globalSettings = *v.config.GlobalSettings
	} else {
		globalSettings = nil // No global settings provided
	}

	config := &pb.ConfigureRequest{
		PluginName:     v.pluginName,
		GlobalSettings: globalSettings,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := v.client.Configure(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to configure plugin: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("plugin configuration failed: %s", resp.Message)
	}

	v.logger.Info("Plugin configured successfully", "name", v.pluginName)
	err = v.settingsState.SetPluginEnabled(v.pluginName, true)
	if err != nil {
		return err
	}
	return nil
}

func (v *PluginClient) discoverPluginFlows() ([]string, error) {
	if v.client == nil {
		return nil, fmt.Errorf("client is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	request := &pb.FlowDiscoveryRequest{}
	resp, err := v.client.GetSupportedFlows(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to discover flows: %w", err)
	}
	v.logger.Info("Discovered plugin flows", "flows", v.configuredFlows)
	configurableFlows := make([]string, 0, len(resp.Flows))
	for _, flow := range resp.Flows {
		configurableFlows = append(configurableFlows, flow.FlowId)
	}

	return configurableFlows, nil
}

func (v *PluginClient) DiscoverPluginFlows() (*pb.FlowDiscoveryResponse, error) {
	if v.client == nil {
		return nil, fmt.Errorf("client is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	request := &pb.FlowDiscoveryRequest{}
	resp, err := v.client.GetSupportedFlows(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to discover flows: %w", err)
	}
	return resp, nil
}

func (v *PluginClient) configureFlows(configurableFlows []string) error {
	flowSet := make(map[string]struct{}, len(configurableFlows))
	for _, f := range configurableFlows {
		flowSet[f] = struct{}{}
	}
	if v.client == nil {
		return fmt.Errorf("client is not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, flow := range v.config.Flows {
		if _, ok := flowSet[flow.FlowID]; ok {
			v.logger.Info("Configuring flow", "flow-id", flow.FlowID)

			request := &pb.ConfigureFlowRequest{
				FlowId:   flow.FlowID,
				Settings: flow.Configuration,
			}
			resp, err := v.client.ConfigureFlow(ctx, request)
			if err != nil {
				return fmt.Errorf("failed to configure flow %s: %w", flow.FlowID, err)
			}
			if !resp.Success {
				return fmt.Errorf("flow configuration failed for %s: %s", flow.FlowID, resp.Message)
			}
			v.logger.Info("Flow configured successfully", "flow-id", flow.FlowID)

			v.configuredFlows = append(v.configuredFlows, flow.FlowID)
		}
	}

	return nil
}

func (v *PluginClient) Close() error {
	if v.stopc != nil {
		close(v.stopc)
	}
	if v.conn != nil {
		return v.conn.Close()
	}
	return nil
}

func (v *PluginClient) StartAuth(flowID string, firstStepInput map[string]string) (*pb.AuthStepResponse, error) {
	if v.client == nil {
		return nil, fmt.Errorf("client is not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	request := &pb.StartAuthRequest{
		FlowId:         flowID,
		FirstStepInput: firstStepInput,
	}
	resp, err := v.client.StartAuth(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to start auth: %w", err)
	}
	return resp, nil
}

func (v *PluginClient) ContinueAuth(sessionID string, stepInput map[string]string) (*pb.AuthStepResponse, error) {
	if v.client == nil {
		return nil, fmt.Errorf("client is not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	request := &pb.ContinueAuthRequest{
		SessionId: sessionID,
		StepData:  stepInput,
	}
	resp, err := v.client.ContinueAuth(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to continue auth: %w", err)
	}
	return resp, nil
}
