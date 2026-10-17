package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Error codes are part of the public API contract: clients switch on
// `code`, never on `message`. Add codes here as domain errors appear.
const (
	CodeBadRequest       = "bad_request"
	CodeValidation       = "validation_failed"
	CodeUnauthenticated  = "unauthenticated"
	CodeForbiddenOrigin  = "forbidden_origin"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeTooLarge         = "payload_too_large"
	CodeRateLimited      = "rate_limited"
	CodeInternal         = "internal_error"
	CodeUnavailable      = "service_unavailable"
)

// ErrorResponse is the single JSON shape for every non-2xx response:
//
//	{"error": {"code": "not_found", "message": "...", "request_id": "..."}}
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details maps a request field to what is wrong with it; only set
	// for validation_failed.
	Details   map[string]string `json:"details,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

// WriteError writes the standard error body and aborts the handler chain.
// message must be safe to show a client: never pass err.Error() from a
// database or library here — log it instead.
func WriteError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, ErrorResponse{Error: ErrorBody{
		Code:      code,
		Message:   message,
		RequestID: RequestID(c),
	}})
}

// WriteValidationError writes a 400 listing every invalid field.
func WriteValidationError(c *gin.Context, details map[string]string) {
	c.AbortWithStatusJSON(http.StatusBadRequest, ErrorResponse{Error: ErrorBody{
		Code:      CodeValidation,
		Message:   "request validation failed",
		Details:   details,
		RequestID: RequestID(c),
	}})
}

// WriteInternalError records err for the request log (see requestLogger)
// and sends the client a generic 500 that reveals nothing about it.
func WriteInternalError(c *gin.Context, err error) {
	_ = c.Error(err)
	WriteError(c, http.StatusInternalServerError, CodeInternal, "internal server error")
}

func notFound(c *gin.Context) {
	WriteError(c, http.StatusNotFound, CodeNotFound, "route not found")
}

func methodNotAllowed(c *gin.Context) {
	WriteError(c, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
}
