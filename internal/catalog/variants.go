package catalog

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Limits keep configurations reviewable and bound the cartesian product
// that "generate" can create.
const (
	MaxOptionsPerProduct  = 3
	MaxValuesPerOption    = 30
	MaxVariantsPerProduct = 100
)

var (
	ErrOptionNotFound  = errors.New("option not found")
	ErrValueNotFound   = errors.New("option value not found")
	ErrVariantNotFound = errors.New("variant not found")

	ErrOptionNameTaken      = errors.New("option name already used on this product")
	ErrValueNameTaken       = errors.New("value name already used in this option")
	ErrSKUTaken             = errors.New("SKU already used in this company")
	ErrDuplicateCombination = errors.New("a variant with this combination already exists")

	// ErrOptionsLocked: adding or removing an option would leave existing
	// variants without a value for it (or with a value for a missing one).
	ErrOptionsLocked = errors.New("options can only be added or removed on a draft product without variants")
	ErrValueInUse    = errors.New("value is used by a variant")
	ErrLastValue     = errors.New("an option must keep at least one value")

	ErrPriceFromVariants   = errors.New("price comes from variants")
	ErrNoAvailableVariants = errors.New("product with options needs an available variant to publish")
	ErrVariantNotDeletable = errors.New("variants can only be deleted while the product is a draft")
)

// LimitError reports which limit was hit. It is a type (like
// auth.ValidationError) because it carries data for the message.
type LimitError struct {
	What string
	Max  int
}

func (e *LimitError) Error() string { return fmt.Sprintf("at most %d %s", e.Max, e.What) }

type OptionValue struct {
	ID          string
	OptionID    string
	Name        string
	Position    int
	SwatchHex   *string
	MaterialKey *string
}

type Option struct {
	ID       string
	Name     string
	Position int
	Values   []OptionValue
}

// Selection is one (option, value) pair of a variant.
type Selection struct {
	OptionID string
	ValueID  string
}

type Variant struct {
	ID         string
	SKU        string
	PriceMinor int64
	Available  bool
	Position   int
	Selections []Selection
}

// Configuration is a product's full option tree and variants.
type Configuration struct {
	Options  []Option
	Variants []Variant
}

// valueByID indexes every value for lookups while building DTOs.
func (cfg Configuration) valueByID() map[string]OptionValue {
	m := map[string]OptionValue{}
	for _, o := range cfg.Options {
		for _, v := range o.Values {
			m[v.ID] = v
		}
	}
	return m
}

// combinationKey is order-independent: the same set of values always
// gives the same key, so the database's unique constraint can spot
// duplicate combinations.
func combinationKey(sel []Selection) string {
	ids := make([]string, len(sel))
	for i, s := range sel {
		ids[i] = s.ValueID
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// combinations returns the cartesian product of the options' values: for
// options [Fabric: Linen, Leather] x [Colour: Grey, Black] it yields
// Linen/Grey, Linen/Black, Leather/Grey, Leather/Black.
//
// Recursive definition: the combinations of [first, rest...] are every
// value of first prepended to every combination of rest; the
// combinations of no options are one empty combination.
func combinations(opts []Option) [][]Selection {
	if len(opts) == 0 {
		return [][]Selection{{}}
	}
	rest := combinations(opts[1:])
	out := make([][]Selection, 0, len(opts[0].Values)*len(rest))
	for _, v := range opts[0].Values {
		for _, r := range rest {
			combo := make([]Selection, 0, len(r)+1)
			combo = append(combo, Selection{OptionID: opts[0].ID, ValueID: v.ID})
			combo = append(combo, r...)
			out = append(out, combo)
		}
	}
	return out
}

// combinationCount computes the cartesian size without building it.
func combinationCount(opts []Option) int {
	n := 1
	for _, o := range opts {
		n *= len(o.Values)
	}
	return n
}

// ---- validation ---------------------------------------------------------

var (
	skuPattern      = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._-]{0,63}$`)
	swatchPattern   = regexp.MustCompile(`^#[0-9A-F]{6}$`)
	materialPattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

func normalizeSKU(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

func validateSKU(s string) string {
	if !skuPattern.MatchString(s) {
		return "must be 1 to 64 characters: letters, digits, '.', '_' or '-' (stored uppercase)"
	}
	return ""
}

func validateLabel(s string) string {
	if n := utf8.RuneCountInString(s); n < 1 || n > 50 {
		return "must be 1 to 50 characters"
	}
	return ""
}

func validatePosition(p int) string {
	if p < 0 || p > 1000 {
		return "must be between 0 and 1000"
	}
	return ""
}

func normalizeSwatch(s *string) *string {
	if s == nil {
		return nil
	}
	v := strings.ToUpper(strings.TrimSpace(*s))
	return &v
}

func validateSwatch(s *string) string {
	if s != nil && !swatchPattern.MatchString(*s) {
		return "must be a hex colour like #A3B18A"
	}
	return ""
}

func normalizeMaterial(s *string) *string {
	if s == nil {
		return nil
	}
	v := strings.ToLower(strings.TrimSpace(*s))
	return &v
}

func validateMaterial(s *string) string {
	if s != nil && !materialPattern.MatchString(*s) {
		return "must be 1 to 64 lowercase letters, digits or underscores (the GLTF material name)"
	}
	return ""
}

// autoSKU builds a readable SKU from the product slug and value names,
// e.g. OSLO-3-SEAT-SOFA-LINEN-GREY, truncated to fit the 64-char limit.
func autoSKU(productSlug string, valueNames []string) string {
	parts := []string{strings.ToUpper(productSlug)}
	for _, n := range valueNames {
		if s := Slugify(n); s != "" {
			parts = append(parts, strings.ToUpper(s))
		}
	}
	sku := strings.Join(parts, "-")
	if len(sku) > 60 { // leave room for a "-NN" collision suffix
		sku = strings.TrimRight(sku[:60], "-")
	}
	return sku
}
