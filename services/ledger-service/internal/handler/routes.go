package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

func RegisterRoutes(r *gin.Engine, pool *pgxpool.Pool, postingHandler *PostingHandler) {
	hc := r.Group("/health")
	{
		hc.GET("/live", live)
		hc.GET("/ready", ready(pool))
	}

	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	traced := r.Group("", otelgin.Middleware("ledger-service"))

	v1 := traced.Group("/ledger")
	{
		v1.POST("/postings", postingHandler.PostPosting)
	}

	admin := traced.Group("/admin")
	{
		admin.GET("/ledger/transactions/:id", postingHandler.GetTransaction)
	}
}
