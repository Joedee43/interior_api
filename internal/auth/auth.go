// Package auth owns identity: user accounts, passwords and login
// sessions. It knows nothing about companies; packages that build on
// identity (company, later catalog, ...) import auth, never the reverse.
package auth

import (
	"errors"
	"net/mail"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
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
	// ErrForbidden means authenticated but not allowed (e.g. not a
	// platform admin).
	ErrForbidden = errors.New("forbidden")
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

// Problems collects field problems and turns them into a *ValidationError
// (or nil). Other packages use it so all validation errors look the same.
type Problems map[string]string

// Add records problem for field unless problem is "".
func (p Problems) Add(field, problem string) {
	if problem != "" {
		p[field] = problem
	}
}

func (p Problems) Err() error {
	if len(p) == 0 {
		return nil
	}
	return &ValidationError{Fields: p}
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

// Membership is a company membership as identity sees it: enough to show
// "which companies am I in" on /auth/me. The company package fills it in
// (see MembershipLister), so auth does not depend on company.
type Membership struct {
	CompanyID     string
	CompanyName   string
	CompanySlug   string
	CompanyStatus string
	Role          string
	Status        string
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

// NewUserInput is everything needed to create an account.
type NewUserInput struct {
	Email    string
	FullName string
	Password string
}

// Normalize trims and canonicalizes fields in place. Passwords are left
// exactly as typed: trimming them would silently change the secret.
func (in *NewUserInput) Normalize() {
	in.Email = NormalizeEmail(in.Email)
	in.FullName = strings.TrimSpace(in.FullName)
}

// Validate adds any problems to p. It takes a Problems instead of
// returning an error so callers can merge it with their own fields
// (register-company also validates company_name and company_slug).
func (in NewUserInput) Validate(p Problems) {
	p.Add("email", validateEmail(in.Email))
	p.Add("full_name", validateFullName(in.FullName))
	p.Add("password", validatePassword(in.Password))
}

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
