package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Joedee43/interior_api/internal/postgres"
)

const mediaColumns = `m.id, m.product_id, m.company_id, m.kind, m.storage_key, m.content_type,
	m.size_bytes, m.sha256, m.original_filename, m.width, m.height, m.alt_text,
	m.position, m.is_primary, m.model_materials, m.model_variants, m.created_at, m.updated_at`

func scanMedia(row pgx.Row) (Media, error) {
	var m Media
	// text[] scans straight into []string with pgx.
	err := row.Scan(&m.ID, &m.ProductID, &m.CompanyID, &m.Kind, &m.StorageKey, &m.ContentType,
		&m.SizeBytes, &m.SHA256, &m.OriginalFilename, &m.Width, &m.Height, &m.AltText,
		&m.Position, &m.IsPrimary, &m.Materials, &m.Variants, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

func oneMedia(row pgx.Row) (Media, error) {
	m, err := scanMedia(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Media{}, ErrMediaNotFound
	}
	if err != nil {
		return Media{}, fmt.Errorf("get media: %w", err)
	}
	return m, nil
}

// listMedia returns a product's media: images by position, then the model.
func listMedia(ctx context.Context, db postgres.DBTX, productID string) (ProductMedia, error) {
	rows, err := db.Query(ctx, `SELECT `+mediaColumns+` FROM product_media m
		WHERE m.product_id = $1 ORDER BY m.kind, m.position, m.created_at, m.id`, productID)
	if err != nil {
		return ProductMedia{}, fmt.Errorf("list media: %w", err)
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Media, error) { return scanMedia(r) })
	if err != nil {
		return ProductMedia{}, fmt.Errorf("scan media: %w", err)
	}
	pm := ProductMedia{Images: []Media{}}
	for i := range all {
		if all[i].Kind == MediaModel {
			pm.Model = &all[i]
		} else {
			pm.Images = append(pm.Images, all[i])
		}
	}
	return pm, nil
}

type mediaRow struct {
	Kind, StorageKey, ContentType, OriginalFilename, AltText string
	SizeBytes                                                int64
	SHA256                                                   []byte
	Width, Height                                            *int32
	Position                                                 int
	IsPrimary                                                bool
	Materials, Variants                                      []string
}

func insertMedia(ctx context.Context, db postgres.DBTX, companyID, productID string, r mediaRow) (Media, error) {
	if r.Materials == nil {
		r.Materials = []string{}
	}
	if r.Variants == nil {
		r.Variants = []string{}
	}
	return oneMedia(db.QueryRow(ctx, `
		INSERT INTO product_media AS m (product_id, company_id, kind, storage_key, content_type, size_bytes, sha256,
			original_filename, width, height, alt_text, position, is_primary, model_materials, model_variants)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		RETURNING `+mediaColumns,
		productID, companyID, r.Kind, r.StorageKey, r.ContentType, r.SizeBytes, r.SHA256,
		r.OriginalFilename, r.Width, r.Height, r.AltText, r.Position, r.IsPrimary, r.Materials, r.Variants))
}

// deleteMedia removes one media row of a product and returns it, so the
// caller can delete its file after the transaction commits.
func deleteMedia(ctx context.Context, db postgres.DBTX, productID, mediaID, kind string) (Media, error) {
	return oneMedia(db.QueryRow(ctx, `
		DELETE FROM product_media AS m WHERE m.product_id = $1 AND m.id = $2 AND m.kind = $3
		RETURNING `+mediaColumns, productID, mediaID, kind))
}

// promoteFirstImage makes the lowest-positioned image primary, if any.
func promoteFirstImage(ctx context.Context, db postgres.DBTX, productID string) error {
	_, err := db.Exec(ctx, `
		UPDATE product_media SET is_primary = true
		WHERE id = (SELECT id FROM product_media
		            WHERE product_id = $1 AND kind = 'image'
		            ORDER BY position, created_at, id LIMIT 1)`, productID)
	if err != nil {
		return fmt.Errorf("promote image: %w", err)
	}
	return nil
}

// setPrimary switches the primary image. Clearing first, then setting,
// keeps the "one primary per product" unique index satisfied at every
// statement.
func setPrimary(ctx context.Context, tx pgx.Tx, productID, mediaID string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE product_media SET is_primary = false WHERE product_id = $1 AND is_primary AND id <> $2`,
		productID, mediaID); err != nil {
		return fmt.Errorf("clear primary: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE product_media SET is_primary = true WHERE product_id = $1 AND id = $2`, productID, mediaID); err != nil {
		return fmt.Errorf("set primary: %w", err)
	}
	return nil
}

func updateMediaFields(ctx context.Context, db postgres.DBTX, productID, mediaID string, set map[string]any) error {
	n, err := updateRow(ctx, db, "product_media", set, map[string]any{"product_id": productID, "id": mediaID, "kind": MediaImage})
	if err != nil {
		return fmt.Errorf("update media: %w", err)
	}
	if n == 0 {
		return ErrMediaNotFound
	}
	return nil
}

func mediaKeys(ctx context.Context, db postgres.DBTX, productID string) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT storage_key FROM product_media WHERE product_id = $1`, productID)
	if err != nil {
		return nil, fmt.Errorf("query media keys: %w", err)
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("scan media keys: %w", err)
	}
	return keys, nil
}

// getPublicMedia returns media only if its product is published and its
// company active — the same visibility rule as the public catalog.
func getPublicMedia(ctx context.Context, db postgres.DBTX, mediaID string) (Media, error) {
	return oneMedia(db.QueryRow(ctx, `SELECT `+mediaColumns+`
		FROM product_media m
		JOIN products p  ON p.id = m.product_id
		JOIN companies c ON c.id = m.company_id
		WHERE m.id = $1 AND p.status = 'published' AND c.status = 'active'`, mediaID))
}

// getCompanyMedia is the tenant-scoped lookup for members (any product
// status).
func getCompanyMedia(ctx context.Context, db postgres.DBTX, companyID, mediaID string) (Media, error) {
	return oneMedia(db.QueryRow(ctx, `SELECT `+mediaColumns+` FROM product_media m
		WHERE m.company_id = $1 AND m.id = $2`, companyID, mediaID))
}
