package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
)

func RegisterRoutes(r *gin.Engine) {
	r.GET("/health/live", live)
	r.GET("/health/ready", ready)
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	v1 := r.Group("/ledger")
	v1.POST("/postings", PostPosting)

	admin := r.Group("/admin")
	admin.GET("/ledger/transactions/:id", GetTransaction)
}

func live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "UP"})
}

func ready(c *gin.Context) {
	// TODO: check DB connectivity
	c.JSON(http.StatusOK, gin.H{"status": "UP"})
}
