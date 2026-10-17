package company

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Joedee43/interior_api/internal/postgres"
)

// Create inserts a company with an explicit status. It returns
// ErrSlugTaken when the slug is already used.
func Create(ctx context.Context, db postgres.DBTX, name, slug, status string) (Company, error) {
	var c Company
	err := db.QueryRow(ctx, `
		INSERT INTO companies (name, slug, status)
		VALUES ($1, $2, $3)
		RETURNING id, name, slug, status, created_at, updated_at`,
		name, slug, status,
	).Scan(&c.ID, &c.Name, &c.Slug, &c.Status, &c.CreatedAt, &c.UpdatedAt)
	if postgres.IsUniqueViolation(err, "companies_slug_key") {
		return Company{}, ErrSlugTaken
	}
	if err != nil {
		return Company{}, fmt.Errorf("insert company: %w", err)
	}
	return c, nil
}

// AddMember creates an active membership.
func AddMember(ctx context.Context, db postgres.DBTX, companyID, userID, role string) (Membership, error) {
	m := Membership{CompanyID: companyID, UserID: userID}
	err := db.QueryRow(ctx, `
		INSERT INTO company_members (company_id, user_id, role, status)
		VALUES ($1, $2, $3, 'active')
		RETURNING role, status, created_at`,
		companyID, userID, role,
	).Scan(&m.Role, &m.Status, &m.CreatedAt)
	if err != nil {
		return Membership{}, fmt.Errorf("insert company member: %w", err)
	}
	return m, nil
}

// ListForUser returns every membership of userID with its company,
// oldest first. Callers decide which statuses count (Phase 3).
func ListForUser(ctx context.Context, db postgres.DBTX, userID string) ([]UserMembership, error) {
	rows, err := db.Query(ctx, `
		SELECT c.id, c.name, c.slug, c.status, c.created_at, c.updated_at, m.role, m.status
		FROM company_members m
		JOIN companies c ON c.id = m.company_id
		WHERE m.user_id = $1
		ORDER BY m.created_at, c.id`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("query memberships: %w", err)
	}
	// CollectRows iterates, scans each row, closes rows and checks
	// rows.Err() — the steps that are easy to forget with a manual loop.
	ms, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (UserMembership, error) {
		var um UserMembership
		c := &um.Company
		err := row.Scan(&c.ID, &c.Name, &c.Slug, &c.Status, &c.CreatedAt, &c.UpdatedAt, &um.Role, &um.Status)
		return um, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan memberships: %w", err)
	}
	return ms, nil
}
