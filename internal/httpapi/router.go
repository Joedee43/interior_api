// Package httpapi is the HTTP boundary: routing, middleware, the JSON
// error format and request/response mapping. Business rules live in
// domain packages; handlers here only bind, call and serialize.
package httpapi

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// Deps lists everything the router needs. Each new domain adds its
// handler or service here, and cmd/api fills it in.
type Deps struct {
	Logger *slog.Logger
	DB     Pinger
	// AllowedOrigins are browser origins (scheme://host[:port]) allowed to
	// make state-changing requests; see checkOrigin.
	AllowedOrigins []string
	// V1Routes lets each domain package mount its own routes on /v1
	// without httpapi importing it (which would create an import cycle,
	// since domain handlers import httpapi for WriteError).
	V1Routes []func(v1 *gin.RouterGroup)
}

// NewRouter builds the Gin engine. It uses gin.New() rather than
// gin.Default() so the logger and panic recovery are our own.
func NewRouter(d Deps) *gin.Engine {
	r := gin.New()

	// No proxy is trusted until we deploy behind a known one; otherwise a
	// client could spoof X-Forwarded-For and fake its IP (matters for rate
	// limiting and audit logs later).
	_ = r.SetTrustedProxies(nil)
	r.HandleMethodNotAllowed = true
	r.NoRoute(notFound)
	r.NoMethod(methodNotAllowed)

	// Order matters: request ID first so logging and recovery can use it.
	// Body size limits are per use: DecodeJSON caps JSON at MaxBodyBytes,
	// uploads set their own (larger) cap.
	r.Use(requestID(), requestLogger(d.Logger), recovery(d.Logger))

	health := &healthHandler{db: d.DB, log: d.Logger, timeout: 2 * time.Second}
	// Operational endpoints stay unversioned; they are not part of the
	// product API contract.
	r.GET("/health", health.live)
	r.GET("/health/database", health.database)

	v1 := r.Group("/v1", checkOrigin(d.AllowedOrigins))
	for _, mount := range d.V1Routes {
		mount(v1)
	}

	return r
}
