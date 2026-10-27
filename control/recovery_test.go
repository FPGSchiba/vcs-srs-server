package control

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRecoveryUnaryPanics(t *testing.T) {
	s := newInterceptorTestServer(t)
	resp, err := s.recoveryUnaryInterceptor(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/srs.AuthService/InitAuth"},
		func(ctx context.Context, req interface{}) (interface{}, error) {
			panic("boom")
		})
	if resp != nil {
		t.Fatalf("resp = %v, want nil", resp)
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("code = %v, want Internal", st.Code())
	}
	if st.Message() != "internal server error" {
		t.Fatalf("message = %q", st.Message())
	}
	if strings.Contains(st.Message(), "boom") {
		t.Fatal("panic text must not reach the client")
	}
}

func TestRecoveryStreamPanics(t *testing.T) {
	s := newInterceptorTestServer(t)
	err := s.recoveryStreamInterceptor(nil, &fakeServerStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/srs.SRSService/SubscribeToUpdates"},
		func(srv interface{}, ss grpc.ServerStream) error {
			panic("boom")
		})
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("code = %v, want Internal", st.Code())
	}
	if st.Message() != "internal server error" {
		t.Fatalf("message = %q", st.Message())
	}
	if strings.Contains(st.Message(), "boom") {
		t.Fatal("panic text must not reach the client")
	}
}

func TestRecoveryPassesThrough(t *testing.T) {
	s := newInterceptorTestServer(t)
	info := &grpc.UnaryServerInfo{FullMethod: "/srs.SRSService/SyncClient"}

	resp, err := s.recoveryUnaryInterceptor(context.Background(), nil, info,
		func(ctx context.Context, req interface{}) (interface{}, error) { return "ok", nil })
	if err != nil || resp != "ok" {
		t.Fatalf("got (%v, %v), want (ok, nil)", resp, err)
	}

	want := status.Error(codes.NotFound, "x")
	resp, err = s.recoveryUnaryInterceptor(context.Background(), nil, info,
		func(ctx context.Context, req interface{}) (interface{}, error) { return nil, want })
	if resp != nil || err != want {
		t.Fatalf("got (%v, %v), want (nil, %v)", resp, err, want)
	}

	streamErr := s.recoveryStreamInterceptor(nil, &fakeServerStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/srs.SRSService/SubscribeToUpdates"},
		func(srv interface{}, ss grpc.ServerStream) error { return want })
	if streamErr != want {
		t.Fatalf("stream err = %v, want %v", streamErr, want)
	}
}
