package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func RegisterRoutes(r *gin.Engine) {
	r.GET("/health/live", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "UP"})
	})
	r.GET("/health/ready", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "UP"})
	})
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	admin := r.Group("/admin/projections")
	admin.POST("/:walletId/rebuild", RebuildProjection)
}

func RebuildProjection(c *gin.Context) {
	// TODO: admin-only rebuild from ledger entries
	c.JSON(http.StatusNotImplemented, gin.H{"message": "not yet implemented"})
}
