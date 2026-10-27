package control

import (
	"context"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// recoveryUnaryInterceptor turns a handler panic into codes.Internal instead
// of letting it kill the process. grpc-go does not recover handler panics.
// It must be the outermost interceptor so it also covers the ones after it.
//
// Only the handler goroutine is covered: a panic in a goroutine the handler
// spawns still crashes the process.
func (s *Server) recoveryUnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logPanic(info.FullMethod, r)
			resp = nil
			err = status.Error(codes.Internal, "internal server error")
		}
	}()
	return handler(ctx, req)
}

// recoveryStreamInterceptor is the streaming counterpart of
// recoveryUnaryInterceptor.
func (s *Server) recoveryStreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logPanic(info.FullMethod, r)
			err = status.Error(codes.Internal, "internal server error")
		}
	}()
	return handler(srv, ss)
}

// logPanic records the panic value and stack server-side; neither reaches the
// client.
func (s *Server) logPanic(method string, r interface{}) {
	s.logger.Error("gRPC handler panic", "method", method, "panic", r, "stack", string(debug.Stack()))
}
