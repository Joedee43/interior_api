package httpapi

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// securityHeaders sets defensive headers on every response. The API
// returns JSON (and media files, whose handler tightens CSP further), so
// nothing it serves should ever be framed, sniffed or used as a page.
//
// No Cross-Origin-Resource-Policy: the web frontend on another origin
// must be able to load product images and models with <img>/fetch.
func securityHeaders(https bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if https {
			// Only meaningful (and only safe to send) over HTTPS.
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// cors enables the optional separate-origin deployment. The usual Next.js
// rewrite is same-origin and does not need these headers. Because sessions
// use cookies, allowed origins are echoed exactly; wildcard origins are never
// valid together with Access-Control-Allow-Credentials.
func cors(allowed []string) gin.HandlerFunc {
	set := make(map[string]struct{}, len(allowed))
	for _, origin := range allowed {
		set[origin] = struct{}{}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		c.Header("Vary", "Origin")
		if _, ok := set[origin]; !ok {
			if c.Request.Method == http.MethodOptions {
				WriteError(c, http.StatusForbidden, CodeForbiddenOrigin, "request origin not allowed")
				return
			}
			c.Next()
			return
		}

		h := c.Writer.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		h.Set("Access-Control-Expose-Headers", "X-Request-ID, ETag, Accept-Ranges, Content-Range")
		if c.Request.Method == http.MethodOptions {
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			h.Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")
			h.Set("Access-Control-Max-Age", "600")
			c.Status(http.StatusNoContent)
			c.Abort()
			return
		}
		c.Next()
	}
}

// checkOrigin is the CSRF defence for cookie-authenticated requests.
//
// Browsers attach an Origin header to every cross-origin POST/PATCH/DELETE,
// and a malicious page cannot forge it. So for state-changing methods:
//   - Origin present and allowed  -> continue
//   - Origin present, not allowed -> 403 (a foreign site is calling us)
//   - Origin absent               -> continue (curl, Postman, server-to-server;
//     these do not carry a victim's browser cookies)
//
// Combined with SameSite=Lax session cookies this follows OWASP's
// "verify origin with standard headers" recommendation.
func checkOrigin(allowed []string) gin.HandlerFunc {
	set := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		set[o] = struct{}{}
	}
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		if _, ok := set[origin]; !ok {
			WriteError(c, http.StatusForbidden, CodeForbiddenOrigin, "request origin not allowed")
			return
		}
		c.Next()
	}
}

// KeyedLimiter is an in-memory token-bucket rate limiter per key (an IP,
// an email, ...). It is safe for concurrent use.
//
// Limitation: state lives in this process only. Running several API
// instances would need a shared store (e.g. PostgreSQL or Redis).
type KeyedLimiter struct {
	every time.Duration // one token is refilled per `every`
	burst int

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// NewKeyedLimiter allows `burst` requests at once per key, refilling one
// every `every`. Example: NewKeyedLimiter(time.Minute/5, 5) is roughly
// "5 per minute".
func NewKeyedLimiter(every time.Duration, burst int) *KeyedLimiter {
	return &KeyedLimiter{every: every, burst: burst, buckets: make(map[string]*bucket)}
}

// Allow reports whether a request for key may proceed now.
func (l *KeyedLimiter) Allow(key string) bool {
	now := time.Now()

	// The mutex guards the map; rate.Limiter itself is already
	// goroutine-safe, but map writes in Go are not.
	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweep(now)
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{limiter: rate.NewLimiter(rate.Every(l.every), l.burst)}
		l.buckets[key] = b
	}
	b.lastUsed = now
	return b.limiter.AllowN(now, 1)
}

// sweep drops buckets idle long enough to have fully refilled, so memory
// does not grow forever. It runs at most once a minute, inline, which
// avoids a background goroutine that would need its own shutdown.
func (l *KeyedLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	idle := l.every * time.Duration(l.burst)
	for k, b := range l.buckets {
		if now.Sub(b.lastUsed) > idle {
			delete(l.buckets, k) // deleting during range is allowed in Go
		}
	}
}

// RateLimitByIP rejects requests from a client IP that exceeds l.
func RateLimitByIP(l *KeyedLimiter, scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !l.Allow(scope + ":" + c.ClientIP()) {
			WriteRateLimited(c)
			return
		}
		c.Next()
	}
}

func WriteRateLimited(c *gin.Context) {
	WriteError(c, http.StatusTooManyRequests, CodeRateLimited, "too many requests, try again later")
}
