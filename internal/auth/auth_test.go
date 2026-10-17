package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	if got := NormalizeEmail("  Bob@Example.COM \n"); got != "bob@example.com" {
		t.Errorf("NormalizeEmail = %q", got)
	}
}

func TestValidateEmail(t *testing.T) {
	tests := []struct {
		email string
		ok    bool
	}{
		{"bob@example.com", true},
		{"a.b+tag@sub.example.co", true},
		{"", false},
		{"bob", false},
		{"bob@localhost", false}, // no dot in domain
		{"Bob <bob@example.com>", false},
		{strings.Repeat("a", 250) + "@x.com", false},
	}
	for _, tt := range tests {
		if got := validateEmail(tt.email) == ""; got != tt.ok {
			t.Errorf("validateEmail(%q) ok = %v, want %v", tt.email, got, tt.ok)
		}
	}
}

func TestValidatePassword_CountsCharactersNotBytes(t *testing.T) {
	// 12 characters but 36 bytes in UTF-8: must be accepted.
	if p := validatePassword(strings.Repeat("ሰ", 12)); p != "" {
		t.Errorf("12 Ethiopic characters rejected: %s", p)
	}
	if p := validatePassword(strings.Repeat("a", 11)); p == "" {
		t.Error("11-character password accepted")
	}
	if p := validatePassword(strings.Repeat("a", 129)); p == "" {
		t.Error("129-character password accepted")
	}
}

func TestNewUserInput_Validate(t *testing.T) {
	valid := NewUserInput{FullName: "Abebe Kebede", Email: "abebe@acme.com", Password: "a long enough password"}
	p := Problems{}
	valid.Validate(p)
	if err := p.Err(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}

	p = Problems{}
	NewUserInput{Email: "nope", Password: "short"}.Validate(p)
	var verr *ValidationError
	if err := p.Err(); !errors.As(err, &verr) {
		t.Fatalf("want *ValidationError, got %v", err)
	}
	for _, f := range []string{"full_name", "email", "password"} {
		if _, ok := verr.Fields[f]; !ok {
			t.Errorf("missing validation error for %s (got %v)", f, verr.Fields)
		}
	}
}

func TestNewUserInput_Normalize(t *testing.T) {
	in := NewUserInput{FullName: " Abebe ", Email: " ABEBE@Acme.com ", Password: "  keep my spaces  "}
	in.Normalize()
	if in.FullName != "Abebe" || in.Email != "abebe@acme.com" {
		t.Errorf("normalize: %+v", in)
	}
	if in.Password != "  keep my spaces  " {
		t.Errorf("password was modified: %q", in.Password)
	}
}
