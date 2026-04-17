package rest

import (
	"net/http"

	"github.com/FPGSchiba/vcs-srs-server/state"
	"github.com/gin-gonic/gin"
)

// ApiKeyMiddleware returns a Gin middleware that validates the X-API-Key header.
// If the key is empty in settings, the endpoint returns 503 (not configured).
// If the key is wrong, returns 401. If correct, passes through.
func ApiKeyMiddleware(settings *state.SettingsState) gin.HandlerFunc {
	return func(c *gin.Context) {
		settings.RLock()
		key := settings.Api.Key
		settings.RUnlock()

		if key == "" {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "API disabled: no API key configured"})
			return
		}

		provided := c.GetHeader("X-API-Key")
		if provided != key {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}

		c.Next()
	}
}
