package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func RegisterRoutes(r *gin.Engine, pool *pgxpool.Pool, postingHandler *PostingHandler) {
	hc := r.Group("/health")
	{
		hc.GET("/live", live)
		hc.GET("/ready", ready(pool))
	}

	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	v1 := r.Group("/ledger")
	v1.POST("/postings", postingHandler.PostPosting)

	admin := r.Group("/admin")
	admin.GET("/ledger/transactions/:id", postingHandler.GetTransaction)
}
