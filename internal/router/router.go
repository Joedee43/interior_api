package router

import (
	"database/sql"

	"github.com/Joedee43/interior_api.git/internal/handler"
	"github.com/gin-gonic/gin"
)

func Setup(db *sql.DB) *gin.Engine {
	r := gin.Default()
	healthHandler := handler.NewHealthHandler(db)

	r.GET("/health", healthHandler.Health)
	r.GET("/health/database", healthHandler.Database)

	return r
}
