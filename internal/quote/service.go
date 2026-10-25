package quote

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/catalog"
	"github.com/Joedee43/interior_api/internal/company"
	"github.com/Joedee43/interior_api/internal/postgres"
)

const (
	DefaultLimit = 20
	MaxLimit     = 100
)

type Service struct {
	pool    *pgxpool.Pool
	catalog Catalog
	designs Designs
	users   Users
}

func NewService(pool *pgxpool.Pool, cat Catalog, designs Designs, users Users) *Service {
	return &Service{pool: pool, catalog: cat, designs: designs, users: users}
}

// ---- submitting -------------------------------------------------------------

// Contact is what the customer adds at submit time; name and email come
// from their account.
type Contact struct {
	Phone           string
	DeliveryAddress string
	Note            string
}

func (c *Contact) validate(p auth.Problems) {
	c.Phone = strings.TrimSpace(c.Phone)
	c.DeliveryAddress = strings.TrimSpace(c.DeliveryAddress)
	c.Note = strings.TrimSpace(c.Note)
	checkLen(p, "contact_phone", c.Phone, 40)
	checkLen(p, "delivery_address", c.DeliveryAddress, 500)
	checkLen(p, "note", c.Note, 2000)
}

// Skipped is a design item left out of the requests because it is not
// available right now (archived product, switched-off variant...).
type Skipped struct {
	ItemID      string
	ProductName string
}

// Submission is the result of one submit: one request per seller.
type Submission struct {
	ID       string
	Requests []Request
	Skipped  []Skipped
}

// pending is one line being assembled before insertion.
type pending struct {
	info catalog.ItemInfo
	qty  int
}

// groupedLines merges identical product/variant pairs into quantities,
// keeping first-seen order, and groups them by company.
type sellerLines struct {
	companyID string
	currency  string
	order     []catalog.ItemRef
	lines     map[catalog.ItemRef]*pending
}

func newSubmissionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func toLine(p pending) Line {
	l := Line{
		ProductName: p.info.ProductName, ProductSlug: p.info.ProductSlug, SKU: p.info.SKU,
		Options: p.info.Options, WidthMM: p.info.WidthMM, DepthMM: p.info.DepthMM, HeightMM: p.info.HeightMM,
		Quantity: p.qty, ListUnit: p.info.PriceMinor,
	}
	pid := p.info.Ref.ProductID
	l.ProductID = &pid
	if p.info.Ref.VariantID != "" {
		vid := p.info.Ref.VariantID
		l.VariantID = &vid
	}
	return l
}

// create inserts one request per seller in a single transaction: either
// every seller's request exists, or none does.
func (s *Service) create(ctx context.Context, userID string, designID *string, contact Contact, sellers []*sellerLines) (Submission, error) {
	user, err := s.users.GetUser(ctx, userID)
	if err != nil {
		return Submission{}, err
	}
	sub := Submission{ID: newSubmissionID()}
	var ids []string
	err = postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, sl := range sellers {
			lines := make([]Line, 0, len(sl.order))
			for _, ref := range sl.order {
				lines = append(lines, toLine(*sl.lines[ref]))
			}
			id, err := insertRequest(ctx, tx, newRequest{
				SubmissionID: sub.ID, CompanyID: sl.companyID, CustomerID: userID, Currency: sl.currency,
				DesignID: designID, ContactName: user.FullName, ContactEmail: user.Email,
				ContactPhone: contact.Phone, DeliveryAddress: contact.DeliveryAddress, CustomerNote: contact.Note,
				Lines: lines,
			})
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return Submission{}, err
	}
	for _, id := range ids {
		r, err := getRequest(ctx, s.pool, customerScope(userID), id)
		if err != nil {
			return Submission{}, err
		}
		sub.Requests = append(sub.Requests, r)
	}
	return sub, nil
}

func addToSellers(sellers *[]*sellerLines, byCompany map[string]*sellerLines, info catalog.ItemInfo, qty int) {
	sl, ok := byCompany[info.CompanyID]
	if !ok {
		sl = &sellerLines{companyID: info.CompanyID, currency: info.Currency, lines: map[catalog.ItemRef]*pending{}}
		byCompany[info.CompanyID] = sl
		*sellers = append(*sellers, sl)
	}
	if p, ok := sl.lines[info.Ref]; ok {
		p.qty = min(p.qty+qty, maxQuantity)
		return
	}
	sl.lines[info.Ref] = &pending{info: info, qty: qty}
	sl.order = append(sl.order, info.Ref)
}

// SubmitFromDesign requests quotes for everything available in one of the
// user's designs, one request per seller. Unavailable items are skipped
// and reported.
func (s *Service) SubmitFromDesign(ctx context.Context, userID, designID string, contact Contact) (Submission, error) {
	p := auth.Problems{}
	contact.validate(p)
	if err := p.Err(); err != nil {
		return Submission{}, err
	}
	d, err := s.designs.Get(ctx, userID, designID) // 404 unless the user owns it
	if err != nil {
		return Submission{}, err
	}

	var sellers []*sellerLines
	byCompany := map[string]*sellerLines{}
	var skipped []Skipped
	for _, it := range d.Items {
		if !it.Info.Available {
			skipped = append(skipped, Skipped{ItemID: it.ID, ProductName: it.Info.ProductName})
			continue
		}
		addToSellers(&sellers, byCompany, it.Info, 1)
	}
	if len(sellers) == 0 {
		return Submission{Skipped: skipped}, ErrNothingAvailable
	}
	id := d.ID
	sub, err := s.create(ctx, userID, &id, contact, sellers)
	sub.Skipped = skipped
	return sub, err
}

// RequestedItem is one product picked directly (not from a design).
type RequestedItem struct {
	ProductID string
	VariantID string
	Quantity  int
}

// SubmitItems requests a quote for products picked directly. They must
// all come from one seller and pass the same rules as placing them in a
// design (published, valid available variant).
func (s *Service) SubmitItems(ctx context.Context, userID string, items []RequestedItem, contact Contact) (Submission, error) {
	p := auth.Problems{}
	contact.validate(p)
	if len(items) == 0 || len(items) > 50 {
		p.Add("items", "send 1 to 50 items")
	}
	for i, it := range items {
		if it.Quantity < 1 || it.Quantity > maxQuantity {
			p.Add(fmt.Sprintf("items[%d].quantity", i), "must be between 1 and 999")
		}
	}
	if err := p.Err(); err != nil {
		return Submission{}, err
	}

	var sellers []*sellerLines
	byCompany := map[string]*sellerLines{}
	for i, it := range items {
		info, err := s.catalog.Placement(ctx, catalog.ItemRef{ProductID: it.ProductID, VariantID: it.VariantID})
		var pe *catalog.PlacementError
		if errors.As(err, &pe) {
			return Submission{}, &auth.ValidationError{Fields: map[string]string{fmt.Sprintf("items[%d].%s", i, pe.Field): pe.Reason}}
		}
		if err != nil {
			return Submission{}, err
		}
		addToSellers(&sellers, byCompany, info, it.Quantity)
	}
	if len(sellers) > 1 {
		return Submission{}, &auth.ValidationError{Fields: map[string]string{
			"items": "all items must come from one seller; request a quote from a design to ask several sellers at once"}}
	}
	return s.create(ctx, userID, nil, contact, sellers)
}

// ---- reading ----------------------------------------------------------------

type Page struct {
	Requests   []Request
	NextCursor string
}

type listCursor struct {
	T  time.Time `json:"t"`
	ID string    `json:"id"`
}

func validStatus(s string) bool {
	switch s {
	case StatusPending, StatusReviewing, StatusQuoted, StatusAccepted, StatusRejected, StatusCancelled, StatusDeclined:
		return true
	}
	return false
}

func (s *Service) list(ctx context.Context, sc scope, status string, limit int, raw string) (Page, error) {
	p := auth.Problems{}
	switch {
	case limit == 0:
		limit = DefaultLimit
	case limit < 1 || limit > MaxLimit:
		p.Add("limit", "must be between 1 and 100")
	}
	if status != "" && !validStatus(status) {
		p.Add("status", "must be pending, reviewing, quoted, accepted, rejected, cancelled or declined")
	}
	var after *listCursor
	if raw != "" {
		var c listCursor
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(b, &c) != nil || c.ID == "" {
			p.Add("cursor", "is invalid; pass next_cursor from the previous page unchanged")
		} else {
			after = &c
		}
	}
	if err := p.Err(); err != nil {
		return Page{}, err
	}

	var rs []Request
	var err error
	if after != nil {
		rs, err = listRequests(ctx, s.pool, sc, status, limit, &after.T, after.ID)
	} else {
		rs, err = listRequests(ctx, s.pool, sc, status, limit, nil, "")
	}
	if err != nil {
		return Page{}, err
	}
	pg := Page{Requests: rs}
	if len(rs) > limit {
		pg.Requests = rs[:limit]
		last := pg.Requests[limit-1]
		b, _ := json.Marshal(listCursor{T: last.SubmittedAt, ID: last.ID})
		pg.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return pg, nil
}

func (s *Service) CustomerList(ctx context.Context, userID, status string, limit int, cursor string) (Page, error) {
	return s.list(ctx, customerScope(userID), status, limit, cursor)
}

func (s *Service) CompanyList(ctx context.Context, m company.Member, status string, limit int, cursor string) (Page, error) {
	return s.list(ctx, companyScope(m.CompanyID), status, limit, cursor)
}

func (s *Service) CustomerGet(ctx context.Context, userID, id string) (Request, error) {
	return getRequest(ctx, s.pool, customerScope(userID), id)
}

func (s *Service) CompanyGet(ctx context.Context, m company.Member, id string) (Request, error) {
	return getRequest(ctx, s.pool, companyScope(m.CompanyID), id)
}

// ---- workflow -----------------------------------------------------------------

// act runs one state-machine action under the request's row lock. check
// may refuse based on the locked state (revision, expiry); apply performs
// the write. The actor must match the action's side.
func (s *Service) act(ctx context.Context, sc scope, actor Actor, id, action string,
	check func(lockedState) error, apply func(tx pgx.Tx, st lockedState) error) (Request, error) {
	t := transitions[action]
	if t.actor != actor {
		return Request{}, ErrInvalidTransition
	}
	err := postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		st, err := lockRequest(ctx, tx, sc, id)
		if err != nil {
			return err
		}
		if !t.allows(st.Status) {
			return ErrInvalidTransition
		}
		if check != nil {
			if err := check(st); err != nil {
				return err
			}
		}
		return apply(tx, st)
	})
	if err != nil {
		return Request{}, err
	}
	return getRequest(ctx, s.pool, sc, id)
}

// Accept requires the revision the customer was looking at, so a quote
// revised a moment ago is never accepted unseen (optimistic concurrency).
func (s *Service) Accept(ctx context.Context, userID, id string, revision int) (Request, error) {
	return s.act(ctx, customerScope(userID), Customer, id, "accept",
		func(st lockedState) error {
			if st.Revision != revision {
				return ErrStaleRevision
			}
			if st.Expired {
				return ErrQuoteExpired
			}
			return nil
		},
		func(tx pgx.Tx, _ lockedState) error { return setStatus(ctx, tx, id, StatusAccepted, "", false) })
}

func (s *Service) Reject(ctx context.Context, userID, id string) (Request, error) {
	return s.act(ctx, customerScope(userID), Customer, id, "reject", nil,
		func(tx pgx.Tx, _ lockedState) error { return setStatus(ctx, tx, id, StatusRejected, "", false) })
}

func (s *Service) Cancel(ctx context.Context, userID, id string) (Request, error) {
	return s.act(ctx, customerScope(userID), Customer, id, "cancel", nil,
		func(tx pgx.Tx, _ lockedState) error { return setStatus(ctx, tx, id, StatusCancelled, "", false) })
}

func (s *Service) Review(ctx context.Context, m company.Member, id string) (Request, error) {
	return s.act(ctx, companyScope(m.CompanyID), Company, id, "review", nil,
		func(tx pgx.Tx, _ lockedState) error { return setStatus(ctx, tx, id, StatusReviewing, "", false) })
}

func (s *Service) Decline(ctx context.Context, m company.Member, id, note string) (Request, error) {
	note = strings.TrimSpace(note)
	p := auth.Problems{}
	checkLen(p, "note", note, 2000)
	if err := p.Err(); err != nil {
		return Request{}, err
	}
	return s.act(ctx, companyScope(m.CompanyID), Company, id, "decline", nil,
		func(tx pgx.Tx, _ lockedState) error { return setStatus(ctx, tx, id, StatusDeclined, note, true) })
}

// PricedLine is the company's price for one line.
type PricedLine struct {
	LineID         string
	UnitPriceMinor int64
	Note           string
}

type QuoteInput struct {
	Lines      []PricedLine
	Charges    []Charge
	ValidUntil string // "YYYY-MM-DD", optional
	Note       string
}

// SendQuote prices every line, sets charges, and marks the request quoted
// with the next revision. Sending again while quoted is a revision.
func (s *Service) SendQuote(ctx context.Context, m company.Member, id string, in QuoteInput) (Request, error) {
	p := auth.Problems{}
	in.Note = strings.TrimSpace(in.Note)
	checkLen(p, "note", in.Note, 2000)
	prices := make(map[string]PricedLine, len(in.Lines))
	for i, l := range in.Lines {
		key := fmt.Sprintf("lines[%d]", i)
		if _, dup := prices[l.LineID]; dup {
			p.Add(key+".line_id", "each line may be priced once")
		}
		if l.UnitPriceMinor < 0 || l.UnitPriceMinor > maxUnitPrice {
			p.Add(key+".unit_price_minor", "must be between 0 and 1000000000000")
		}
		l.Note = strings.TrimSpace(l.Note)
		checkLen(p, key+".note", l.Note, 500)
		prices[l.LineID] = l
	}
	if len(in.Charges) > maxCharges {
		p.Add("charges", fmt.Sprintf("at most %d charges", maxCharges))
	}
	for i := range in.Charges {
		in.Charges[i].Label = strings.TrimSpace(in.Charges[i].Label)
		key := fmt.Sprintf("charges[%d]", i)
		if n := len([]rune(in.Charges[i].Label)); n < 1 || n > 100 {
			p.Add(key+".label", "must be 1 to 100 characters")
		}
		if a := in.Charges[i].AmountMinor; a < 0 || a > maxUnitPrice {
			p.Add(key+".amount_minor", "must be between 0 and 1000000000000")
		}
	}
	var validUntil *time.Time
	if in.ValidUntil != "" {
		// A calendar date, not an instant: parse the date only.
		d, err := time.Parse(time.DateOnly, in.ValidUntil)
		if err != nil {
			p.Add("valid_until", "must be a date like 2026-12-31")
		} else {
			validUntil = &d
		}
	}
	if err := p.Err(); err != nil {
		return Request{}, err
	}

	return s.act(ctx, companyScope(m.CompanyID), Company, id, "quote", nil, func(tx pgx.Tx, st lockedState) error {
		if validUntil != nil && validUntil.Before(st.Today) {
			return &auth.ValidationError{Fields: map[string]string{"valid_until": "must be today or later"}}
		}
		lines, err := lineIDs(ctx, tx, id)
		if err != nil {
			return err
		}
		lp := auth.Problems{}
		var total int64
		for lineID, qty := range lines {
			pl, ok := prices[lineID]
			if !ok {
				lp.Add("lines", "every line needs a price; missing "+lineID)
				continue
			}
			// Bounded: price <= 1e12, qty <= 999, lines <= 200 keeps the
			// total far below the int64 limit (9.2e18).
			total += pl.UnitPriceMinor * int64(qty)
		}
		for lineID := range prices {
			if _, ok := lines[lineID]; !ok {
				lp.Add("lines", "unknown line "+lineID+" (lines belong to this request only)")
			}
		}
		if err := lp.Err(); err != nil {
			return err
		}
		for _, c := range in.Charges {
			total += c.AmountMinor
		}
		return saveQuote(ctx, tx, id, prices, in.Charges, validUntil, in.Note, total)
	})
}
