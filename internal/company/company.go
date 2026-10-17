// Package company owns tenants (companies), who belongs to them
// (memberships) and company-scoped authorization. It builds on auth for
// identity; auth never imports company.
package company

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Joedee43/interior_api/internal/auth"
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

var (
	ErrSlugTaken = errors.New("company slug already taken")
	// ErrNotFound means "no such company, or you cannot see it". The two
	// are deliberately indistinguishable so IDs cannot be probed.
	ErrNotFound       = errors.New("company not found")
	ErrMemberNotFound = errors.New("member not found")
	// ErrForbidden means a member of the company without the needed role.
	ErrForbidden = errors.New("insufficient company role")
	// ErrLastAdmin protects the invariant "an active company always has at
	// least one active admin".
	ErrLastAdmin = errors.New("cannot remove the last active company admin")
)

type Company struct {
	ID        string
	Name      string
	Slug      string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Member is the authorized actor for company-scoped requests: who is
// acting, in which company, with which role. Every service method that
// touches company data takes one and scopes its SQL by Member.CompanyID.
type Member struct {
	CompanyID string
	UserID    string
	Role      string
}

func (m Member) IsAdmin() bool { return m.Role == RoleAdmin }

// MemberInfo is a membership joined with the user it belongs to.
type MemberInfo struct {
	UserID    string
	Email     string
	FullName  string
	Role      string
	Status    string
	CreatedAt time.Time
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

func validateRole(role string) string {
	if role != RoleAdmin && role != RoleStaff {
		return "must be company_admin or company_staff"
	}
	return ""
}

func validateMemberStatus(status string) string {
	if status != MemberActive && status != MemberDisabled {
		return "must be active or disabled"
	}
	return ""
}

// ---- request context ----------------------------------------------------

type memberKey struct{}

// WithMember stores the authorized Member on ctx (done by RequireMember).
func WithMember(ctx context.Context, m Member) context.Context {
	return context.WithValue(ctx, memberKey{}, m)
}

// MemberFrom returns the Member stored by RequireMember. Later domain
// packages (catalog, designs) use it to scope their own queries.
func MemberFrom(ctx context.Context) (Member, bool) {
	m, ok := ctx.Value(memberKey{}).(Member)
	return m, ok
}

func toAuthMembership(c Company, role, status string) auth.Membership {
	return auth.Membership{
		CompanyID: c.ID, CompanyName: c.Name, CompanySlug: c.Slug, CompanyStatus: c.Status,
		Role: role, Status: status,
	}
}
