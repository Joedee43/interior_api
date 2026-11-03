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
	// TrustedProxies are the Next.js server IPs/CIDRs allowed to supply
	// forwarding headers. Keep nil/empty when the API is reached directly.
	TrustedProxies []string
	// EnableCORS permits credentialed browser calls from AllowedOrigins.
	// The normal same-origin Next.js rewrite leaves this disabled.
	EnableCORS bool
	// HTTPS enables headers that only make sense over TLS (HSTS).
	HTTPS bool
	// Extra mounts routes outside /v1 (e.g. GET /openapi.yaml).
	Extra []func(r *gin.Engine)
	// V1Routes lets each domain package mount its own routes on /v1
	// without httpapi importing it (which would create an import cycle,
	// since domain handlers import httpapi for WriteError).
	V1Routes []func(v1 *gin.RouterGroup)
}

// NewRouter builds the Gin engine. It uses gin.New() rather than
// gin.Default() so the logger and panic recovery are our own.
func NewRouter(d Deps) *gin.Engine {
	r := gin.New()

	// Gin ignores forwarding headers from every peer except these explicit
	// addresses/CIDRs. Config validates them before they reach the router.
	if len(d.TrustedProxies) == 0 {
		_ = r.SetTrustedProxies(nil)
	} else if err := r.SetTrustedProxies(d.TrustedProxies); err != nil {
		panic("httpapi: invalid pre-validated trusted proxy: " + err.Error())
	}
	r.HandleMethodNotAllowed = true
	r.NoRoute(notFound)
	r.NoMethod(methodNotAllowed)

	// Order matters: request ID first so logging and recovery can use it.
	// Body size limits are per use: DecodeJSON caps JSON at MaxBodyBytes,
	// uploads set their own (larger) cap.
	r.Use(requestID(), requestLogger(d.Logger), recovery(d.Logger), securityHeaders(d.HTTPS))
	if d.EnableCORS {
		r.Use(cors(d.AllowedOrigins))
	}

	health := &healthHandler{db: d.DB, log: d.Logger, timeout: 2 * time.Second}
	// Operational endpoints stay unversioned; they are not part of the
	// product API contract.
	r.GET("/health", health.live)
	r.GET("/health/database", health.database)

	for _, mount := range d.Extra {
		mount(r)
	}

	v1 := r.Group("/v1", checkOrigin(d.AllowedOrigins))
	for _, mount := range d.V1Routes {
		mount(v1)
	}

	return r
}
