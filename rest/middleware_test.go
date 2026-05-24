package rest_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FPGSchiba/vcs-srs-server/rest"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/gin-gonic/gin"
)

func newTestSettings(key string) *state.SettingsState {
	s := &state.SettingsState{}
	s.Api.Key = key
	return s
}

func TestApiKeyMiddleware_EmptyKey_Returns503(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(rest.ApiKeyMiddleware(newTestSettings("")))
	r.GET("/test", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestApiKeyMiddleware_WrongKey_Returns401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(rest.ApiKeyMiddleware(newTestSettings("correct-key")))
	r.GET("/test", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestApiKeyMiddleware_CorrectKey_PassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(rest.ApiKeyMiddleware(newTestSettings("correct-key")))
	r.GET("/test", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-API-Key", "correct-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
