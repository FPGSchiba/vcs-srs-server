package topology

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

var gqlHTTPClient = &http.Client{Timeout: 10 * time.Second}

// requireLoopback returns an error unless hostport's host is a loopback IP
// literal. The control process serves its admin API as plaintext HTTP only,
// so the API key may never leave the machine.
func requireLoopback(hostport string) error {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return fmt.Errorf("graphql: refusing %q: not a loopback host:port: %w", hostport, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("graphql: refusing %q: host is not a loopback IP literal", hostport)
	}
	return nil
}

// postGraphQL POSTs one GraphQL request to http://<httpAddr>/api/v1/graphql
// with the X-API-Key header and decodes "data" into out (when non-nil). A
// non-200 status or a non-empty "errors" array is an error carrying the body.
// httpAddr must be a loopback IP literal (requireLoopback).
func postGraphQL(ctx context.Context, httpAddr, apiKey, query string, vars map[string]any, out any) error {
	if err := requireLoopback(httpAddr); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	// Plaintext is deliberate: the server's admin API has no TLS listener, and
	// requireLoopback above keeps the request on this machine.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+httpAddr+"/api/v1/graphql", bytes.NewReader(body)) // NOSONAR go:S5332 loopback-only, enforced by requireLoopback
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	resp, err := gqlHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("graphql: status %d: %s", resp.StatusCode, raw)
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("graphql: decode %s: %w", raw, err)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("graphql: errors: %s", raw)
	}
	if out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("graphql: decode data %s: %w", env.Data, err)
		}
	}
	return nil
}
