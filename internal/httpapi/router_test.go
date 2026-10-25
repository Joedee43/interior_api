package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// fakePinger lets tests choose the database's health without PostgreSQL.
type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func newTestRouter(db Pinger) *gin.Engine {
	return NewRouter(Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:     db,
	})
}

func do(r http.Handler, method, path string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v) // Add canonicalizes the key; direct map assignment would not
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeError(t *testing.T, w *httptest.ResponseRecorder) ErrorBody {
	t.Helper()
	var resp ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body is not the standard error JSON: %v (%s)", err, w.Body)
	}
	return resp.Error
}

func TestHealthEndpoints(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		dbErr      error
		wantStatus int
		wantCode   string // error code, empty for success
	}{
		{"live ok", "/health", nil, http.StatusOK, ""},
		{"live ignores db outage", "/health", errors.New("down"), http.StatusOK, ""},
		{"database ok", "/health/database", nil, http.StatusOK, ""},
		{"database down", "/health/database", errors.New("dial tcp 10.0.0.5:5433: secret detail"), http.StatusServiceUnavailable, CodeUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := do(newTestRouter(fakePinger{err: tt.dbErr}), http.MethodGet, tt.path, nil)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tt.wantStatus, w.Body)
			}
			if tt.wantCode == "" {
				if !strings.Contains(w.Body.String(), `"status":"ok"`) {
					t.Errorf("body = %s, want status ok", w.Body)
				}
				return
			}
			body := decodeError(t, w)
			if body.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", body.Code, tt.wantCode)
			}
			if strings.Contains(w.Body.String(), "secret detail") {
				t.Errorf("internal error leaked to client: %s", w.Body)
			}
		})
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	r := newTestRouter(fakePinger{})

	w := do(r, http.MethodGet, "/nope", nil)
	if w.Code != http.StatusNotFound || decodeError(t, w).Code != CodeNotFound {
		t.Errorf("GET /nope = %d %s", w.Code, w.Body)
	}

	w = do(r, http.MethodPost, "/health", nil)
	if w.Code != http.StatusMethodNotAllowed || decodeError(t, w).Code != CodeMethodNotAllowed {
		t.Errorf("POST /health = %d %s", w.Code, w.Body)
	}
}

func TestRequestID(t *testing.T) {
	r := newTestRouter(fakePinger{})

	t.Run("generated when absent", func(t *testing.T) {
		w := do(r, http.MethodGet, "/nope", nil)
		id := w.Header().Get(headerRequestID)
		if len(id) != 32 {
			t.Fatalf("generated id = %q, want 32 hex chars", id)
		}
		if decodeError(t, w).RequestID != id {
			t.Errorf("error body request_id does not match header")
		}
	})

	t.Run("valid incoming id is reused", func(t *testing.T) {
		w := do(r, http.MethodGet, "/health", http.Header{headerRequestID: {"abc-123"}})
		if got := w.Header().Get(headerRequestID); got != "abc-123" {
			t.Errorf("id = %q, want abc-123", got)
		}
	})

	t.Run("unsafe incoming id is replaced", func(t *testing.T) {
		w := do(r, http.MethodGet, "/health", http.Header{headerRequestID: {"bad id\nforged-log-line"}})
		// Assert it was replaced by a fresh generated id. (Checking that it
		// does not contain "bad" was flaky: b, a and d are hex digits, so a
		// random id can contain "bad" by chance.)
		if got := w.Header().Get(headerRequestID); len(got) != 32 || strings.ContainsAny(got, " \n") || strings.Contains(got, "forged") {
			t.Errorf("unsafe id was not replaced with a generated one: %q", got)
		}
	})
}

func TestSecurityHeaders(t *testing.T) {
	w := do(newTestRouter(fakePinger{}), http.MethodGet, "/health", nil)
	for h, want := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	} {
		if got := w.Header().Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	if w.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS sent without HTTPS")
	}

	https := NewRouter(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: fakePinger{}, HTTPS: true})
	if do(https, http.MethodGet, "/health", nil).Header().Get("Strict-Transport-Security") == "" {
		t.Error("HSTS missing with HTTPS")
	}
	// Error responses carry the headers too.
	if do(https, http.MethodGet, "/nope", nil).Header().Get("X-Frame-Options") != "DENY" {
		t.Error("404 response lacks security headers")
	}
}

func TestRecoveryReturnsJSON500(t *testing.T) {
	r := newTestRouter(fakePinger{})
	r.GET("/boom", func(*gin.Context) { panic("kaboom") })

	w := do(r, http.MethodGet, "/boom", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	body := decodeError(t, w)
	if body.Code != CodeInternal || strings.Contains(w.Body.String(), "kaboom") {
		t.Errorf("unexpected body: %s", w.Body)
	}
}
