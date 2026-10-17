package auth

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Joedee43/interior_api/internal/company"
	"github.com/Joedee43/interior_api/internal/httpapi"
)

// Error codes specific to auth (generic ones live in httpapi).
const (
	codeEmailTaken         = "email_taken"
	codeSlugTaken          = "slug_taken"
	codeInvalidCredentials = "invalid_credentials"
)

const SessionCookieName = "interior_session"

// CookieConfig controls the session cookie. Secure must be true whenever
// the site is served over HTTPS (production); browsers refuse to send
// Secure cookies over plain http://localhost, so development turns it off.
type CookieConfig struct {
	Secure bool
}

// Limits are the rate limiters for unauthenticated endpoints.
type Limits struct {
	LoginPerIP    *httpapi.KeyedLimiter
	LoginPerEmail *httpapi.KeyedLimiter
	RegisterPerIP *httpapi.KeyedLimiter
}

func DefaultLimits() Limits {
	return Limits{
		LoginPerIP:    httpapi.NewKeyedLimiter(6*time.Second, 10), // ~10/min per IP
		LoginPerEmail: httpapi.NewKeyedLimiter(12*time.Second, 5), // ~5/min per account
		RegisterPerIP: httpapi.NewKeyedLimiter(12*time.Minute, 5), // ~5/hour per IP
	}
}

type Handler struct {
	svc    *Service
	cookie CookieConfig
	limits Limits
}

func NewHandler(svc *Service, cookie CookieConfig, limits Limits) *Handler {
	return &Handler{svc: svc, cookie: cookie, limits: limits}
}

// Mount registers /v1/auth/* (passed to httpapi.Deps.V1Routes).
func (h *Handler) Mount(v1 *gin.RouterGroup) {
	g := v1.Group("/auth")
	g.POST("/register-company", httpapi.RateLimitByIP(h.limits.RegisterPerIP, "register"), h.registerCompany)
	g.POST("/login", httpapi.RateLimitByIP(h.limits.LoginPerIP, "login"), h.login)
	g.POST("/logout", h.logout)
	g.GET("/me", h.RequireSession(), h.me)
}

// ---- request / response DTOs ----------------------------------------
//
// These structs are the JSON contract. Keeping them separate from the
// domain types means adding a field to auth.User (say, a password hash)
// can never leak into a response by accident.

type registerCompanyRequest struct {
	CompanyName string `json:"company_name"`
	CompanySlug string `json:"company_slug"`
	FullName    string `json:"full_name"`
	Email       string `json:"email"`
	Password    string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userDTO struct {
	ID              string    `json:"id"`
	Email           string    `json:"email"`
	FullName        string    `json:"full_name"`
	Status          string    `json:"status"`
	IsPlatformAdmin bool      `json:"is_platform_admin"`
	CreatedAt       time.Time `json:"created_at"`
}

type companyDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	Status string `json:"status"`
}

type membershipDTO struct {
	Company companyDTO `json:"company"`
	Role    string     `json:"role"`
	Status  string     `json:"status"`
}

type accountResponse struct {
	User        userDTO         `json:"user"`
	Memberships []membershipDTO `json:"memberships"`
}

func toAccountResponse(a Account) accountResponse {
	// make(..., 0, n) so an empty list encodes as [] rather than null.
	ms := make([]membershipDTO, 0, len(a.Memberships))
	for _, m := range a.Memberships {
		ms = append(ms, membershipDTO{
			Company: toCompanyDTO(m.Company),
			Role:    m.Role,
			Status:  m.Status,
		})
	}
	u := a.User
	return accountResponse{
		User: userDTO{
			ID: u.ID, Email: u.Email, FullName: u.FullName, Status: u.Status,
			IsPlatformAdmin: u.IsPlatformAdmin, CreatedAt: u.CreatedAt,
		},
		Memberships: ms,
	}
}

func toCompanyDTO(c company.Company) companyDTO {
	return companyDTO{ID: c.ID, Name: c.Name, Slug: c.Slug, Status: c.Status}
}

// ---- handlers ---------------------------------------------------------

func (h *Handler) registerCompany(c *gin.Context) {
	var req registerCompanyRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	acct, token, err := h.svc.RegisterCompany(c.Request.Context(), RegisterCompanyInput{
		CompanyName: req.CompanyName,
		CompanySlug: req.CompanySlug,
		FullName:    req.FullName,
		Email:       req.Email,
		Password:    req.Password,
	}, sessionMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	h.setSessionCookie(c, token)
	c.JSON(http.StatusCreated, toAccountResponse(acct))
}

func (h *Handler) login(c *gin.Context) {
	var req loginRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	// Per-account limit, in addition to the per-IP one on the route: stops
	// one attacker spreading guesses for a single account across many IPs.
	if !h.limits.LoginPerEmail.Allow("login:" + NormalizeEmail(req.Email)) {
		httpapi.WriteRateLimited(c)
		return
	}

	previous, _ := c.Cookie(SessionCookieName)
	acct, token, err := h.svc.Login(c.Request.Context(), req.Email, req.Password, previous, sessionMeta(c))
	if err != nil {
		writeError(c, err)
		return
	}
	h.setSessionCookie(c, token)
	c.JSON(http.StatusOK, toAccountResponse(acct))
}

func (h *Handler) logout(c *gin.Context) {
	token, _ := c.Cookie(SessionCookieName)
	if err := h.svc.Logout(c.Request.Context(), token); err != nil {
		httpapi.WriteInternalError(c, err)
		return
	}
	h.clearSessionCookie(c)
	c.Status(http.StatusNoContent)
}

func (h *Handler) me(c *gin.Context) {
	p, _ := PrincipalFrom(c.Request.Context()) // guaranteed by RequireSession
	acct, err := h.svc.Account(c.Request.Context(), p.UserID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAccountResponse(acct))
}

// RequireSession rejects requests without a valid session cookie and
// stores the Principal on the request context for later handlers.
func (h *Handler) RequireSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, _ := c.Cookie(SessionCookieName)
		p, err := h.svc.Authenticate(c.Request.Context(), token)
		if err != nil {
			writeError(c, err) // aborts the chain; c.Next() is never reached
			return
		}
		// Replace the request with one whose context carries the principal.
		c.Request = c.Request.WithContext(WithPrincipal(c.Request.Context(), p))
		c.Next()
	}
}

// ---- helpers ----------------------------------------------------------

// writeError maps domain errors to HTTP. This switch is the only place
// that knows both sides.
func writeError(c *gin.Context, err error) {
	var verr *ValidationError
	switch {
	case errors.As(err, &verr):
		httpapi.WriteValidationError(c, verr.Fields)
	case errors.Is(err, ErrEmailTaken):
		httpapi.WriteError(c, http.StatusConflict, codeEmailTaken, "an account with this email already exists")
	case errors.Is(err, company.ErrSlugTaken):
		httpapi.WriteError(c, http.StatusConflict, codeSlugTaken, "this company slug is already taken")
	case errors.Is(err, ErrInvalidCredentials):
		httpapi.WriteError(c, http.StatusUnauthorized, codeInvalidCredentials, "invalid email or password")
	case errors.Is(err, ErrUnauthenticated):
		httpapi.WriteError(c, http.StatusUnauthorized, httpapi.CodeUnauthenticated, "authentication required")
	default:
		httpapi.WriteInternalError(c, err)
	}
}

func sessionMeta(c *gin.Context) SessionMeta {
	return SessionMeta{UserAgent: c.Request.UserAgent(), IP: c.ClientIP()}
}

// http.SetCookie is used instead of gin's c.SetCookie because it exposes
// every attribute, SameSite included, in one struct.
func (h *Handler) setSessionCookie(c *gin.Context, token string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(h.svc.SessionTTL().Seconds()),
		HttpOnly: true, // not readable from JavaScript, so XSS cannot steal it
		Secure:   h.cookie.Secure,
		SameSite: http.SameSiteLaxMode, // not sent on cross-site POSTs
	})
}

func (h *Handler) clearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1, // tells the browser to delete it now
		HttpOnly: true,
		Secure:   h.cookie.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}
