package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"
)

// MaxBodyBytes caps JSON request bodies. File uploads set their own,
// larger limit with LimitBody on their routes.
const MaxBodyBytes = 1 << 20 // 1 MiB

// LimitBody caps the request body at limit bytes; reading past it fails
// with *http.MaxBytesError.
func LimitBody(limit int64) gin.HandlerFunc { return limitBody(limit) }

func limitBody(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidUUID reports whether s is a canonical UUID. Check path IDs with it
// before querying: PostgreSQL rejects a malformed uuid with an error,
// which would otherwise surface as a 500 instead of a 404.
func ValidUUID(s string) bool { return uuidPattern.MatchString(s) }

// DecodeJSON strictly decodes the request body into dst and, on failure,
// writes the error response itself and returns false:
//
//	var req loginRequest
//	if !httpapi.DecodeJSON(c, &req) {
//		return
//	}
//
// Unknown fields are rejected so a typo like "passwrod" fails loudly
// instead of being silently ignored.
func DecodeJSON(c *gin.Context, dst any) bool {
	// The JSON size cap lives here rather than in a global middleware, so
	// upload routes can accept larger bodies with their own limit while
	// every JSON endpoint is still capped (they all decode through here).
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxBodyBytes)
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()

	err := dec.Decode(dst)
	if err == nil {
		// Reject trailing data such as `{"a":1}{"b":2}`.
		if dec.Decode(&struct{}{}) != io.EOF {
			err = errors.New("body must contain a single JSON object")
		}
	}
	if err == nil {
		return true
	}

	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		WriteError(c, http.StatusRequestEntityTooLarge, CodeTooLarge, "request body too large")
	case errors.Is(err, io.EOF):
		WriteError(c, http.StatusBadRequest, CodeBadRequest, "request body is empty")
	default:
		// json errors describe the problem (field name, wrong type) without
		// exposing anything internal, so they are safe to return.
		WriteError(c, http.StatusBadRequest, CodeBadRequest, "invalid JSON: "+err.Error())
	}
	return false
}
