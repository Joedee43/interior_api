package company

import "testing"

func TestValidateSlug(t *testing.T) {
	tests := []struct {
		slug string
		ok   bool
	}{
		{"acme", true},
		{"acme-furniture-2", true},
		{"ab", false},         // too short
		{"-acme", false},      // leading hyphen
		{"acme-", false},      // trailing hyphen
		{"acme--home", false}, // double hyphen
		{"Acme", false},       // uppercase (NormalizeSlug runs first in practice)
		{"acme home", false},  // space
		{"ኣክሜ", false},        // non-ASCII
		{string(make([]byte, 51)), false},
	}
	for _, tt := range tests {
		if got := ValidateSlug(tt.slug) == ""; got != tt.ok {
			t.Errorf("ValidateSlug(%q) ok = %v, want %v", tt.slug, got, tt.ok)
		}
	}
}

func TestNormalizeSlug(t *testing.T) {
	if got := NormalizeSlug("  Acme-Furniture "); got != "acme-furniture" {
		t.Errorf("NormalizeSlug = %q", got)
	}
}
