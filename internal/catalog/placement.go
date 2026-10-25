package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// This file is the catalog's API for packages that reference products
// from elsewhere (saved designs now, quotes later). They depend on these
// exported types and methods, never on catalog tables directly.

var (
	// ErrProductInUse / ErrVariantInUse: a saved design references it, so
	// it cannot be deleted (archive or switch it off instead).
	ErrProductInUse = errors.New("product is used in saved designs")
	ErrVariantInUse = errors.New("variant is used in saved designs")
)

// ItemRef identifies a product, optionally a specific variant. It is a
// comparable struct (strings only), so it can be a map key.
type ItemRef struct {
	ProductID string
	VariantID string // "" when the product has no variants
}

// ItemInfo describes a referenced product (and variant) as it is now.
type ItemInfo struct {
	Ref             ItemRef
	CompanyID       string
	CompanySlug     string
	CompanyName     string
	ProductSlug     string
	ProductName     string
	Currency        string
	PriceMinor      int64 // the variant's price, else the product's
	WidthMM         int32
	DepthMM         int32
	HeightMM        int32
	Options         map[string]string // option name -> value name (variants only)
	PrimaryImageURL *string
	ModelURL        *string
	// Listed: the product is published and its company active.
	Listed bool
	// Available: Listed, and the variant (if any) exists and is available.
	Available bool
}

// PlacementError explains why a product/variant cannot be placed, keyed
// by the request field at fault.
type PlacementError struct {
	Field, Reason string
}

func (e *PlacementError) Error() string { return e.Field + ": " + e.Reason }

// Items describes many refs in one round trip. Refs that match no product
// are absent from the result.
func (s *Service) Items(ctx context.Context, refs []ItemRef) (map[ItemRef]ItemInfo, error) {
	out := make(map[ItemRef]ItemInfo, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	productIDs := make([]string, len(refs))
	variantIDs := make([]*string, len(refs)) // nil -> SQL NULL in the array
	for i, r := range refs {
		productIDs[i] = r.ProductID
		if r.VariantID != "" {
			v := r.VariantID
			variantIDs[i] = &v
		}
	}

	// unnest pairs the two arrays row by row, so one query resolves every
	// (product, variant) pair. The variant join also requires the variant
	// to belong to that product.
	rows, err := s.pool.Query(ctx, `
		SELECT r.product_id, r.variant_id, v.id,
		       c.id, c.slug, c.name, c.currency, p.slug, p.name,
		       COALESCE(v.price_minor, p.price_minor),
		       p.width_mm, p.depth_mm, p.height_mm,
		       (SELECT m.id FROM product_media m WHERE m.product_id = p.id AND m.is_primary),
		       (SELECT m.id FROM product_media m WHERE m.product_id = p.id AND m.kind = 'model'),
		       (p.status = 'published' AND c.status = 'active'),
		       COALESCE(v.available, true)
		FROM unnest($1::uuid[], $2::uuid[]) AS r(product_id, variant_id)
		JOIN products p  ON p.id = r.product_id
		JOIN companies c ON c.id = p.company_id
		LEFT JOIN product_variants v ON v.id = r.variant_id AND v.product_id = p.id`,
		productIDs, variantIDs)
	if err != nil {
		return nil, fmt.Errorf("query items: %w", err)
	}
	var variantKeys []string
	for rows.Next() {
		var (
			info               ItemInfo
			reqVariant, foundV *string
			imageID, modelID   *string
			variantAvailable   bool
		)
		if err := rows.Scan(&info.Ref.ProductID, &reqVariant, &foundV,
			&info.CompanyID, &info.CompanySlug, &info.CompanyName, &info.Currency, &info.ProductSlug, &info.ProductName,
			&info.PriceMinor, &info.WidthMM, &info.DepthMM, &info.HeightMM,
			&imageID, &modelID, &info.Listed, &variantAvailable); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan item: %w", err)
		}
		if reqVariant != nil {
			info.Ref.VariantID = *reqVariant
		}
		// A requested variant that did not join (wrong product, deleted)
		// makes the item unavailable.
		variantOK := reqVariant == nil || foundV != nil
		info.Available = info.Listed && variantOK && variantAvailable
		if imageID != nil {
			u := publicMediaURL(*imageID)
			info.PrimaryImageURL = &u
		}
		if modelID != nil {
			u := publicMediaURL(*modelID)
			info.ModelURL = &u
		}
		if foundV != nil {
			variantKeys = append(variantKeys, *foundV)
		}
		out[info.Ref] = info
	}
	if err := rows.Err(); err != nil { // errors during iteration surface here
		return nil, fmt.Errorf("iterate items: %w", err)
	}

	if len(variantKeys) > 0 {
		if err := s.attachVariantOptions(ctx, variantKeys, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// attachVariantOptions fills Options ("Fabric" -> "Linen") for variants.
func (s *Service) attachVariantOptions(ctx context.Context, variantIDs []string, out map[ItemRef]ItemInfo) error {
	rows, err := s.pool.Query(ctx, `
		SELECT vv.product_id, vv.variant_id, o.name, val.name
		FROM product_variant_values vv
		JOIN product_options o         ON o.id = vv.option_id
		JOIN product_option_values val ON val.id = vv.value_id
		WHERE vv.variant_id = ANY($1::uuid[])
		ORDER BY o.position, o.created_at`, variantIDs)
	if err != nil {
		return fmt.Errorf("query variant options: %w", err)
	}
	type row struct{ productID, variantID, option, value string }
	rs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.productID, &x.variantID, &x.option, &x.value)
		return x, err
	})
	if err != nil {
		return fmt.Errorf("scan variant options: %w", err)
	}
	for _, x := range rs {
		ref := ItemRef{ProductID: x.productID, VariantID: x.variantID}
		info, ok := out[ref]
		if !ok {
			continue
		}
		if info.Options == nil {
			info.Options = map[string]string{}
		}
		info.Options[x.option] = x.value
		out[ref] = info // info is a copy: write it back into the map
	}
	return nil
}

// Placement checks that a product (and variant) may be placed in a design
// right now and returns its description. Rules: the product is published
// by an active company; a product with options needs one of its own
// available variants; a product without options takes no variant.
func (s *Service) Placement(ctx context.Context, ref ItemRef) (ItemInfo, error) {
	infos, err := s.Items(ctx, []ItemRef{ref})
	if err != nil {
		return ItemInfo{}, err
	}
	info, ok := infos[ref]
	if !ok || !info.Listed {
		return ItemInfo{}, &PlacementError{Field: "product_id", Reason: "no published product with this id"}
	}
	var hasOptions bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM product_options WHERE product_id = $1)`, ref.ProductID).Scan(&hasOptions); err != nil {
		return ItemInfo{}, fmt.Errorf("check options: %w", err)
	}
	switch {
	case hasOptions && ref.VariantID == "":
		return ItemInfo{}, &PlacementError{Field: "variant_id", Reason: "required: this product has options; choose one of its variants"}
	case !hasOptions && ref.VariantID != "":
		return ItemInfo{}, &PlacementError{Field: "variant_id", Reason: "this product has no variants; omit variant_id"}
	case !info.Available:
		return ItemInfo{}, &PlacementError{Field: "variant_id", Reason: "not an available variant of this product"}
	}
	return info, nil
}
