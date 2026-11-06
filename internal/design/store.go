package design

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Joedee43/interior_api/internal/postgres"
)

// Every design query filters by user_id from the session as well as by
// the design id: a design id alone never grants access.

// setClause builds "col = $n, ..." from set, appending values to args.
// Column names come from code, never from requests.
func setClause(set map[string]any, args *[]any) string {
	parts := make([]string, 0, len(set))
	for col, v := range set {
		*args = append(*args, v)
		parts = append(parts, fmt.Sprintf("%s = $%d", col, len(*args)))
	}
	return strings.Join(parts, ", ")
}

// ---- templates --------------------------------------------------------------

const templateColumns = `id, slug, name, description, width_mm, depth_mm, height_mm, features, status, sort_order, created_at, updated_at`

func scanTemplate(row pgx.Row) (Template, error) {
	var t Template
	// pgx decodes the jsonb column straight into the Features struct.
	err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.Description, &t.Room.WidthMM, &t.Room.DepthMM, &t.Room.HeightMM,
		&t.Features, &t.Status, &t.SortOrder, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func oneTemplate(row pgx.Row) (Template, error) {
	t, err := scanTemplate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, ErrTemplateNotFound
	}
	if err != nil {
		return Template{}, fmt.Errorf("get template: %w", err)
	}
	t.Features.normalize()
	return t, nil
}

func listTemplates(ctx context.Context, db postgres.DBTX, publishedOnly bool) ([]Template, error) {
	rows, err := db.Query(ctx, `SELECT `+templateColumns+` FROM room_templates
		WHERE NOT $1 OR status = 'published' ORDER BY sort_order, slug`, publishedOnly)
	if err != nil {
		return nil, fmt.Errorf("list templates: %w", err)
	}
	ts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Template, error) { return scanTemplate(r) })
	if err != nil {
		return nil, fmt.Errorf("scan templates: %w", err)
	}
	for i := range ts {
		ts[i].Features.normalize()
	}
	return ts, nil
}

func getTemplateBySlug(ctx context.Context, db postgres.DBTX, slug string, publishedOnly bool) (Template, error) {
	return oneTemplate(db.QueryRow(ctx, `SELECT `+templateColumns+` FROM room_templates
		WHERE slug = $1 AND (NOT $2 OR status = 'published')`, slug, publishedOnly))
}

func getTemplateByID(ctx context.Context, db postgres.DBTX, id string) (Template, error) {
	return oneTemplate(db.QueryRow(ctx, `SELECT `+templateColumns+` FROM room_templates WHERE id = $1`, id))
}

func insertTemplate(ctx context.Context, db postgres.DBTX, t Template) (Template, error) {
	out, err := oneTemplate(db.QueryRow(ctx, `
		INSERT INTO room_templates (slug, name, description, width_mm, depth_mm, height_mm, features, status, sort_order)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+templateColumns,
		t.Slug, t.Name, t.Description, t.Room.WidthMM, t.Room.DepthMM, t.Room.HeightMM, t.Features, t.Status, t.SortOrder))
	if postgres.IsUniqueViolation(err, "room_templates_slug_key") {
		return Template{}, ErrTemplateSlugTaken
	}
	return out, err
}

func updateTemplate(ctx context.Context, db postgres.DBTX, id string, set map[string]any) (Template, error) {
	args := []any{id}
	return oneTemplate(db.QueryRow(ctx,
		`UPDATE room_templates SET `+setClause(set, &args)+` WHERE id = $1 RETURNING `+templateColumns, args...))
}

// ---- designs ----------------------------------------------------------------

const designColumns = `d.id, d.user_id, d.template_id, d.name, d.width_mm, d.depth_mm, d.height_mm, d.features,
	(SELECT count(*) FROM design_items i WHERE i.design_id = d.id), d.revision, d.created_at, d.updated_at`

func scanDesign(row pgx.Row) (Design, error) {
	var d Design
	err := row.Scan(&d.ID, &d.UserID, &d.TemplateID, &d.Name, &d.Room.WidthMM, &d.Room.DepthMM, &d.Room.HeightMM,
		&d.Features, &d.ItemCount, &d.Revision, &d.CreatedAt, &d.UpdatedAt)
	d.Features.normalize()
	return d, err
}

func oneDesign(row pgx.Row) (Design, error) {
	d, err := scanDesign(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Design{}, ErrNotFound
	}
	if err != nil {
		return Design{}, fmt.Errorf("get design: %w", err)
	}
	return d, nil
}

func insertDesign(ctx context.Context, db postgres.DBTX, d Design) (string, error) {
	var id string
	err := db.QueryRow(ctx, `
		INSERT INTO designs (user_id, template_id, name, width_mm, depth_mm, height_mm, features)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		d.UserID, d.TemplateID, d.Name, d.Room.WidthMM, d.Room.DepthMM, d.Room.HeightMM, d.Features,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert design: %w", err)
	}
	return id, nil
}

func getDesign(ctx context.Context, db postgres.DBTX, userID, id string) (Design, error) {
	return oneDesign(db.QueryRow(ctx, `SELECT `+designColumns+` FROM designs d WHERE d.user_id = $1 AND d.id = $2`, userID, id))
}

func listDesigns(ctx context.Context, db postgres.DBTX, userID string, limit int, afterTime *time.Time, afterID string) ([]Design, error) {
	args := []any{userID, limit + 1}
	where := "d.user_id = $1"
	if afterTime != nil {
		args = append(args, *afterTime, afterID)
		where += " AND (d.updated_at, d.id) < ($3, $4::uuid)"
	}
	rows, err := db.Query(ctx, `SELECT `+designColumns+` FROM designs d WHERE `+where+`
		ORDER BY d.updated_at DESC, d.id DESC LIMIT $2`, args...)
	if err != nil {
		return nil, fmt.Errorf("list designs: %w", err)
	}
	ds, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Design, error) { return scanDesign(r) })
	if err != nil {
		return nil, fmt.Errorf("scan designs: %w", err)
	}
	return ds, nil
}

func updateDesign(ctx context.Context, db postgres.DBTX, userID, id string, set map[string]any) error {
	args := []any{userID, id}
	tag, err := db.Exec(ctx, `UPDATE designs SET `+setClause(set, &args)+` WHERE user_id = $1 AND id = $2`, args...)
	if err != nil {
		return fmt.Errorf("update design: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func deleteDesign(ctx context.Context, db postgres.DBTX, userID, id string) error {
	tag, err := db.Exec(ctx, `DELETE FROM designs WHERE user_id = $1 AND id = $2`, userID, id)
	if err != nil {
		return fmt.Errorf("delete design: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// lockDesign serializes all mutations for one design, verifies the aggregate
// revision, and advances it. A later error rolls this update back with the
// surrounding transaction, so failed mutations never consume a revision.
func lockDesign(ctx context.Context, tx pgx.Tx, userID, id string, expected int64) (Room, int64, error) {
	var r Room
	var current int64
	err := tx.QueryRow(ctx, `SELECT width_mm, depth_mm, height_mm, revision
		FROM designs WHERE user_id = $1 AND id = $2 FOR UPDATE`, userID, id).
		Scan(&r.WidthMM, &r.DepthMM, &r.HeightMM, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return Room{}, 0, ErrNotFound
	}
	if err != nil {
		return Room{}, 0, fmt.Errorf("lock design: %w", err)
	}
	if current != expected {
		return Room{}, current, &StaleRevisionError{Current: current}
	}
	next := current + 1
	if _, err := tx.Exec(ctx, `UPDATE designs SET revision = $3, updated_at = now()
		WHERE user_id = $1 AND id = $2`, userID, id, next); err != nil {
		return Room{}, 0, fmt.Errorf("advance design revision: %w", err)
	}
	return r, next, nil
}

// ---- items ------------------------------------------------------------------

const itemColumns = `id, design_id, product_id, variant_id, x_mm, y_mm, z_mm, rotation_deg, created_at, updated_at`

func scanItem(row pgx.Row) (Item, error) {
	var it Item
	// numeric(5,2) scans into float64; values have at most 2 decimals.
	err := row.Scan(&it.ID, &it.DesignID, &it.ProductID, &it.VariantID, &it.XMM, &it.YMM, &it.ZMM, &it.RotationDeg, &it.CreatedAt, &it.UpdatedAt)
	return it, err
}

func listItems(ctx context.Context, db postgres.DBTX, designID string) ([]Item, error) {
	rows, err := db.Query(ctx, `SELECT `+itemColumns+` FROM design_items WHERE design_id = $1 ORDER BY created_at, id`, designID)
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	its, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) { return scanItem(r) })
	if err != nil {
		return nil, fmt.Errorf("scan items: %w", err)
	}
	return its, nil
}

func getItem(ctx context.Context, db postgres.DBTX, designID, itemID string) (Item, error) {
	it, err := scanItem(db.QueryRow(ctx, `SELECT `+itemColumns+` FROM design_items WHERE design_id = $1 AND id = $2`, designID, itemID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrItemNotFound
	}
	if err != nil {
		return Item{}, fmt.Errorf("get item: %w", err)
	}
	return it, nil
}

func countItems(ctx context.Context, db postgres.DBTX, designID string) (int, error) {
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM design_items WHERE design_id = $1`, designID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count items: %w", err)
	}
	return n, nil
}

// countItemsOutside counts items whose centre would fall outside room r.
func countItemsOutside(ctx context.Context, db postgres.DBTX, designID string, r Room) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM design_items
		WHERE design_id = $1 AND (x_mm > $2 OR z_mm > $3 OR y_mm > $4)`,
		designID, r.WidthMM, r.DepthMM, r.HeightMM).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count items outside: %w", err)
	}
	return n, nil
}

func insertItem(ctx context.Context, db postgres.DBTX, it Item) (Item, error) {
	out, err := scanItem(db.QueryRow(ctx, `
		INSERT INTO design_items (design_id, product_id, variant_id, x_mm, y_mm, z_mm, rotation_deg)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+itemColumns,
		it.DesignID, it.ProductID, it.VariantID, it.XMM, it.YMM, it.ZMM, it.RotationDeg))
	if err != nil {
		return Item{}, fmt.Errorf("insert item: %w", err)
	}
	return out, nil
}

func updateItem(ctx context.Context, db postgres.DBTX, designID, itemID string, set map[string]any) (Item, error) {
	args := []any{designID, itemID}
	it, err := scanItem(db.QueryRow(ctx,
		`UPDATE design_items SET `+setClause(set, &args)+` WHERE design_id = $1 AND id = $2 RETURNING `+itemColumns, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrItemNotFound
	}
	if err != nil {
		return Item{}, fmt.Errorf("update item: %w", err)
	}
	return it, nil
}

func deleteItem(ctx context.Context, db postgres.DBTX, designID, itemID string) error {
	tag, err := db.Exec(ctx, `DELETE FROM design_items WHERE design_id = $1 AND id = $2`, designID, itemID)
	if err != nil {
		return fmt.Errorf("delete item: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrItemNotFound
	}
	return nil
}
