// Package catalog owns furniture products: company-managed CRUD and
// lifecycle, plus the public read-only catalog of published products.
// It builds on company for tenancy (company.Member scopes every private
// query) and never the reverse.
package catalog

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusArchived  = "archived"

	maxDimensionMM = 10_000     // 10 m: larger than any furniture
	maxWeightG     = 10_000_000 // 10 t
	maxPriceMinor  = 1_000_000_000_000
)

var (
	ErrNotFound  = errors.New("product not found")
	ErrSlugTaken = errors.New("product slug already used in this company")
	// ErrSlugLocked: a published (or once-published) product's slug is
	// part of its public URL, so it can only change while in draft.
	ErrSlugLocked        = errors.New("slug can only change while the product is a draft")
	ErrInvalidTransition = errors.New("product status does not allow this action")
	ErrNotDraft          = errors.New("only draft products can be deleted")
)

type Category struct {
	ID        string
	Slug      string
	Name      string
	SortOrder int
}

// Product is one furniture item. Dimensions are integer millimetres and
// weight integer grams; PriceMinor is in the smallest unit of Currency
// (the owning company's currency).
type Product struct {
	ID           string
	CompanyID    string
	CompanySlug  string
	CompanyName  string
	Currency     string
	CategorySlug string
	CategoryName string
	Name         string
	Slug         string
	Description  string
	PriceMinor   int64
	WidthMM      int32
	DepthMM      int32
	HeightMM     int32
	WeightG      *int32 // optional; nil when unknown
	Status       string
	PublishedAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// transitions is the product state machine: action -> (from, to).
// Anything not listed is ErrInvalidTransition.
var transitions = map[string]struct{ from, to string }{
	"publish": {StatusDraft, StatusPublished},
	"archive": {StatusPublished, StatusArchived},
	"restore": {StatusArchived, StatusDraft},
}

// ---- validation ---------------------------------------------------------

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func validateSlug(s string) string {
	switch {
	case len(s) < 3 || len(s) > 100:
		return "must be 3 to 100 characters"
	case !slugPattern.MatchString(s):
		return "may contain only lowercase letters, digits and single hyphens between them"
	}
	return ""
}

// Slugify turns a name into a URL slug: "Oslo 3-Seat Sofa!" -> "oslo-3-seat-sofa".
// Only ASCII letters and digits survive, so a name written entirely in
// another script (e.g. Amharic) yields "" and the caller must supply a slug.
func Slugify(name string) string {
	var b strings.Builder
	pendingHyphen := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingHyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			pendingHyphen = false
			continue
		}
		pendingHyphen = true
	}
	s := b.String()
	if len(s) > 100 {
		s = strings.TrimRight(s[:100], "-")
	}
	return s
}

func validateName(n string) string {
	if c := utf8.RuneCountInString(n); c < 2 || c > 200 {
		return "must be 2 to 200 characters"
	}
	return ""
}

func validateDescription(d string) string {
	if utf8.RuneCountInString(d) > 5000 {
		return "must be at most 5000 characters"
	}
	return ""
}

func validatePrice(p int64) string {
	if p < 0 || p > maxPriceMinor {
		return "must be between 0 and 1000000000000 (in minor units, e.g. santim)"
	}
	return ""
}

func validateDimension(mm int32) string {
	if mm < 1 || mm > maxDimensionMM {
		return "must be between 1 and 10000 millimetres"
	}
	return ""
}

func validateWeight(g *int32) string {
	if g != nil && (*g < 1 || *g > maxWeightG) {
		return "must be between 1 and 10000000 grams"
	}
	return ""
}

func validStatus(s string) bool {
	return s == StatusDraft || s == StatusPublished || s == StatusArchived
}
