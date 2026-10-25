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

func TestDecodeJSONMessagesHideGoTypes(t *testing.T) {
	type payload struct {
		Price int64  `json:"price_minor"`
		Name  string `json:"name"`
	}
	r := gin.New()
	r.POST("/x", func(c *gin.Context) {
		var p payload
		DecodeJSON(c, &p)
	})
	for body, want := range map[string]string{
		`{"price_minor":12.5}`: `field "price_minor" must be a whole number`,
		`{"name":5}`:           `field "name" must be a string`,
		`{"name":"a",}`:        "invalid JSON at byte",
		`[1,2]`:                "expected an object",
		`{"nmae":"x"}`:         `unknown field "nmae"`,
		`{"name":"a"`:          "ends too early",
	} {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		msg := decodeError(t, w).Message // compare the decoded text, not escaped JSON
		if !strings.Contains(msg, want) || strings.Contains(msg, "Go struct") || strings.Contains(msg, "payload") || strings.Contains(msg, "int64") {
			t.Errorf("%s -> %s (want %q, no Go type names)", body, msg, want)
		}
	}
}
