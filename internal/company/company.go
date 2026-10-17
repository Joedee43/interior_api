// Package company owns tenants (companies) and who belongs to them
// (memberships). Store functions take a postgres.DBTX so callers such as
// auth.Service can run them inside a wider transaction.
package company

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	StatusPending   = "pending"
	StatusActive    = "active"
	StatusSuspended = "suspended"

	RoleAdmin = "company_admin"
	RoleStaff = "company_staff"

	MemberActive   = "active"
	MemberDisabled = "disabled"
)

var ErrSlugTaken = errors.New("company slug already taken")

type Company struct {
	ID        string
	Name      string
	Slug      string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Membership struct {
	CompanyID string
	UserID    string
	Role      string
	Status    string
	CreatedAt time.Time
}

// UserMembership is a membership seen from the user's side, with the
// company it points to — what /auth/me returns.
type UserMembership struct {
	Company Company
	Role    string
	Status  string
}

// slugPattern must match companies_slug_check in migration 00003.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func NormalizeSlug(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ValidateSlug returns a client-facing problem description, or "" if ok.
func ValidateSlug(slug string) string {
	switch {
	case len(slug) < 3 || len(slug) > 50:
		return "must be 3 to 50 characters"
	case !slugPattern.MatchString(slug):
		return "may contain only lowercase letters, digits and single hyphens between them"
	}
	return ""
}

// ValidateName returns a client-facing problem description, or "" if ok.
func ValidateName(name string) string {
	n := utf8.RuneCountInString(name)
	if n < 2 || n > 100 {
		return "must be 2 to 100 characters"
	}
	return ""
}
