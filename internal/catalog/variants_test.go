package catalog

import (
	"strings"
	"testing"

	"github.com/Joedee43/interior_api/internal/auth"
)

func testOptions() []Option {
	return []Option{
		{ID: "o1", Name: "Fabric", Values: []OptionValue{{ID: "linen", Name: "Linen"}, {ID: "leather", Name: "Leather"}}},
		{ID: "o2", Name: "Colour", Values: []OptionValue{{ID: "grey", Name: "Grey"}, {ID: "black", Name: "Black"}, {ID: "sage", Name: "Sage"}}},
	}
}

func TestCombinations(t *testing.T) {
	opts := testOptions()
	got := combinations(opts)
	if len(got) != 6 || combinationCount(opts) != 6 {
		t.Fatalf("got %d combinations (count %d), want 6", len(got), combinationCount(opts))
	}
	seen := map[string]bool{}
	for _, c := range got {
		if len(c) != 2 || c[0].OptionID != "o1" || c[1].OptionID != "o2" {
			t.Errorf("malformed combination %+v", c)
		}
		seen[combinationKey(c)] = true
	}
	if len(seen) != 6 {
		t.Errorf("combinations not unique: %d distinct", len(seen))
	}
	if n := len(combinations(nil)); n != 1 {
		t.Errorf("no options should give one empty combination, got %d", n)
	}
}

func TestCombinationKeyIsOrderIndependent(t *testing.T) {
	a := []Selection{{"o1", "linen"}, {"o2", "grey"}}
	b := []Selection{{"o2", "grey"}, {"o1", "linen"}}
	if combinationKey(a) != combinationKey(b) {
		t.Errorf("%q != %q", combinationKey(a), combinationKey(b))
	}
}

func TestAutoSKU(t *testing.T) {
	if got := autoSKU("oslo-sofa", []string{"Linen", "Light Grey"}); got != "OSLO-SOFA-LINEN-LIGHT-GREY" {
		t.Errorf("autoSKU = %q", got)
	}
	long := autoSKU(strings.Repeat("a", 90), []string{"Linen"})
	if len(long) > 60 || validateSKU(long) != "" {
		t.Errorf("long SKU %q (%d chars) invalid", long, len(long))
	}
}

func TestResolveSelections(t *testing.T) {
	cfg := Configuration{Options: testOptions()}

	p := auth.Problems{}
	sels := resolveSelections(cfg, map[string]string{"fabric": "LINEN", " Colour ": "grey"}, p)
	if p.Err() != nil || len(sels) != 2 || sels[0].ValueID != "linen" || sels[1].ValueID != "grey" {
		t.Errorf("case-insensitive match failed: %v %+v", p, sels)
	}

	for name, choices := range map[string]map[string]string{
		"missing option": {"Fabric": "Linen"},
		"unknown value":  {"Fabric": "Velvet", "Colour": "Grey"},
		"extra option":   {"Fabric": "Linen", "Colour": "Grey", "Legs": "Oak"},
	} {
		p := auth.Problems{}
		resolveSelections(cfg, choices, p)
		if p["options"] == "" {
			t.Errorf("%s: no problem reported", name)
		}
	}
}
