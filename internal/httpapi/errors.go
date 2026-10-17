package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Error codes are part of the public API contract: clients switch on
// `code`, never on `message`. Add codes here as domain errors appear.
const (
	CodeBadRequest       = "bad_request"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
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
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
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

func notFound(c *gin.Context) {
	WriteError(c, http.StatusNotFound, CodeNotFound, "route not found")
}

func methodNotAllowed(c *gin.Context) {
	WriteError(c, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method not allowed")
}
