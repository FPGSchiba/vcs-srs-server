package rest

import (
	"github.com/FPGSchiba/vcs-srs-server/utils"
	"github.com/gin-gonic/gin"
	"log/slog"
)

func GetRouter(logger *slog.Logger) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(utils.LogMiddleware(logger))
	router.Use(utils.CORS(utils.CORSOptions{
		Origin: "*",
	}))

	apiGroup := router.Group("/api/v1")
	{
		apiGroup.GET("/", func(c *gin.Context) {
			c.JSON(200, gin.H{"version": "1.0.0", "status": "success", "message": "API is running"})
		})
	}

	return router
}
