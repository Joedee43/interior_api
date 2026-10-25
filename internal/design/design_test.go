package design

import (
	"math"
	"testing"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/catalog"
)

var office = Room{WidthMM: 3000, DepthMM: 4000, HeightMM: 2700}

func TestFeaturesValidate(t *testing.T) {
	ok := Features{WallColor: "#f2efe9", Floor: "OAK", Openings: []Opening{
		{Type: "Door", Wall: "south", OffsetMM: 300, WidthMM: 900, HeightMM: 2100},
		{Type: "window", Wall: "east", OffsetMM: 0, WidthMM: 4000, HeightMM: 1200, SillMM: 900},
	}}
	ok.normalize()
	p := auth.Problems{}
	ok.validate(office, p)
	if err := p.Err(); err != nil {
		t.Fatalf("valid features rejected: %v", err)
	}
	if ok.WallColor != "#F2EFE9" || ok.Floor != "oak" || ok.Openings[0].Type != "door" {
		t.Errorf("not normalized: %+v", ok)
	}

	for name, tc := range map[string]struct {
		f     Features
		field string
	}{
		"bad colour":      {Features{WallColor: "red"}, "features.wall_color"},
		"bad floor":       {Features{Floor: "lava"}, "features.floor"},
		"bad type":        {Features{Openings: []Opening{{Type: "hatch", Wall: "north", WidthMM: 500, HeightMM: 500}}}, "features.openings[0].type"},
		"bad wall":        {Features{Openings: []Opening{{Type: "door", Wall: "up", WidthMM: 500, HeightMM: 500}}}, "features.openings[0].wall"},
		"too wide":        {Features{Openings: []Opening{{Type: "door", Wall: "north", OffsetMM: 2500, WidthMM: 900, HeightMM: 2000}}}, "features.openings[0]"},
		"too tall":        {Features{Openings: []Opening{{Type: "window", Wall: "west", WidthMM: 900, HeightMM: 2000, SillMM: 1000}}}, "features.openings[0]"},
		"negative offset": {Features{Openings: []Opening{{Type: "door", Wall: "west", OffsetMM: -1, WidthMM: 900, HeightMM: 2000}}}, "features.openings[0]"},
	} {
		tc.f.normalize()
		p := auth.Problems{}
		tc.f.validate(office, p)
		if p[tc.field] == "" {
			t.Errorf("%s: no problem on %s (got %v)", name, tc.field, p)
		}
	}
}

func TestNormalizeRotation(t *testing.T) {
	for in, want := range map[float64]float64{
		0: 0, 90: 90, 359.99: 359.99, 360: 0, -90: 270, 450: 90, 12.345: 12.35, -0.001: 0, 720.5: 0.5,
	} {
		p := auth.Problems{}
		if got := normalizeRotation(in, p); got != want || p.Err() != nil {
			t.Errorf("normalizeRotation(%v) = %v, want %v (%v)", in, got, want, p)
		}
	}
	if got := normalizeRotation(-0.001, auth.Problems{}); math.Signbit(got) {
		t.Error("negative zero leaked out")
	}
	p := auth.Problems{}
	normalizeRotation(1e9, p)
	if p["rotation_deg"] == "" {
		t.Error("absurd rotation accepted")
	}
}

func TestEstimate(t *testing.T) {
	item := func(company, currency string, price int64, available bool) PlacedItem {
		return PlacedItem{Info: catalog.ItemInfo{CompanySlug: company, CompanyName: company, Currency: currency, PriceMinor: price, Available: available}}
	}
	e := estimate([]PlacedItem{
		item("b-co", "ETB", 100, true),
		item("a-co", "ETB", 50, true),
		item("a-co", "ETB", 25, true),
		item("a-co", "USD", 7, true),
		item("a-co", "ETB", 999, false), // unavailable: counted, not priced
	})
	if e.ItemCount != 5 || e.UnavailableCount != 1 || len(e.Totals) != 3 {
		t.Fatalf("estimate: %+v", e)
	}
	if e.Totals[0] != (Total{"a-co", "a-co", "ETB", 75, 2}) || e.Totals[1].Currency != "USD" || e.Totals[2].CompanySlug != "b-co" {
		t.Errorf("totals not grouped/sorted: %+v", e.Totals)
	}
}
