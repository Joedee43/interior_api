package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/company"
	"github.com/Joedee43/interior_api/internal/postgres"
)

// productTx runs fn in a transaction holding the product's row lock, after
// proving the product belongs to the actor's company. Every option and
// variant change goes through it, so per-product limits, structure rules
// and the from-price are evaluated with no concurrent change in between.
func (s *Service) productTx(ctx context.Context, actor company.Member, productID string,
	fn func(tx pgx.Tx, status, slug string) error) error {
	return postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		status, slug, err := lockProduct(ctx, tx, actor.CompanyID, productID)
		if err != nil {
			return err
		}
		return fn(tx, status, slug)
	})
}

// mutate is productTx plus "load the configuration first, return it
// afterwards" — the shape of every option/variant write.
func (s *Service) mutate(ctx context.Context, actor company.Member, productID string,
	fn func(tx pgx.Tx, cfg Configuration, status, slug string) error) (Configuration, error) {
	var out Configuration
	err := s.productTx(ctx, actor, productID, func(tx pgx.Tx, status, slug string) error {
		cfg, err := loadConfiguration(ctx, tx, productID)
		if err != nil {
			return err
		}
		if err := fn(tx, cfg, status, slug); err != nil {
			return err
		}
		out, err = loadConfiguration(ctx, tx, productID)
		return err
	})
	return out, err
}

func (s *Service) Configuration(ctx context.Context, actor company.Member, productID string) (Configuration, error) {
	if _, err := getCompanyProduct(ctx, s.pool, actor.CompanyID, productID); err != nil {
		return Configuration{}, err
	}
	return loadConfiguration(ctx, s.pool, productID)
}

// structureEditable reports whether options may be added or removed: only
// on a draft without variants. Variants would lose (or lack) a value for
// the changed option, and a live product would show pickers with nothing
// to buy until variants exist. Restructure via archive -> restore (draft).
func structureEditable(status string, cfg Configuration) bool {
	return status == StatusDraft && len(cfg.Variants) == 0
}

func findOption(cfg Configuration, optionID string) (Option, bool) {
	for _, o := range cfg.Options {
		if o.ID == optionID {
			return o, true
		}
	}
	return Option{}, false
}

func findVariant(cfg Configuration, variantID string) (Variant, bool) {
	for _, v := range cfg.Variants {
		if v.ID == variantID {
			return v, true
		}
	}
	return Variant{}, false
}

// ---- options ----------------------------------------------------------------

type ValueInput struct {
	Name        string
	SwatchHex   *string
	MaterialKey *string
}

func (v *ValueInput) normalize() {
	v.Name = strings.TrimSpace(v.Name)
	v.SwatchHex = normalizeSwatch(v.SwatchHex)
	v.MaterialKey = normalizeMaterial(v.MaterialKey)
}

func (v ValueInput) validate(p auth.Problems, prefix string) {
	p.Add(prefix+"name", validateLabel(v.Name))
	p.Add(prefix+"swatch_hex", validateSwatch(v.SwatchHex))
	p.Add(prefix+"material_key", validateMaterial(v.MaterialKey))
}

type OptionInput struct {
	Name   string
	Values []ValueInput
}

func (s *Service) AddOption(ctx context.Context, actor company.Member, productID string, in OptionInput) (Configuration, error) {
	in.Name = strings.TrimSpace(in.Name)
	p := auth.Problems{}
	p.Add("name", validateLabel(in.Name))
	if len(in.Values) == 0 {
		p.Add("values", "add at least one value")
	}
	if len(in.Values) > MaxValuesPerOption {
		p.Add("values", fmt.Sprintf("at most %d values per option", MaxValuesPerOption))
	}
	seen := map[string]bool{}
	for i := range in.Values {
		in.Values[i].normalize()
		in.Values[i].validate(p, fmt.Sprintf("values[%d].", i))
		key := strings.ToLower(in.Values[i].Name)
		if seen[key] {
			p.Add(fmt.Sprintf("values[%d].name", i), "duplicate value name")
		}
		seen[key] = true
	}
	if err := p.Err(); err != nil {
		return Configuration{}, err
	}

	return s.mutate(ctx, actor, productID, func(tx pgx.Tx, cfg Configuration, status, _ string) error {
		if !structureEditable(status, cfg) {
			return ErrOptionsLocked
		}
		if len(cfg.Options) >= MaxOptionsPerProduct {
			return &LimitError{What: "options per product", Max: MaxOptionsPerProduct}
		}
		optionID, err := insertOption(ctx, tx, actor.CompanyID, productID, in.Name, len(cfg.Options))
		if err != nil {
			return err
		}
		for i, v := range in.Values {
			if _, err := insertValue(ctx, tx, optionID, valueRow{Name: v.Name, Position: i, SwatchHex: v.SwatchHex, MaterialKey: v.MaterialKey}); err != nil {
				return err
			}
		}
		return nil
	})
}

type OptionUpdate struct {
	Name     *string
	Position *int
}

// UpdateOption renames or reorders; always allowed, since it does not
// change which combinations exist.
func (s *Service) UpdateOption(ctx context.Context, actor company.Member, productID, optionID string, upd OptionUpdate) (Configuration, error) {
	set := map[string]any{}
	p := auth.Problems{}
	if upd.Name != nil {
		n := strings.TrimSpace(*upd.Name)
		p.Add("name", validateLabel(n))
		set["name"] = n
	}
	if upd.Position != nil {
		p.Add("position", validatePosition(*upd.Position))
		set["position"] = *upd.Position
	}
	if len(set) == 0 {
		p.Add("body", "provide name and/or position")
	}
	if err := p.Err(); err != nil {
		return Configuration{}, err
	}
	return s.mutate(ctx, actor, productID, func(tx pgx.Tx, _ Configuration, _, _ string) error {
		return updateOption(ctx, tx, productID, optionID, set)
	})
}

func (s *Service) DeleteOption(ctx context.Context, actor company.Member, productID, optionID string) (Configuration, error) {
	return s.mutate(ctx, actor, productID, func(tx pgx.Tx, cfg Configuration, status, _ string) error {
		if _, ok := findOption(cfg, optionID); !ok {
			return ErrOptionNotFound
		}
		if !structureEditable(status, cfg) {
			return ErrOptionsLocked
		}
		return deleteOption(ctx, tx, productID, optionID)
	})
}

// ---- values -----------------------------------------------------------------

func (s *Service) AddValue(ctx context.Context, actor company.Member, productID, optionID string, in ValueInput) (Configuration, error) {
	in.normalize()
	p := auth.Problems{}
	in.validate(p, "")
	if err := p.Err(); err != nil {
		return Configuration{}, err
	}
	return s.mutate(ctx, actor, productID, func(tx pgx.Tx, cfg Configuration, _, _ string) error {
		opt, ok := findOption(cfg, optionID)
		if !ok {
			return ErrOptionNotFound
		}
		if len(opt.Values) >= MaxValuesPerOption {
			return &LimitError{What: "values per option", Max: MaxValuesPerOption}
		}
		_, err := insertValue(ctx, tx, optionID, valueRow{Name: in.Name, Position: len(opt.Values), SwatchHex: in.SwatchHex, MaterialKey: in.MaterialKey})
		return err
	})
}

type ValueUpdate struct {
	Name        *string
	Position    *int
	SwatchHex   Optional[string]
	MaterialKey Optional[string]
}

func (s *Service) UpdateValue(ctx context.Context, actor company.Member, productID, optionID, valueID string, upd ValueUpdate) (Configuration, error) {
	set := map[string]any{}
	p := auth.Problems{}
	if upd.Name != nil {
		n := strings.TrimSpace(*upd.Name)
		p.Add("name", validateLabel(n))
		set["name"] = n
	}
	if upd.Position != nil {
		p.Add("position", validatePosition(*upd.Position))
		set["position"] = *upd.Position
	}
	if upd.SwatchHex.Set {
		v := normalizeSwatch(upd.SwatchHex.Value)
		p.Add("swatch_hex", validateSwatch(v))
		set["swatch_hex"] = v
	}
	if upd.MaterialKey.Set {
		v := normalizeMaterial(upd.MaterialKey.Value)
		p.Add("material_key", validateMaterial(v))
		set["material_key"] = v
	}
	if len(set) == 0 {
		p.Add("body", "provide at least one field to change")
	}
	if err := p.Err(); err != nil {
		return Configuration{}, err
	}
	return s.mutate(ctx, actor, productID, func(tx pgx.Tx, cfg Configuration, _, _ string) error {
		if _, ok := findOption(cfg, optionID); !ok {
			return ErrOptionNotFound
		}
		return updateValue(ctx, tx, optionID, valueID, set)
	})
}

func (s *Service) DeleteValue(ctx context.Context, actor company.Member, productID, optionID, valueID string) (Configuration, error) {
	cfg, err := s.mutate(ctx, actor, productID, func(tx pgx.Tx, cfg Configuration, _, _ string) error {
		opt, ok := findOption(cfg, optionID)
		if !ok {
			return ErrOptionNotFound
		}
		found := false
		for _, v := range opt.Values {
			found = found || v.ID == valueID
		}
		if !found {
			return ErrValueNotFound
		}
		if len(opt.Values) == 1 {
			return ErrLastValue
		}
		for _, variant := range cfg.Variants {
			for _, sel := range variant.Selections {
				if sel.ValueID == valueID {
					return ErrValueInUse
				}
			}
		}
		return deleteValue(ctx, tx, optionID, valueID)
	})
	// The deferred foreign key reports a violation at COMMIT, wrapped by
	// WithTx; errors.As (inside isForeignKeyViolation) still finds it.
	if isForeignKeyViolation(err, "product_variant_values_value_fkey") {
		return Configuration{}, ErrValueInUse
	}
	return cfg, err
}

// ---- variants ---------------------------------------------------------------

// resolveSelections maps {"Fabric":"Linen","Colour":"Grey"} (names,
// case-insensitive) to option/value ids. Every option needs exactly one
// value; unknown option or value names are reported together.
func resolveSelections(cfg Configuration, choices map[string]string, p auth.Problems) []Selection {
	byName := make(map[string]string, len(choices)) // lower(option) -> value name
	for k, v := range choices {
		byName[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}

	var problems []string
	sels := make([]Selection, 0, len(cfg.Options))
	for _, o := range cfg.Options {
		key := strings.ToLower(o.Name)
		valueName, ok := byName[key]
		delete(byName, key)
		if !ok || valueName == "" {
			problems = append(problems, "choose a value for "+o.Name)
			continue
		}
		matched := false
		for _, v := range o.Values {
			if strings.EqualFold(v.Name, valueName) {
				sels = append(sels, Selection{OptionID: o.ID, ValueID: v.ID})
				matched = true
				break
			}
		}
		if !matched {
			problems = append(problems, fmt.Sprintf("%q is not a value of %s", valueName, o.Name))
		}
	}
	leftover := make([]string, 0, len(byName))
	for k := range byName {
		leftover = append(leftover, k)
	}
	sort.Strings(leftover)
	for _, k := range leftover {
		problems = append(problems, fmt.Sprintf("%q is not an option of this product", k))
	}
	if len(problems) > 0 {
		p.Add("options", strings.Join(problems, "; "))
	}
	return sels
}

type VariantInput struct {
	SKU        string
	PriceMinor *int64
	Available  *bool // default true
	Options    map[string]string
}

func (s *Service) CreateVariant(ctx context.Context, actor company.Member, productID string, in VariantInput) (Configuration, error) {
	in.SKU = normalizeSKU(in.SKU)
	p := auth.Problems{}
	p.Add("sku", validateSKU(in.SKU))
	requireNumber(p, "price_minor", in.PriceMinor, validatePrice)
	if err := p.Err(); err != nil {
		return Configuration{}, err
	}
	available := in.Available == nil || *in.Available

	return s.mutate(ctx, actor, productID, func(tx pgx.Tx, cfg Configuration, _, _ string) error {
		if len(cfg.Options) == 0 {
			return &auth.ValidationError{Fields: map[string]string{"options": "add product options before creating variants"}}
		}
		sp := auth.Problems{}
		sels := resolveSelections(cfg, in.Options, sp)
		if err := sp.Err(); err != nil {
			return err
		}
		if len(cfg.Variants) >= MaxVariantsPerProduct {
			return &LimitError{What: "variants per product", Max: MaxVariantsPerProduct}
		}
		if _, err := insertVariant(ctx, tx, actor.CompanyID, productID, variantRow{
			SKU: in.SKU, PriceMinor: *in.PriceMinor, Available: available, Position: len(cfg.Variants), Selections: sels,
		}); err != nil {
			return err
		}
		return syncFromPrice(ctx, tx, productID)
	})
}

type GenerateInput struct {
	PriceMinor *int64
	Available  *bool
}

// GenerateVariants creates every combination not yet listed, all at the
// given price, with readable auto-SKUs. It refuses (and creates nothing)
// if the product would exceed MaxVariantsPerProduct.
func (s *Service) GenerateVariants(ctx context.Context, actor company.Member, productID string, in GenerateInput) (Configuration, int, error) {
	p := auth.Problems{}
	requireNumber(p, "price_minor", in.PriceMinor, validatePrice)
	if err := p.Err(); err != nil {
		return Configuration{}, 0, err
	}
	available := in.Available == nil || *in.Available

	created := 0
	cfg, err := s.mutate(ctx, actor, productID, func(tx pgx.Tx, cfg Configuration, _, slug string) error {
		if len(cfg.Options) == 0 {
			return &auth.ValidationError{Fields: map[string]string{"options": "add product options before generating variants"}}
		}
		// Check the size before building anything.
		if combinationCount(cfg.Options) > MaxVariantsPerProduct {
			return &LimitError{What: "variants per product", Max: MaxVariantsPerProduct}
		}
		existing := make(map[string]bool, len(cfg.Variants))
		for _, v := range cfg.Variants {
			existing[combinationKey(v.Selections)] = true
		}
		values := cfg.valueByID()
		prefix := strings.ToUpper(slug)
		taken, err := companySKUsWithPrefix(ctx, tx, actor.CompanyID, prefix)
		if err != nil {
			return err
		}

		position := len(cfg.Variants)
		for _, combo := range combinations(cfg.Options) {
			if existing[combinationKey(combo)] {
				continue
			}
			names := make([]string, len(combo))
			for i, sel := range combo {
				names[i] = values[sel.ValueID].Name
			}
			base := autoSKU(slug, names)
			sku := base
			for n := 2; taken[sku]; n++ {
				sku = fmt.Sprintf("%s-%d", base, n)
			}
			taken[sku] = true

			if _, err := insertVariant(ctx, tx, actor.CompanyID, productID, variantRow{
				SKU: sku, PriceMinor: *in.PriceMinor, Available: available, Position: position, Selections: combo,
			}); err != nil {
				return err
			}
			position++
			created++
		}
		return syncFromPrice(ctx, tx, productID)
	})
	return cfg, created, err
}

type VariantUpdate struct {
	SKU        *string
	PriceMinor *int64
	Available  *bool
	Position   *int
}

func (s *Service) UpdateVariant(ctx context.Context, actor company.Member, productID, variantID string, upd VariantUpdate) (Configuration, error) {
	set := map[string]any{}
	p := auth.Problems{}
	if upd.SKU != nil {
		sku := normalizeSKU(*upd.SKU)
		p.Add("sku", validateSKU(sku))
		set["sku"] = sku
	}
	if upd.PriceMinor != nil {
		p.Add("price_minor", validatePrice(*upd.PriceMinor))
		set["price_minor"] = *upd.PriceMinor
	}
	if upd.Available != nil {
		set["available"] = *upd.Available
	}
	if upd.Position != nil {
		p.Add("position", validatePosition(*upd.Position))
		set["position"] = *upd.Position
	}
	if len(set) == 0 {
		p.Add("body", "provide at least one field to change")
	}
	if err := p.Err(); err != nil {
		return Configuration{}, err
	}
	return s.mutate(ctx, actor, productID, func(tx pgx.Tx, _ Configuration, _, _ string) error {
		if err := updateVariant(ctx, tx, productID, variantID, set); err != nil {
			return err
		}
		return syncFromPrice(ctx, tx, productID)
	})
}

// DeleteVariant is only allowed on drafts. Once a product has been
// published, its variants may be referenced by saved designs and quotes,
// so they are switched off (available=false) instead.
func (s *Service) DeleteVariant(ctx context.Context, actor company.Member, productID, variantID string) (Configuration, error) {
	cfg, err := s.mutate(ctx, actor, productID, func(tx pgx.Tx, cfg Configuration, status, _ string) error {
		if _, ok := findVariant(cfg, variantID); !ok {
			return ErrVariantNotFound
		}
		if status != StatusDraft {
			return ErrVariantNotDeletable
		}
		if err := deleteVariant(ctx, tx, productID, variantID); err != nil {
			return err
		}
		return syncFromPrice(ctx, tx, productID)
	})
	if isForeignKeyViolation(err, "design_items_variant_fkey") {
		return Configuration{}, ErrVariantInUse
	}
	return cfg, err
}

// ---- public -----------------------------------------------------------------

// PublicDetail returns a published product with its configuration and media.
func (s *Service) PublicDetail(ctx context.Context, companySlug, productSlug string) (Product, Configuration, ProductMedia, error) {
	p, err := s.PublicGet(ctx, companySlug, productSlug)
	if err != nil {
		return Product{}, Configuration{}, ProductMedia{}, err
	}
	cfg, err := loadConfiguration(ctx, s.pool, p.ID)
	if err != nil {
		return Product{}, Configuration{}, ProductMedia{}, err
	}
	pm, err := s.publicMedia(ctx, p.ID)
	if err != nil {
		return Product{}, Configuration{}, ProductMedia{}, err
	}
	return p, cfg, pm, nil
}
