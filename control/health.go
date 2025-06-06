package control

import (
	"context"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"sync"
)

type FullHealthServer struct {
	healthpb.UnimplementedHealthServer
	statusMap map[string]healthpb.HealthCheckResponse_ServingStatus
	mu        sync.RWMutex
}

func NewFullHealthServer() *FullHealthServer {
	return &FullHealthServer{
		statusMap: map[string]healthpb.HealthCheckResponse_ServingStatus{
			"": healthpb.HealthCheckResponse_SERVING,
		},
	}
}

func (s *FullHealthServer) Check(ctx context.Context, req *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status, ok := s.statusMap[req.Service]
	if !ok {
		status = healthpb.HealthCheckResponse_SERVICE_UNKNOWN
	}
	return &healthpb.HealthCheckResponse{Status: status}, nil
}

func (s *FullHealthServer) Watch(req *healthpb.HealthCheckRequest, stream healthpb.Health_WatchServer) error {
	resp, _ := s.Check(stream.Context(), req)
	return stream.Send(resp)
}

func (s *FullHealthServer) List(ctx context.Context, req *healthpb.HealthListRequest) (*healthpb.HealthListResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	services := make(map[string]*healthpb.HealthCheckResponse)
	for service, status := range s.statusMap {
		services[service] = &healthpb.HealthCheckResponse{Status: status}
	}
	return &healthpb.HealthListResponse{Statuses: services}, nil
}

func (s *FullHealthServer) SetServingStatus(service string, status healthpb.HealthCheckResponse_ServingStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status == healthpb.HealthCheckResponse_SERVING || status == healthpb.HealthCheckResponse_NOT_SERVING {
		s.statusMap[service] = status
	} else {
		delete(s.statusMap, service)
	}
}
