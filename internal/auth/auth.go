// Package auth owns user accounts, passwords and login sessions: company
// registration, login, logout and resolving a session cookie to a user.
// Company-scoped authorization (which company, which role) builds on top
// of this in Phase 3.
package auth

import (
	"errors"
	"net/mail"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Joedee43/interior_api/internal/company"
)

const (
	UserActive   = "active"
	UserDisabled = "disabled"

	minPasswordLen = 12
	maxPasswordLen = 128
)

// Sentinel errors. Handlers match them with errors.Is and choose the HTTP
// status; nothing below the handler knows about HTTP.
var (
	ErrEmailTaken = errors.New("email already registered")
	// ErrInvalidCredentials covers unknown email, wrong password and
	// disabled account alike, so a login response never reveals which.
	ErrInvalidCredentials = errors.New("invalid email or password")
	// ErrUnauthenticated means no valid session: missing, unknown,
	// expired or revoked token, or the user was disabled.
	ErrUnauthenticated = errors.New("not authenticated")
)

// ValidationError lists invalid input fields. It is a type, not a
// sentinel, because it carries data; callers use errors.As to get it.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys) // map order is random in Go; keep messages stable
	return "invalid input: " + strings.Join(keys, ", ")
}

type User struct {
	ID              string
	Email           string
	FullName        string
	Status          string
	IsPlatformAdmin bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Account is a user plus their company memberships.
type Account struct {
	User        User
	Memberships []company.UserMembership
}

// SessionMeta is optional client information recorded with a session.
type SessionMeta struct {
	UserAgent string
	IP        string
}

// NormalizeEmail trims and lowercases. Lowercasing the whole address is a
// deliberate simplification: technically the local part may be
// case-sensitive, but no mainstream provider treats it that way, and
// treating Bob@x.com and bob@x.com as two accounts causes real confusion.
func NormalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func validateEmail(email string) string {
	if email == "" {
		return "is required"
	}
	if len(email) > 254 {
		return "must be at most 254 characters"
	}
	// ParseAddress also accepts `Name <a@b.c>`; requiring the parsed
	// address to equal the input rejects that form.
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || !strings.Contains(email[strings.LastIndex(email, "@")+1:], ".") {
		return "must be a valid email address"
	}
	return ""
}

func validatePassword(pw string) string {
	n := utf8.RuneCountInString(pw)
	switch {
	case n < minPasswordLen:
		return "must be at least 12 characters"
	case n > maxPasswordLen:
		return "must be at most 128 characters"
	}
	return ""
}

func validateFullName(name string) string {
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 100 {
		return "must be 1 to 100 characters"
	}
	return ""
}

// RegisterCompanyInput is the domain input; the HTTP request struct is
// mapped onto it in the handler.
type RegisterCompanyInput struct {
	CompanyName string
	CompanySlug string
	FullName    string
	Email       string
	Password    string
}

// normalize trims and canonicalizes fields in place. Passwords are left
// exactly as typed: trimming them would silently change the secret.
func (in *RegisterCompanyInput) normalize() {
	in.CompanyName = strings.TrimSpace(in.CompanyName)
	in.CompanySlug = company.NormalizeSlug(in.CompanySlug)
	in.FullName = strings.TrimSpace(in.FullName)
	in.Email = NormalizeEmail(in.Email)
}

func (in RegisterCompanyInput) validate() error {
	fields := map[string]string{}
	add := func(field, problem string) {
		if problem != "" {
			fields[field] = problem
		}
	}
	add("company_name", company.ValidateName(in.CompanyName))
	add("company_slug", company.ValidateSlug(in.CompanySlug))
	add("full_name", validateFullName(in.FullName))
	add("email", validateEmail(in.Email))
	add("password", validatePassword(in.Password))
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}
