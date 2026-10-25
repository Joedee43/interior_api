package design

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/catalog"
	"github.com/Joedee43/interior_api/internal/httpapi"
)

type Handler struct {
	svc            *Service
	requireSession gin.HandlerFunc
}

func NewHandler(svc *Service, requireSession gin.HandlerFunc) *Handler {
	return &Handler{svc: svc, requireSession: requireSession}
}

func (h *Handler) Mount(v1 *gin.RouterGroup) {
	v1.GET("/room-templates", h.publicTemplates)
	v1.GET("/room-templates/:slug", h.publicTemplate)

	admin := v1.Group("/admin/room-templates", h.requireSession, auth.RequirePlatformAdmin())
	admin.GET("", h.adminTemplates)
	admin.POST("", h.createTemplate)
	admin.PATCH("/:templateID", h.updateTemplate)

	// Any logged-in user (customer or company member) can keep designs;
	// every route is scoped to the session's user.
	d := v1.Group("/designs", h.requireSession)
	d.POST("", h.create)
	d.GET("", h.list)
	d.GET("/:designID", h.get)
	d.PATCH("/:designID", h.update)
	d.DELETE("/:designID", h.delete)
	d.POST("/:designID/items", h.addItem)
	d.PATCH("/:designID/items/:itemID", h.updateItem)
	d.DELETE("/:designID/items/:itemID", h.deleteItem)
}

// ---- DTOs -------------------------------------------------------------

type roomDTO struct {
	WidthMM  int32 `json:"width_mm"`
	DepthMM  int32 `json:"depth_mm"`
	HeightMM int32 `json:"height_mm"`
}

func toRoomDTO(r Room) roomDTO { return roomDTO(r) }

type templateDTO struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Room        roomDTO   `json:"room"`
	Features    Features  `json:"features"`
	Status      string    `json:"status,omitempty"` // admin responses only
	SortOrder   int       `json:"sort_order"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toTemplateDTO(t Template, admin bool) templateDTO {
	d := templateDTO{ID: t.ID, Slug: t.Slug, Name: t.Name, Description: t.Description,
		Room: toRoomDTO(t.Room), Features: t.Features, SortOrder: t.SortOrder, UpdatedAt: t.UpdatedAt}
	if admin {
		d.Status = t.Status
	}
	return d
}

type designSummaryDTO struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	TemplateID *string   `json:"template_id"`
	Room       roomDTO   `json:"room"`
	ItemCount  int       `json:"item_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toSummaryDTO(d Design) designSummaryDTO {
	return designSummaryDTO{ID: d.ID, Name: d.Name, TemplateID: d.TemplateID, Room: toRoomDTO(d.Room),
		ItemCount: d.ItemCount, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
}

type productRefDTO struct {
	ID              string            `json:"id"`
	Slug            string            `json:"slug"`
	Name            string            `json:"name"`
	CompanySlug     string            `json:"company_slug"`
	CompanyName     string            `json:"company_name"`
	VariantID       *string           `json:"variant_id"`
	Options         map[string]string `json:"options"`
	Price           priceDTO          `json:"price"`
	Dimensions      roomDTO           `json:"dimensions"`
	PrimaryImageURL *string           `json:"primary_image_url"`
	ModelURL        *string           `json:"model_url"`
}

type priceDTO struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type itemDTO struct {
	ID          string        `json:"id"`
	Position    positionDTO   `json:"position"`
	RotationDeg float64       `json:"rotation_deg"`
	Available   bool          `json:"available"`
	Product     productRefDTO `json:"product"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

type positionDTO struct {
	XMM int32 `json:"x_mm"`
	YMM int32 `json:"y_mm"`
	ZMM int32 `json:"z_mm"`
}

func toItemDTO(p PlacedItem) itemDTO {
	info := p.Info
	opts := info.Options
	if opts == nil {
		opts = map[string]string{}
	}
	return itemDTO{
		ID:          p.ID,
		Position:    positionDTO{p.XMM, p.YMM, p.ZMM},
		RotationDeg: p.RotationDeg,
		Available:   info.Available,
		Product: productRefDTO{
			ID: p.ProductID, Slug: info.ProductSlug, Name: info.ProductName,
			CompanySlug: info.CompanySlug, CompanyName: info.CompanyName,
			VariantID: p.VariantID, Options: opts,
			Price:           priceDTO{AmountMinor: info.PriceMinor, Currency: info.Currency},
			Dimensions:      roomDTO{info.WidthMM, info.DepthMM, info.HeightMM},
			PrimaryImageURL: info.PrimaryImageURL, ModelURL: info.ModelURL,
		},
		UpdatedAt: p.UpdatedAt,
	}
}

type totalDTO struct {
	CompanySlug string `json:"company_slug"`
	CompanyName string `json:"company_name"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
	Items       int    `json:"items"`
}

type estimateDTO struct {
	Note             string     `json:"note"`
	ItemCount        int        `json:"item_count"`
	UnavailableCount int        `json:"unavailable_count"`
	Totals           []totalDTO `json:"totals"`
}

type detailDTO struct {
	designSummaryDTO
	Features Features    `json:"features"`
	Items    []itemDTO   `json:"items"`
	Estimate estimateDTO `json:"estimate"`
}

func toDetailDTO(d Detail) detailDTO {
	items := make([]itemDTO, len(d.Items))
	for i, it := range d.Items {
		items[i] = toItemDTO(it)
	}
	totals := make([]totalDTO, len(d.Estimate.Totals))
	for i, t := range d.Estimate.Totals {
		totals[i] = totalDTO(t) // identical field sets: a direct conversion
	}
	return detailDTO{
		designSummaryDTO: toSummaryDTO(d.Design),
		Features:         d.Features,
		Items:            items,
		Estimate: estimateDTO{
			Note:             "Estimate at current catalog prices for available items; not a quote.",
			ItemCount:        d.Estimate.ItemCount,
			UnavailableCount: d.Estimate.UnavailableCount,
			Totals:           totals,
		},
	}
}

// ---- helpers ----------------------------------------------------------

func userID(c *gin.Context) string {
	p, _ := auth.PrincipalFrom(c.Request.Context()) // guaranteed by requireSession
	return p.UserID
}

func principal(c *gin.Context) auth.Principal {
	p, _ := auth.PrincipalFrom(c.Request.Context())
	return p
}

// ids validates path ids; a malformed id cannot exist, so it is a 404.
func ids(c *gin.Context, names map[string]error) bool {
	for name, notFound := range names {
		if !httpapi.ValidUUID(c.Param(name)) {
			writeError(c, notFound)
			return false
		}
	}
	return true
}

// ---- templates --------------------------------------------------------

func (h *Handler) publicTemplates(c *gin.Context) {
	ts, err := h.svc.PublicTemplates(c.Request.Context())
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]templateDTO, len(ts))
	for i, t := range ts {
		out[i] = toTemplateDTO(t, false)
	}
	c.JSON(http.StatusOK, gin.H{"templates": out})
}

func (h *Handler) publicTemplate(c *gin.Context) {
	t, err := h.svc.PublicTemplate(c.Request.Context(), c.Param("slug"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toTemplateDTO(t, false))
}

func (h *Handler) adminTemplates(c *gin.Context) {
	ts, err := h.svc.AdminTemplates(c.Request.Context(), principal(c))
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]templateDTO, len(ts))
	for i, t := range ts {
		out[i] = toTemplateDTO(t, true)
	}
	c.JSON(http.StatusOK, gin.H{"templates": out})
}

type createTemplateRequest struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	WidthMM     int32    `json:"width_mm"`
	DepthMM     int32    `json:"depth_mm"`
	HeightMM    int32    `json:"height_mm"`
	Features    Features `json:"features"`
	Status      string   `json:"status"`
	SortOrder   int      `json:"sort_order"`
}

func (h *Handler) createTemplate(c *gin.Context) {
	var req createTemplateRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	t, err := h.svc.CreateTemplate(c.Request.Context(), principal(c), TemplateInput{
		Slug: req.Slug, Name: req.Name, Description: req.Description,
		Room:     Room{req.WidthMM, req.DepthMM, req.HeightMM},
		Features: req.Features, Status: req.Status, SortOrder: req.SortOrder,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toTemplateDTO(t, true))
}

type updateTemplateRequest struct {
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	WidthMM     *int32    `json:"width_mm"`
	DepthMM     *int32    `json:"depth_mm"`
	HeightMM    *int32    `json:"height_mm"`
	Features    *Features `json:"features"`
	Status      *string   `json:"status"`
	SortOrder   *int      `json:"sort_order"`
}

func (h *Handler) updateTemplate(c *gin.Context) {
	if !ids(c, map[string]error{"templateID": ErrTemplateNotFound}) {
		return
	}
	var req updateTemplateRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	t, err := h.svc.UpdateTemplate(c.Request.Context(), principal(c), c.Param("templateID"), TemplateUpdate(req))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toTemplateDTO(t, true))
}

// ---- designs ----------------------------------------------------------

type createRequest struct {
	Name     string    `json:"name"`
	Template string    `json:"template"`
	WidthMM  *int32    `json:"width_mm"`
	DepthMM  *int32    `json:"depth_mm"`
	HeightMM *int32    `json:"height_mm"`
	Features *Features `json:"features"`
}

func (h *Handler) create(c *gin.Context) {
	var req createRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	in := CreateInput{Name: req.Name, Template: req.Template, Features: req.Features}
	if req.WidthMM != nil || req.DepthMM != nil || req.HeightMM != nil {
		in.Room = &Room{}
		if req.WidthMM != nil {
			in.Room.WidthMM = *req.WidthMM
		}
		if req.DepthMM != nil {
			in.Room.DepthMM = *req.DepthMM
		}
		if req.HeightMM != nil {
			in.Room.HeightMM = *req.HeightMM
		}
	}
	d, err := h.svc.Create(c.Request.Context(), userID(c), in)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toDetailDTO(d))
}

func (h *Handler) list(c *gin.Context) {
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			httpapi.WriteValidationError(c, map[string]string{"limit": "must be a whole number"})
			return
		}
		limit = n
	}
	pg, err := h.svc.List(c.Request.Context(), userID(c), limit, c.Query("cursor"))
	if err != nil {
		writeError(c, err)
		return
	}
	out := make([]designSummaryDTO, len(pg.Designs))
	for i, d := range pg.Designs {
		out[i] = toSummaryDTO(d)
	}
	var next *string
	if pg.NextCursor != "" {
		next = &pg.NextCursor
	}
	c.JSON(http.StatusOK, gin.H{"designs": out, "next_cursor": next})
}

func (h *Handler) get(c *gin.Context) {
	if !ids(c, map[string]error{"designID": ErrNotFound}) {
		return
	}
	d, err := h.svc.Get(c.Request.Context(), userID(c), c.Param("designID"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toDetailDTO(d))
}

type updateRequest struct {
	Name     *string   `json:"name"`
	WidthMM  *int32    `json:"width_mm"`
	DepthMM  *int32    `json:"depth_mm"`
	HeightMM *int32    `json:"height_mm"`
	Features *Features `json:"features"`
}

func (h *Handler) update(c *gin.Context) {
	if !ids(c, map[string]error{"designID": ErrNotFound}) {
		return
	}
	var req updateRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	d, err := h.svc.Update(c.Request.Context(), userID(c), c.Param("designID"), DesignUpdate(req))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toDetailDTO(d))
}

func (h *Handler) delete(c *gin.Context) {
	if !ids(c, map[string]error{"designID": ErrNotFound}) {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), userID(c), c.Param("designID")); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type addItemRequest struct {
	ProductID   string   `json:"product_id"`
	VariantID   *string  `json:"variant_id"`
	XMM         *int32   `json:"x_mm"`
	YMM         *int32   `json:"y_mm"`
	ZMM         *int32   `json:"z_mm"`
	RotationDeg *float64 `json:"rotation_deg"`
}

func (h *Handler) addItem(c *gin.Context) {
	if !ids(c, map[string]error{"designID": ErrNotFound}) {
		return
	}
	var req addItemRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	// Malformed ids are a field error, not a 404: the design exists.
	p := map[string]string{}
	if !httpapi.ValidUUID(req.ProductID) {
		p["product_id"] = "must be a product id"
	}
	if req.VariantID != nil && !httpapi.ValidUUID(*req.VariantID) {
		p["variant_id"] = "must be a variant id"
	}
	if len(p) > 0 {
		httpapi.WriteValidationError(c, p)
		return
	}
	item, err := h.svc.AddItem(c.Request.Context(), userID(c), c.Param("designID"), ItemInput(req))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toItemDTO(item))
}

type updateItemRequest struct {
	XMM         *int32   `json:"x_mm"`
	YMM         *int32   `json:"y_mm"`
	ZMM         *int32   `json:"z_mm"`
	RotationDeg *float64 `json:"rotation_deg"`
	VariantID   *string  `json:"variant_id"`
}

func (h *Handler) updateItem(c *gin.Context) {
	if !ids(c, map[string]error{"designID": ErrNotFound, "itemID": ErrItemNotFound}) {
		return
	}
	var req updateItemRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	if req.VariantID != nil && !httpapi.ValidUUID(*req.VariantID) {
		httpapi.WriteValidationError(c, map[string]string{"variant_id": "must be a variant id"})
		return
	}
	item, err := h.svc.UpdateItem(c.Request.Context(), userID(c), c.Param("designID"), c.Param("itemID"), ItemUpdate(req))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, toItemDTO(item))
}

func (h *Handler) deleteItem(c *gin.Context) {
	if !ids(c, map[string]error{"designID": ErrNotFound, "itemID": ErrItemNotFound}) {
		return
	}
	if err := h.svc.DeleteItem(c.Request.Context(), userID(c), c.Param("designID"), c.Param("itemID")); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---- errors -----------------------------------------------------------

func writeError(c *gin.Context, err error) {
	var verr *auth.ValidationError
	var placement *catalog.PlacementError
	switch {
	case errors.As(err, &verr):
		httpapi.WriteValidationError(c, verr.Fields)
	case errors.As(err, &placement):
		httpapi.WriteValidationError(c, map[string]string{placement.Field: placement.Reason})
	case errors.Is(err, ErrNotFound):
		httpapi.WriteError(c, http.StatusNotFound, httpapi.CodeNotFound, "design not found")
	case errors.Is(err, ErrItemNotFound):
		httpapi.WriteError(c, http.StatusNotFound, httpapi.CodeNotFound, "design item not found")
	case errors.Is(err, ErrTemplateNotFound):
		httpapi.WriteError(c, http.StatusNotFound, httpapi.CodeNotFound, "room template not found")
	case errors.Is(err, ErrTemplateSlugTaken):
		httpapi.WriteError(c, http.StatusConflict, "slug_taken", "a room template already uses this slug")
	case errors.Is(err, ErrItemsOutsideRoom):
		httpapi.WriteError(c, http.StatusConflict, "items_outside_room", "some placed items would be outside the resized room; move them first")
	case errors.Is(err, ErrTooManyItems):
		httpapi.WriteError(c, http.StatusConflict, "limit_reached", "a design can hold at most 200 items")
	case errors.Is(err, auth.ErrForbidden):
		httpapi.WriteError(c, http.StatusForbidden, httpapi.CodeForbidden, "you do not have permission to do this")
	default:
		httpapi.WriteInternalError(c, err)
	}
}
