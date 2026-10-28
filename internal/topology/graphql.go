package topology

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

var gqlHTTPClient = &http.Client{Timeout: 10 * time.Second}

// postGraphQL POSTs one GraphQL request to http://<httpAddr>/api/v1/graphql
// with the X-API-Key header and decodes "data" into out (when non-nil). A
// non-200 status or a non-empty "errors" array is an error carrying the body.
func postGraphQL(ctx context.Context, httpAddr, apiKey, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+httpAddr+"/api/v1/graphql", bytes.NewReader(body))
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
