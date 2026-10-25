package company

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/httpapi"
)

const (
	codeEmailTaken = "email_taken"
	codeSlugTaken  = "slug_taken"
	codeLastAdmin  = "last_admin"

	codeCurrencyLocked = "currency_locked"
)

type Handler struct {
	svc *Service
	// requireSession is auth.Handler.RequireSession(), injected so this
	// package does not need the auth handler, only its middleware.
	requireSession gin.HandlerFunc
	cookies        auth.Cookies
	registerLimit  *httpapi.KeyedLimiter
}

func NewHandler(svc *Service, requireSession gin.HandlerFunc, cookies auth.Cookies, registerLimit *httpapi.KeyedLimiter) *Handler {
	return &Handler{svc: svc, requireSession: requireSession, cookies: cookies, registerLimit: registerLimit}
}

// DefaultRegisterLimit is ~5 registrations per hour per IP.
func DefaultRegisterLimit() *httpapi.KeyedLimiter { return httpapi.NewKeyedLimiter(12*time.Minute, 5) }

// Mount registers this package's routes on /v1. Middleware order in each
// chain matters: session (who are you?) -> membership (are you in this
// company?) -> role (may you do this?) -> handler.
func (h *Handler) Mount(v1 *gin.RouterGroup) {
	v1.POST("/auth/register-company", httpapi.RateLimitByIP(h.registerLimit, "register"), h.register)

	c := v1.Group("/companies/:companyID", h.requireSession, h.RequireMember())
	c.GET("", h.get)
	c.PATCH("", RequireRole(RoleAdmin), h.update)
	c.GET("/members", h.listMembers)
	c.POST("/members", RequireRole(RoleAdmin), h.createMember)
	c.PATCH("/members/:userID", RequireRole(RoleAdmin), h.updateMember)

	a := v1.Group("/admin", h.requireSession, auth.RequirePlatformAdmin())
	a.GET("/companies", h.adminListCompanies)
}

// ---- middleware -------------------------------------------------------

// RequireMember resolves :companyID to the caller's active membership and
// stores it on the request context. Must run after RequireSession.
func (h *Handler) RequireMember() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := auth.PrincipalFrom(c.Request.Context())
		if !ok {
			httpapi.WriteError(c, http.StatusUnauthorized, httpapi.CodeUnauthenticated, "authentication required")
			return
		}
		companyID := c.Param("companyID")
		if !httpapi.ValidUUID(companyID) {
			writeError(c, ErrNotFound)
			return
		}
		m, err := h.svc.Authorize(c.Request.Context(), p.UserID, companyID)
		if err != nil {
			writeError(c, err)
			return
		}
		c.Request = c.Request.WithContext(WithMember(c.Request.Context(), m))
		c.Next()
	}
}

// RequireRole allows only members whose role is one of roles. Must run
// after RequireMember. The service layer re-checks; this gives a fast,
// uniform 403 and documents the rule next to the route.
func RequireRole(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		m, ok := MemberFrom(c.Request.Context())
		if !ok {
			writeError(c, ErrNotFound)
			return
		}
		for _, r := range roles {
			if m.Role == r {
				c.Next()
				return
			}
		}
		writeError(c, ErrForbidden)
	}
}

// member is safe to call in handlers behind RequireMember.
func member(c *gin.Context) Member {
	m, _ := MemberFrom(c.Request.Context())
	return m
}

// ---- DTOs -------------------------------------------------------------

type companyDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Status    string    `json:"status"`
	Currency  string    `json:"currency"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toCompanyDTO(co Company) companyDTO {
	return companyDTO(co) // identical fields: a type conversion, no copying by hand
}

type memberDTO struct {
	UserID   string    `json:"user_id"`
	Email    string    `json:"email"`
	FullName string    `json:"full_name"`
	Role     string    `json:"role"`
	Status   string    `json:"status"`
	JoinedAt time.Time `json:"joined_at"`
}

func toMemberDTO(mi MemberInfo) memberDTO {
	return memberDTO{UserID: mi.UserID, Email: mi.Email, FullName: mi.FullName, Role: mi.Role, Status: mi.Status, JoinedAt: mi.CreatedAt}
}

// ---- handlers ---------------------------------------------------------

type registerRequest struct {
	CompanyName string `json:"company_name"`
	CompanySlug string `json:"company_slug"`
	FullName    string `json:"full_name"`
	Email       string `json:"email"`
	Password    string `json:"password"`
}

func (h *Handler) register(c *gin.Context) {
	var req registerRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	user, ms, token, err := h.svc.RegisterCompany(c.Request.Context(), RegisterInput{
		CompanyName: req.CompanyName,
		CompanySlug: req.CompanySlug,
		User:        auth.NewUserInput{Email: req.Email, FullName: req.FullName, Password: req.Password},
	}, auth.SessionMetaFrom(c))
	if err != nil {
		writeError(c, err)
		return
	}
	h.cookies.Set(c, token)
	c.JSON(http.StatusCreated, auth.NewAccountResponse(user, ms))
}

func (h *Handler) get(c *gin.Context) {
	co, err := h.svc.Get(c.Request.Context(), member(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toCompanyDTO(co))
}

type updateRequest struct {
	Name     *string `json:"name"`
	Currency *string `json:"currency"`
}

func (h *Handler) update(c *gin.Context) {
	var req updateRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	co, err := h.svc.Update(c.Request.Context(), member(c), CompanyUpdate(req))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toCompanyDTO(co))
}

func (h *Handler) listMembers(c *gin.Context) {
	ms, err := h.svc.ListMembers(c.Request.Context(), member(c))
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]memberDTO, 0, len(ms))
	for _, m := range ms {
		out = append(out, toMemberDTO(m))
	}
	c.JSON(http.StatusOK, gin.H{"members": out})
}

type createMemberRequest struct {
	Email    string `json:"email"`
	FullName string `json:"full_name"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (h *Handler) createMember(c *gin.Context) {
	var req createMemberRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	mi, err := h.svc.CreateMember(c.Request.Context(), member(c), NewMemberInput{
		User: auth.NewUserInput{Email: req.Email, FullName: req.FullName, Password: req.Password},
		Role: req.Role,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toMemberDTO(mi))
}

// updateMemberRequest uses pointers so the JSON decoder can tell
// "field omitted" (nil) from "field set" — PATCH semantics.
type updateMemberRequest struct {
	Role   *string `json:"role"`
	Status *string `json:"status"`
}

func (h *Handler) updateMember(c *gin.Context) {
	userID := c.Param("userID")
	if !httpapi.ValidUUID(userID) {
		writeError(c, ErrMemberNotFound)
		return
	}
	var req updateMemberRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	mi, err := h.svc.UpdateMember(c.Request.Context(), member(c), userID, MemberUpdate(req))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toMemberDTO(mi))
}

func (h *Handler) adminListCompanies(c *gin.Context) {
	limit := MaxListLimit
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > MaxListLimit {
			httpapi.WriteValidationError(c, map[string]string{"limit": "must be a number from 1 to 100"})
			return
		}
		limit = n
	}
	p, _ := auth.PrincipalFrom(c.Request.Context())
	cs, err := h.svc.ListAll(c.Request.Context(), p, limit)
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]companyDTO, 0, len(cs))
	for _, co := range cs {
		out = append(out, toCompanyDTO(co))
	}
	c.JSON(http.StatusOK, gin.H{"companies": out})
}

// ---- errors -----------------------------------------------------------

func writeError(c *gin.Context, err error) {
	var verr *auth.ValidationError
	switch {
	case errors.As(err, &verr):
		httpapi.WriteValidationError(c, verr.Fields)
	case errors.Is(err, auth.ErrEmailTaken):
		httpapi.WriteError(c, http.StatusConflict, codeEmailTaken, "an account with this email already exists")
	case errors.Is(err, ErrSlugTaken):
		httpapi.WriteError(c, http.StatusConflict, codeSlugTaken, "this company slug is already taken")
	case errors.Is(err, ErrNotFound):
		httpapi.WriteError(c, http.StatusNotFound, httpapi.CodeNotFound, "company not found")
	case errors.Is(err, ErrMemberNotFound):
		httpapi.WriteError(c, http.StatusNotFound, httpapi.CodeNotFound, "member not found")
	case errors.Is(err, ErrForbidden), errors.Is(err, auth.ErrForbidden):
		httpapi.WriteError(c, http.StatusForbidden, httpapi.CodeForbidden, "you do not have permission to do this")
	case errors.Is(err, ErrCurrencyLocked):
		httpapi.WriteError(c, http.StatusConflict, codeCurrencyLocked, "currency cannot change once the company has products")
	case errors.Is(err, ErrLastAdmin):
		httpapi.WriteError(c, http.StatusConflict, codeLastAdmin, "a company must keep at least one active admin")
	default:
		httpapi.WriteInternalError(c, err)
	}
}
