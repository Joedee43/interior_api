package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/company"
	"github.com/Joedee43/interior_api/internal/postgres"
	"github.com/Joedee43/interior_api/internal/storage"
)

const (
	DefaultLimit = 20
	MaxLimit     = 100
)

type Service struct {
	pool  *pgxpool.Pool
	files storage.Store
	log   *slog.Logger
}

// NewService takes the file store as an interface: production passes a
// *storage.LocalStore, and a cloud store can replace it without changes here.
func NewService(pool *pgxpool.Pool, files storage.Store, log *slog.Logger) *Service {
	return &Service{pool: pool, files: files, log: log}
}

// Optional is a PATCH field that can be absent, explicitly null, or set:
//
//	{}                 -> Set=false             (leave unchanged)
//	{"weight_g": null} -> Set=true, Value=nil   (clear it)
//	{"weight_g": 7500} -> Set=true, Value=&7500 (change it)
//
// A plain *int32 cannot tell the first two apart: both decode to nil.
// It is generic ([T any]) so any nullable column can reuse it.
type Optional[T any] struct {
	Set   bool
	Value *T
}

// UnmarshalJSON is only called when the key is present in the JSON, which
// is exactly what lets Optional detect "absent".
func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	if bytes.Equal(data, []byte("null")) {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

// ---- categories -----------------------------------------------------------

func (s *Service) Categories(ctx context.Context) ([]Category, error) {
	return listCategories(ctx, s.pool)
}

// resolveCategory adds a field problem for an unknown slug.
func (s *Service) resolveCategory(ctx context.Context, slug string, p auth.Problems) (string, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		p.Add("category", "is required")
		return "", nil
	}
	id, ok, err := categoryID(ctx, s.pool, slug)
	if err != nil {
		return "", err
	}
	if !ok {
		p.Add("category", "unknown category; see GET /v1/catalog/categories")
	}
	return id, nil
}

// ---- company-scoped writes ------------------------------------------------

// ProductInput creates a product. Required numbers are pointers so a
// missing field ("price_minor" omitted) is distinguishable from 0.
type ProductInput struct {
	Category    string
	Name        string
	Slug        string // optional; derived from Name when empty
	Description string
	PriceMinor  *int64
	WidthMM     *int32
	DepthMM     *int32
	HeightMM    *int32
	WeightG     *int32
}

func requireNumber[T int32 | int64](p auth.Problems, field string, v *T, check func(T) string) {
	if v == nil {
		p.Add(field, "is required")
		return
	}
	p.Add(field, check(*v))
}

// Create adds a draft product to the actor's company. Any active member
// (admin or staff) may manage products.
func (s *Service) Create(ctx context.Context, actor company.Member, in ProductInput) (Product, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if in.Slug == "" {
		in.Slug = Slugify(in.Name)
	}

	p := auth.Problems{}
	p.Add("name", validateName(in.Name))
	if in.Slug == "" {
		p.Add("slug", "is required when the name has no latin letters or digits")
	} else {
		p.Add("slug", validateSlug(in.Slug))
	}
	p.Add("description", validateDescription(in.Description))
	requireNumber(p, "price_minor", in.PriceMinor, validatePrice)
	requireNumber(p, "width_mm", in.WidthMM, validateDimension)
	requireNumber(p, "depth_mm", in.DepthMM, validateDimension)
	requireNumber(p, "height_mm", in.HeightMM, validateDimension)
	p.Add("weight_g", validateWeight(in.WeightG))
	catID, err := s.resolveCategory(ctx, in.Category, p)
	if err != nil {
		return Product{}, err
	}
	if err := p.Err(); err != nil {
		return Product{}, err
	}

	var out Product
	err = postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Hold a share lock on the company so its currency cannot change
		// while this product (priced in that currency) is being created.
		if err := lockCompanyShared(ctx, tx, actor.CompanyID); err != nil {
			return err
		}
		id, err := insertProduct(ctx, tx, actor.CompanyID, productRow{
			CategoryID: catID, Name: in.Name, Slug: in.Slug, Description: in.Description,
			PriceMinor: *in.PriceMinor, WidthMM: *in.WidthMM, DepthMM: *in.DepthMM, HeightMM: *in.HeightMM,
			WeightG: in.WeightG,
		})
		if err != nil {
			return err
		}
		out, err = getCompanyProduct(ctx, tx, actor.CompanyID, id)
		return err
	})
	return out, err
}

// ProductUpdate fields are all optional; nil / !Set means unchanged.
type ProductUpdate struct {
	Category    *string
	Name        *string
	Slug        *string
	Description *string
	PriceMinor  *int64
	WidthMM     *int32
	DepthMM     *int32
	HeightMM    *int32
	WeightG     Optional[int32]
}

func (s *Service) Update(ctx context.Context, actor company.Member, id string, upd ProductUpdate) (Product, error) {
	// set maps column name -> new value. The keys are written here in
	// code, never taken from the request, so they are safe to put in SQL.
	set := map[string]any{}
	p := auth.Problems{}

	if upd.Name != nil {
		n := strings.TrimSpace(*upd.Name)
		p.Add("name", validateName(n))
		set["name"] = n
	}
	if upd.Description != nil {
		d := strings.TrimSpace(*upd.Description)
		p.Add("description", validateDescription(d))
		set["description"] = d
	}
	var newSlug string
	if upd.Slug != nil {
		newSlug = strings.ToLower(strings.TrimSpace(*upd.Slug))
		p.Add("slug", validateSlug(newSlug))
	}
	if upd.PriceMinor != nil {
		p.Add("price_minor", validatePrice(*upd.PriceMinor))
		set["price_minor"] = *upd.PriceMinor
	}
	for col, v := range map[string]*int32{"width_mm": upd.WidthMM, "depth_mm": upd.DepthMM, "height_mm": upd.HeightMM} {
		if v != nil {
			p.Add(col, validateDimension(*v))
			set[col] = *v
		}
	}
	if upd.WeightG.Set {
		p.Add("weight_g", validateWeight(upd.WeightG.Value))
		set["weight_g"] = upd.WeightG.Value // nil becomes SQL NULL
	}
	if upd.Category != nil {
		catID, err := s.resolveCategory(ctx, *upd.Category, p)
		if err != nil {
			return Product{}, err
		}
		set["category_id"] = catID
	}
	if len(set) == 0 && upd.Slug == nil {
		p.Add("body", "provide at least one field to change")
	}
	if err := p.Err(); err != nil {
		return Product{}, err
	}

	var out Product
	err := postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		status, currentSlug, err := lockProduct(ctx, tx, actor.CompanyID, id)
		if err != nil {
			return err
		}
		if upd.Slug != nil && newSlug != currentSlug {
			if status != StatusDraft {
				return ErrSlugLocked
			}
			set["slug"] = newSlug
		}
		if _, ok := set["price_minor"]; ok {
			// With variants, price_minor is the derived "from" price.
			has, err := hasVariants(ctx, tx, id)
			if err != nil {
				return err
			}
			if has {
				return ErrPriceFromVariants
			}
		}
		if err := updateProduct(ctx, tx, actor.CompanyID, id, set); err != nil {
			return err
		}
		out, err = getCompanyProduct(ctx, tx, actor.CompanyID, id)
		return err
	})
	return out, err
}

// Transition applies a lifecycle action: "publish", "archive" or "restore".
func (s *Service) Transition(ctx context.Context, actor company.Member, id, action string) (Product, error) {
	t, ok := transitions[action]
	if !ok {
		return Product{}, ErrInvalidTransition
	}
	changed, err := transition(ctx, s.pool, actor.CompanyID, id, t.from, t.to)
	if err != nil {
		return Product{}, err
	}
	if !changed {
		// Either it is not ours/does not exist (404), or it exists in the
		// wrong status for this action (409).
		current, err := getCompanyProduct(ctx, s.pool, actor.CompanyID, id)
		if err != nil {
			return Product{}, err // ErrNotFound when not ours / missing
		}
		if action == "publish" && current.Status == t.from {
			// Right status, so the variant condition is what blocked it.
			return Product{}, ErrNoAvailableVariants
		}
		return Product{}, ErrInvalidTransition
	}
	return getCompanyProduct(ctx, s.pool, actor.CompanyID, id)
}

// Delete removes a draft. Published and archived products are kept (they
// may be referenced by designs and quotes later); archive them instead.
//
// Media rows cascade with the product; their files are removed after the
// transaction commits (see removeFiles).
func (s *Service) Delete(ctx context.Context, actor company.Member, id string) error {
	var keys []string
	err := s.productTx(ctx, actor, id, func(tx pgx.Tx, status, _ string) error {
		if status != StatusDraft {
			return ErrNotDraft
		}
		var err error
		if keys, err = mediaKeys(ctx, tx, id); err != nil {
			return err
		}
		_, err = deleteDraft(ctx, tx, actor.CompanyID, id)
		return err
	})
	if err != nil {
		return err
	}
	s.removeFiles(ctx, keys...)
	return nil
}

// ---- company-scoped reads -------------------------------------------------

func (s *Service) Get(ctx context.Context, actor company.Member, id string) (Product, error) {
	return getCompanyProduct(ctx, s.pool, actor.CompanyID, id)
}

func normalizeLimit(limit int, p auth.Problems) int {
	switch {
	case limit == 0:
		return DefaultLimit
	case limit < 1 || limit > MaxLimit:
		p.Add("limit", "must be between 1 and 100")
	}
	return limit
}

func (s *Service) List(ctx context.Context, actor company.Member, f CompanyFilter) (Page, error) {
	p := auth.Problems{}
	f.Limit = normalizeLimit(f.Limit, p)
	if f.Status != "" && !validStatus(f.Status) {
		p.Add("status", "must be draft, published or archived")
	}
	f.Query = strings.TrimSpace(f.Query)
	var after *cursor
	if f.Cursor != "" {
		c, err := decodeCursor(f.Cursor, sortCreated)
		if err != nil {
			p.Add("cursor", "is invalid; pass next_cursor from the previous page unchanged")
		} else {
			after = &c
		}
	}
	if err := p.Err(); err != nil {
		return Page{}, err
	}

	ps, err := listCompanyProducts(ctx, s.pool, actor.CompanyID, f, after)
	if err != nil {
		return Page{}, err
	}
	return paginate(ps, f.Limit, sortCreated), nil
}

// paginate trims the one extra row fetched (limit+1) and, if it existed,
// builds the cursor for the next page from the last row kept.
func paginate(ps []Product, limit int, sort string) Page {
	if len(ps) <= limit {
		return Page{Products: ps}
	}
	ps = ps[:limit]
	return Page{Products: ps, NextCursor: cursorAfter(ps[len(ps)-1], sort).encode()}
}

// ---- public catalog -------------------------------------------------------

func (s *Service) PublicList(ctx context.Context, f PublicFilter) (Page, error) {
	p := auth.Problems{}
	f.Limit = normalizeLimit(f.Limit, p)
	if f.Sort == "" {
		f.Sort = SortNewest
	}
	if _, ok := publicSorts[f.Sort]; !ok {
		p.Add("sort", "must be newest, price_asc or price_desc")
	}
	f.Query = strings.TrimSpace(f.Query)
	f.Category = strings.ToLower(strings.TrimSpace(f.Category))
	f.Company = strings.ToLower(strings.TrimSpace(f.Company))
	f.Currency = company.NormalizeCurrency(f.Currency)
	if f.Currency != "" {
		p.Add("currency", company.ValidateCurrency(f.Currency))
	}
	// Comparing prices across currencies is meaningless, so price filters
	// only apply within one currency.
	if (f.MinPrice != nil || f.MaxPrice != nil) && f.Currency == "" {
		p.Add("currency", "is required when filtering by min_price or max_price")
	}
	if f.MinPrice != nil && *f.MinPrice < 0 {
		p.Add("min_price", "must not be negative")
	}
	if f.MinPrice != nil && f.MaxPrice != nil && *f.MinPrice > *f.MaxPrice {
		p.Add("max_price", "must be greater than or equal to min_price")
	}
	var after *cursor
	if f.Cursor != "" && p["sort"] == "" {
		c, err := decodeCursor(f.Cursor, f.Sort)
		if err != nil {
			p.Add("cursor", "is invalid for this sort; pass next_cursor from the previous page unchanged")
		} else {
			after = &c
		}
	}
	if err := p.Err(); err != nil {
		return Page{}, err
	}

	ps, err := listPublicProducts(ctx, s.pool, f, after)
	if err != nil {
		return Page{}, err
	}
	return paginate(ps, f.Limit, f.Sort), nil
}

func (s *Service) PublicGet(ctx context.Context, companySlug, productSlug string) (Product, error) {
	return getPublicProduct(ctx, s.pool, strings.ToLower(companySlug), strings.ToLower(productSlug))
}
