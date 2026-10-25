package company

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/postgres"
)

// Every query that reads or changes company data has `company_id = $1`
// (or `id = $1` on companies) in its WHERE clause, bound from an
// authorized Member — never from raw request input alone. That is the
// tenant-isolation rule: a handler bug cannot reach another company's rows.

const companyColumns = `id, name, slug, status, currency, created_at, updated_at`

func scanCompany(row pgx.Row) (Company, error) {
	var c Company
	err := row.Scan(&c.ID, &c.Name, &c.Slug, &c.Status, &c.Currency, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func createCompany(ctx context.Context, db postgres.DBTX, name, slug, status string) (Company, error) {
	c, err := scanCompany(db.QueryRow(ctx, `
		INSERT INTO companies (name, slug, status) VALUES ($1, $2, $3)
		RETURNING `+companyColumns,
		name, slug, status,
	))
	if postgres.IsUniqueViolation(err, "companies_slug_key") {
		return Company{}, ErrSlugTaken
	}
	if err != nil {
		return Company{}, fmt.Errorf("insert company: %w", err)
	}
	return c, nil
}

func getCompany(ctx context.Context, db postgres.DBTX, id string) (Company, error) {
	c, err := scanCompany(db.QueryRow(ctx, `SELECT `+companyColumns+` FROM companies WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Company{}, ErrNotFound
	}
	if err != nil {
		return Company{}, fmt.Errorf("get company: %w", err)
	}
	return c, nil
}

// updateCompany sets name and currency. COALESCE keeps the current value
// when the argument is NULL (a nil *string from Go), so one statement
// serves every combination of "fields provided".
func updateCompany(ctx context.Context, db postgres.DBTX, id string, name, currency *string) (Company, error) {
	c, err := scanCompany(db.QueryRow(ctx, `
		UPDATE companies
		SET name = COALESCE($2, name), currency = COALESCE($3, currency)
		WHERE id = $1
		RETURNING `+companyColumns,
		id, name, currency,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Company{}, ErrNotFound
	}
	if err != nil {
		return Company{}, fmt.Errorf("update company: %w", err)
	}
	return c, nil
}

// hasProducts reports whether the company has any product, in any status.
func hasProducts(ctx context.Context, db postgres.DBTX, companyID string) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM products WHERE company_id = $1)`, companyID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check products: %w", err)
	}
	return exists, nil
}

func listCompanies(ctx context.Context, db postgres.DBTX, limit int) ([]Company, error) {
	rows, err := db.Query(ctx,
		`SELECT `+companyColumns+` FROM companies ORDER BY created_at DESC, id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list companies: %w", err)
	}
	cs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Company, error) { return scanCompany(row) })
	if err != nil {
		return nil, fmt.Errorf("scan companies: %w", err)
	}
	return cs, nil
}

// lockCompany takes a row lock on the company until the transaction ends.
// Every membership change takes it first, so changes within one company
// run one at a time (no "two admins demote each other" race), and since
// every transaction locks the same single row first, they cannot deadlock.
func lockCompany(ctx context.Context, tx pgx.Tx, id string) error {
	var got string
	err := tx.QueryRow(ctx, `SELECT id FROM companies WHERE id = $1 FOR UPDATE`, id).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock company: %w", err)
	}
	return nil
}

// findActiveMember is the authorization query: the user must have an
// active membership in an active company. Anything else is ErrNotFound.
func findActiveMember(ctx context.Context, db postgres.DBTX, companyID, userID string) (Member, error) {
	m := Member{CompanyID: companyID, UserID: userID}
	err := db.QueryRow(ctx, `
		SELECT m.role
		FROM company_members m
		JOIN companies c ON c.id = m.company_id
		WHERE m.company_id = $1 AND m.user_id = $2
		  AND m.status = 'active' AND c.status = 'active'`,
		companyID, userID,
	).Scan(&m.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, ErrNotFound
	}
	if err != nil {
		return Member{}, fmt.Errorf("find member: %w", err)
	}
	return m, nil
}

func addMember(ctx context.Context, db postgres.DBTX, companyID, userID, role string) error {
	_, err := db.Exec(ctx, `
		INSERT INTO company_members (company_id, user_id, role, status)
		VALUES ($1, $2, $3, 'active')`,
		companyID, userID, role,
	)
	if err != nil {
		return fmt.Errorf("insert company member: %w", err)
	}
	return nil
}

type memberState struct {
	Role   string
	Status string
}

func getMemberState(ctx context.Context, db postgres.DBTX, companyID, userID string) (memberState, error) {
	var s memberState
	err := db.QueryRow(ctx,
		`SELECT role, status FROM company_members WHERE company_id = $1 AND user_id = $2`,
		companyID, userID,
	).Scan(&s.Role, &s.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return memberState{}, ErrMemberNotFound
	}
	if err != nil {
		return memberState{}, fmt.Errorf("get member: %w", err)
	}
	return s, nil
}

func countActiveAdmins(ctx context.Context, db postgres.DBTX, companyID string) (int, error) {
	var n int
	err := db.QueryRow(ctx, `
		SELECT count(*) FROM company_members
		WHERE company_id = $1 AND role = 'company_admin' AND status = 'active'`,
		companyID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

func updateMember(ctx context.Context, db postgres.DBTX, companyID, userID, role, status string) error {
	_, err := db.Exec(ctx, `
		UPDATE company_members SET role = $3, status = $4
		WHERE company_id = $1 AND user_id = $2`,
		companyID, userID, role, status,
	)
	if err != nil {
		return fmt.Errorf("update member: %w", err)
	}
	return nil
}

const memberInfoSelect = `
	SELECT u.id, u.email, u.full_name, m.role, m.status, m.created_at
	FROM company_members m
	JOIN users u ON u.id = m.user_id`

func scanMemberInfo(row pgx.Row) (MemberInfo, error) {
	var mi MemberInfo
	err := row.Scan(&mi.UserID, &mi.Email, &mi.FullName, &mi.Role, &mi.Status, &mi.CreatedAt)
	return mi, err
}

func getMemberInfo(ctx context.Context, db postgres.DBTX, companyID, userID string) (MemberInfo, error) {
	mi, err := scanMemberInfo(db.QueryRow(ctx,
		memberInfoSelect+` WHERE m.company_id = $1 AND m.user_id = $2`, companyID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberInfo{}, ErrMemberNotFound
	}
	if err != nil {
		return MemberInfo{}, fmt.Errorf("get member info: %w", err)
	}
	return mi, nil
}

func listMembers(ctx context.Context, db postgres.DBTX, companyID string) ([]MemberInfo, error) {
	rows, err := db.Query(ctx,
		memberInfoSelect+` WHERE m.company_id = $1 ORDER BY m.created_at, u.id`, companyID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	// CollectRows iterates, scans each row, closes rows and checks
	// rows.Err() — the steps that are easy to forget with a manual loop.
	ms, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (MemberInfo, error) { return scanMemberInfo(row) })
	if err != nil {
		return nil, fmt.Errorf("scan members: %w", err)
	}
	return ms, nil
}

// listMembershipsForUser returns every membership of userID with its
// company, oldest first — for /auth/me.
func listMembershipsForUser(ctx context.Context, db postgres.DBTX, userID string) ([]auth.Membership, error) {
	rows, err := db.Query(ctx, `
		SELECT c.id, c.name, c.slug, c.status, m.role, m.status
		FROM company_members m
		JOIN companies c ON c.id = m.company_id
		WHERE m.user_id = $1
		ORDER BY m.created_at, c.id`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("query memberships: %w", err)
	}
	ms, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (auth.Membership, error) {
		var m auth.Membership
		err := row.Scan(&m.CompanyID, &m.CompanyName, &m.CompanySlug, &m.CompanyStatus, &m.Role, &m.Status)
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan memberships: %w", err)
	}
	return ms, nil
}
