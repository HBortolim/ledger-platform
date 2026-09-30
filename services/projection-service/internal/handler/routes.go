package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func RegisterRoutes(r *gin.Engine, pool *pgxpool.Pool, rebuildHandler *RebuildHandler) {
	hc := r.Group("/health")
	{
		hc.GET("/live", live)
		hc.GET("/ready", ready(pool))
	}

	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	admin := r.Group("/admin/projections")
	{
		// Internal, unauthenticated by design (ADR-0016): the Wallet Service owns the
		// admin gate and the audit event, exactly as it owns auth for the balance read
		// this service also serves raw (SPEC.md §7.2, §5.2). Do not add auth here --
		// that would give this service a second, divergent auth mechanism.
		admin.POST("/:walletId/rebuild", rebuildHandler.Rebuild)
	}
}
