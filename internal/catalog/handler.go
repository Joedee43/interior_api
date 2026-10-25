package catalog

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/company"
	"github.com/Joedee43/interior_api/internal/httpapi"
)

const (
	codeSlugTaken         = "slug_taken"
	codeSlugLocked        = "slug_locked"
	codeInvalidTransition = "invalid_transition"
	codeNotDraft          = "product_not_draft"
)

type Handler struct {
	svc *Service
	// Injected from auth and company so catalog depends only on their
	// middleware, not their handlers.
	requireSession gin.HandlerFunc
	requireMember  gin.HandlerFunc
}

func NewHandler(svc *Service, requireSession, requireMember gin.HandlerFunc) *Handler {
	return &Handler{svc: svc, requireSession: requireSession, requireMember: requireMember}
}

func (h *Handler) Mount(v1 *gin.RouterGroup) {
	// Private: any active member of the company (admin or staff).
	p := v1.Group("/companies/:companyID/products", h.requireSession, h.requireMember)
	p.POST("", h.create)
	p.GET("", h.list)
	p.GET("/:productID", h.get)
	p.PATCH("/:productID", h.update)
	p.DELETE("/:productID", h.delete)
	for _, action := range []string{"publish", "archive", "restore"} {
		p.POST("/:productID/"+action, h.transition(action))
	}
	h.mountConfiguration(p)

	// Public: no session, published products of active companies only.
	pub := v1.Group("/catalog")
	pub.GET("/categories", h.categories)
	pub.GET("/products", h.publicList)
	pub.GET("/products/:companySlug/:productSlug", h.publicGet)
}

// ---- DTOs -------------------------------------------------------------
//
// Two response shapes for one Product: the public one omits lifecycle
// and internal fields, so a draft's details or a status can never leak
// through the catalog even if a query filter were wrong elsewhere.

type categoryDTO struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type priceDTO struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type dimensionsDTO struct {
	WidthMM  int32 `json:"width_mm"`
	DepthMM  int32 `json:"depth_mm"`
	HeightMM int32 `json:"height_mm"`
}

type sellerDTO struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type publicProductDTO struct {
	ID          string        `json:"id"`
	Slug        string        `json:"slug"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Category    categoryDTO   `json:"category"`
	Company     sellerDTO     `json:"company"`
	Price       priceDTO      `json:"price"`
	Dimensions  dimensionsDTO `json:"dimensions"`
	WeightG     *int32        `json:"weight_g"`
	PublishedAt *time.Time    `json:"published_at"`
}

type companyProductDTO struct {
	publicProductDTO           // embedded: its fields appear at the top level of the JSON
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func toPublicDTO(p Product) publicProductDTO {
	return publicProductDTO{
		ID: p.ID, Slug: p.Slug, Name: p.Name, Description: p.Description,
		Category:    categoryDTO{Slug: p.CategorySlug, Name: p.CategoryName},
		Company:     sellerDTO{Slug: p.CompanySlug, Name: p.CompanyName},
		Price:       priceDTO{AmountMinor: p.PriceMinor, Currency: p.Currency},
		Dimensions:  dimensionsDTO{WidthMM: p.WidthMM, DepthMM: p.DepthMM, HeightMM: p.HeightMM},
		WeightG:     p.WeightG,
		PublishedAt: p.PublishedAt,
	}
}

func toCompanyDTO(p Product) companyProductDTO {
	return companyProductDTO{publicProductDTO: toPublicDTO(p), Status: p.Status, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

// pageResponse is generic over the item DTO, so the public and private
// lists share one shape: {"products": [...], "next_cursor": "..." | null}.
type pageResponse[T any] struct {
	Products   []T     `json:"products"`
	NextCursor *string `json:"next_cursor"`
}

func toPage[T any](pg Page, conv func(Product) T) pageResponse[T] {
	items := make([]T, 0, len(pg.Products))
	for _, p := range pg.Products {
		items = append(items, conv(p))
	}
	var next *string
	if pg.NextCursor != "" {
		next = &pg.NextCursor
	}
	return pageResponse[T]{Products: items, NextCursor: next}
}

// ---- private handlers -------------------------------------------------

func member(c *gin.Context) company.Member {
	m, _ := company.MemberFrom(c.Request.Context()) // guaranteed by requireMember
	return m
}

type createRequest struct {
	Category    string `json:"category"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	PriceMinor  *int64 `json:"price_minor"`
	WidthMM     *int32 `json:"width_mm"`
	DepthMM     *int32 `json:"depth_mm"`
	HeightMM    *int32 `json:"height_mm"`
	WeightG     *int32 `json:"weight_g"`
}

func (h *Handler) create(c *gin.Context) {
	var req createRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	p, err := h.svc.Create(c.Request.Context(), member(c), ProductInput(req))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toCompanyDTO(p))
}

func (h *Handler) list(c *gin.Context) {
	limit, ok := intQuery(c, "limit")
	if !ok {
		return
	}
	pg, err := h.svc.List(c.Request.Context(), member(c), CompanyFilter{
		Status:   c.Query("status"),
		Category: c.Query("category"),
		Query:    c.Query("q"),
		Limit:    limit,
		Cursor:   c.Query("cursor"),
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toPage(pg, toCompanyDTO))
}

// productID validates the :productID path segment; a malformed id cannot
// exist, so it is a plain 404 (and never reaches PostgreSQL as bad uuid).
func productID(c *gin.Context) (string, bool) {
	id := c.Param("productID")
	if !httpapi.ValidUUID(id) {
		writeError(c, ErrNotFound)
		return "", false
	}
	return id, true
}

func (h *Handler) get(c *gin.Context) {
	id, ok := productID(c)
	if !ok {
		return
	}
	p, err := h.svc.Get(c.Request.Context(), member(c), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toCompanyDTO(p))
}

type updateRequest struct {
	Category    *string         `json:"category"`
	Name        *string         `json:"name"`
	Slug        *string         `json:"slug"`
	Description *string         `json:"description"`
	PriceMinor  *int64          `json:"price_minor"`
	WidthMM     *int32          `json:"width_mm"`
	DepthMM     *int32          `json:"depth_mm"`
	HeightMM    *int32          `json:"height_mm"`
	WeightG     Optional[int32] `json:"weight_g"`
}

func (h *Handler) update(c *gin.Context) {
	id, ok := productID(c)
	if !ok {
		return
	}
	var req updateRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	// The two structs have identical fields, so Go allows a direct type
	// conversion instead of copying field by field.
	p, err := h.svc.Update(c.Request.Context(), member(c), id, ProductUpdate(req))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toCompanyDTO(p))
}

// transition returns a handler for one lifecycle action — a closure that
// captures `action`, so three routes share one implementation.
func (h *Handler) transition(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := productID(c)
		if !ok {
			return
		}
		p, err := h.svc.Transition(c.Request.Context(), member(c), id, action)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, toCompanyDTO(p))
	}
}

func (h *Handler) delete(c *gin.Context) {
	id, ok := productID(c)
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), member(c), id); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- public handlers --------------------------------------------------

func (h *Handler) categories(c *gin.Context) {
	cs, err := h.svc.Categories(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]categoryDTO, 0, len(cs))
	for _, cat := range cs {
		out = append(out, categoryDTO{Slug: cat.Slug, Name: cat.Name})
	}
	c.JSON(http.StatusOK, gin.H{"categories": out})
}

func (h *Handler) publicList(c *gin.Context) {
	limit, ok := intQuery(c, "limit")
	if !ok {
		return
	}
	minPrice, ok := int64Query(c, "min_price")
	if !ok {
		return
	}
	maxPrice, ok := int64Query(c, "max_price")
	if !ok {
		return
	}
	pg, err := h.svc.PublicList(c.Request.Context(), PublicFilter{
		Category: c.Query("category"),
		Company:  c.Query("company"),
		Query:    c.Query("q"),
		Currency: c.Query("currency"),
		MinPrice: minPrice,
		MaxPrice: maxPrice,
		Sort:     c.Query("sort"),
		Limit:    limit,
		Cursor:   c.Query("cursor"),
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toPage(pg, toPublicDTO))
}

func (h *Handler) publicGet(c *gin.Context) {
	p, cfg, err := h.svc.PublicDetail(c.Request.Context(), c.Param("companySlug"), c.Param("productSlug"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, publicDetailDTO{toPublicDTO(p), toConfigurationDTO(cfg, p.Currency)})
}

// ---- helpers ----------------------------------------------------------

// intQuery parses an optional integer query parameter (0 when absent).
func intQuery(c *gin.Context, name string) (int, bool) {
	raw := c.Query(name)
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		httpapi.WriteValidationError(c, map[string]string{name: "must be a whole number"})
		return 0, false
	}
	return n, true
}

// int64Query parses an optional int64 query parameter (nil when absent).
func int64Query(c *gin.Context, name string) (*int64, bool) {
	raw := c.Query(name)
	if raw == "" {
		return nil, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		httpapi.WriteValidationError(c, map[string]string{name: "must be a whole number in minor units"})
		return nil, false
	}
	return &n, true
}

func writeError(c *gin.Context, err error) {
	var limitErr *LimitError
	var verr *auth.ValidationError
	switch {
	case errors.As(err, &verr):
		httpapi.WriteValidationError(c, verr.Fields)
	case errors.Is(err, ErrNotFound):
		httpapi.WriteError(c, http.StatusNotFound, httpapi.CodeNotFound, "product not found")
	case errors.Is(err, ErrSlugTaken):
		httpapi.WriteError(c, http.StatusConflict, codeSlugTaken, "another product in this company already uses this slug")
	case errors.Is(err, ErrSlugLocked):
		httpapi.WriteError(c, http.StatusConflict, codeSlugLocked, "the slug can only change while the product is a draft")
	case errors.Is(err, ErrInvalidTransition):
		httpapi.WriteError(c, http.StatusConflict, codeInvalidTransition, "the product's current status does not allow this action")
	case errors.Is(err, ErrNotDraft):
		httpapi.WriteError(c, http.StatusConflict, codeNotDraft, "only draft products can be deleted; archive it instead")
	case errors.Is(err, ErrOptionNotFound):
		httpapi.WriteError(c, http.StatusNotFound, httpapi.CodeNotFound, "option not found")
	case errors.Is(err, ErrValueNotFound):
		httpapi.WriteError(c, http.StatusNotFound, httpapi.CodeNotFound, "option value not found")
	case errors.Is(err, ErrVariantNotFound):
		httpapi.WriteError(c, http.StatusNotFound, httpapi.CodeNotFound, "variant not found")
	case errors.Is(err, ErrOptionNameTaken):
		httpapi.WriteError(c, http.StatusConflict, "option_name_taken", "this product already has an option with that name")
	case errors.Is(err, ErrValueNameTaken):
		httpapi.WriteError(c, http.StatusConflict, "value_name_taken", "this option already has a value with that name")
	case errors.Is(err, ErrSKUTaken):
		httpapi.WriteError(c, http.StatusConflict, "sku_taken", "another variant in this company already uses this SKU")
	case errors.Is(err, ErrDuplicateCombination):
		httpapi.WriteError(c, http.StatusConflict, "duplicate_combination", "a variant with this combination already exists")
	case errors.Is(err, ErrOptionsLocked):
		httpapi.WriteError(c, http.StatusConflict, "options_locked", "options can only be added or removed on a draft product without variants")
	case errors.Is(err, ErrValueInUse):
		httpapi.WriteError(c, http.StatusConflict, "value_in_use", "a variant uses this value; delete or change that variant first")
	case errors.Is(err, ErrLastValue):
		httpapi.WriteError(c, http.StatusConflict, "last_value", "an option must keep at least one value")
	case errors.Is(err, ErrPriceFromVariants):
		httpapi.WriteError(c, http.StatusConflict, "price_from_variants", "this product's price is the cheapest available variant; change variant prices instead")
	case errors.Is(err, ErrNoAvailableVariants):
		httpapi.WriteError(c, http.StatusConflict, "no_available_variants", "a product with options needs at least one available variant to be published")
	case errors.Is(err, ErrVariantNotDeletable):
		httpapi.WriteError(c, http.StatusConflict, codeNotDraft, "variants can only be deleted while the product is a draft; set available to false instead")
	case errors.As(err, &limitErr):
		httpapi.WriteError(c, http.StatusConflict, "limit_reached", "limit reached: "+limitErr.Error())
	default:
		httpapi.WriteInternalError(c, err)
	}
}
