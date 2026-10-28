package topology

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// checkHealth does one grpc_health_v1 Check("") against addr over TLS,
// trusting only the PEM certificate in certFile (ServerName "localhost").
// It returns nil iff the status is SERVING.
func checkHealth(ctx context.Context, addr, certFile string) error {
	pemBytes, err := os.ReadFile(certFile)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return fmt.Errorf("no certificate in %s", certFile)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(
		credentials.NewTLS(&tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12})))
	if err != nil {
		return err
	}
	defer conn.Close()
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	resp, err := grpc_health_v1.NewHealthClient(conn).Check(cctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		return err
	}
	if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		return fmt.Errorf("health status %s", resp.GetStatus())
	}
	return nil
}
