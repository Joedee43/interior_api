// Package quote owns requests for quotation: a customer asks one company
// to price products (from a saved design or picked directly); the company
// reviews and quotes; the customer accepts or rejects.
//
// It depends on catalog, design and auth only through the small
// interfaces below, and snapshots everything it needs at submit time.
package quote

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/catalog"
	"github.com/Joedee43/interior_api/internal/design"
)

// Consumer-side interfaces: what quote needs from other packages.
type (
	Catalog interface {
		Placement(ctx context.Context, ref catalog.ItemRef) (catalog.ItemInfo, error)
	}
	Designs interface {
		Get(ctx context.Context, userID, id string) (design.Detail, error)
	}
	Users interface {
		GetUser(ctx context.Context, userID string) (auth.User, error)
	}
)

var (
	_ Catalog = (*catalog.Service)(nil)
	_ Designs = (*design.Service)(nil)
	_ Users   = (*auth.Service)(nil)
)

const (
	StatusPending   = "pending"
	StatusReviewing = "reviewing"
	StatusQuoted    = "quoted"
	StatusAccepted  = "accepted"
	StatusRejected  = "rejected"
	StatusCancelled = "cancelled"
	StatusDeclined  = "declined"

	maxItemsPerRequest = 200
	maxQuantity        = 999
	maxCharges         = 20
	maxUnitPrice       = 1_000_000_000_000
)

var (
	ErrNotFound          = errors.New("quote request not found")
	ErrInvalidTransition = errors.New("the request's status does not allow this action")
	ErrStaleRevision     = errors.New("the quote was revised; review the latest revision")
	ErrQuoteExpired      = errors.New("the quote has expired")
	ErrNothingAvailable  = errors.New("no available items to quote")
)

// Actor is which side of the conversation is acting.
type Actor int

const (
	Customer Actor = iota
	Company
)

// transition is one row of the state machine.
type transition struct {
	actor Actor
	from  []string
	to    string
}

// transitions is the whole workflow in one table. Anything not listed —
// a wrong status, or the wrong side acting — is refused.
var transitions = map[string]transition{
	"review":  {Company, []string{StatusPending}, StatusReviewing},
	"quote":   {Company, []string{StatusPending, StatusReviewing, StatusQuoted}, StatusQuoted},
	"decline": {Company, []string{StatusPending, StatusReviewing}, StatusDeclined},
	"accept":  {Customer, []string{StatusQuoted}, StatusAccepted},
	"reject":  {Customer, []string{StatusQuoted}, StatusRejected},
	"cancel":  {Customer, []string{StatusPending, StatusReviewing, StatusQuoted}, StatusCancelled},
}

func (t transition) allows(status string) bool {
	for _, f := range t.from {
		if f == status {
			return true
		}
	}
	return false
}

// IsFinal reports whether no further action is possible.
func IsFinal(status string) bool {
	switch status {
	case StatusAccepted, StatusRejected, StatusCancelled, StatusDeclined:
		return true
	}
	return false
}

// ---- data -------------------------------------------------------------------

type Request struct {
	ID              string
	SubmissionID    string
	CompanyID       string
	CompanySlug     string
	CompanyName     string
	CustomerID      string
	DesignID        *string
	Status          string
	Revision        int
	Currency        string
	ContactName     string
	ContactEmail    string
	ContactPhone    string
	DeliveryAddress string
	CustomerNote    string
	CompanyNote     string
	ValidUntil      *time.Time // a calendar date (time part is midnight UTC)
	Expired         bool       // valid_until is before the database's current date
	QuotedTotal     *int64
	ListTotal       int64 // Σ list price × quantity (computed)
	LineCount       int
	SubmittedAt     time.Time
	QuotedAt        *time.Time
	DecidedAt       *time.Time
	UpdatedAt       time.Time
	Lines           []Line
	Charges         []Charge
}

type Line struct {
	ID          string
	Position    int
	ProductID   *string
	VariantID   *string
	ProductName string
	ProductSlug string
	SKU         *string
	Options     map[string]string
	WidthMM     int32
	DepthMM     int32
	HeightMM    int32
	Quantity    int
	ListUnit    int64
	QuotedUnit  *int64
	Note        string
}

type Charge struct {
	Label       string
	AmountMinor int64
}

// ---- validation -------------------------------------------------------------

func checkLen(p auth.Problems, field, s string, max int) {
	if utf8.RuneCountInString(s) > max {
		p.Add(field, fmt.Sprintf("must be at most %d characters", max))
	}
}
