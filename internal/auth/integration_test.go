package auth_test

// Integration tests: real PostgreSQL (interior_test), real router, real
// middleware. They live in package auth_test (an external test package),
// so they can only use auth's exported API — exactly like main.go does.

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Joedee43/interior_api/internal/apitest"
	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/httpapi"
	"github.com/Joedee43/interior_api/internal/postgres/pgtest"
)

const registerPath = "/v1/auth/register-company"

func register(e *apitest.Env, r apitest.Registration) *httptest.ResponseRecorder {
	e.T.Helper()
	return e.Do(http.MethodPost, registerPath, r.Body())
}

func TestRegisterMeLogoutFlow(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	reg := apitest.NewRegistration()

	w := register(e, reg)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	cookie := apitest.SessionCookie(t, w)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.MaxAge != 3600 {
		t.Errorf("cookie attributes: %+v", cookie)
	}

	var acct auth.AccountResponse
	apitest.Decode(t, w, &acct)
	if acct.User.Email != reg.Email || len(acct.Memberships) != 1 ||
		acct.Memberships[0].Role != "company_admin" ||
		acct.Memberships[0].Company.Slug != reg.Slug ||
		acct.Memberships[0].Company.Status != "active" {
		t.Errorf("unexpected register response: %s", w.Body)
	}
	for _, secret := range []string{"password", "hash", cookie.Value} {
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("response body contains %q: %s", secret, w.Body)
		}
	}

	w = e.As(cookie, http.MethodGet, "/v1/auth/me", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), reg.Email) {
		t.Fatalf("me: %d %s", w.Code, w.Body)
	}

	w = e.As(cookie, http.MethodPost, "/v1/auth/logout", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", w.Code, w.Body)
	}
	if c := apitest.SessionCookie(t, w); c.MaxAge >= 0 {
		t.Errorf("logout did not clear cookie: %+v", c)
	}

	w = e.As(cookie, http.MethodGet, "/v1/auth/me", "")
	if w.Code != http.StatusUnauthorized || apitest.ErrorCode(t, w) != httpapi.CodeUnauthenticated {
		t.Errorf("me after logout: %d %s", w.Code, w.Body)
	}

	// Logging out again is harmless.
	if w = e.As(cookie, http.MethodPost, "/v1/auth/logout", ""); w.Code != http.StatusNoContent {
		t.Errorf("second logout: %d", w.Code)
	}
}

func TestRegister_EmailIsNormalized(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	reg := apitest.NewRegistration()
	reg.Email = "  " + strings.ToUpper(reg.Email) + " "

	if w := register(e, reg); w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	want := strings.ToLower(strings.TrimSpace(reg.Email))
	if n := e.Count(`SELECT count(*) FROM users WHERE email = $1`, want); n != 1 {
		t.Errorf("normalized email rows = %d, want 1", n)
	}
}

func TestRegister_DuplicateEmailRollsBack(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	first := apitest.NewRegistration()
	if w := register(e, first); w.Code != http.StatusCreated {
		t.Fatalf("first register: %d %s", w.Code, w.Body)
	}

	second := apitest.NewRegistration()
	second.Email = first.Email
	w := register(e, second)
	if w.Code != http.StatusConflict || apitest.ErrorCode(t, w) != "email_taken" {
		t.Fatalf("duplicate email: %d %s", w.Code, w.Body)
	}
	if n := e.Count(`SELECT count(*) FROM companies WHERE slug = $1`, second.Slug); n != 0 {
		t.Errorf("company %s was created despite failed registration", second.Slug)
	}
}

func TestRegister_DuplicateSlugRollsBack(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	first := apitest.NewRegistration()
	if w := register(e, first); w.Code != http.StatusCreated {
		t.Fatalf("first register: %d %s", w.Code, w.Body)
	}

	second := apitest.NewRegistration()
	second.Slug = first.Slug
	w := register(e, second)
	if w.Code != http.StatusConflict || apitest.ErrorCode(t, w) != "slug_taken" {
		t.Fatalf("duplicate slug: %d %s", w.Code, w.Body)
	}
	// The user insert ran before the company insert failed; the
	// transaction must have rolled it back.
	if n := e.Count(`SELECT count(*) FROM users WHERE email = $1`, second.Email); n != 0 {
		t.Errorf("user %s was left behind by a failed registration", second.Email)
	}
}

func TestRegister_Validation(t *testing.T) {
	e := apitest.New(t, apitest.Options{})

	w := e.Do(http.MethodPost, registerPath,
		`{"company_name":"A","company_slug":"Bad Slug","full_name":"","email":"nope","password":"short"}`)
	if w.Code != http.StatusBadRequest || apitest.ErrorCode(t, w) != httpapi.CodeValidation {
		t.Fatalf("validation: %d %s", w.Code, w.Body)
	}
	for _, field := range []string{"company_name", "company_slug", "full_name", "email", "password"} {
		if !strings.Contains(w.Body.String(), `"`+field+`"`) {
			t.Errorf("details missing %s: %s", field, w.Body)
		}
	}

	w = e.Do(http.MethodPost, registerPath, `{"emial":"typo@example.com"}`)
	if w.Code != http.StatusBadRequest || apitest.ErrorCode(t, w) != httpapi.CodeBadRequest {
		t.Errorf("unknown field: %d %s", w.Code, w.Body)
	}
}

func loginBody(email, pw string) string {
	return `{"email":"` + email + `","password":"` + pw + `"}`
}

func TestLogin(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	reg := apitest.NewRegistration()
	w := register(e, reg)
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	registerCookie := apitest.SessionCookie(t, w)

	t.Run("wrong password and unknown email look identical", func(t *testing.T) {
		wrong := e.Do(http.MethodPost, "/v1/auth/login", loginBody(reg.Email, "not the right password"))
		unknown := e.Do(http.MethodPost, "/v1/auth/login", loginBody("nobody-"+pgtest.Unique()+"@example.com", "whatever password"))
		for _, w := range []*httptest.ResponseRecorder{wrong, unknown} {
			if w.Code != http.StatusUnauthorized || apitest.ErrorCode(t, w) != "invalid_credentials" {
				t.Errorf("login failure: %d %s", w.Code, w.Body)
			}
		}
	})

	t.Run("success rotates the session", func(t *testing.T) {
		w := e.Do(http.MethodPost, "/v1/auth/login", loginBody(strings.ToUpper(reg.Email), reg.Password), apitest.WithCookie(registerCookie))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"company_admin"`) {
			t.Fatalf("login: %d %s", w.Code, w.Body)
		}
		loginCookie := apitest.SessionCookie(t, w)
		if loginCookie.Value == registerCookie.Value {
			t.Fatal("login reused the previous session token")
		}
		if w := e.As(registerCookie, http.MethodGet, "/v1/auth/me", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("previous session still valid after login: %d", w.Code)
		}
		if w := e.As(loginCookie, http.MethodGet, "/v1/auth/me", ""); w.Code != http.StatusOK {
			t.Errorf("new session rejected: %d %s", w.Code, w.Body)
		}
	})
}

func TestDisabledUser(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()

	e.Exec(`UPDATE users SET status = 'disabled' WHERE id = $1`, co.AdminID)

	if w := e.As(co.AdminCookie, http.MethodGet, "/v1/auth/me", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("disabled user's session still works: %d", w.Code)
	}
	w := e.Do(http.MethodPost, "/v1/auth/login", loginBody(co.AdminEmail, apitest.Password))
	if w.Code != http.StatusUnauthorized || apitest.ErrorCode(t, w) != "invalid_credentials" {
		t.Errorf("disabled user login: %d %s", w.Code, w.Body)
	}
}

func TestExpiredSession(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()

	sum := sha256.Sum256([]byte(co.AdminCookie.Value))
	// Move the session into the past (keeping expires_at > created_at).
	e.Exec(`UPDATE sessions SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 second'
		WHERE token_hash = $1`, sum[:])

	if w := e.As(co.AdminCookie, http.MethodGet, "/v1/auth/me", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("expired session accepted: %d", w.Code)
	}
}

func TestSessionTokenStoredOnlyAsHash(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	cookie := e.RegisterCompany().AdminCookie

	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token is not 32 bytes of base64url: %q", cookie.Value)
	}
	sum := sha256.Sum256([]byte(cookie.Value))
	if n := e.Count(`SELECT count(*) FROM sessions WHERE token_hash = $1`, sum[:]); n != 1 {
		t.Errorf("sessions with sha256(token) = %d, want 1", n)
	}
	if n := e.Count(`SELECT count(*) FROM sessions WHERE token_hash = $1 OR encode(token_hash, 'escape') = $2`, []byte(cookie.Value), cookie.Value); n != 0 {
		t.Error("raw token found in sessions table")
	}
}

func TestOriginCheck(t *testing.T) {
	e := apitest.New(t, apitest.Options{})

	w := e.Do(http.MethodPost, registerPath, apitest.NewRegistration().Body(), apitest.WithOrigin("https://evil.example"))
	if w.Code != http.StatusForbidden || apitest.ErrorCode(t, w) != httpapi.CodeForbiddenOrigin {
		t.Errorf("foreign origin: %d %s", w.Code, w.Body)
	}
	w = e.Do(http.MethodPost, registerPath, apitest.NewRegistration().Body(), apitest.WithOrigin(apitest.Origin))
	if w.Code != http.StatusCreated {
		t.Errorf("allowed origin: %d %s", w.Code, w.Body)
	}
}

func TestLoginRateLimitPerEmail(t *testing.T) {
	e := apitest.New(t, apitest.Options{LoginLimits: &auth.Limits{
		LoginPerIP:    httpapi.NewKeyedLimiter(time.Millisecond, 1000),
		LoginPerEmail: httpapi.NewKeyedLimiter(time.Hour, 3),
		RegisterPerIP: httpapi.NewKeyedLimiter(time.Millisecond, 1000),
	}})

	body := loginBody("target-"+pgtest.Unique()+"@example.com", "guess guess guess")
	for i := 0; i < 3; i++ {
		if w := e.Do(http.MethodPost, "/v1/auth/login", body); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d %s", i+1, w.Code, w.Body)
		}
	}
	w := e.Do(http.MethodPost, "/v1/auth/login", body)
	if w.Code != http.StatusTooManyRequests || apitest.ErrorCode(t, w) != httpapi.CodeRateLimited {
		t.Errorf("4th attempt: %d %s", w.Code, w.Body)
	}
}

func TestMeWithoutValidCookie(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	if w := e.Do(http.MethodGet, "/v1/auth/me", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("me without cookie: %d", w.Code)
	}
	forged := &http.Cookie{Name: auth.SessionCookieName, Value: "forged-token"}
	if w := e.As(forged, http.MethodGet, "/v1/auth/me", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("me with forged cookie: %d", w.Code)
	}
}
