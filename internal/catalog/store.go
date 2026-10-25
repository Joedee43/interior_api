package catalog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Joedee43/interior_api/internal/postgres"
)

// sqlBuilder assembles WHERE/SET clauses for queries whose shape depends
// on which filters a request supplies. Values never go into the SQL
// text: arg() appends the value to args and returns its placeholder ($3),
// so the SQL stays parameterized however the clauses combine.
type sqlBuilder struct {
	args []any
}

func (b *sqlBuilder) arg(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

// likePattern escapes LIKE wildcards in user input so "50%" searches for
// the literal text, then wraps it for a contains-match.
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// ---- categories -----------------------------------------------------------

func listCategories(ctx context.Context, db postgres.DBTX) ([]Category, error) {
	rows, err := db.Query(ctx, `SELECT id, slug, name, sort_order FROM categories ORDER BY sort_order, slug`)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	cs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Category, error) {
		var c Category
		err := row.Scan(&c.ID, &c.Slug, &c.Name, &c.SortOrder)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan categories: %w", err)
	}
	return cs, nil
}

// categoryID returns ok=false for an unknown slug.
func categoryID(ctx context.Context, db postgres.DBTX, slug string) (string, bool, error) {
	var id string
	err := db.QueryRow(ctx, `SELECT id FROM categories WHERE slug = $1`, slug).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find category: %w", err)
	}
	return id, true, nil
}

// lockCompanyShared takes a SHARE lock on the company row. Many product
// writes can hold it at once, but it conflicts with the FOR UPDATE lock
// a currency change takes — so currency cannot change mid-insert.
func lockCompanyShared(ctx context.Context, tx pgx.Tx, companyID string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM companies WHERE id = $1 FOR SHARE`, companyID).Scan(&id)
	if err != nil {
		return fmt.Errorf("lock company: %w", err)
	}
	return nil
}

// ---- products: reads --------------------------------------------------------

const productSelect = `
	SELECT p.id, p.company_id, c.slug, c.name, c.currency, cat.slug, cat.name,
	       p.name, p.slug, p.description, p.price_minor,
	       p.width_mm, p.depth_mm, p.height_mm, p.weight_g,
	       p.status, p.published_at, p.created_at, p.updated_at
	FROM products p
	JOIN companies  c   ON c.id = p.company_id
	JOIN categories cat ON cat.id = p.category_id`

func scanProduct(row pgx.Row) (Product, error) {
	var p Product
	err := row.Scan(&p.ID, &p.CompanyID, &p.CompanySlug, &p.CompanyName, &p.Currency, &p.CategorySlug, &p.CategoryName,
		&p.Name, &p.Slug, &p.Description, &p.PriceMinor,
		&p.WidthMM, &p.DepthMM, &p.HeightMM, &p.WeightG,
		&p.Status, &p.PublishedAt, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func one(row pgx.Row) (Product, error) {
	p, err := scanProduct(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Product{}, ErrNotFound
	}
	if err != nil {
		return Product{}, fmt.Errorf("get product: %w", err)
	}
	return p, nil
}

// getCompanyProduct is the tenant-scoped lookup: the id alone is never
// enough, it must also belong to companyID.
func getCompanyProduct(ctx context.Context, db postgres.DBTX, companyID, id string) (Product, error) {
	return one(db.QueryRow(ctx, productSelect+` WHERE p.company_id = $1 AND p.id = $2`, companyID, id))
}

// getPublicProduct returns a product only if it is published and its
// company is active; everything else is ErrNotFound.
func getPublicProduct(ctx context.Context, db postgres.DBTX, companySlug, productSlug string) (Product, error) {
	return one(db.QueryRow(ctx, productSelect+`
		WHERE c.slug = $1 AND p.slug = $2
		  AND p.status = 'published' AND c.status = 'active'`,
		companySlug, productSlug))
}

func collect(rows pgx.Rows) ([]Product, error) {
	ps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Product, error) { return scanProduct(row) })
	if err != nil {
		return nil, fmt.Errorf("scan products: %w", err)
	}
	return ps, nil
}

// CompanyFilter narrows a company's own product list.
type CompanyFilter struct {
	Status   string // "" = all statuses
	Category string // category slug, "" = all
	Query    string // name contains, case-insensitive
	Limit    int
	Cursor   string
}

func listCompanyProducts(ctx context.Context, db postgres.DBTX, companyID string, f CompanyFilter, after *cursor) ([]Product, error) {
	b := &sqlBuilder{}
	where := []string{"p.company_id = " + b.arg(companyID)}
	if f.Status != "" {
		where = append(where, "p.status = "+b.arg(f.Status))
	}
	if f.Category != "" {
		where = append(where, "cat.slug = "+b.arg(f.Category))
	}
	if f.Query != "" {
		where = append(where, "p.name ILIKE "+b.arg(likePattern(f.Query)))
	}
	if after != nil {
		// Row comparison: "(a, b) < (x, y)" means a < x OR (a = x AND b < y),
		// exactly the "comes after" test for ORDER BY a DESC, b DESC.
		where = append(where, "(p.created_at, p.id) < ("+b.arg(*after.Time)+", "+b.arg(after.ID)+"::uuid)")
	}
	sql := productSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY p.created_at DESC, p.id DESC LIMIT " + b.arg(f.Limit+1)

	rows, err := db.Query(ctx, sql, b.args...)
	if err != nil {
		return nil, fmt.Errorf("list company products: %w", err)
	}
	return collect(rows)
}

// PublicFilter narrows the public catalog.
type PublicFilter struct {
	Category string // category slug
	Company  string // company slug
	Query    string
	Currency string // required when MinPrice or MaxPrice is set
	MinPrice *int64
	MaxPrice *int64
	Sort     string
	Limit    int
	Cursor   string
}

// orderBy and keyset hold, per sort, the ORDER BY clause and the
// row-comparison operator that means "after the cursor" in that order.
var publicSorts = map[string]struct {
	orderBy string
	keyCol  string
	op      string
}{
	SortNewest:    {"p.published_at DESC, p.id DESC", "p.published_at", "<"},
	SortPriceAsc:  {"p.price_minor ASC, p.id ASC", "p.price_minor", ">"},
	SortPriceDesc: {"p.price_minor DESC, p.id DESC", "p.price_minor", "<"},
}

func listPublicProducts(ctx context.Context, db postgres.DBTX, f PublicFilter, after *cursor) ([]Product, error) {
	s := publicSorts[f.Sort]
	b := &sqlBuilder{}
	where := []string{"p.status = 'published'", "c.status = 'active'"}
	if f.Category != "" {
		where = append(where, "cat.slug = "+b.arg(f.Category))
	}
	if f.Company != "" {
		where = append(where, "c.slug = "+b.arg(f.Company))
	}
	if f.Query != "" {
		where = append(where, "p.name ILIKE "+b.arg(likePattern(f.Query)))
	}
	if f.Currency != "" {
		where = append(where, "c.currency = "+b.arg(f.Currency))
	}
	if f.MinPrice != nil {
		where = append(where, "p.price_minor >= "+b.arg(*f.MinPrice))
	}
	if f.MaxPrice != nil {
		where = append(where, "p.price_minor <= "+b.arg(*f.MaxPrice))
	}
	if after != nil {
		var key any
		if after.Time != nil {
			key = *after.Time
		} else {
			key = *after.Price
		}
		where = append(where, "("+s.keyCol+", p.id) "+s.op+" ("+b.arg(key)+", "+b.arg(after.ID)+"::uuid)")
	}
	sql := productSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + s.orderBy + " LIMIT " + b.arg(f.Limit+1)

	rows, err := db.Query(ctx, sql, b.args...)
	if err != nil {
		return nil, fmt.Errorf("list public products: %w", err)
	}
	return collect(rows)
}

// ---- products: writes -------------------------------------------------------

type productRow struct {
	CategoryID  string
	Name        string
	Slug        string
	Description string
	PriceMinor  int64
	WidthMM     int32
	DepthMM     int32
	HeightMM    int32
	WeightG     *int32
}

func insertProduct(ctx context.Context, db postgres.DBTX, companyID string, r productRow) (string, error) {
	var id string
	err := db.QueryRow(ctx, `
		INSERT INTO products (company_id, category_id, name, slug, description, price_minor,
		                      width_mm, depth_mm, height_mm, weight_g)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id`,
		companyID, r.CategoryID, r.Name, r.Slug, r.Description, r.PriceMinor,
		r.WidthMM, r.DepthMM, r.HeightMM, r.WeightG,
	).Scan(&id)
	if postgres.IsUniqueViolation(err, "products_company_slug_key") {
		return "", ErrSlugTaken
	}
	if err != nil {
		return "", fmt.Errorf("insert product: %w", err)
	}
	return id, nil
}

// lockProduct locks the product row until the transaction ends and returns
// its current status and slug.
func lockProduct(ctx context.Context, tx pgx.Tx, companyID, id string) (status, slug string, err error) {
	err = tx.QueryRow(ctx,
		`SELECT status, slug FROM products WHERE company_id = $1 AND id = $2 FOR UPDATE`, companyID, id,
	).Scan(&status, &slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("lock product: %w", err)
	}
	return status, slug, nil
}

// updateProduct applies only the columns present in set (column -> value).
func updateProduct(ctx context.Context, db postgres.DBTX, companyID, id string, set map[string]any) error {
	if len(set) == 0 {
		return nil
	}
	b := &sqlBuilder{}
	// Column names come from a fixed allowlist in the service, never from
	// the request, so concatenating them is safe; values are placeholders.
	cols := make([]string, 0, len(set))
	for col := range set {
		cols = append(cols, col)
	}
	assign := make([]string, 0, len(cols))
	for _, col := range cols {
		assign = append(assign, col+" = "+b.arg(set[col]))
	}
	sql := "UPDATE products SET " + strings.Join(assign, ", ") +
		" WHERE company_id = " + b.arg(companyID) + " AND id = " + b.arg(id)

	tag, err := db.Exec(ctx, sql, b.args...)
	if postgres.IsUniqueViolation(err, "products_company_slug_key") {
		return ErrSlugTaken
	}
	if err != nil {
		return fmt.Errorf("update product: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// transition moves a product from one status to another in a single
// conditional UPDATE: if the row is not currently in `from`, nothing
// changes. Doing the check inside the UPDATE (not read-then-write) means
// two concurrent "publish" calls cannot both succeed.
func transition(ctx context.Context, db postgres.DBTX, companyID, id, from, to string) (bool, error) {
	tag, err := db.Exec(ctx, `
		UPDATE products
		SET status = $4,
		    published_at = CASE WHEN $4 = 'published' THEN now() ELSE published_at END
		WHERE company_id = $1 AND id = $2 AND status = $3
		  -- Publishing a product that has options needs a sellable variant.
		  AND ($4 <> 'published'
		       OR NOT EXISTS (SELECT 1 FROM product_options o WHERE o.product_id = products.id)
		       OR EXISTS (SELECT 1 FROM product_variants v WHERE v.product_id = products.id AND v.available))`,
		companyID, id, from, to,
	)
	if err != nil {
		return false, fmt.Errorf("change product status: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func deleteDraft(ctx context.Context, db postgres.DBTX, companyID, id string) (bool, error) {
	tag, err := db.Exec(ctx,
		`DELETE FROM products WHERE company_id = $1 AND id = $2 AND status = 'draft'`, companyID, id)
	if err != nil {
		return false, fmt.Errorf("delete product: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func productExists(ctx context.Context, db postgres.DBTX, companyID, id string) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM products WHERE company_id = $1 AND id = $2)`, companyID, id).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("check product: %w", err)
	}
	return ok, nil
}
