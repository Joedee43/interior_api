package design

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/catalog"
	"github.com/Joedee43/interior_api/internal/postgres"
)

const (
	DefaultLimit = 20
	MaxLimit     = 100
)

type Service struct {
	pool    *pgxpool.Pool
	catalog Catalog
}

func NewService(pool *pgxpool.Pool, cat Catalog) *Service {
	return &Service{pool: pool, catalog: cat}
}

// ---- room templates -----------------------------------------------------------

func (s *Service) PublicTemplates(ctx context.Context) ([]Template, error) {
	return listTemplates(ctx, s.pool, true)
}

func (s *Service) PublicTemplate(ctx context.Context, slug string) (Template, error) {
	return getTemplateBySlug(ctx, s.pool, strings.ToLower(slug), true)
}

func requirePlatformAdmin(p auth.Principal) error {
	if !p.IsPlatformAdmin {
		return auth.ErrForbidden
	}
	return nil
}

func (s *Service) AdminTemplates(ctx context.Context, p auth.Principal) ([]Template, error) {
	if err := requirePlatformAdmin(p); err != nil {
		return nil, err
	}
	return listTemplates(ctx, s.pool, false)
}

type TemplateInput struct {
	Slug        string
	Name        string
	Description string
	Room        Room
	Features    Features
	Status      string // default draft
	SortOrder   int
}

func validateTemplateMeta(name, description, status string, sortOrder int, p auth.Problems) {
	validateName(name, "name", p)
	if len([]rune(description)) > 500 {
		p.Add("description", "must be at most 500 characters")
	}
	if status != TemplateDraft && status != TemplatePublished {
		p.Add("status", "must be draft or published")
	}
	if sortOrder < 0 || sortOrder > 10000 {
		p.Add("sort_order", "must be between 0 and 10000")
	}
}

func (s *Service) CreateTemplate(ctx context.Context, p auth.Principal, in TemplateInput) (Template, error) {
	if err := requirePlatformAdmin(p); err != nil {
		return Template{}, err
	}
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Status == "" {
		in.Status = TemplateDraft
	}
	in.Features.normalize()

	pr := auth.Problems{}
	if len(in.Slug) < 3 || len(in.Slug) > 60 || !slugPattern.MatchString(in.Slug) {
		pr.Add("slug", "must be 3 to 60 lowercase letters, digits and single hyphens")
	}
	validateTemplateMeta(in.Name, in.Description, in.Status, in.SortOrder, pr)
	validateRoom(in.Room, pr)
	in.Features.validate(in.Room, pr)
	if err := pr.Err(); err != nil {
		return Template{}, err
	}
	return insertTemplate(ctx, s.pool, Template{
		Slug: in.Slug, Name: in.Name, Description: in.Description, Room: in.Room,
		Features: in.Features, Status: in.Status, SortOrder: in.SortOrder,
	})
}

// TemplateUpdate fields are optional. The slug is fixed once created.
type TemplateUpdate struct {
	Name        *string
	Description *string
	WidthMM     *int32
	DepthMM     *int32
	HeightMM    *int32
	Features    *Features
	Status      *string
	SortOrder   *int
}

func (s *Service) UpdateTemplate(ctx context.Context, p auth.Principal, id string, upd TemplateUpdate) (Template, error) {
	if err := requirePlatformAdmin(p); err != nil {
		return Template{}, err
	}
	var out Template
	err := postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := getTemplateByID(ctx, tx, id)
		if err != nil {
			return err
		}
		next := cur
		set := map[string]any{}
		if upd.Name != nil {
			next.Name = strings.TrimSpace(*upd.Name)
			set["name"] = next.Name
		}
		if upd.Description != nil {
			next.Description = strings.TrimSpace(*upd.Description)
			set["description"] = next.Description
		}
		if upd.Status != nil {
			next.Status = *upd.Status
			set["status"] = next.Status
		}
		if upd.SortOrder != nil {
			next.SortOrder = *upd.SortOrder
			set["sort_order"] = next.SortOrder
		}
		applyRoom(&next.Room, upd.WidthMM, upd.DepthMM, upd.HeightMM, set)
		if upd.Features != nil {
			next.Features = *upd.Features
			next.Features.normalize()
		}
		// Features are always re-checked against the (possibly new) size.
		set["features"] = next.Features

		pr := auth.Problems{}
		if len(set) == 1 && upd.Features == nil {
			pr.Add("body", "provide at least one field to change")
		}
		validateTemplateMeta(next.Name, next.Description, next.Status, next.SortOrder, pr)
		validateRoom(next.Room, pr)
		next.Features.validate(next.Room, pr)
		if err := pr.Err(); err != nil {
			return err
		}
		out, err = updateTemplate(ctx, tx, id, set)
		return err
	})
	return out, err
}

func applyRoom(r *Room, w, d, h *int32, set map[string]any) {
	if w != nil {
		r.WidthMM = *w
		set["width_mm"] = *w
	}
	if d != nil {
		r.DepthMM = *d
		set["depth_mm"] = *d
	}
	if h != nil {
		r.HeightMM = *h
		set["height_mm"] = *h
	}
}

// ---- designs ----------------------------------------------------------------

// CreateInput starts a design either from a published template (Template
// = its slug) or from a custom Room (+ optional Features), not both.
type CreateInput struct {
	Name     string
	Template string
	Room     *Room
	Features *Features
}

func (s *Service) Create(ctx context.Context, userID string, in CreateInput) (Detail, error) {
	in.Name = strings.TrimSpace(in.Name)
	pr := auth.Problems{}
	validateName(in.Name, "name", pr)

	d := Design{UserID: userID, Name: in.Name}
	switch {
	case in.Template != "" && (in.Room != nil || in.Features != nil):
		pr.Add("template", "send either a template or a custom room size, not both")
	case in.Template != "":
		t, err := getTemplateBySlug(ctx, s.pool, strings.ToLower(strings.TrimSpace(in.Template)), true)
		if errors.Is(err, ErrTemplateNotFound) {
			pr.Add("template", "no published room template with this slug")
		} else if err != nil {
			return Detail{}, err
		}
		d.TemplateID, d.Room, d.Features = &t.ID, t.Room, t.Features
	case in.Room != nil:
		d.Room = *in.Room
		validateRoom(d.Room, pr)
		if in.Features != nil {
			d.Features = *in.Features
		}
		d.Features.normalize()
		d.Features.validate(d.Room, pr)
	default:
		pr.Add("template", "send a template slug, or width_mm, depth_mm and height_mm")
	}
	if err := pr.Err(); err != nil {
		return Detail{}, err
	}

	id, err := insertDesign(ctx, s.pool, d)
	if err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, userID, id)
}

// Page is one page of designs, newest-edited first.
type Page struct {
	Designs    []Design
	NextCursor string
}

type listCursor struct {
	T  time.Time `json:"t"`
	ID string    `json:"id"`
}

func (s *Service) List(ctx context.Context, userID string, limit int, rawCursor string) (Page, error) {
	pr := auth.Problems{}
	switch {
	case limit == 0:
		limit = DefaultLimit
	case limit < 1 || limit > MaxLimit:
		pr.Add("limit", "must be between 1 and 100")
	}
	var after *listCursor
	if rawCursor != "" {
		var c listCursor
		b, err := base64.RawURLEncoding.DecodeString(rawCursor)
		if err != nil || json.Unmarshal(b, &c) != nil || c.ID == "" {
			pr.Add("cursor", "is invalid; pass next_cursor from the previous page unchanged")
		} else {
			after = &c
		}
	}
	if err := pr.Err(); err != nil {
		return Page{}, err
	}

	var ds []Design
	var err error
	if after != nil {
		ds, err = listDesigns(ctx, s.pool, userID, limit, &after.T, after.ID)
	} else {
		ds, err = listDesigns(ctx, s.pool, userID, limit, nil, "")
	}
	if err != nil {
		return Page{}, err
	}
	pg := Page{Designs: ds}
	if len(ds) > limit {
		pg.Designs = ds[:limit]
		last := pg.Designs[limit-1]
		b, _ := json.Marshal(listCursor{T: last.UpdatedAt, ID: last.ID})
		pg.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return pg, nil
}

// Get returns the user's design with every item described by the catalog
// as it is now, and a price estimate.
func (s *Service) Get(ctx context.Context, userID, id string) (Detail, error) {
	d, err := getDesign(ctx, s.pool, userID, id)
	if err != nil {
		return Detail{}, err
	}
	items, err := listItems(ctx, s.pool, d.ID)
	if err != nil {
		return Detail{}, err
	}
	placed, err := s.describe(ctx, items)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Design: d, Items: placed, Estimate: estimate(placed)}, nil
}

// describe asks the catalog about all items in one call.
func (s *Service) describe(ctx context.Context, items []Item) ([]PlacedItem, error) {
	refs := make([]catalog.ItemRef, 0, len(items))
	seen := map[catalog.ItemRef]bool{}
	for _, it := range items {
		if r := it.ref(); !seen[r] {
			seen[r] = true
			refs = append(refs, r)
		}
	}
	infos, err := s.catalog.Items(ctx, refs)
	if err != nil {
		return nil, err
	}
	out := make([]PlacedItem, len(items))
	for i, it := range items {
		// A missing entry cannot happen while design_items' foreign keys
		// hold; the zero ItemInfo (Available=false) is the safe fallback.
		out[i] = PlacedItem{Item: it, Info: infos[it.ref()]}
	}
	return out, nil
}

// estimate sums current prices of available items per seller and
// currency. Unavailable items are counted but not priced.
func estimate(items []PlacedItem) Estimate {
	e := Estimate{ItemCount: len(items), Totals: []Total{}}
	type key struct{ company, currency string }
	idx := map[key]int{}
	for _, it := range items {
		if !it.Info.Available {
			e.UnavailableCount++
			continue
		}
		k := key{it.Info.CompanySlug, it.Info.Currency}
		i, ok := idx[k]
		if !ok {
			i = len(e.Totals)
			idx[k] = i
			e.Totals = append(e.Totals, Total{CompanySlug: it.Info.CompanySlug, CompanyName: it.Info.CompanyName, Currency: it.Info.Currency})
		}
		e.Totals[i].AmountMinor += it.Info.PriceMinor
		e.Totals[i].Items++
	}
	sort.Slice(e.Totals, func(a, b int) bool {
		if e.Totals[a].CompanySlug != e.Totals[b].CompanySlug {
			return e.Totals[a].CompanySlug < e.Totals[b].CompanySlug
		}
		return e.Totals[a].Currency < e.Totals[b].Currency
	})
	return e
}

// DesignUpdate fields are optional. Changing the room size re-checks both
// the features and every placed item against the new size.
type DesignUpdate struct {
	Name     *string
	WidthMM  *int32
	DepthMM  *int32
	HeightMM *int32
	Features *Features
}

func (s *Service) Update(ctx context.Context, userID, id string, expected int64, upd DesignUpdate) (Detail, error) {
	err := postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, err := lockDesign(ctx, tx, userID, id, expected); err != nil {
			return err
		}
		cur, err := getDesign(ctx, tx, userID, id)
		if err != nil {
			return err
		}
		next := cur
		set := map[string]any{}
		if upd.Name != nil {
			next.Name = strings.TrimSpace(*upd.Name)
			set["name"] = next.Name
		}
		applyRoom(&next.Room, upd.WidthMM, upd.DepthMM, upd.HeightMM, set)
		if upd.Features != nil {
			next.Features = *upd.Features
			next.Features.normalize()
		}
		set["features"] = next.Features

		pr := auth.Problems{}
		if len(set) == 1 && upd.Features == nil {
			pr.Add("body", "provide at least one field to change")
		}
		validateName(next.Name, "name", pr)
		validateRoom(next.Room, pr)
		next.Features.validate(next.Room, pr)
		if err := pr.Err(); err != nil {
			return err
		}
		if next.Room != cur.Room { // structs compare field by field with ==
			n, err := countItemsOutside(ctx, tx, id, next.Room)
			if err != nil {
				return err
			}
			if n > 0 {
				return ErrItemsOutsideRoom
			}
		}
		return updateDesign(ctx, tx, userID, id, set)
	})
	if err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, userID, id)
}

func (s *Service) Delete(ctx context.Context, userID, id string, expected int64) error {
	return postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, err := lockDesign(ctx, tx, userID, id, expected); err != nil {
			return err
		}
		return deleteDesign(ctx, tx, userID, id)
	})
}

// ---- items ------------------------------------------------------------------

type ItemInput struct {
	ProductID   string
	VariantID   *string
	XMM         *int32
	YMM         *int32 // default 0 (on the floor)
	ZMM         *int32
	RotationDeg *float64
}

func requireCoord(v *int32, field string, p auth.Problems) int32 {
	if v == nil {
		p.Add(field, "is required")
		return 0
	}
	return *v
}

func (s *Service) AddItem(ctx context.Context, userID, designID string, expected int64, in ItemInput) (PlacedItem, int64, error) {
	pr := auth.Problems{}
	it := Item{DesignID: designID, ProductID: in.ProductID, VariantID: in.VariantID}
	it.XMM = requireCoord(in.XMM, "x_mm", pr)
	it.ZMM = requireCoord(in.ZMM, "z_mm", pr)
	if in.YMM != nil {
		it.YMM = *in.YMM
	}
	if in.RotationDeg != nil {
		it.RotationDeg = normalizeRotation(*in.RotationDeg, pr)
	}
	if err := pr.Err(); err != nil {
		return PlacedItem{}, 0, err
	}

	var out PlacedItem
	var revision int64
	err := postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		room, next, err := lockDesign(ctx, tx, userID, designID, expected)
		if err != nil {
			return err
		}
		revision = next
		bp := auth.Problems{}
		validatePosition(it.XMM, it.YMM, it.ZMM, room, bp)
		if err := bp.Err(); err != nil {
			return err
		}
		n, err := countItems(ctx, tx, designID)
		if err != nil {
			return err
		}
		if n >= MaxItemsPerDesign {
			return ErrTooManyItems
		}
		info, err := s.catalog.Placement(ctx, it.ref())
		if err != nil {
			return err
		}
		saved, err := insertItem(ctx, tx, it)
		if err != nil {
			return err
		}
		out = PlacedItem{Item: saved, Info: info}
		return nil
	})
	return out, revision, err
}

// ItemUpdate moves, rotates or re-picks the variant of a placed item. The
// product itself cannot change: place a new item instead.
type ItemUpdate struct {
	XMM         *int32
	YMM         *int32
	ZMM         *int32
	RotationDeg *float64
	VariantID   *string
}

func (s *Service) UpdateItem(ctx context.Context, userID, designID, itemID string, expected int64, upd ItemUpdate) (PlacedItem, int64, error) {
	var out PlacedItem
	var revision int64
	err := postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		room, nextRevision, err := lockDesign(ctx, tx, userID, designID, expected)
		if err != nil {
			return err
		}
		revision = nextRevision
		cur, err := getItem(ctx, tx, designID, itemID)
		if err != nil {
			return err
		}
		next := cur
		set := map[string]any{}
		pr := auth.Problems{}
		if upd.XMM != nil {
			next.XMM, set["x_mm"] = *upd.XMM, *upd.XMM
		}
		if upd.YMM != nil {
			next.YMM, set["y_mm"] = *upd.YMM, *upd.YMM
		}
		if upd.ZMM != nil {
			next.ZMM, set["z_mm"] = *upd.ZMM, *upd.ZMM
		}
		if upd.RotationDeg != nil {
			next.RotationDeg = normalizeRotation(*upd.RotationDeg, pr)
			set["rotation_deg"] = next.RotationDeg
		}
		if upd.VariantID != nil {
			next.VariantID = upd.VariantID
			set["variant_id"] = *upd.VariantID
		}
		if len(set) == 0 {
			pr.Add("body", "provide x_mm, y_mm, z_mm, rotation_deg and/or variant_id")
		}
		validatePosition(next.XMM, next.YMM, next.ZMM, room, pr)
		if err := pr.Err(); err != nil {
			return err
		}

		info := catalog.ItemInfo{}
		if upd.VariantID != nil {
			// A new variant must pass the same rules as placing it fresh.
			if info, err = s.catalog.Placement(ctx, next.ref()); err != nil {
				return err
			}
		}
		saved, err := updateItem(ctx, tx, designID, itemID, set)
		if err != nil {
			return err
		}
		out = PlacedItem{Item: saved, Info: info}
		return nil
	})
	if err != nil {
		return PlacedItem{}, 0, err
	}
	if out.Info.Ref.ProductID == "" {
		placed, err := s.describe(ctx, []Item{out.Item})
		if err != nil {
			return PlacedItem{}, 0, err
		}
		out = placed[0]
	}
	return out, revision, nil
}

func (s *Service) DeleteItem(ctx context.Context, userID, designID, itemID string, expected int64) (int64, error) {
	var revision int64
	err := postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, next, err := lockDesign(ctx, tx, userID, designID, expected)
		if err != nil {
			return err
		}
		revision = next
		return deleteItem(ctx, tx, designID, itemID)
	})
	return revision, err
}
