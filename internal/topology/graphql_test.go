package topology

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostGraphQL(t *testing.T) {
	var gotPath, gotKey string
	var gotBody map[string]any
	status := http.StatusOK
	reply := `{"data":{"x":1}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-API-Key")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	var out struct{ X int }
	if err := postGraphQL(context.Background(), addr, "k-1", "query{x}", map[string]any{"a": 1}, &out); err != nil {
		t.Fatalf("ok case: %v", err)
	}
	if gotPath != "/api/v1/graphql" {
		t.Errorf("path = %q", gotPath)
	}
	if gotKey != "k-1" {
		t.Errorf("X-API-Key = %q", gotKey)
	}
	if gotBody["query"] != "query{x}" {
		t.Errorf("query = %v", gotBody["query"])
	}
	if v, _ := gotBody["variables"].(map[string]any); v["a"] != float64(1) {
		t.Errorf("variables = %v", gotBody["variables"])
	}
	if out.X != 1 {
		t.Errorf("out = %+v", out)
	}

	status, reply = http.StatusUnauthorized, `{"error":"Unauthorized"}`
	if err := postGraphQL(context.Background(), addr, "bad", "query{x}", nil, nil); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("401 case: %v", err)
	}

	status, reply = http.StatusOK, `{"errors":[{"message":"x"}]}`
	if err := postGraphQL(context.Background(), addr, "k-1", "query{x}", nil, nil); err == nil || !strings.Contains(err.Error(), "x") {
		t.Errorf("errors case: %v", err)
	}
}
