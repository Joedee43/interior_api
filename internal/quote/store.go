package quote

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Joedee43/interior_api/internal/postgres"
)

// scope restricts every request query to one side: the customer who
// asked (customer_id) or the company asked (company_id). A request id on
// its own never grants access.
type scope struct {
	column string // "customer_id" or "company_id" — fixed in code
	id     string
}

func customerScope(userID string) scope   { return scope{"customer_id", userID} }
func companyScope(companyID string) scope { return scope{"company_id", companyID} }

const requestSelect = `
	SELECT q.id, q.submission_id, q.company_id, c.slug, c.name, q.customer_id, q.design_id,
	       q.status, q.revision, q.currency, q.contact_name, q.contact_email, q.contact_phone,
	       q.delivery_address, q.customer_note, q.company_note, q.valid_until,
	       (q.valid_until IS NOT NULL AND q.valid_until < current_date),
	       q.quoted_total_minor,
	       (SELECT COALESCE(sum(l.list_unit_price_minor * l.quantity), 0)::bigint FROM quote_lines l WHERE l.request_id = q.id),
	       (SELECT count(*) FROM quote_lines l WHERE l.request_id = q.id),
	       q.submitted_at, q.quoted_at, q.decided_at, q.updated_at
	FROM quote_requests q
	JOIN companies c ON c.id = q.company_id`

func scanRequest(row pgx.Row) (Request, error) {
	var r Request
	err := row.Scan(&r.ID, &r.SubmissionID, &r.CompanyID, &r.CompanySlug, &r.CompanyName, &r.CustomerID, &r.DesignID,
		&r.Status, &r.Revision, &r.Currency, &r.ContactName, &r.ContactEmail, &r.ContactPhone,
		&r.DeliveryAddress, &r.CustomerNote, &r.CompanyNote, &r.ValidUntil, &r.Expired,
		&r.QuotedTotal, &r.ListTotal, &r.LineCount, &r.SubmittedAt, &r.QuotedAt, &r.DecidedAt, &r.UpdatedAt)
	return r, err
}

func getRequest(ctx context.Context, db postgres.DBTX, sc scope, id string) (Request, error) {
	r, err := scanRequest(db.QueryRow(ctx, requestSelect+` WHERE q.`+sc.column+` = $1 AND q.id = $2`, sc.id, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, fmt.Errorf("get quote request: %w", err)
	}
	if err := loadParts(ctx, db, &r); err != nil {
		return Request{}, err
	}
	return r, nil
}

func loadParts(ctx context.Context, db postgres.DBTX, r *Request) error {
	rows, err := db.Query(ctx, `
		SELECT id, position, product_id, variant_id, product_name, product_slug, sku, options,
		       width_mm, depth_mm, height_mm, quantity, list_unit_price_minor, quoted_unit_price_minor, line_note
		FROM quote_lines WHERE request_id = $1 ORDER BY position`, r.ID)
	if err != nil {
		return fmt.Errorf("query lines: %w", err)
	}
	r.Lines, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Line, error) {
		var l Line
		err := row.Scan(&l.ID, &l.Position, &l.ProductID, &l.VariantID, &l.ProductName, &l.ProductSlug, &l.SKU, &l.Options,
			&l.WidthMM, &l.DepthMM, &l.HeightMM, &l.Quantity, &l.ListUnit, &l.QuotedUnit, &l.Note)
		return l, err
	})
	if err != nil {
		return fmt.Errorf("scan lines: %w", err)
	}
	rows, err = db.Query(ctx, `SELECT label, amount_minor FROM quote_charges WHERE request_id = $1 ORDER BY position`, r.ID)
	if err != nil {
		return fmt.Errorf("query charges: %w", err)
	}
	r.Charges, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Charge, error) {
		var c Charge
		err := row.Scan(&c.Label, &c.AmountMinor)
		return c, err
	})
	if err != nil {
		return fmt.Errorf("scan charges: %w", err)
	}
	return nil
}

// listRequests pages newest first, keyset on (submitted_at, id).
func listRequests(ctx context.Context, db postgres.DBTX, sc scope, status string, limit int, afterTime *time.Time, afterID string) ([]Request, error) {
	args := []any{sc.id, limit + 1}
	where := "q." + sc.column + " = $1"
	if status != "" {
		args = append(args, status)
		where += fmt.Sprintf(" AND q.status = $%d", len(args))
	}
	if afterTime != nil {
		args = append(args, *afterTime, afterID)
		where += fmt.Sprintf(" AND (q.submitted_at, q.id) < ($%d, $%d::uuid)", len(args)-1, len(args))
	}
	rows, err := db.Query(ctx, requestSelect+` WHERE `+where+` ORDER BY q.submitted_at DESC, q.id DESC LIMIT $2`, args...)
	if err != nil {
		return nil, fmt.Errorf("list quote requests: %w", err)
	}
	rs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Request, error) { return scanRequest(r) })
	if err != nil {
		return nil, fmt.Errorf("scan quote requests: %w", err)
	}
	return rs, nil
}

// lockedState is what a transition needs to know, read under a row lock.
type lockedState struct {
	Status   string
	Revision int
	Expired  bool
	Today    time.Time // the database's current date
}

// lockRequest locks the request row (scoped to one side) for the rest of
// the transaction, so two actions on one request run one after another.
func lockRequest(ctx context.Context, tx pgx.Tx, sc scope, id string) (lockedState, error) {
	var st lockedState
	err := tx.QueryRow(ctx, `
		SELECT status, revision, (valid_until IS NOT NULL AND valid_until < current_date), current_date
		FROM quote_requests WHERE `+sc.column+` = $1 AND id = $2 FOR UPDATE`, sc.id, id,
	).Scan(&st.Status, &st.Revision, &st.Expired, &st.Today)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedState{}, ErrNotFound
	}
	if err != nil {
		return lockedState{}, fmt.Errorf("lock quote request: %w", err)
	}
	return st, nil
}

type newRequest struct {
	SubmissionID, CompanyID, CustomerID, Currency string
	DesignID                                      *string
	ContactName, ContactEmail, ContactPhone       string
	DeliveryAddress, CustomerNote                 string
	Lines                                         []Line
}

func insertRequest(ctx context.Context, tx pgx.Tx, r newRequest) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO quote_requests (submission_id, company_id, customer_id, design_id, currency,
			contact_name, contact_email, contact_phone, delivery_address, customer_note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		r.SubmissionID, r.CompanyID, r.CustomerID, r.DesignID, r.Currency,
		r.ContactName, r.ContactEmail, r.ContactPhone, r.DeliveryAddress, r.CustomerNote,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert quote request: %w", err)
	}
	for i, l := range r.Lines {
		opts := l.Options
		if opts == nil {
			opts = map[string]string{}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO quote_lines (request_id, position, product_id, variant_id, product_name, product_slug, sku,
				options, width_mm, depth_mm, height_mm, quantity, list_unit_price_minor)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			id, i, l.ProductID, l.VariantID, l.ProductName, l.ProductSlug, l.SKU,
			opts, l.WidthMM, l.DepthMM, l.HeightMM, l.Quantity, l.ListUnit); err != nil {
			return "", fmt.Errorf("insert quote line: %w", err)
		}
	}
	return id, nil
}

// setStatus moves the (locked) request to a new status and stamps when.
func setStatus(ctx context.Context, tx pgx.Tx, id, status, companyNote string, setNote bool) error {
	_, err := tx.Exec(ctx, `
		UPDATE quote_requests
		SET status = $2,
		    company_note = CASE WHEN $4 THEN $3 ELSE company_note END,
		    decided_at = CASE WHEN $2 IN ('accepted', 'rejected', 'cancelled', 'declined') THEN now() ELSE decided_at END
		WHERE id = $1`, id, status, companyNote, setNote)
	if err != nil {
		return fmt.Errorf("update quote status: %w", err)
	}
	return nil
}

func lineIDs(ctx context.Context, tx pgx.Tx, requestID string) (map[string]int, error) {
	rows, err := tx.Query(ctx, `SELECT id, quantity FROM quote_lines WHERE request_id = $1`, requestID)
	if err != nil {
		return nil, fmt.Errorf("query line ids: %w", err)
	}
	out := map[string]int{}
	for rows.Next() {
		var id string
		var qty int
		if err := rows.Scan(&id, &qty); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan line id: %w", err)
		}
		out[id] = qty
	}
	return out, rows.Err()
}

// saveQuote stores prices, replaces charges and marks the request quoted
// with the next revision.
func saveQuote(ctx context.Context, tx pgx.Tx, id string, prices map[string]PricedLine, charges []Charge,
	validUntil *time.Time, note string, total int64) error {
	for lineID, pl := range prices {
		if _, err := tx.Exec(ctx,
			`UPDATE quote_lines SET quoted_unit_price_minor = $3, line_note = $4 WHERE request_id = $1 AND id = $2`,
			id, lineID, pl.UnitPriceMinor, pl.Note); err != nil {
			return fmt.Errorf("price line: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM quote_charges WHERE request_id = $1`, id); err != nil {
		return fmt.Errorf("clear charges: %w", err)
	}
	for i, c := range charges {
		if _, err := tx.Exec(ctx,
			`INSERT INTO quote_charges (request_id, position, label, amount_minor) VALUES ($1, $2, $3, $4)`,
			id, i, c.Label, c.AmountMinor); err != nil {
			return fmt.Errorf("insert charge: %w", err)
		}
	}
	_, err := tx.Exec(ctx, `
		UPDATE quote_requests
		SET status = 'quoted', revision = revision + 1, quoted_total_minor = $2,
		    valid_until = $3, company_note = $4, quoted_at = now()
		WHERE id = $1`, id, total, validUntil, note)
	if err != nil {
		return fmt.Errorf("save quote: %w", err)
	}
	return nil
}
