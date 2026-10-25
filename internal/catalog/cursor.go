package catalog

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// Sort orders. Each has a matching ORDER BY and keyset condition in
// store.go; the cursor records which sort it belongs to.
const (
	SortNewest    = "newest"     // public: published_at DESC, id DESC
	SortPriceAsc  = "price_asc"  // public: price_minor ASC, id ASC
	SortPriceDesc = "price_desc" // public: price_minor DESC, id DESC
	sortCreated   = "created"    // private list: created_at DESC, id DESC
)

var errBadCursor = errors.New("invalid cursor")

// cursor is keyset pagination state: the sort key and id of the last row
// on the previous page. The next page is "rows strictly after this one in
// sort order", which stays correct while rows are inserted or removed —
// unlike OFFSET, which shifts and skips or repeats rows.
//
// It is sent to clients as opaque base64url(JSON). Clients must not build
// or parse it; only its round trip matters.
type cursor struct {
	Sort  string     `json:"s"`
	Time  *time.Time `json:"t,omitempty"`
	Price *int64     `json:"p,omitempty"`
	ID    string     `json:"id"`
}

func (c cursor) encode() string {
	b, _ := json.Marshal(c) // marshaling this struct cannot fail
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor parses s and checks it belongs to sort. A cursor from a
// different sort (or a tampered one) is rejected rather than misapplied.
func decodeCursor(s, sort string) (cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, errBadCursor
	}
	var c cursor
	if err := json.Unmarshal(b, &c); err != nil || c.Sort != sort || c.ID == "" {
		return cursor{}, errBadCursor
	}
	switch sort {
	case SortNewest, sortCreated:
		if c.Time == nil {
			return cursor{}, errBadCursor
		}
	case SortPriceAsc, SortPriceDesc:
		if c.Price == nil {
			return cursor{}, errBadCursor
		}
	}
	return c, nil
}

// cursorAfter builds the cursor pointing just past p for sort.
func cursorAfter(p Product, sort string) cursor {
	c := cursor{Sort: sort, ID: p.ID}
	switch sort {
	case SortNewest:
		c.Time = p.PublishedAt
	case sortCreated:
		t := p.CreatedAt
		c.Time = &t
	case SortPriceAsc, SortPriceDesc:
		price := p.PriceMinor
		c.Price = &price
	}
	return c
}

// Page is one page of results plus the cursor for the next page ("" when
// this is the last page).
type Page struct {
	Products   []Product
	NextCursor string
}
