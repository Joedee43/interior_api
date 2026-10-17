package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	r := gin.New()
	r.Use(limitBody(64))
	r.POST("/x", func(c *gin.Context) {
		var p payload
		if !DecodeJSON(c, &p) {
			return
		}
		c.String(http.StatusOK, p.Name)
	})

	tests := []struct {
		name     string
		body     string
		want     int
		wantCode string
	}{
		{"valid", `{"name":"ok"}`, http.StatusOK, ""},
		{"empty", ``, http.StatusBadRequest, CodeBadRequest},
		{"malformed", `{"name":`, http.StatusBadRequest, CodeBadRequest},
		{"unknown field", `{"name":"a","nmae":"typo"}`, http.StatusBadRequest, CodeBadRequest},
		{"wrong type", `{"name":5}`, http.StatusBadRequest, CodeBadRequest},
		{"trailing data", `{"name":"a"}{"name":"b"}`, http.StatusBadRequest, CodeBadRequest},
		{"too large", `{"name":"` + strings.Repeat("x", 100) + `"}`, http.StatusRequestEntityTooLarge, CodeTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tt.want, w.Body)
			}
			if tt.wantCode != "" && decodeError(t, w).Code != tt.wantCode {
				t.Errorf("code = %q, want %q", decodeError(t, w).Code, tt.wantCode)
			}
		})
	}
}
