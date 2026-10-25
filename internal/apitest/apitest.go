// Package apitest builds the complete HTTP API against the test database,
// wired the same way as cmd/api, for integration tests in any package.
// Only _test.go files import it.
package apitest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/catalog"
	"github.com/Joedee43/interior_api/internal/company"
	"github.com/Joedee43/interior_api/internal/design"
	"github.com/Joedee43/interior_api/internal/httpapi"
	"github.com/Joedee43/interior_api/internal/postgres/pgtest"
	"github.com/Joedee43/interior_api/internal/quote"
	"github.com/Joedee43/interior_api/internal/storage"
)

const Origin = "http://localhost:5173"

func init() { gin.SetMode(gin.TestMode) }

// Options tune the environment; zero values mean generous defaults.
type Options struct {
	LoginLimits   *auth.Limits
	RegisterLimit *httpapi.KeyedLimiter
}

type Env struct {
	T          *testing.T
	Pool       *pgxpool.Pool
	Router     http.Handler
	StorageDir string
}

func generous() *httpapi.KeyedLimiter { return httpapi.NewKeyedLimiter(time.Millisecond, 1000) }

func New(t *testing.T, opts Options) *Env {
	t.Helper()
	pool := pgtest.New(t)

	// Cheap Argon2 parameters: tests hash many passwords.
	hasher := auth.NewPasswordHasher(auth.Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32})
	authSvc, err := auth.NewService(pool, hasher, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	companySvc := company.NewService(pool, authSvc)

	limits := auth.Limits{LoginPerIP: generous(), LoginPerEmail: generous(), RegisterPerIP: generous()}
	if opts.LoginLimits != nil {
		limits = *opts.LoginLimits
	}
	registerLimit := generous()
	if opts.RegisterLimit != nil {
		registerLimit = opts.RegisterLimit
	}

	cookies := auth.Cookies{Secure: false, MaxAge: time.Hour}
	authH := auth.NewHandler(authSvc, companySvc, cookies, limits)
	companyH := company.NewHandler(companySvc, authH.RequireSession(), cookies, registerLimit)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Each environment gets its own storage folder, removed after the test.
	storageDir := t.TempDir()
	files, err := storage.NewLocalStore(storageDir)
	if err != nil {
		t.Fatal(err)
	}
	catalogSvc := catalog.NewService(pool, files, logger)
	catalogH := catalog.NewHandler(catalogSvc, authH.RequireSession(), companyH.RequireMember())
	designSvc := design.NewService(pool, catalogSvc)
	designH := design.NewHandler(designSvc, authH.RequireSession())
	quoteH := quote.NewHandler(quote.NewService(pool, catalogSvc, designSvc, authSvc),
		authH.RequireSession(), companyH.RequireMember(), generous())

	router := httpapi.NewRouter(httpapi.Deps{
		Logger:         logger,
		DB:             pool,
		AllowedOrigins: []string{Origin},
		V1Routes:       []func(*gin.RouterGroup){authH.Mount, companyH.Mount, catalogH.Mount, designH.Mount, quoteH.Mount},
	})
	return &Env{T: t, Pool: pool, Router: router, StorageDir: storageDir}
}

// StoredFiles counts files under the environment's storage folder.
func (e *Env) StoredFiles() int {
	e.T.Helper()
	n := 0
	err := filepath.WalkDir(e.StorageDir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	})
	if err != nil {
		e.T.Fatal(err)
	}
	return n
}

// Upload sends a multipart/form-data request with one file and optional
// text fields, the way a browser form or `curl -F` would.
func (e *Env) Upload(cookie *http.Cookie, method, path, filename string, content []byte, fields map[string]string) *httptest.ResponseRecorder {
	e.T.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		e.T.Fatal(err)
	}
	_, _ = fw.Write(content)
	_ = mw.Close()

	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	e.Router.ServeHTTP(w, req)
	return w
}

// ---- requests ---------------------------------------------------------

type ReqOpt func(*http.Request)

func WithCookie(c *http.Cookie) ReqOpt { return func(r *http.Request) { r.AddCookie(c) } }
func WithOrigin(o string) ReqOpt       { return func(r *http.Request) { r.Header.Set("Origin", o) } }

// Do sends a request through the router. A nil cookie in WithCookie is
// not allowed; omit the option instead.
func (e *Env) Do(method, path, body string, opts ...ReqOpt) *httptest.ResponseRecorder {
	e.T.Helper()
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
	e.Router.ServeHTTP(w, req)
	return w
}

// As is Do with a session cookie, the common case for authorized calls.
func (e *Env) As(c *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	e.T.Helper()
	return e.Do(method, path, body, WithCookie(c))
}

func SessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in response (status %d, body %s)", auth.SessionCookieName, w.Code, w.Body)
	return nil
}

func ErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp httpapi.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("not an error body: %s", w.Body)
	}
	return resp.Error.Code
}

// Decode unmarshals a JSON response body into v, failing the test on error.
func Decode(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
}

// ---- database helpers -------------------------------------------------

func (e *Env) Count(sql string, args ...any) int {
	e.T.Helper()
	var n int
	if err := e.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.T.Fatal(err)
	}
	return n
}

func (e *Env) Exec(sql string, args ...any) {
	e.T.Helper()
	if _, err := e.Pool.Exec(context.Background(), sql, args...); err != nil {
		e.T.Fatal(err)
	}
}

// ---- fixtures ---------------------------------------------------------

const Password = "correct horse battery"

type Registration struct {
	Slug, Email, Password string
}

func NewRegistration() Registration {
	u := pgtest.Unique()
	return Registration{Slug: "acme-" + u, Email: "admin-" + u + "@example.com", Password: Password}
}

func (r Registration) Body() string {
	return `{"company_name":"Acme Furniture","company_slug":"` + r.Slug +
		`","full_name":"Abebe Kebede","email":"` + r.Email + `","password":"` + r.Password + `"}`
}

// Company is a registered company with its first admin logged in.
type Company struct {
	ID          string
	Slug        string
	AdminID     string
	AdminEmail  string
	AdminCookie *http.Cookie
}

// RegisterCompany registers a fresh company and fails the test otherwise.
func (e *Env) RegisterCompany() Company {
	e.T.Helper()
	reg := NewRegistration()
	w := e.Do(http.MethodPost, "/v1/auth/register-company", reg.Body())
	if w.Code != http.StatusCreated {
		e.T.Fatalf("register company: %d %s", w.Code, w.Body)
	}
	var resp auth.AccountResponse
	Decode(e.T, w, &resp)
	return Company{
		ID:          resp.Memberships[0].Company.ID,
		Slug:        reg.Slug,
		AdminID:     resp.User.ID,
		AdminEmail:  reg.Email,
		AdminCookie: SessionCookie(e.T, w),
	}
}

// User is a member created through the API, then logged in.
type User struct {
	ID     string
	Email  string
	Cookie *http.Cookie
}

// AddMember creates a member of co with role via the admin API, logs
// them in, and returns their session.
func (e *Env) AddMember(co Company, role string) User {
	e.T.Helper()
	email := role + "-" + pgtest.Unique() + "@example.com"
	w := e.As(co.AdminCookie, http.MethodPost, "/v1/companies/"+co.ID+"/members",
		`{"email":"`+email+`","full_name":"Team Member","password":"`+Password+`","role":"`+role+`"}`)
	if w.Code != http.StatusCreated {
		e.T.Fatalf("add member: %d %s", w.Code, w.Body)
	}
	var m struct {
		UserID string `json:"user_id"`
	}
	Decode(e.T, w, &m)
	return User{ID: m.UserID, Email: email, Cookie: e.Login(email, Password)}
}

func (e *Env) Login(email, password string) *http.Cookie {
	e.T.Helper()
	w := e.Do(http.MethodPost, "/v1/auth/login", `{"email":"`+email+`","password":"`+password+`"}`)
	if w.Code != http.StatusOK {
		e.T.Fatalf("login %s: %d %s", email, w.Code, w.Body)
	}
	return SessionCookie(e.T, w)
}

// RegisterCustomer creates a customer account (no company) and returns
// its id and session cookie.
func (e *Env) RegisterCustomer() User {
	e.T.Helper()
	email := "customer-" + pgtest.Unique() + "@example.com"
	w := e.Do(http.MethodPost, "/v1/auth/register",
		`{"full_name":"Selam Tesfaye","email":"`+email+`","password":"`+Password+`"}`)
	if w.Code != http.StatusCreated {
		e.T.Fatalf("register customer: %d %s", w.Code, w.Body)
	}
	var resp auth.AccountResponse
	Decode(e.T, w, &resp)
	return User{ID: resp.User.ID, Email: email, Cookie: SessionCookie(e.T, w)}
}

// MakePlatformAdmin sets the flag directly, as cmd/admin would.
func (e *Env) MakePlatformAdmin(userID string) {
	e.T.Helper()
	e.Exec(`UPDATE users SET is_platform_admin = true WHERE id = $1`, userID)
}
