package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Pinger is the only thing the health check needs from the database.
// *pgxpool.Pool satisfies it implicitly; tests pass a fake. The interface
// is declared here, where it is consumed, not in the postgres package.
type Pinger interface {
	Ping(ctx context.Context) error
}

type healthHandler struct {
	db      Pinger
	log     *slog.Logger
	timeout time.Duration
}

type healthResponse struct {
	Status string `json:"status"`
}

// live reports that the process is up and serving HTTP. It deliberately
// does not touch the database, so a DB outage does not get the process
// restarted by an orchestrator.
func (h *healthHandler) live(c *gin.Context) {
	c.JSON(http.StatusOK, healthResponse{Status: "ok"})
}

// database reports whether PostgreSQL is reachable right now.
func (h *healthHandler) database(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), h.timeout)
	defer cancel()

	if err := h.db.Ping(ctx); err != nil {
		// Full detail goes to the log; the client gets a generic message.
		h.log.ErrorContext(ctx, "database health check failed",
			slog.String("request_id", RequestID(c)),
			slog.Any("error", err),
		)
		WriteError(c, http.StatusServiceUnavailable, CodeUnavailable, "database unavailable")
		return
	}
	c.JSON(http.StatusOK, healthResponse{Status: "ok"})
}
