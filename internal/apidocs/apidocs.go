// Package apidocs serves the hand-written OpenAPI 3.1 description of the
// API at GET /openapi.yaml. A test (drift_test.go) fails whenever a route
// exists in the router but not in the spec, or the other way round, so the
// document cannot silently fall behind the code.
package apidocs

import (
	_ "embed" // needed for the //go:embed directive below
	"net/http"

	"github.com/gin-gonic/gin"
)

// The file is compiled into the binary, so the server never reads the
// disk (and the spec always matches the deployed code).
//
//go:embed openapi.yaml
var spec []byte

// Spec returns the raw OpenAPI document.
func Spec() []byte { return spec }

// Mount registers GET /openapi.yaml (passed to httpapi.Deps.Extra).
func Mount(r *gin.Engine) {
	r.GET("/openapi.yaml", func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", spec)
	})
}
