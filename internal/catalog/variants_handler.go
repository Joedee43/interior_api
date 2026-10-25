package catalog

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Joedee43/interior_api/internal/httpapi"
)

// mountConfiguration registers option/value/variant routes on the
// company products group (behind session + membership middleware).
func (h *Handler) mountConfiguration(p *gin.RouterGroup) {
	p.GET("/:productID/configuration", h.configuration)

	p.POST("/:productID/options", h.addOption)
	p.PATCH("/:productID/options/:optionID", h.updateOption)
	p.DELETE("/:productID/options/:optionID", h.deleteOption)

	p.POST("/:productID/options/:optionID/values", h.addValue)
	p.PATCH("/:productID/options/:optionID/values/:valueID", h.updateValue)
	p.DELETE("/:productID/options/:optionID/values/:valueID", h.deleteValue)

	p.POST("/:productID/variants", h.createVariant)
	p.POST("/:productID/variants/generate", h.generateVariants)
	p.PATCH("/:productID/variants/:variantID", h.updateVariant)
	p.DELETE("/:productID/variants/:variantID", h.deleteVariant)
}

// ---- DTOs -------------------------------------------------------------

type valueDTO struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Position    int     `json:"position"`
	SwatchHex   *string `json:"swatch_hex"`
	MaterialKey *string `json:"material_key"`
}

type optionDTO struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Position int        `json:"position"`
	Values   []valueDTO `json:"values"`
}

type variantDTO struct {
	ID        string   `json:"id"`
	SKU       string   `json:"sku"`
	Price     priceDTO `json:"price"`
	Available bool     `json:"available"`
	Position  int      `json:"position"`
	// Options maps option name -> value name, e.g. {"Fabric":"Linen"},
	// for display; ValueIDs are what a client stores or sends back.
	Options  map[string]string `json:"options"`
	ValueIDs []string          `json:"value_ids"`
}

type configurationDTO struct {
	Options  []optionDTO  `json:"options"`
	Variants []variantDTO `json:"variants"`
}

func toConfigurationDTO(cfg Configuration, currency string) configurationDTO {
	out := configurationDTO{
		Options:  make([]optionDTO, 0, len(cfg.Options)),
		Variants: make([]variantDTO, 0, len(cfg.Variants)),
	}
	optionName := make(map[string]string, len(cfg.Options))
	for _, o := range cfg.Options {
		optionName[o.ID] = o.Name
		vals := make([]valueDTO, 0, len(o.Values))
		for _, v := range o.Values {
			vals = append(vals, valueDTO{ID: v.ID, Name: v.Name, Position: v.Position, SwatchHex: v.SwatchHex, MaterialKey: v.MaterialKey})
		}
		out.Options = append(out.Options, optionDTO{ID: o.ID, Name: o.Name, Position: o.Position, Values: vals})
	}
	values := cfg.valueByID()
	for _, v := range cfg.Variants {
		d := variantDTO{
			ID: v.ID, SKU: v.SKU, Available: v.Available, Position: v.Position,
			Price:    priceDTO{AmountMinor: v.PriceMinor, Currency: currency},
			Options:  make(map[string]string, len(v.Selections)),
			ValueIDs: make([]string, 0, len(v.Selections)),
		}
		for _, sel := range v.Selections {
			d.Options[optionName[sel.OptionID]] = values[sel.ValueID].Name
			d.ValueIDs = append(d.ValueIDs, sel.ValueID)
		}
		out.Variants = append(out.Variants, d)
	}
	return out
}

// publicDetailDTO is the public product page: the product plus its
// options and variants, so the UI can render pickers and prices.
type publicDetailDTO struct {
	publicProductDTO
	configurationDTO
	Images []mediaDTO `json:"images"`
	Model  *mediaDTO  `json:"model"`
}

// ---- helpers ----------------------------------------------------------

// uuidParams validates path ids; any malformed one cannot exist, so the
// matching not-found error is returned without touching the database.
func uuidParams(c *gin.Context, names map[string]error) bool {
	for name, notFound := range names {
		if !httpapi.ValidUUID(c.Param(name)) {
			writeError(c, notFound)
			return false
		}
	}
	return true
}

// respondConfiguration replies with the updated configuration. The
// currency comes from the product (i.e. its company).
func (h *Handler) respondConfiguration(c *gin.Context, status int, cfg Configuration, err error) {
	if err != nil {
		writeError(c, err)
		return
	}
	p, err := h.svc.Get(c.Request.Context(), member(c), c.Param("productID"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(status, toConfigurationDTO(cfg, p.Currency))
}

// ---- handlers ---------------------------------------------------------

func (h *Handler) configuration(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound}) {
		return
	}
	cfg, err := h.svc.Configuration(c.Request.Context(), member(c), c.Param("productID"))
	h.respondConfiguration(c, http.StatusOK, cfg, err)
}

type valueRequest struct {
	Name        string  `json:"name"`
	SwatchHex   *string `json:"swatch_hex"`
	MaterialKey *string `json:"material_key"`
}

type addOptionRequest struct {
	Name   string         `json:"name"`
	Values []valueRequest `json:"values"`
}

func (h *Handler) addOption(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound}) {
		return
	}
	var req addOptionRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	in := OptionInput{Name: req.Name, Values: make([]ValueInput, len(req.Values))}
	for i, v := range req.Values {
		in.Values[i] = ValueInput(v)
	}
	cfg, err := h.svc.AddOption(c.Request.Context(), member(c), c.Param("productID"), in)
	h.respondConfiguration(c, http.StatusCreated, cfg, err)
}

type updateOptionRequest struct {
	Name     *string `json:"name"`
	Position *int    `json:"position"`
}

func (h *Handler) updateOption(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound, "optionID": ErrOptionNotFound}) {
		return
	}
	var req updateOptionRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	cfg, err := h.svc.UpdateOption(c.Request.Context(), member(c), c.Param("productID"), c.Param("optionID"), OptionUpdate(req))
	h.respondConfiguration(c, http.StatusOK, cfg, err)
}

func (h *Handler) deleteOption(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound, "optionID": ErrOptionNotFound}) {
		return
	}
	cfg, err := h.svc.DeleteOption(c.Request.Context(), member(c), c.Param("productID"), c.Param("optionID"))
	h.respondConfiguration(c, http.StatusOK, cfg, err)
}

func (h *Handler) addValue(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound, "optionID": ErrOptionNotFound}) {
		return
	}
	var req valueRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	cfg, err := h.svc.AddValue(c.Request.Context(), member(c), c.Param("productID"), c.Param("optionID"), ValueInput(req))
	h.respondConfiguration(c, http.StatusCreated, cfg, err)
}

type updateValueRequest struct {
	Name        *string          `json:"name"`
	Position    *int             `json:"position"`
	SwatchHex   Optional[string] `json:"swatch_hex"`
	MaterialKey Optional[string] `json:"material_key"`
}

func (h *Handler) updateValue(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound, "optionID": ErrOptionNotFound, "valueID": ErrValueNotFound}) {
		return
	}
	var req updateValueRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	cfg, err := h.svc.UpdateValue(c.Request.Context(), member(c), c.Param("productID"), c.Param("optionID"), c.Param("valueID"), ValueUpdate(req))
	h.respondConfiguration(c, http.StatusOK, cfg, err)
}

func (h *Handler) deleteValue(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound, "optionID": ErrOptionNotFound, "valueID": ErrValueNotFound}) {
		return
	}
	cfg, err := h.svc.DeleteValue(c.Request.Context(), member(c), c.Param("productID"), c.Param("optionID"), c.Param("valueID"))
	h.respondConfiguration(c, http.StatusOK, cfg, err)
}

type createVariantRequest struct {
	SKU        string            `json:"sku"`
	PriceMinor *int64            `json:"price_minor"`
	Available  *bool             `json:"available"`
	Options    map[string]string `json:"options"`
}

func (h *Handler) createVariant(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound}) {
		return
	}
	var req createVariantRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	cfg, err := h.svc.CreateVariant(c.Request.Context(), member(c), c.Param("productID"), VariantInput(req))
	h.respondConfiguration(c, http.StatusCreated, cfg, err)
}

type generateRequest struct {
	PriceMinor *int64 `json:"price_minor"`
	Available  *bool  `json:"available"`
}

func (h *Handler) generateVariants(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound}) {
		return
	}
	var req generateRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	cfg, created, err := h.svc.GenerateVariants(c.Request.Context(), member(c), c.Param("productID"), GenerateInput(req))
	if err != nil {
		writeError(c, err)
		return
	}
	p, err := h.svc.Get(c.Request.Context(), member(c), c.Param("productID"))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"created": created, "configuration": toConfigurationDTO(cfg, p.Currency)})
}

type updateVariantRequest struct {
	SKU        *string `json:"sku"`
	PriceMinor *int64  `json:"price_minor"`
	Available  *bool   `json:"available"`
	Position   *int    `json:"position"`
}

func (h *Handler) updateVariant(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound, "variantID": ErrVariantNotFound}) {
		return
	}
	var req updateVariantRequest
	if !httpapi.DecodeJSON(c, &req) {
		return
	}
	cfg, err := h.svc.UpdateVariant(c.Request.Context(), member(c), c.Param("productID"), c.Param("variantID"), VariantUpdate(req))
	h.respondConfiguration(c, http.StatusOK, cfg, err)
}

func (h *Handler) deleteVariant(c *gin.Context) {
	if !uuidParams(c, map[string]error{"productID": ErrNotFound, "variantID": ErrVariantNotFound}) {
		return
	}
	cfg, err := h.svc.DeleteVariant(c.Request.Context(), member(c), c.Param("productID"), c.Param("variantID"))
	h.respondConfiguration(c, http.StatusOK, cfg, err)
}
