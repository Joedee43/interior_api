// Package design owns room templates and customer-saved designs: a room
// (size + features) with furniture placed in it.
//
// Coordinates follow glTF / Three.js: right-handed, Y up, millimetres.
// The origin is the room's north-west floor corner; x runs east along the
// width, z runs south along the depth, y runs up. A placed item's position
// is the centre of its footprint on the floor (y = 0 unless raised), and
// rotation_deg turns it around the vertical axis.
//
// design depends on catalog through the small Catalog interface below; it
// never reads catalog tables itself.
package design

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/catalog"
)

// Catalog is what design needs from the product catalog. *catalog.Service
// satisfies it; tests could substitute a fake.
type Catalog interface {
	Placement(ctx context.Context, ref catalog.ItemRef) (catalog.ItemInfo, error)
	Items(ctx context.Context, refs []catalog.ItemRef) (map[catalog.ItemRef]catalog.ItemInfo, error)
}

var _ Catalog = (*catalog.Service)(nil)

const (
	TemplateDraft     = "draft"
	TemplatePublished = "published"

	MaxItemsPerDesign = 200
	maxOpenings       = 20
)

var (
	ErrNotFound          = errors.New("design not found")
	ErrItemNotFound      = errors.New("design item not found")
	ErrTemplateNotFound  = errors.New("room template not found")
	ErrTemplateSlugTaken = errors.New("room template slug already used")
	ErrItemsOutsideRoom  = errors.New("items would be outside the room")
	ErrTooManyItems      = errors.New("too many items in this design")
)

// Room is a rectangular room's inside dimensions.
type Room struct {
	WidthMM  int32 // along x (west -> east)
	DepthMM  int32 // along z (north -> south)
	HeightMM int32 // along y (floor -> ceiling)
}

// Features are a room's non-furniture details. They are stored as JSONB
// but always pass through this struct: decoding rejects unknown keys and
// validation checks every value, so the database only ever holds this
// exact shape.
type Features struct {
	WallColor string    `json:"wall_color,omitempty"`
	Floor     string    `json:"floor,omitempty"`
	Openings  []Opening `json:"openings"`
}

// Opening is a door or window in one wall. OffsetMM is measured along the
// wall from its lower-coordinate end: the west end of the north and south
// walls, the north end of the east and west walls.
type Opening struct {
	Type     string `json:"type"` // door | window
	Wall     string `json:"wall"` // north | south | east | west
	OffsetMM int32  `json:"offset_mm"`
	WidthMM  int32  `json:"width_mm"`
	HeightMM int32  `json:"height_mm"`
	SillMM   int32  `json:"sill_mm"` // height of the bottom edge above the floor
}

type Template struct {
	ID          string
	Slug        string
	Name        string
	Description string
	Room        Room
	Features    Features
	Status      string
	SortOrder   int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Design struct {
	ID         string
	UserID     string
	TemplateID *string
	Name       string
	Room       Room
	Features   Features
	ItemCount  int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Item struct {
	ID          string
	DesignID    string
	ProductID   string
	VariantID   *string
	XMM         int32
	YMM         int32
	ZMM         int32
	RotationDeg float64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (it Item) ref() catalog.ItemRef {
	r := catalog.ItemRef{ProductID: it.ProductID}
	if it.VariantID != nil {
		r.VariantID = *it.VariantID
	}
	return r
}

// PlacedItem is an item with the catalog's current view of what it is.
type PlacedItem struct {
	Item
	Info catalog.ItemInfo
}

// Total is the estimate for one seller in one currency.
type Total struct {
	CompanySlug string
	CompanyName string
	Currency    string
	AmountMinor int64
	Items       int
}

// Estimate prices the available items at today's catalog prices. It is
// informational, not a quote.
type Estimate struct {
	ItemCount        int
	UnavailableCount int
	Totals           []Total
}

// Detail is a design with its items and estimate.
type Detail struct {
	Design
	Items    []PlacedItem
	Estimate Estimate
}

// ---- validation -------------------------------------------------------------

var (
	slugPattern  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	colorPattern = regexp.MustCompile(`^#[0-9A-F]{6}$`)
	floors       = map[string]bool{"oak": true, "tile": true, "carpet": true, "concrete": true}
)

func validateRoom(r Room, p auth.Problems) {
	if r.WidthMM < 1000 || r.WidthMM > 30000 {
		p.Add("width_mm", "must be between 1000 and 30000 millimetres")
	}
	if r.DepthMM < 1000 || r.DepthMM > 30000 {
		p.Add("depth_mm", "must be between 1000 and 30000 millimetres")
	}
	if r.HeightMM < 2000 || r.HeightMM > 6000 {
		p.Add("height_mm", "must be between 2000 and 6000 millimetres")
	}
}

// normalize uppercases the colour, lowercases enum-like strings and makes
// openings an empty list rather than null.
func (f *Features) normalize() {
	f.WallColor = strings.ToUpper(strings.TrimSpace(f.WallColor))
	f.Floor = strings.ToLower(strings.TrimSpace(f.Floor))
	if f.Openings == nil {
		f.Openings = []Opening{}
	}
	for i := range f.Openings {
		f.Openings[i].Type = strings.ToLower(strings.TrimSpace(f.Openings[i].Type))
		f.Openings[i].Wall = strings.ToLower(strings.TrimSpace(f.Openings[i].Wall))
	}
}

// validate checks features against the room they belong to: an opening
// must fit on its wall and below the ceiling.
func (f Features) validate(r Room, p auth.Problems) {
	if f.WallColor != "" && !colorPattern.MatchString(f.WallColor) {
		p.Add("features.wall_color", "must be a hex colour like #F2EFE9")
	}
	if f.Floor != "" && !floors[f.Floor] {
		p.Add("features.floor", "must be oak, tile, carpet or concrete")
	}
	if len(f.Openings) > maxOpenings {
		p.Add("features.openings", fmt.Sprintf("at most %d openings", maxOpenings))
		return
	}
	for i, o := range f.Openings {
		key := fmt.Sprintf("features.openings[%d]", i)
		if o.Type != "door" && o.Type != "window" {
			p.Add(key+".type", "must be door or window")
		}
		var wallLen int32
		switch o.Wall {
		case "north", "south":
			wallLen = r.WidthMM
		case "east", "west":
			wallLen = r.DepthMM
		default:
			p.Add(key+".wall", "must be north, south, east or west")
			continue
		}
		switch {
		case o.WidthMM < 300 || o.HeightMM < 300:
			p.Add(key, "width_mm and height_mm must be at least 300")
		case o.OffsetMM < 0 || o.SillMM < 0:
			p.Add(key, "offset_mm and sill_mm must not be negative")
		case int64(o.OffsetMM)+int64(o.WidthMM) > int64(wallLen):
			p.Add(key, fmt.Sprintf("does not fit on the %s wall (%d mm long)", o.Wall, wallLen))
		case int64(o.SillMM)+int64(o.HeightMM) > int64(r.HeightMM):
			p.Add(key, fmt.Sprintf("is taller than the room (%d mm high)", r.HeightMM))
		}
	}
}

func validateName(n string, field string, p auth.Problems) {
	if c := utf8.RuneCountInString(n); c < 1 || c > 100 {
		p.Add(field, "must be 1 to 100 characters")
	}
}

// validatePosition checks an item's centre lies within the room. Items
// are not collision-checked against each other or the walls' thickness.
func validatePosition(x, y, z int32, r Room, p auth.Problems) {
	if x < 0 || x > r.WidthMM {
		p.Add("x_mm", fmt.Sprintf("must be between 0 and %d (the room width)", r.WidthMM))
	}
	if z < 0 || z > r.DepthMM {
		p.Add("z_mm", fmt.Sprintf("must be between 0 and %d (the room depth)", r.DepthMM))
	}
	if y < 0 || y > r.HeightMM {
		p.Add("y_mm", fmt.Sprintf("must be between 0 and %d (the room height)", r.HeightMM))
	}
}

// normalizeRotation rounds to 2 decimals (the column's precision) and
// wraps into [0, 360), so -90 is stored as 270 and 360 as 0.
func normalizeRotation(deg float64, p auth.Problems) float64 {
	if math.IsNaN(deg) || math.IsInf(deg, 0) || math.Abs(deg) > 3600 {
		p.Add("rotation_deg", "must be a number of degrees")
		return 0
	}
	r := math.Mod(math.Round(deg*100)/100, 360)
	if r < 0 {
		r += 360
	}
	r = math.Round(r*100) / 100
	if r >= 360 || r == 0 {
		// 359.999 rounds up to 360; and -0.001 rounds to negative zero,
		// which == 0 but would be encoded in JSON as "-0".
		r = 0
	}
	return r
}
