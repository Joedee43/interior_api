package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Joedee43/interior_api/internal/httpapi"
)

const codeInvalidCredentials = "invalid_credentials"

const SessionCookieName = "interior_session"

// Cookies writes the session cookie. Secure must be true whenever the
// site is served over HTTPS (production); browsers refuse to send Secure
// cookies over plain http://localhost, so development turns it off.
type Cookies struct {
	Secure bool
	MaxAge time.Duration
}

// http.SetCookie is used instead of gin's c.SetCookie because it exposes
// every attribute, SameSite included, in one struct.
func (k Cookies) Set(c *gin.Context, token string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(k.MaxAge.Seconds()),
		HttpOnly: true, // not readable from JavaScript, so XSS cannot steal it
		Secure:   k.Secure,
		SameSite: http.SameSiteLaxMode, // not sent on cross-site POSTs
	})
}

func (k Cookies) Clear(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1, // tells the browser to delete it now
		HttpOnly: true,
		Secure:   k.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// MembershipLister supplies the memberships shown by /auth/me and login.
// The company package implements it; declaring the interface here, on the
// consuming side, is what lets auth avoid importing company.
type MembershipLister interface {
	ListMemberships(ctx context.Context, userID string) ([]Membership, error)
}

// Limits are the rate limiters for unauthenticated auth endpoints.
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
	svc         *Service
	memberships MembershipLister
	cookies     Cookies
	limits      Limits
}

func NewHandler(svc *Service, memberships MembershipLister, cookies Cookies, limits Limits) *Handler {
	return &Handler{svc: svc, memberships: memberships, cookies: cookies, limits: limits}
}

// Mount registers /v1/auth/* (passed to httpapi.Deps.V1Routes).
// register-company is mounted by the company package.
func (h *Handler) Mount(v1 *gin.RouterGroup) {
	g := v1.Group("/auth")
	g.POST("/register", httpapi.RateLimitByIP(h.limits.RegisterPerIP, "register-customer"), h.register)
	g.POST("/login", httpapi.RateLimitByIP(h.limits.LoginPerIP, "login"), h.login)
	g.POST("/logout", h.logout)
	g.GET("/me", h.RequireSession(), h.me)
}

// ---- response DTOs ----------------------------------------------------
//
// These structs are the JSON contract. Keeping them separate from the
// domain types means adding a field to auth.User (say, a password hash)
// can never leak into a response by accident.

type UserDTO struct {
	ID              string    `json:"id"`
	Email           string    `json:"email"`
	FullName        string    `json:"full_name"`
	Status          string    `json:"status"`
	IsPlatformAdmin bool      `json:"is_platform_admin"`
	CreatedAt       time.Time `json:"created_at"`
}

type MembershipDTO struct {
	Company struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Slug   string `json:"slug"`
		Status string `json:"status"`
	} `json:"company"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

type AccountResponse struct {
	User        UserDTO         `json:"user"`
	Memberships []MembershipDTO `json:"memberships"`
}

func ToUserDTO(u User) UserDTO {
	return UserDTO{
		ID: u.ID, Email: u.Email, FullName: u.FullName, Status: u.Status,
		IsPlatformAdmin: u.IsPlatformAdmin, CreatedAt: u.CreatedAt,
	}
}

func NewAccountResponse(u User, ms []Membership) AccountResponse {
	// make(..., 0, n) so an empty list encodes as [] rather than null.
	dtos := make([]MembershipDTO, 0, len(ms))
	for _, m := range ms {
		var d MembershipDTO
		d.Company.ID, d.Company.Name, d.Company.Slug, d.Company.Status = m.CompanyID, m.CompanyName, m.CompanySlug, m.CompanyStatus
		d.Role, d.Status = m.Role, m.Status
		dtos = append(dtos, d)
	}
	return AccountResponse{User: ToUserDTO(u), Memberships: dtos}
}

// ---- handlers ---------------------------------------------------------

type registerRequest struct {
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// register creates a customer account (a user with no company) and logs
// it in. Companies register through /auth/register-company instead.
func (h *Handler) register(c *gin.Context) {
	var req registerRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	user, token, err := h.svc.RegisterCustomer(c.Request.Context(),
		NewUserInput{Email: req.Email, FullName: req.FullName, Password: req.Password}, SessionMetaFrom(c))
	if err != nil {
		writeError(c, err)
		return
	}
	h.cookies.Set(c, token)
	h.respondAccount(c, http.StatusCreated, user)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
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
	user, token, err := h.svc.Login(c.Request.Context(), req.Email, req.Password, previous, SessionMetaFrom(c))
	if err != nil {
		writeError(c, err)
		return
	}
	h.cookies.Set(c, token)
	h.respondAccount(c, http.StatusOK, user)
}

func (h *Handler) logout(c *gin.Context) {
	token, _ := c.Cookie(SessionCookieName)
	if err := h.svc.Logout(c.Request.Context(), token); err != nil {
		httpapi.WriteInternalError(c, err)
		return
	}
	h.cookies.Clear(c)
	c.Status(http.StatusNoContent)
}

func (h *Handler) me(c *gin.Context) {
	p, _ := PrincipalFrom(c.Request.Context()) // guaranteed by RequireSession
	user, err := h.svc.GetUser(c.Request.Context(), p.UserID)
	if err != nil {
		writeError(c, err)
		return
	}
	h.respondAccount(c, http.StatusOK, user)
}

func (h *Handler) respondAccount(c *gin.Context, status int, user User) {
	ms, err := h.memberships.ListMemberships(c.Request.Context(), user.ID)
	if err != nil {
		httpapi.WriteInternalError(c, err)
		return
	}
	c.JSON(status, NewAccountResponse(user, ms))
}

// ---- middleware -------------------------------------------------------

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

// RequirePlatformAdmin must come after RequireSession in the chain.
func RequirePlatformAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := PrincipalFrom(c.Request.Context())
		if !ok {
			writeError(c, ErrUnauthenticated)
			return
		}
		if !p.IsPlatformAdmin {
			writeError(c, ErrForbidden)
			return
		}
		c.Next()
	}
}

// ---- helpers ----------------------------------------------------------

// writeError maps auth's domain errors to HTTP.
func writeError(c *gin.Context, err error) {
	var verr *ValidationError
	switch {
	case errors.As(err, &verr):
		httpapi.WriteValidationError(c, verr.Fields)
	case errors.Is(err, ErrEmailTaken):
		httpapi.WriteError(c, http.StatusConflict, "email_taken", "an account with this email already exists")
	case errors.Is(err, ErrInvalidCredentials):
		httpapi.WriteError(c, http.StatusUnauthorized, codeInvalidCredentials, "invalid email or password")
	case errors.Is(err, ErrUnauthenticated):
		httpapi.WriteError(c, http.StatusUnauthorized, httpapi.CodeUnauthenticated, "authentication required")
	case errors.Is(err, ErrForbidden):
		httpapi.WriteError(c, http.StatusForbidden, httpapi.CodeForbidden, "you do not have permission to do this")
	default:
		httpapi.WriteInternalError(c, err)
	}
}

func SessionMetaFrom(c *gin.Context) SessionMeta {
	return SessionMeta{UserAgent: c.Request.UserAgent(), IP: c.ClientIP()}
}
