package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func RegisterRoutes(r *gin.Engine, pool *pgxpool.Pool) {
	hc := r.Group("/health")
	{
		hc.GET("/live", live)
		hc.GET("/ready", ready(pool))
	}
	
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	admin := r.Group("/admin/projections")
	{
		admin.POST("/:walletId/rebuild", RebuildProjection)
	}
}

func RebuildProjection(c *gin.Context) {
	// TODO: admin-only rebuild from ledger entries
	c.JSON(http.StatusNotImplemented, gin.H{"message": "not yet implemented"})
}
