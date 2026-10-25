package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"

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
		WriteError(c, http.StatusBadRequest, CodeBadRequest, jsonProblem(err))
	}
	return false
}

// jsonProblem turns encoding/json errors into client-facing messages. The
// raw messages name internal Go types ("Go struct field
// createRequest.price_minor of type int64"), which callers should not see.
func jsonProblem(err error) string {
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		return fmt.Sprintf("invalid JSON at byte %d", syntax.Offset)
	case errors.As(err, &typeErr):
		field := typeErr.Field
		if field == "" {
			return "the body has the wrong JSON type (expected an object)"
		}
		return fmt.Sprintf("field %q must be %s", field, jsonKind(typeErr.Type.Kind()))
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "invalid JSON: the body ends too early"
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		// e.g. json: unknown field "passwrod" — names only the client's own key.
		return "unknown field " + strings.TrimPrefix(err.Error(), "json: unknown field ")
	case strings.Contains(err.Error(), "single JSON object"):
		return err.Error()
	}
	return "invalid JSON body"
}

func jsonKind(k reflect.Kind) string {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.Map, reflect.Struct:
		return "an object"
	}
	return "a different type"
}
