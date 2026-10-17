package auth_test

// Integration tests: real PostgreSQL (interior_test), real router, real
// middleware. They live in package auth_test (an external test package),
// so they can only use auth's exported API — exactly like main.go does.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/httpapi"
	"github.com/Joedee43/interior_api/internal/postgres/pgtest"
)

func init() { gin.SetMode(gin.TestMode) }

const testOrigin = "http://localhost:5173"

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	router http.Handler
}

func generousLimits() auth.Limits {
	return auth.Limits{
		LoginPerIP:    httpapi.NewKeyedLimiter(time.Millisecond, 1000),
		LoginPerEmail: httpapi.NewKeyedLimiter(time.Millisecond, 1000),
		RegisterPerIP: httpapi.NewKeyedLimiter(time.Millisecond, 1000),
	}
}

func newEnv(t *testing.T, limits auth.Limits) *env {
	t.Helper()
	pool := pgtest.New(t)
	hasher := auth.NewPasswordHasher(auth.Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32})
	svc, err := auth.NewService(pool, hasher, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := auth.NewHandler(svc, auth.CookieConfig{Secure: false}, limits)
	router := httpapi.NewRouter(httpapi.Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             pool,
		AllowedOrigins: []string{testOrigin},
		V1Routes:       []func(*gin.RouterGroup){h.Mount},
	})
	return &env{t: t, pool: pool, router: router}
}

type reqOpt func(*http.Request)

func withCookie(c *http.Cookie) reqOpt { return func(r *http.Request) { r.AddCookie(c) } }
func withOrigin(o string) reqOpt       { return func(r *http.Request) { r.Header.Set("Origin", o) } }

func (e *env) do(method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	for _, o := range opts {
		o(req)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in response (status %d, body %s)", auth.SessionCookieName, w.Code, w.Body)
	return nil
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp httpapi.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("not an error body: %s", w.Body)
	}
	return resp.Error.Code
}

type registration struct {
	slug, email, password string
}

func newRegistration() registration {
	u := pgtest.Unique()
	return registration{
		slug:     "acme-" + u,
		email:    "admin-" + u + "@example.com",
		password: "correct horse battery",
	}
}

func (r registration) body() string {
	return `{"company_name":"Acme Furniture","company_slug":"` + r.slug +
		`","full_name":"Abebe Kebede","email":"` + r.email + `","password":"` + r.password + `"}`
}

func (e *env) register(r registration) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/v1/auth/register-company", r.body())
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// ---- tests ------------------------------------------------------------

func TestRegisterMeLogoutFlow(t *testing.T) {
	e := newEnv(t, generousLimits())
	reg := newRegistration()

	w := e.register(reg)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	cookie := sessionCookie(t, w)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.MaxAge != 3600 {
		t.Errorf("cookie attributes: %+v", cookie)
	}

	var acct struct {
		User struct {
			ID, Email string
		} `json:"user"`
		Memberships []struct {
			Company struct{ Slug, Status string } `json:"company"`
			Role    string                        `json:"role"`
		} `json:"memberships"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &acct); err != nil {
		t.Fatal(err)
	}
	if acct.User.Email != reg.email || len(acct.Memberships) != 1 ||
		acct.Memberships[0].Role != "company_admin" ||
		acct.Memberships[0].Company.Slug != reg.slug ||
		acct.Memberships[0].Company.Status != "active" {
		t.Errorf("unexpected register response: %s", w.Body)
	}
	for _, secret := range []string{"password", "hash", cookie.Value} {
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("response body contains %q: %s", secret, w.Body)
		}
	}

	w = e.do(http.MethodGet, "/v1/auth/me", "", withCookie(cookie))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), reg.email) {
		t.Fatalf("me: %d %s", w.Code, w.Body)
	}

	w = e.do(http.MethodPost, "/v1/auth/logout", "", withCookie(cookie))
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", w.Code, w.Body)
	}
	if c := sessionCookie(t, w); c.MaxAge >= 0 {
		t.Errorf("logout did not clear cookie: %+v", c)
	}

	w = e.do(http.MethodGet, "/v1/auth/me", "", withCookie(cookie))
	if w.Code != http.StatusUnauthorized || errorCode(t, w) != httpapi.CodeUnauthenticated {
		t.Errorf("me after logout: %d %s", w.Code, w.Body)
	}

	// Logging out again is harmless.
	if w = e.do(http.MethodPost, "/v1/auth/logout", "", withCookie(cookie)); w.Code != http.StatusNoContent {
		t.Errorf("second logout: %d", w.Code)
	}
}

func TestRegister_EmailIsNormalized(t *testing.T) {
	e := newEnv(t, generousLimits())
	reg := newRegistration()
	reg.email = "  " + strings.ToUpper(reg.email) + " "

	w := e.register(reg)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	want := strings.ToLower(strings.TrimSpace(reg.email))
	if n := e.count(`SELECT count(*) FROM users WHERE email = $1`, want); n != 1 {
		t.Errorf("normalized email rows = %d, want 1", n)
	}
}

func TestRegister_DuplicateEmailRollsBack(t *testing.T) {
	e := newEnv(t, generousLimits())
	first := newRegistration()
	if w := e.register(first); w.Code != http.StatusCreated {
		t.Fatalf("first register: %d %s", w.Code, w.Body)
	}

	second := newRegistration()
	second.email = first.email
	w := e.register(second)
	if w.Code != http.StatusConflict || errorCode(t, w) != "email_taken" {
		t.Fatalf("duplicate email: %d %s", w.Code, w.Body)
	}
	if n := e.count(`SELECT count(*) FROM companies WHERE slug = $1`, second.slug); n != 0 {
		t.Errorf("company %s was created despite failed registration", second.slug)
	}
}

func TestRegister_DuplicateSlugRollsBack(t *testing.T) {
	e := newEnv(t, generousLimits())
	first := newRegistration()
	if w := e.register(first); w.Code != http.StatusCreated {
		t.Fatalf("first register: %d %s", w.Code, w.Body)
	}

	second := newRegistration()
	second.slug = first.slug
	w := e.register(second)
	if w.Code != http.StatusConflict || errorCode(t, w) != "slug_taken" {
		t.Fatalf("duplicate slug: %d %s", w.Code, w.Body)
	}
	// The user insert ran before the company insert failed; the
	// transaction must have rolled it back.
	if n := e.count(`SELECT count(*) FROM users WHERE email = $1`, second.email); n != 0 {
		t.Errorf("user %s was left behind by a failed registration", second.email)
	}
}

func TestRegister_Validation(t *testing.T) {
	e := newEnv(t, generousLimits())

	w := e.do(http.MethodPost, "/v1/auth/register-company",
		`{"company_name":"A","company_slug":"Bad Slug","full_name":"","email":"nope","password":"short"}`)
	if w.Code != http.StatusBadRequest || errorCode(t, w) != httpapi.CodeValidation {
		t.Fatalf("validation: %d %s", w.Code, w.Body)
	}
	for _, field := range []string{"company_name", "company_slug", "full_name", "email", "password"} {
		if !strings.Contains(w.Body.String(), `"`+field+`"`) {
			t.Errorf("details missing %s: %s", field, w.Body)
		}
	}

	w = e.do(http.MethodPost, "/v1/auth/register-company", `{"emial":"typo@example.com"}`)
	if w.Code != http.StatusBadRequest || errorCode(t, w) != httpapi.CodeBadRequest {
		t.Errorf("unknown field: %d %s", w.Code, w.Body)
	}
}

func TestLogin(t *testing.T) {
	e := newEnv(t, generousLimits())
	reg := newRegistration()
	w := e.register(reg)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	registerCookie := sessionCookie(t, w)

	loginBody := func(email, pw string) string {
		return `{"email":"` + email + `","password":"` + pw + `"}`
	}

	t.Run("wrong password and unknown email look identical", func(t *testing.T) {
		wrong := e.do(http.MethodPost, "/v1/auth/login", loginBody(reg.email, "not the right password"))
		unknown := e.do(http.MethodPost, "/v1/auth/login", loginBody("nobody-"+pgtest.Unique()+"@example.com", "whatever password"))
		for _, w := range []*httptest.ResponseRecorder{wrong, unknown} {
			if w.Code != http.StatusUnauthorized || errorCode(t, w) != "invalid_credentials" {
				t.Errorf("login failure: %d %s", w.Code, w.Body)
			}
		}
	})

	t.Run("success rotates the session", func(t *testing.T) {
		w := e.do(http.MethodPost, "/v1/auth/login", loginBody(strings.ToUpper(reg.email), reg.password), withCookie(registerCookie))
		if w.Code != http.StatusOK {
			t.Fatalf("login: %d %s", w.Code, w.Body)
		}
		loginCookie := sessionCookie(t, w)
		if loginCookie.Value == registerCookie.Value {
			t.Fatal("login reused the previous session token")
		}
		if w := e.do(http.MethodGet, "/v1/auth/me", "", withCookie(registerCookie)); w.Code != http.StatusUnauthorized {
			t.Errorf("previous session still valid after login: %d", w.Code)
		}
		if w := e.do(http.MethodGet, "/v1/auth/me", "", withCookie(loginCookie)); w.Code != http.StatusOK {
			t.Errorf("new session rejected: %d %s", w.Code, w.Body)
		}
	})
}

func TestDisabledUser(t *testing.T) {
	e := newEnv(t, generousLimits())
	reg := newRegistration()
	w := e.register(reg)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	cookie := sessionCookie(t, w)

	if _, err := e.pool.Exec(context.Background(), `UPDATE users SET status = 'disabled' WHERE email = $1`, reg.email); err != nil {
		t.Fatal(err)
	}

	if w := e.do(http.MethodGet, "/v1/auth/me", "", withCookie(cookie)); w.Code != http.StatusUnauthorized {
		t.Errorf("disabled user's session still works: %d", w.Code)
	}
	w = e.do(http.MethodPost, "/v1/auth/login", `{"email":"`+reg.email+`","password":"`+reg.password+`"}`)
	if w.Code != http.StatusUnauthorized || errorCode(t, w) != "invalid_credentials" {
		t.Errorf("disabled user login: %d %s", w.Code, w.Body)
	}
}

func TestExpiredSession(t *testing.T) {
	e := newEnv(t, generousLimits())
	w := e.register(newRegistration())
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	cookie := sessionCookie(t, w)

	sum := sha256.Sum256([]byte(cookie.Value))
	// Move the session into the past (keeping expires_at > created_at).
	_, err := e.pool.Exec(context.Background(), `
		UPDATE sessions SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 second'
		WHERE token_hash = $1`, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	if w := e.do(http.MethodGet, "/v1/auth/me", "", withCookie(cookie)); w.Code != http.StatusUnauthorized {
		t.Errorf("expired session accepted: %d", w.Code)
	}
}

func TestSessionTokenStoredOnlyAsHash(t *testing.T) {
	e := newEnv(t, generousLimits())
	w := e.register(newRegistration())
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	cookie := sessionCookie(t, w)

	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token is not 32 bytes of base64url: %q", cookie.Value)
	}
	sum := sha256.Sum256([]byte(cookie.Value))
	if n := e.count(`SELECT count(*) FROM sessions WHERE token_hash = $1`, sum[:]); n != 1 {
		t.Errorf("sessions with sha256(token) = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM sessions WHERE token_hash = $1 OR encode(token_hash, 'escape') = $2`, []byte(cookie.Value), cookie.Value); n != 0 {
		t.Error("raw token found in sessions table")
	}
}

func TestOriginCheck(t *testing.T) {
	e := newEnv(t, generousLimits())

	w := e.do(http.MethodPost, "/v1/auth/register-company", newRegistration().body(), withOrigin("https://evil.example"))
	if w.Code != http.StatusForbidden || errorCode(t, w) != httpapi.CodeForbiddenOrigin {
		t.Errorf("foreign origin: %d %s", w.Code, w.Body)
	}
	w = e.do(http.MethodPost, "/v1/auth/register-company", newRegistration().body(), withOrigin(testOrigin))
	if w.Code != http.StatusCreated {
		t.Errorf("allowed origin: %d %s", w.Code, w.Body)
	}
}

func TestLoginRateLimitPerEmail(t *testing.T) {
	limits := generousLimits()
	limits.LoginPerEmail = httpapi.NewKeyedLimiter(time.Hour, 3)
	e := newEnv(t, limits)

	body := `{"email":"target-` + pgtest.Unique() + `@example.com","password":"guess guess guess"}`
	for i := 0; i < 3; i++ {
		if w := e.do(http.MethodPost, "/v1/auth/login", body); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d %s", i+1, w.Code, w.Body)
		}
	}
	w := e.do(http.MethodPost, "/v1/auth/login", body)
	if w.Code != http.StatusTooManyRequests || errorCode(t, w) != httpapi.CodeRateLimited {
		t.Errorf("4th attempt: %d %s", w.Code, w.Body)
	}
}

func TestMeWithoutCookie(t *testing.T) {
	e := newEnv(t, generousLimits())
	for _, c := range []*http.Cookie{nil, {Name: auth.SessionCookieName, Value: "forged-token"}} {
		var opts []reqOpt
		if c != nil {
			opts = append(opts, withCookie(c))
		}
		if w := e.do(http.MethodGet, "/v1/auth/me", "", opts...); w.Code != http.StatusUnauthorized {
			t.Errorf("me with cookie %v: %d", c, w.Code)
		}
	}
}
