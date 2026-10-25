package catalog

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSlugify(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Oslo 3-Seat Sofa!", "oslo-3-seat-sofa"},
		{"  Office   Chair  ", "office-chair"},
		{"Café Table", "caf-table"}, // non-ASCII letters are dropped, not transliterated
		{"ወንበር", ""},                // Amharic only: caller must supply a slug
		{"--A--B--", "a-b"},
	}
	for _, tt := range tests {
		if got := Slugify(tt.in); got != tt.want {
			t.Errorf("Slugify(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 123456000, time.UTC)
	p := Product{ID: "11111111-1111-1111-1111-111111111111", PublishedAt: &now, CreatedAt: now, PriceMinor: 4200}

	for _, sort := range []string{SortNewest, SortPriceAsc, SortPriceDesc, sortCreated} {
		enc := cursorAfter(p, sort).encode()
		got, err := decodeCursor(enc, sort)
		if err != nil {
			t.Fatalf("%s: decode: %v", sort, err)
		}
		if got.ID != p.ID {
			t.Errorf("%s: id = %q", sort, got.ID)
		}
		if got.Time != nil && !got.Time.Equal(now) {
			t.Errorf("%s: time lost precision: %v", sort, got.Time)
		}
	}
}

func TestDecodeCursor_Rejects(t *testing.T) {
	newest := cursorAfter(Product{ID: "x", PublishedAt: &time.Time{}}, SortNewest).encode()
	for name, tc := range map[string]struct{ raw, sort string }{
		"wrong sort":     {newest, SortPriceAsc},
		"not base64":     {"!!!", SortNewest},
		"not json":       {"bm90IGpzb24", SortNewest},
		"missing fields": {cursor{Sort: SortPriceAsc, ID: "x"}.encode(), SortPriceAsc},
	} {
		if _, err := decodeCursor(tc.raw, tc.sort); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestOptional(t *testing.T) {
	var body struct {
		W Optional[int32] `json:"w"`
	}
	for _, tc := range []struct {
		json    string
		set     bool
		wantNil bool
		want    int32
	}{
		{`{}`, false, true, 0},
		{`{"w":null}`, true, true, 0},
		{`{"w":7500}`, true, false, 7500},
	} {
		body.W = Optional[int32]{}
		if err := json.Unmarshal([]byte(tc.json), &body); err != nil {
			t.Fatalf("%s: %v", tc.json, err)
		}
		if body.W.Set != tc.set || (body.W.Value == nil) != tc.wantNil || (!tc.wantNil && *body.W.Value != tc.want) {
			t.Errorf("%s: got %+v", tc.json, body.W)
		}
	}
}
