package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestCheckOrigin(t *testing.T) {
	r := gin.New()
	r.Use(checkOrigin([]string{"http://localhost:5173"}))
	ok := func(c *gin.Context) { c.Status(http.StatusNoContent) }
	r.POST("/x", ok)
	r.GET("/x", ok)

	tests := []struct {
		name   string
		method string
		origin string
		want   int
	}{
		{"no origin (curl)", http.MethodPost, "", http.StatusNoContent},
		{"allowed origin", http.MethodPost, "http://localhost:5173", http.StatusNoContent},
		{"foreign origin", http.MethodPost, "https://evil.example", http.StatusForbidden},
		{"null origin", http.MethodPost, "null", http.StatusForbidden},
		{"lookalike port", http.MethodPost, "http://localhost:5174", http.StatusForbidden},
		{"GET is never blocked", http.MethodGet, "https://evil.example", http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.origin != "" {
				h.Set("Origin", tt.origin)
			}
			w := do(r, tt.method, "/x", h)
			if w.Code != tt.want {
				t.Errorf("status = %d, want %d", w.Code, tt.want)
			}
			if tt.want == http.StatusForbidden && !strings.Contains(w.Body.String(), CodeForbiddenOrigin) {
				t.Errorf("body = %s, want code %s", w.Body, CodeForbiddenOrigin)
			}
		})
	}
}

func TestKeyedLimiter(t *testing.T) {
	l := NewKeyedLimiter(time.Hour, 3) // 3 allowed, then effectively no refill during the test

	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("request %d for key a denied within burst", i+1)
		}
	}
	if l.Allow("a") {
		t.Error("4th request for key a allowed, want denied")
	}
	if !l.Allow("b") {
		t.Error("key b denied; keys must be limited independently")
	}
}
