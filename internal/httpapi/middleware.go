package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	headerRequestID = "X-Request-ID"
	ctxRequestID    = "request_id"
	maxRequestIDLen = 64
)

// requestID reuses a well-formed incoming X-Request-ID (useful behind a
// proxy) or generates one, then exposes it on the response and the Gin
// context so logs and error bodies can be correlated.
func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(headerRequestID)
		if !validRequestID(id) {
			id = newRequestID()
		}
		c.Set(ctxRequestID, id)
		c.Header(headerRequestID, id)
		c.Next()
	}
}

// RequestID returns the current request's ID, or "" outside the middleware.
func RequestID(c *gin.Context) string {
	return c.GetString(ctxRequestID)
}

// validRequestID only accepts a short, conservative charset so a client
// cannot inject newlines or huge values into our logs.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error on supported platforms
	return hex.EncodeToString(b)
}

// requestLogger emits one structured log line per request after the
// handler chain has run (c.Next() blocks until downstream handlers return).
func requestLogger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		status := c.Writer.Status()
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}

		attrs := []slog.Attr{
			slog.String("request_id", RequestID(c)),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.String("route", c.FullPath()), // pattern like /v1/products/:id; "" if unmatched
			slog.Int("status", status),
			slog.Duration("latency", time.Since(start)),
			slog.String("client_ip", c.ClientIP()),
			slog.Int("bytes", max(c.Writer.Size(), 0)), // Gin reports -1 when no body was written
		}
		for _, e := range c.Errors {
			attrs = append(attrs, slog.String("error", e.Error()))
		}
		log.LogAttrs(c.Request.Context(), level, "http request", attrs...)
	}
}

// recovery turns a panic in any handler into a logged 500 with the
// standard error body, instead of Gin's default plain-text response.
// Panics are bugs; normal failures must be returned as errors.
func recovery(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// net/http uses this sentinel to abort a response on purpose.
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(rec)
			}
			log.ErrorContext(c.Request.Context(), "panic recovered",
				slog.String("request_id", RequestID(c)),
				slog.Any("panic", rec),
				slog.String("stack", string(debug.Stack())),
			)
			WriteError(c, http.StatusInternalServerError, CodeInternal, "internal server error")
		}()
		c.Next()
	}
}
