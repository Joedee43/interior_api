package quote

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/company"
	"github.com/Joedee43/interior_api/internal/design"
	"github.com/Joedee43/interior_api/internal/httpapi"
)

type Handler struct {
	svc            *Service
	requireSession gin.HandlerFunc
	requireMember  gin.HandlerFunc
	submitLimit    *httpapi.KeyedLimiter
}

// DefaultSubmitLimit allows ~20 submissions per hour per user.
func DefaultSubmitLimit() *httpapi.KeyedLimiter { return httpapi.NewKeyedLimiter(3*time.Minute, 20) }

func NewHandler(svc *Service, requireSession, requireMember gin.HandlerFunc, submitLimit *httpapi.KeyedLimiter) *Handler {
	return &Handler{svc: svc, requireSession: requireSession, requireMember: requireMember, submitLimit: submitLimit}
}

func (h *Handler) Mount(v1 *gin.RouterGroup) {
	// Customer side: the session's user is the customer.
	c := v1.Group("/quote-requests", h.requireSession)
	c.POST("/from-design", h.limited(h.fromDesign))
	c.POST("", h.limited(h.fromItems))
	c.GET("", h.customerList)
	c.GET("/:requestID", h.customerGet)
	c.POST("/:requestID/accept", h.accept)
	c.POST("/:requestID/reject", h.customerAction("reject"))
	c.POST("/:requestID/cancel", h.customerAction("cancel"))

	// Company side: any active member of the company.
	m := v1.Group("/companies/:companyID/quote-requests", h.requireSession, h.requireMember)
	m.GET("", h.companyList)
	m.GET("/:requestID", h.companyGet)
	m.POST("/:requestID/review", h.review)
	m.PUT("/:requestID/quote", h.sendQuote)
	m.POST("/:requestID/decline", h.decline)
}

// ---- DTOs -------------------------------------------------------------

type lineDTO struct {
	ID              string            `json:"id"`
	ProductID       *string           `json:"product_id"`
	VariantID       *string           `json:"variant_id"`
	ProductName     string            `json:"product_name"`
	ProductSlug     string            `json:"product_slug"`
	SKU             *string           `json:"sku"`
	Options         map[string]string `json:"options"`
	Dimensions      dimensionsDTO     `json:"dimensions"`
	Quantity        int               `json:"quantity"`
	ListUnitPrice   int64             `json:"list_unit_price_minor"`
	QuotedUnitPrice *int64            `json:"quoted_unit_price_minor"`
	Note            string            `json:"note"`
}

type dimensionsDTO struct {
	WidthMM  int32 `json:"width_mm"`
	DepthMM  int32 `json:"depth_mm"`
	HeightMM int32 `json:"height_mm"`
}

type chargeDTO struct {
	Label       string `json:"label"`
	AmountMinor int64  `json:"amount_minor"`
}

type requestDTO struct {
	ID           string  `json:"id"`
	SubmissionID string  `json:"submission_id"`
	DesignID     *string `json:"design_id"`
	Status       string  `json:"status"`
	Revision     int     `json:"revision"`
	Final        bool    `json:"final"`
	Company      struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"company"`
	Customer struct {
		Name            string `json:"name"`
		Email           string `json:"email"`
		Phone           string `json:"phone"`
		DeliveryAddress string `json:"delivery_address"`
		Note            string `json:"note"`
	} `json:"customer"`
	Currency    string     `json:"currency"`
	ListTotal   int64      `json:"list_total_minor"`
	QuotedTotal *int64     `json:"quoted_total_minor"`
	ValidUntil  *string    `json:"valid_until"` // "YYYY-MM-DD"
	Expired     bool       `json:"expired"`
	CompanyNote string     `json:"company_note"`
	LineCount   int        `json:"line_count"`
	SubmittedAt time.Time  `json:"submitted_at"`
	QuotedAt    *time.Time `json:"quoted_at"`
	DecidedAt   *time.Time `json:"decided_at"`
}

// requestDetailDTO is one request with its lines and charges. Embedding
// requestDTO (no field name, just the type) promotes its fields, so
// encoding/json writes them into the same flat object, like spreading an
// object in JavaScript. Lines and charges have no omitempty: the frontend
// requires both arrays, and an unquoted request has zero charges, which
// omitempty would have dropped from the JSON entirely.
type requestDetailDTO struct {
	requestDTO
	Lines   []lineDTO   `json:"lines"`
	Charges []chargeDTO `json:"charges"`
}

// toSummaryDTO is the list-row shape: no lines or charges.
func toSummaryDTO(r Request) requestDTO {
	d := requestDTO{
		ID: r.ID, SubmissionID: r.SubmissionID, DesignID: r.DesignID, Status: r.Status, Revision: r.Revision,
		Final: IsFinal(r.Status), Currency: r.Currency, ListTotal: r.ListTotal, QuotedTotal: r.QuotedTotal,
		Expired: r.Expired, CompanyNote: r.CompanyNote, LineCount: r.LineCount,
		SubmittedAt: r.SubmittedAt, QuotedAt: r.QuotedAt, DecidedAt: r.DecidedAt,
	}
	d.Company.ID, d.Company.Slug, d.Company.Name = r.CompanyID, r.CompanySlug, r.CompanyName
	d.Customer.Name, d.Customer.Email, d.Customer.Phone = r.ContactName, r.ContactEmail, r.ContactPhone
	d.Customer.DeliveryAddress, d.Customer.Note = r.DeliveryAddress, r.CustomerNote
	if r.ValidUntil != nil {
		s := r.ValidUntil.Format(time.DateOnly)
		d.ValidUntil = &s
	}
	return d
}

// toDetailDTO always builds non-nil slices: make(..., 0) encodes as [],
// while a nil slice would encode as null.
func toDetailDTO(r Request) requestDetailDTO {
	d := requestDetailDTO{
		requestDTO: toSummaryDTO(r),
		Lines:      make([]lineDTO, len(r.Lines)),
		Charges:    make([]chargeDTO, len(r.Charges)),
	}
	for i, l := range r.Lines {
		opts := l.Options
		if opts == nil {
			opts = map[string]string{}
		}
		d.Lines[i] = lineDTO{
			ID: l.ID, ProductID: l.ProductID, VariantID: l.VariantID, ProductName: l.ProductName,
			ProductSlug: l.ProductSlug, SKU: l.SKU, Options: opts,
			Dimensions: dimensionsDTO{l.WidthMM, l.DepthMM, l.HeightMM},
			Quantity:   l.Quantity, ListUnitPrice: l.ListUnit, QuotedUnitPrice: l.QuotedUnit, Note: l.Note,
		}
	}
	for i, c := range r.Charges {
		d.Charges[i] = chargeDTO(c)
	}
	return d
}

// ---- helpers ----------------------------------------------------------

func userID(c *gin.Context) string {
	p, _ := auth.PrincipalFrom(c.Request.Context())
	return p.UserID
}

func member(c *gin.Context) company.Member {
	m, _ := company.MemberFrom(c.Request.Context())
	return m
}

func requestID(c *gin.Context) (string, bool) {
	id := c.Param("requestID")
	if !httpapi.ValidUUID(id) {
		writeError(c, ErrNotFound)
		return "", false
	}
	return id, true
}

// limited applies the per-user submission limit (keyed by user id, not
// IP, since the caller is always logged in here).
func (h *Handler) limited(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !h.submitLimit.Allow("quote-submit:" + userID(c)) {
			httpapi.WriteRateLimited(c)
			return
		}
		next(c)
	}
}

func listParams(c *gin.Context) (int, bool) {
	raw := c.Query("limit")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		httpapi.WriteValidationError(c, map[string]string{"limit": "must be a whole number"})
		return 0, false
	}
	return n, true
}

func respondPage(c *gin.Context, pg Page) {
	out := make([]requestDTO, len(pg.Requests))
	for i, r := range pg.Requests {
		out[i] = toSummaryDTO(r)
	}
	var next *string
	if pg.NextCursor != "" {
		next = &pg.NextCursor
	}
	c.JSON(http.StatusOK, gin.H{"requests": out, "next_cursor": next})
}

// ---- customer handlers -----------------------------------------------

type contactFields struct {
	ContactPhone    string `json:"contact_phone"`
	DeliveryAddress string `json:"delivery_address"`
	Note            string `json:"note"`
}

func (f contactFields) contact() Contact {
	return Contact{Phone: f.ContactPhone, DeliveryAddress: f.DeliveryAddress, Note: f.Note}
}

type fromDesignRequest struct {
	DesignID string `json:"design_id"`
	contactFields
}

type skippedDTO struct {
	ItemID      string `json:"item_id"`
	ProductName string `json:"product_name"`
}

func respondSubmission(c *gin.Context, sub Submission) {
	reqs := make([]requestDetailDTO, len(sub.Requests))
	for i, r := range sub.Requests {
		reqs[i] = toDetailDTO(r)
	}
	skipped := make([]skippedDTO, len(sub.Skipped))
	for i, s := range sub.Skipped {
		skipped[i] = skippedDTO(s)
	}
	c.JSON(http.StatusCreated, gin.H{"submission_id": sub.ID, "requests": reqs, "skipped": skipped})
}

func (h *Handler) fromDesign(c *gin.Context) {
	var req fromDesignRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	if !httpapi.ValidUUID(req.DesignID) {
		httpapi.WriteValidationError(c, map[string]string{"design_id": "must be one of your design ids"})
		return
	}
	sub, err := h.svc.SubmitFromDesign(c.Request.Context(), userID(c), req.DesignID, req.contact())
	if err != nil {
		writeError(c, err)
		return
	}
	respondSubmission(c, sub)
}

type fromItemsRequest struct {
	Items []struct {
		ProductID string `json:"product_id"`
		VariantID string `json:"variant_id"`
		Quantity  int    `json:"quantity"`
	} `json:"items"`
	contactFields
}

func (h *Handler) fromItems(c *gin.Context) {
	var req fromItemsRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	items := make([]RequestedItem, len(req.Items))
	p := map[string]string{}
	for i, it := range req.Items {
		if !httpapi.ValidUUID(it.ProductID) {
			p["items["+strconv.Itoa(i)+"].product_id"] = "must be a product id"
		}
		if it.VariantID != "" && !httpapi.ValidUUID(it.VariantID) {
			p["items["+strconv.Itoa(i)+"].variant_id"] = "must be a variant id"
		}
		items[i] = RequestedItem(it)
	}
	if len(p) > 0 {
		httpapi.WriteValidationError(c, p)
		return
	}
	sub, err := h.svc.SubmitItems(c.Request.Context(), userID(c), items, req.contact())
	if err != nil {
		writeError(c, err)
		return
	}
	respondSubmission(c, sub)
}

func (h *Handler) customerList(c *gin.Context) {
	limit, ok := listParams(c)
	if !ok {
		return
	}
	pg, err := h.svc.CustomerList(c.Request.Context(), userID(c), c.Query("status"), limit, c.Query("cursor"))
	if err != nil {
		writeError(c, err)
		return
	}
	respondPage(c, pg)
}

func (h *Handler) customerGet(c *gin.Context) {
	id, ok := requestID(c)
	if !ok {
		return
	}
	r, err := h.svc.CustomerGet(c.Request.Context(), userID(c), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toDetailDTO(r))
}

type acceptRequest struct {
	Revision *int `json:"revision"`
}

func (h *Handler) accept(c *gin.Context) {
	id, ok := requestID(c)
	if !ok {
		return
	}
	var req acceptRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	if req.Revision == nil {
		httpapi.WriteValidationError(c, map[string]string{"revision": "required: the revision you are accepting"})
		return
	}
	r, err := h.svc.Accept(c.Request.Context(), userID(c), id, *req.Revision)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toDetailDTO(r))
}

func (h *Handler) customerAction(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := requestID(c)
		if !ok {
			return
		}
		var (
			r   Request
			err error
		)
		if action == "reject" {
			r, err = h.svc.Reject(c.Request.Context(), userID(c), id)
		} else {
			r, err = h.svc.Cancel(c.Request.Context(), userID(c), id)
		}
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, toDetailDTO(r))
	}
}

// ---- company handlers ------------------------------------------------

func (h *Handler) companyList(c *gin.Context) {
	limit, ok := listParams(c)
	if !ok {
		return
	}
	pg, err := h.svc.CompanyList(c.Request.Context(), member(c), c.Query("status"), limit, c.Query("cursor"))
	if err != nil {
		writeError(c, err)
		return
	}
	respondPage(c, pg)
}

func (h *Handler) companyGet(c *gin.Context) {
	id, ok := requestID(c)
	if !ok {
		return
	}
	r, err := h.svc.CompanyGet(c.Request.Context(), member(c), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toDetailDTO(r))
}

func (h *Handler) review(c *gin.Context) {
	id, ok := requestID(c)
	if !ok {
		return
	}
	r, err := h.svc.Review(c.Request.Context(), member(c), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toDetailDTO(r))
}

type sendQuoteRequest struct {
	Lines []struct {
		LineID         string `json:"line_id"`
		UnitPriceMinor int64  `json:"unit_price_minor"`
		Note           string `json:"note"`
	} `json:"lines"`
	Charges []struct {
		Label       string `json:"label"`
		AmountMinor int64  `json:"amount_minor"`
	} `json:"charges"`
	ValidUntil string `json:"valid_until"`
	Note       string `json:"note"`
}

func (h *Handler) sendQuote(c *gin.Context) {
	id, ok := requestID(c)
	if !ok {
		return
	}
	var req sendQuoteRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	in := QuoteInput{ValidUntil: req.ValidUntil, Note: req.Note}
	for _, l := range req.Lines {
		in.Lines = append(in.Lines, PricedLine(l))
	}
	for _, ch := range req.Charges {
		in.Charges = append(in.Charges, Charge(ch))
	}
	r, err := h.svc.SendQuote(c.Request.Context(), member(c), id, in)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toDetailDTO(r))
}

type declineRequest struct {
	Note string `json:"note"`
}

func (h *Handler) decline(c *gin.Context) {
	id, ok := requestID(c)
	if !ok {
		return
	}
	var req declineRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	r, err := h.svc.Decline(c.Request.Context(), member(c), id, req.Note)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toDetailDTO(r))
}

// ---- errors -----------------------------------------------------------

func writeError(c *gin.Context, err error) {
	var verr *auth.ValidationError
	switch {
	case errors.As(err, &verr):
		httpapi.WriteValidationError(c, verr.Fields)
	case errors.Is(err, ErrNotFound):
		httpapi.WriteError(c, http.StatusNotFound, httpapi.CodeNotFound, "quote request not found")
	case errors.Is(err, design.ErrNotFound):
		httpapi.WriteError(c, http.StatusNotFound, httpapi.CodeNotFound, "design not found")
	case errors.Is(err, ErrInvalidTransition):
		httpapi.WriteError(c, http.StatusConflict, "invalid_transition", "the request's status does not allow this action")
	case errors.Is(err, ErrStaleRevision):
		httpapi.WriteError(c, http.StatusConflict, "stale_revision", "the quote was revised; review the latest revision before accepting")
	case errors.Is(err, ErrQuoteExpired):
		httpapi.WriteError(c, http.StatusConflict, "quote_expired", "this quote is past its valid-until date")
	case errors.Is(err, ErrNothingAvailable):
		httpapi.WriteError(c, http.StatusBadRequest, "nothing_available", "none of the design's items are currently available")
	default:
		httpapi.WriteInternalError(c, err)
	}
}
