package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Joedee43/interior_api/internal/postgres"
)

// Scoping note: these functions filter by product_id. Callers first prove
// the product belongs to the actor's company (lockProduct or
// getCompanyProduct, both company-scoped); from there, the composite
// foreign keys guarantee every option/variant row of that product carries
// the same company_id, so product_id is a sufficient scope.

// ---- configuration (read) ---------------------------------------------------

// loadConfiguration reads the option tree and variants with four flat
// queries and assembles them in Go. One giant JOIN would repeat every
// option row for every value and variant; four queries return each row
// exactly once and are simpler to scan.
func loadConfiguration(ctx context.Context, db postgres.DBTX, productID string) (Configuration, error) {
	var cfg Configuration

	rows, err := db.Query(ctx, `
		SELECT id, name, position FROM product_options
		WHERE product_id = $1 ORDER BY position, created_at, id`, productID)
	if err != nil {
		return cfg, fmt.Errorf("query options: %w", err)
	}
	cfg.Options, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Option, error) {
		var o Option
		err := r.Scan(&o.ID, &o.Name, &o.Position)
		return o, err
	})
	if err != nil {
		return cfg, fmt.Errorf("scan options: %w", err)
	}

	// Index by id so values can be attached in one pass. The map stores
	// indexes, not *Option: pointers into a slice are fine here, but an
	// index makes it obvious the slice is not reallocated meanwhile.
	optIdx := make(map[string]int, len(cfg.Options))
	for i, o := range cfg.Options {
		optIdx[o.ID] = i
	}

	rows, err = db.Query(ctx, `
		SELECT v.id, v.option_id, v.name, v.position, v.swatch_hex, v.material_key
		FROM product_option_values v
		JOIN product_options o ON o.id = v.option_id
		WHERE o.product_id = $1
		ORDER BY v.position, v.created_at, v.id`, productID)
	if err != nil {
		return cfg, fmt.Errorf("query values: %w", err)
	}
	values, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (OptionValue, error) {
		var v OptionValue
		err := r.Scan(&v.ID, &v.OptionID, &v.Name, &v.Position, &v.SwatchHex, &v.MaterialKey)
		return v, err
	})
	if err != nil {
		return cfg, fmt.Errorf("scan values: %w", err)
	}
	for _, v := range values {
		i := optIdx[v.OptionID]
		cfg.Options[i].Values = append(cfg.Options[i].Values, v)
	}

	rows, err = db.Query(ctx, `
		SELECT id, sku, price_minor, available, position FROM product_variants
		WHERE product_id = $1 ORDER BY position, created_at, id`, productID)
	if err != nil {
		return cfg, fmt.Errorf("query variants: %w", err)
	}
	cfg.Variants, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Variant, error) {
		var v Variant
		err := r.Scan(&v.ID, &v.SKU, &v.PriceMinor, &v.Available, &v.Position)
		return v, err
	})
	if err != nil {
		return cfg, fmt.Errorf("scan variants: %w", err)
	}
	varIdx := make(map[string]int, len(cfg.Variants))
	for i, v := range cfg.Variants {
		varIdx[v.ID] = i
	}

	rows, err = db.Query(ctx, `
		SELECT vv.variant_id, vv.option_id, vv.value_id
		FROM product_variant_values vv
		JOIN product_options o ON o.id = vv.option_id
		WHERE vv.product_id = $1
		ORDER BY o.position, o.created_at, o.id`, productID)
	if err != nil {
		return cfg, fmt.Errorf("query selections: %w", err)
	}
	type sel struct{ variantID, optionID, valueID string }
	sels, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (sel, error) {
		var s sel
		err := r.Scan(&s.variantID, &s.optionID, &s.valueID)
		return s, err
	})
	if err != nil {
		return cfg, fmt.Errorf("scan selections: %w", err)
	}
	for _, s := range sels {
		i := varIdx[s.variantID]
		cfg.Variants[i].Selections = append(cfg.Variants[i].Selections, Selection{OptionID: s.optionID, ValueID: s.valueID})
	}
	return cfg, nil
}

// ---- options and values (write) ---------------------------------------------

func insertOption(ctx context.Context, db postgres.DBTX, companyID, productID, name string, position int) (string, error) {
	var id string
	err := db.QueryRow(ctx, `
		INSERT INTO product_options (product_id, company_id, name, position)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		productID, companyID, name, position,
	).Scan(&id)
	// A unique *index* (not constraint) reports its index name here.
	if postgres.IsUniqueViolation(err, "product_options_product_name_key") {
		return "", ErrOptionNameTaken
	}
	if err != nil {
		return "", fmt.Errorf("insert option: %w", err)
	}
	return id, nil
}

type valueRow struct {
	Name        string
	Position    int
	SwatchHex   *string
	MaterialKey *string
}

func insertValue(ctx context.Context, db postgres.DBTX, optionID string, v valueRow) (string, error) {
	var id string
	err := db.QueryRow(ctx, `
		INSERT INTO product_option_values (option_id, name, position, swatch_hex, material_key)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		optionID, v.Name, v.Position, v.SwatchHex, v.MaterialKey,
	).Scan(&id)
	if postgres.IsUniqueViolation(err, "product_option_values_option_name_key") {
		return "", ErrValueNameTaken
	}
	if err != nil {
		return "", fmt.Errorf("insert value: %w", err)
	}
	return id, nil
}

// updateRow applies set (column -> value) to one row of table matched by
// where (column -> value). Column names always come from code, never from
// requests; values are placeholders.
func updateRow(ctx context.Context, db postgres.DBTX, table string, set, where map[string]any) (int64, error) {
	if len(set) == 0 {
		return 1, nil
	}
	b := &sqlBuilder{}
	assign := make([]string, 0, len(set))
	for col, v := range set {
		assign = append(assign, col+" = "+b.arg(v))
	}
	conds := make([]string, 0, len(where))
	for col, v := range where {
		conds = append(conds, col+" = "+b.arg(v))
	}
	tag, err := db.Exec(ctx,
		"UPDATE "+table+" SET "+strings.Join(assign, ", ")+" WHERE "+strings.Join(conds, " AND "),
		b.args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func updateOption(ctx context.Context, db postgres.DBTX, productID, optionID string, set map[string]any) error {
	n, err := updateRow(ctx, db, "product_options", set, map[string]any{"product_id": productID, "id": optionID})
	if postgres.IsUniqueViolation(err, "product_options_product_name_key") {
		return ErrOptionNameTaken
	}
	if err != nil {
		return fmt.Errorf("update option: %w", err)
	}
	if n == 0 {
		return ErrOptionNotFound
	}
	return nil
}

func updateValue(ctx context.Context, db postgres.DBTX, optionID, valueID string, set map[string]any) error {
	n, err := updateRow(ctx, db, "product_option_values", set, map[string]any{"option_id": optionID, "id": valueID})
	if postgres.IsUniqueViolation(err, "product_option_values_option_name_key") {
		return ErrValueNameTaken
	}
	if err != nil {
		return fmt.Errorf("update value: %w", err)
	}
	if n == 0 {
		return ErrValueNotFound
	}
	return nil
}

func deleteOption(ctx context.Context, db postgres.DBTX, productID, optionID string) error {
	tag, err := db.Exec(ctx, `DELETE FROM product_options WHERE product_id = $1 AND id = $2`, productID, optionID)
	if err != nil {
		return fmt.Errorf("delete option: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrOptionNotFound
	}
	return nil
}

// isForeignKeyViolation reports PostgreSQL error 23503 for constraint.
func isForeignKeyViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == constraint
}

func deleteValue(ctx context.Context, db postgres.DBTX, optionID, valueID string) error {
	// The service checks first that no variant uses the value. The foreign
	// key is the backstop, but it is deferred, so a violation surfaces at
	// COMMIT (see DeleteValue), not here.
	tag, err := db.Exec(ctx, `DELETE FROM product_option_values WHERE option_id = $1 AND id = $2`, optionID, valueID)
	if err != nil {
		return fmt.Errorf("delete value: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrValueNotFound
	}
	return nil
}

// ---- variants (write) -------------------------------------------------------

type variantRow struct {
	SKU        string
	PriceMinor int64
	Available  bool
	Position   int
	Selections []Selection
}

func insertVariant(ctx context.Context, tx pgx.Tx, companyID, productID string, v variantRow) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO product_variants (product_id, company_id, sku, price_minor, available, position, combination_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		productID, companyID, v.SKU, v.PriceMinor, v.Available, v.Position, combinationKey(v.Selections),
	).Scan(&id)
	switch {
	case postgres.IsUniqueViolation(err, "product_variants_company_sku_key"):
		return "", ErrSKUTaken
	case postgres.IsUniqueViolation(err, "product_variants_combination_key"):
		return "", ErrDuplicateCombination
	case err != nil:
		return "", fmt.Errorf("insert variant: %w", err)
	}

	// CopyFrom uses PostgreSQL's COPY protocol: one round trip for all
	// rows instead of one INSERT per selection.
	rows := make([][]any, len(v.Selections))
	for i, s := range v.Selections {
		rows[i] = []any{id, productID, s.OptionID, s.ValueID}
	}
	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"product_variant_values"},
		[]string{"variant_id", "product_id", "option_id", "value_id"},
		pgx.CopyFromRows(rows))
	if err != nil {
		return "", fmt.Errorf("insert variant values: %w", err)
	}
	return id, nil
}

func updateVariant(ctx context.Context, db postgres.DBTX, productID, variantID string, set map[string]any) error {
	n, err := updateRow(ctx, db, "product_variants", set, map[string]any{"product_id": productID, "id": variantID})
	if postgres.IsUniqueViolation(err, "product_variants_company_sku_key") {
		return ErrSKUTaken
	}
	if err != nil {
		return fmt.Errorf("update variant: %w", err)
	}
	if n == 0 {
		return ErrVariantNotFound
	}
	return nil
}

func deleteVariant(ctx context.Context, db postgres.DBTX, productID, variantID string) error {
	tag, err := db.Exec(ctx, `DELETE FROM product_variants WHERE product_id = $1 AND id = $2`, productID, variantID)
	if err != nil {
		return fmt.Errorf("delete variant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrVariantNotFound
	}
	return nil
}

// companySKUsWithPrefix returns the company's SKUs starting with prefix,
// so generated SKUs can avoid collisions before inserting.
func companySKUsWithPrefix(ctx context.Context, db postgres.DBTX, companyID, prefix string) (map[string]bool, error) {
	rows, err := db.Query(ctx,
		`SELECT sku FROM product_variants WHERE company_id = $1 AND starts_with(sku, $2)`, companyID, prefix)
	if err != nil {
		return nil, fmt.Errorf("query skus: %w", err)
	}
	skus, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("scan skus: %w", err)
	}
	set := make(map[string]bool, len(skus))
	for _, s := range skus {
		set[s] = true
	}
	return set, nil
}

// syncFromPrice sets the product's price to its cheapest available
// variant, so the public list, sort and price filters (which read
// products.price_minor) show the "from" price. With no available
// variants the last price is kept.
func syncFromPrice(ctx context.Context, db postgres.DBTX, productID string) error {
	_, err := db.Exec(ctx, `
		UPDATE products p
		SET price_minor = v.min_price
		FROM (SELECT min(price_minor) AS min_price
		      FROM product_variants WHERE product_id = $1 AND available) v
		WHERE p.id = $1 AND v.min_price IS NOT NULL AND p.price_minor <> v.min_price`,
		productID)
	if err != nil {
		return fmt.Errorf("sync from-price: %w", err)
	}
	return nil
}

func hasVariants(ctx context.Context, db postgres.DBTX, productID string) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM product_variants WHERE product_id = $1)`, productID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("check variants: %w", err)
	}
	return ok, nil
}
