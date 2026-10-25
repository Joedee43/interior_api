package quote_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Joedee43/interior_api/internal/apitest"
)

// ---- fixtures -----------------------------------------------------------------

type product struct {
	ID, Slug string
	LIN      string // variant id (sofas only)
}

func decode(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}

func sellerDo(t *testing.T, e *apitest.Env, co apitest.Company, method, path, body string, want int) []byte {
	t.Helper()
	w := e.As(co.AdminCookie, method, path, body)
	if w.Code != want {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
	}
	return w.Body.Bytes()
}

func chair(t *testing.T, e *apitest.Env, co apitest.Company, name string, price int64) product {
	t.Helper()
	var p product
	decode(t, sellerDo(t, e, co, http.MethodPost, "/v1/companies/"+co.ID+"/products",
		fmt.Sprintf(`{"category":"chairs","name":%q,"price_minor":%d,"width_mm":600,"depth_mm":550,"height_mm":900}`, name, price),
		http.StatusCreated), &p)
	sellerDo(t, e, co, http.MethodPost, "/v1/companies/"+co.ID+"/products/"+p.ID+"/publish", "", http.StatusOK)
	return p
}

func sofa(t *testing.T, e *apitest.Env, co apitest.Company, name string) product {
	t.Helper()
	var p product
	decode(t, sellerDo(t, e, co, http.MethodPost, "/v1/companies/"+co.ID+"/products",
		fmt.Sprintf(`{"category":"sofas","name":%q,"price_minor":1,"width_mm":2100,"depth_mm":900,"height_mm":820}`, name),
		http.StatusCreated), &p)
	base := "/v1/companies/" + co.ID + "/products/" + p.ID
	sellerDo(t, e, co, http.MethodPost, base+"/options", `{"name":"Fabric","values":[{"name":"Linen"}]}`, http.StatusCreated)
	var cfg struct {
		Variants []struct{ ID string } `json:"variants"`
	}
	decode(t, sellerDo(t, e, co, http.MethodPost, base+"/variants",
		`{"sku":"`+strings.ToUpper(p.Slug)+`-LIN","price_minor":50000,"options":{"Fabric":"Linen"}}`, http.StatusCreated), &cfg)
	p.LIN = cfg.Variants[0].ID
	sellerDo(t, e, co, http.MethodPost, base+"/publish", "", http.StatusOK)
	return p
}

type line struct {
	ID          string            `json:"id"`
	ProductName string            `json:"product_name"`
	SKU         *string           `json:"sku"`
	Options     map[string]string `json:"options"`
	Quantity    int               `json:"quantity"`
	ListUnit    int64             `json:"list_unit_price_minor"`
	QuotedUnit  *int64            `json:"quoted_unit_price_minor"`
}

type request struct {
	ID           string `json:"id"`
	SubmissionID string `json:"submission_id"`
	Status       string `json:"status"`
	Revision     int    `json:"revision"`
	Final        bool   `json:"final"`
	Company      struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	} `json:"company"`
	Customer struct {
		Email string `json:"email"`
		Phone string `json:"phone"`
	} `json:"customer"`
	ListTotal   int64   `json:"list_total_minor"`
	QuotedTotal *int64  `json:"quoted_total_minor"`
	ValidUntil  *string `json:"valid_until"`
	Expired     bool    `json:"expired"`
	Lines       []line  `json:"lines"`
	Charges     []struct {
		Label       string `json:"label"`
		AmountMinor int64  `json:"amount_minor"`
	} `json:"charges"`
}

type submission struct {
	SubmissionID string    `json:"submission_id"`
	Requests     []request `json:"requests"`
	Skipped      []struct {
		ProductName string `json:"product_name"`
	} `json:"skipped"`
}

func as(t *testing.T, e *apitest.Env, cookie *http.Cookie, method, path, body string, want int) []byte {
	t.Helper()
	w := e.As(cookie, method, path, body)
	if w.Code != want {
		t.Fatalf("%s %s %s: %d %s, want %d", method, path, body, w.Code, w.Body, want)
	}
	return w.Body.Bytes()
}

func expectErr(t *testing.T, e *apitest.Env, cookie *http.Cookie, method, path, body string, status int, code string) {
	t.Helper()
	w := e.As(cookie, method, path, body)
	if w.Code != status || apitest.ErrorCode(t, w) != code {
		t.Errorf("%s %s %s: %d %s, want %d %s", method, path, body, w.Code, w.Body, status, code)
	}
}

// directRequest submits one product directly and returns the request.
func directRequest(t *testing.T, e *apitest.Env, u apitest.User, p product, qty int) request {
	t.Helper()
	var s submission
	decode(t, as(t, e, u.Cookie, http.MethodPost, "/v1/quote-requests",
		fmt.Sprintf(`{"items":[{"product_id":%q,"quantity":%d}],"contact_phone":"+251 911 000000"}`, p.ID, qty), http.StatusCreated), &s)
	return s.Requests[0]
}

func companyPath(co apitest.Company, id string) string {
	return "/v1/companies/" + co.ID + "/quote-requests/" + id
}

func priceAll(r request, unit int64) string {
	parts := make([]string, len(r.Lines))
	for i, l := range r.Lines {
		parts[i] = fmt.Sprintf(`{"line_id":%q,"unit_price_minor":%d}`, l.ID, unit)
	}
	return `{"lines":[` + strings.Join(parts, ",") + `]}`
}

// ---- tests --------------------------------------------------------------------

func TestSubmitFromDesignSplitsBySeller(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	b := e.RegisterCompany()
	ch := chair(t, e, a, "Split Chair", 12000)
	gone := chair(t, e, a, "Soon Archived Chair", 999)
	so := sofa(t, e, b, "Split Sofa")

	u := e.RegisterCustomer()
	var d struct{ ID string }
	decode(t, as(t, e, u.Cookie, http.MethodPost, "/v1/designs", `{"name":"Office","template":"open-office-8x10"}`, http.StatusCreated), &d)
	place := func(body string) {
		as(t, e, u.Cookie, http.MethodPost, "/v1/designs/"+d.ID+"/items", body, http.StatusCreated)
	}
	place(`{"product_id":"` + ch.ID + `","x_mm":100,"z_mm":100}`)
	place(`{"product_id":"` + ch.ID + `","x_mm":900,"z_mm":100}`)
	place(`{"product_id":"` + so.ID + `","variant_id":"` + so.LIN + `","x_mm":3000,"z_mm":3000}`)
	place(`{"product_id":"` + gone.ID + `","x_mm":5000,"z_mm":100}`)
	sellerDo(t, e, a, http.MethodPost, "/v1/companies/"+a.ID+"/products/"+gone.ID+"/archive", "", http.StatusOK)

	var s submission
	decode(t, as(t, e, u.Cookie, http.MethodPost, "/v1/quote-requests/from-design",
		`{"design_id":"`+d.ID+`","delivery_address":"Bole, Addis Ababa","note":"Delivery before December please"}`, http.StatusCreated), &s)

	if len(s.Requests) != 2 || len(s.Skipped) != 1 || s.Skipped[0].ProductName != "Soon Archived Chair" {
		t.Fatalf("submission: %+v", s)
	}
	bySeller := map[string]request{}
	for _, r := range s.Requests {
		if r.SubmissionID != s.SubmissionID || r.Status != "pending" || r.Customer.Email != u.Email {
			t.Errorf("request %+v", r)
		}
		bySeller[r.Company.ID] = r
	}
	ra, rb := bySeller[a.ID], bySeller[b.ID]
	if len(ra.Lines) != 1 || ra.Lines[0].Quantity != 2 || ra.Lines[0].ListUnit != 12000 || ra.ListTotal != 24000 {
		t.Errorf("seller A request: %+v", ra)
	}
	if len(rb.Lines) != 1 || rb.Lines[0].SKU == nil || rb.Lines[0].Options["Fabric"] != "Linen" || rb.Lines[0].ListUnit != 50000 {
		t.Errorf("seller B request: %+v", rb)
	}

	// Each seller sees only its own request, even from a shared submission.
	var la struct{ Requests []request }
	decode(t, as(t, e, a.AdminCookie, http.MethodGet, "/v1/companies/"+a.ID+"/quote-requests", "", http.StatusOK), &la)
	if len(la.Requests) != 1 || la.Requests[0].ID != ra.ID {
		t.Errorf("seller A list: %+v", la.Requests)
	}
	expectErr(t, e, a.AdminCookie, http.MethodGet, companyPath(a, rb.ID), "", http.StatusNotFound, "not_found")
	expectErr(t, e, a.AdminCookie, http.MethodGet, companyPath(b, rb.ID), "", http.StatusNotFound, "not_found")
}

func TestNothingAvailable(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	ch := chair(t, e, a, "Lonely Chair", 1)
	u := e.RegisterCustomer()
	var d struct{ ID string }
	decode(t, as(t, e, u.Cookie, http.MethodPost, "/v1/designs", `{"name":"Den","template":"home-office-3x4"}`, http.StatusCreated), &d)
	as(t, e, u.Cookie, http.MethodPost, "/v1/designs/"+d.ID+"/items", `{"product_id":"`+ch.ID+`","x_mm":1,"z_mm":1}`, http.StatusCreated)
	sellerDo(t, e, a, http.MethodPost, "/v1/companies/"+a.ID+"/products/"+ch.ID+"/archive", "", http.StatusOK)

	expectErr(t, e, u.Cookie, http.MethodPost, "/v1/quote-requests/from-design", `{"design_id":"`+d.ID+`"}`, http.StatusBadRequest, "nothing_available")
	other := e.RegisterCustomer()
	expectErr(t, e, other.Cookie, http.MethodPost, "/v1/quote-requests/from-design", `{"design_id":"`+d.ID+`"}`, http.StatusNotFound, "not_found")
}

func TestSubmitItems(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	b := e.RegisterCompany()
	ch := chair(t, e, a, "Direct Chair", 3000)
	so := sofa(t, e, a, "Direct Sofa")
	other := chair(t, e, b, "Other Seller Chair", 1)
	u := e.RegisterCustomer()

	var s submission
	decode(t, as(t, e, u.Cookie, http.MethodPost, "/v1/quote-requests",
		`{"items":[{"product_id":"`+ch.ID+`","quantity":2},{"product_id":"`+so.ID+`","variant_id":"`+so.LIN+`","quantity":1},{"product_id":"`+ch.ID+`","quantity":3}]}`,
		http.StatusCreated), &s)
	if len(s.Requests) != 1 || len(s.Requests[0].Lines) != 2 || s.Requests[0].Lines[0].Quantity != 5 {
		t.Fatalf("merged request: %+v", s.Requests)
	}

	for body, code := range map[string]string{
		`{"items":[{"product_id":"` + ch.ID + `","quantity":1},{"product_id":"` + other.ID + `","quantity":1}]}`: "validation_failed",
		`{"items":[{"product_id":"` + so.ID + `","quantity":1}]}`:                                                "validation_failed",
		`{"items":[{"product_id":"` + ch.ID + `","quantity":0}]}`:                                                "validation_failed",
		`{"items":[]}`: "validation_failed",
		`{"items":[{"product_id":"nope","quantity":1}]}`: "validation_failed",
	} {
		expectErr(t, e, u.Cookie, http.MethodPost, "/v1/quote-requests", body, http.StatusBadRequest, code)
	}
	if w := e.Do(http.MethodPost, "/v1/quote-requests", `{"items":[]}`); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous submit: %d", w.Code)
	}
}

func TestQuoteLifecycle(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	ch := chair(t, e, a, "Lifecycle Chair", 10000)
	u := e.RegisterCustomer()
	r := directRequest(t, e, u, ch, 4)

	var rv request
	decode(t, as(t, e, a.AdminCookie, http.MethodPost, companyPath(a, r.ID)+"/review", "", http.StatusOK), &rv)
	if rv.Status != "reviewing" {
		t.Errorf("after review: %s", rv.Status)
	}

	tomorrow := time.Now().AddDate(0, 0, 2).Format(time.DateOnly)
	body := fmt.Sprintf(`{"lines":[{"line_id":%q,"unit_price_minor":9000,"note":"bulk discount"}],
		"charges":[{"label":"Delivery to Bole","amount_minor":150000},{"label":"Assembly","amount_minor":20000}],
		"valid_until":%q,"note":"Prices include VAT."}`, r.Lines[0].ID, tomorrow)
	var q request
	decode(t, as(t, e, a.AdminCookie, http.MethodPut, companyPath(a, r.ID)+"/quote", body, http.StatusOK), &q)
	want := int64(9000*4 + 150000 + 20000)
	if q.Status != "quoted" || q.Revision != 1 || q.QuotedTotal == nil || *q.QuotedTotal != want ||
		len(q.Charges) != 2 || q.ValidUntil == nil || *q.ValidUntil != tomorrow || *q.Lines[0].QuotedUnit != 9000 {
		t.Fatalf("quoted: %+v", q)
	}

	path := "/v1/quote-requests/" + r.ID
	expectErr(t, e, u.Cookie, http.MethodPost, path+"/accept", `{"revision":0}`, http.StatusConflict, "stale_revision")
	expectErr(t, e, u.Cookie, http.MethodPost, path+"/accept", `{}`, http.StatusBadRequest, "validation_failed")
	var acc request
	decode(t, as(t, e, u.Cookie, http.MethodPost, path+"/accept", `{"revision":1}`, http.StatusOK), &acc)
	if acc.Status != "accepted" || !acc.Final {
		t.Errorf("accepted: %+v", acc)
	}
	// Final: nothing else is possible.
	expectErr(t, e, u.Cookie, http.MethodPost, path+"/cancel", "", http.StatusConflict, "invalid_transition")
	expectErr(t, e, a.AdminCookie, http.MethodPost, companyPath(a, r.ID)+"/decline", `{}`, http.StatusConflict, "invalid_transition")
	expectErr(t, e, a.AdminCookie, http.MethodPut, companyPath(a, r.ID)+"/quote", priceAll(r, 1), http.StatusConflict, "invalid_transition")
}

func TestRevisions(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	ch := chair(t, e, a, "Revised Chair", 10000)
	u := e.RegisterCustomer()
	r := directRequest(t, e, u, ch, 1)

	as(t, e, a.AdminCookie, http.MethodPut, companyPath(a, r.ID)+"/quote", priceAll(r, 9000), http.StatusOK)
	var r2 request
	decode(t, as(t, e, a.AdminCookie, http.MethodPut, companyPath(a, r.ID)+"/quote", priceAll(r, 8500), http.StatusOK), &r2)
	if r2.Revision != 2 || *r2.QuotedTotal != 8500 {
		t.Errorf("revision 2: %+v", r2)
	}
	// The customer accepting what they saw (revision 1) is refused.
	expectErr(t, e, u.Cookie, http.MethodPost, "/v1/quote-requests/"+r.ID+"/accept", `{"revision":1}`, http.StatusConflict, "stale_revision")
	as(t, e, u.Cookie, http.MethodPost, "/v1/quote-requests/"+r.ID+"/accept", `{"revision":2}`, http.StatusOK)
}

// TestTransitionsOverHTTP drives a request into every status and tries
// every action from it, comparing with the agreed table.
func TestTransitionsOverHTTP(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	ch := chair(t, e, a, "Matrix Chair", 100)
	u := e.RegisterCustomer()

	// into returns a fresh request in the given status.
	into := func(status string) request {
		r := directRequest(t, e, u, ch, 1)
		cp, up := companyPath(a, r.ID), "/v1/quote-requests/"+r.ID
		switch status {
		case "reviewing":
			as(t, e, a.AdminCookie, http.MethodPost, cp+"/review", "", http.StatusOK)
		case "quoted", "accepted", "rejected":
			as(t, e, a.AdminCookie, http.MethodPut, cp+"/quote", priceAll(r, 90), http.StatusOK)
			if status == "accepted" {
				as(t, e, u.Cookie, http.MethodPost, up+"/accept", `{"revision":1}`, http.StatusOK)
			}
			if status == "rejected" {
				as(t, e, u.Cookie, http.MethodPost, up+"/reject", "", http.StatusOK)
			}
		case "cancelled":
			as(t, e, u.Cookie, http.MethodPost, up+"/cancel", "", http.StatusOK)
		case "declined":
			as(t, e, a.AdminCookie, http.MethodPost, cp+"/decline", `{"note":"Out of stock"}`, http.StatusOK)
		}
		return r
	}
	allowed := map[string][]string{
		"pending":   {"review", "quote", "decline", "cancel"},
		"reviewing": {"quote", "decline", "cancel"},
		"quoted":    {"quote", "accept", "reject", "cancel"},
		"accepted":  {}, "rejected": {}, "cancelled": {}, "declined": {},
	}
	for status, ok := range allowed {
		for _, action := range []string{"review", "quote", "decline", "accept", "reject", "cancel"} {
			r := into(status)
			want := http.StatusConflict
			for _, a := range ok {
				if a == action {
					want = http.StatusOK
				}
			}
			var w int
			switch action {
			case "review":
				w = e.As(a.AdminCookie, http.MethodPost, companyPath(a, r.ID)+"/review", "").Code
			case "quote":
				w = e.As(a.AdminCookie, http.MethodPut, companyPath(a, r.ID)+"/quote", priceAll(r, 80)).Code
			case "decline":
				w = e.As(a.AdminCookie, http.MethodPost, companyPath(a, r.ID)+"/decline", `{}`).Code
			case "accept":
				// Use the request's current revision so only the status decides.
				var cur request
				decode(t, as(t, e, u.Cookie, http.MethodGet, "/v1/quote-requests/"+r.ID, "", http.StatusOK), &cur)
				w = e.As(u.Cookie, http.MethodPost, "/v1/quote-requests/"+r.ID+"/accept", fmt.Sprintf(`{"revision":%d}`, cur.Revision)).Code
			case "reject", "cancel":
				w = e.As(u.Cookie, http.MethodPost, "/v1/quote-requests/"+r.ID+"/"+action, "").Code
			}
			if w != want {
				t.Errorf("%s from %s: %d, want %d", action, status, w, want)
			}
		}
	}
}

func TestQuoteValidation(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	ch := chair(t, e, a, "Validated Chair", 100)
	ch2 := chair(t, e, a, "Second Chair", 100)
	u := e.RegisterCustomer()
	var s submission
	decode(t, as(t, e, u.Cookie, http.MethodPost, "/v1/quote-requests",
		`{"items":[{"product_id":"`+ch.ID+`","quantity":1},{"product_id":"`+ch2.ID+`","quantity":1}]}`, http.StatusCreated), &s)
	r := s.Requests[0]
	otherReq := directRequest(t, e, u, ch, 1)
	path := companyPath(a, r.ID) + "/quote"
	yesterday := time.Now().AddDate(0, 0, -2).Format(time.DateOnly)

	for name, body := range map[string]string{
		"one line unpriced": fmt.Sprintf(`{"lines":[{"line_id":%q,"unit_price_minor":1}]}`, r.Lines[0].ID),
		"another request's line": fmt.Sprintf(`{"lines":[{"line_id":%q,"unit_price_minor":1},{"line_id":%q,"unit_price_minor":1},{"line_id":%q,"unit_price_minor":1}]}`,
			r.Lines[0].ID, r.Lines[1].ID, otherReq.Lines[0].ID),
		"negative price":  fmt.Sprintf(`{"lines":[{"line_id":%q,"unit_price_minor":-1},{"line_id":%q,"unit_price_minor":1}]}`, r.Lines[0].ID, r.Lines[1].ID),
		"negative charge": strings.TrimSuffix(priceAll(r, 1), "}") + `,"charges":[{"label":"Delivery","amount_minor":-5}]}`,
		"empty label":     strings.TrimSuffix(priceAll(r, 1), "}") + `,"charges":[{"label":" ","amount_minor":5}]}`,
		"past date":       strings.TrimSuffix(priceAll(r, 1), "}") + `,"valid_until":"` + yesterday + `"}`,
		"bad date":        strings.TrimSuffix(priceAll(r, 1), "}") + `,"valid_until":"31/12/2026"}`,
	} {
		w := e.As(a.AdminCookie, http.MethodPut, path, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	var cur request
	decode(t, as(t, e, a.AdminCookie, http.MethodGet, companyPath(a, r.ID), "", http.StatusOK), &cur)
	if cur.Status != "pending" || cur.Revision != 0 || cur.QuotedTotal != nil {
		t.Errorf("rejected quotes changed the request: %+v", cur)
	}
}

func TestExpiredQuoteCannotBeAccepted(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	ch := chair(t, e, a, "Expiring Chair", 100)
	u := e.RegisterCustomer()
	r := directRequest(t, e, u, ch, 1)
	as(t, e, a.AdminCookie, http.MethodPut, companyPath(a, r.ID)+"/quote",
		strings.TrimSuffix(priceAll(r, 90), "}")+`,"valid_until":"`+time.Now().AddDate(0, 0, 1).Format(time.DateOnly)+`"}`, http.StatusOK)

	e.Exec(`UPDATE quote_requests SET valid_until = current_date - 1 WHERE id = $1`, r.ID)
	var cur request
	decode(t, as(t, e, u.Cookie, http.MethodGet, "/v1/quote-requests/"+r.ID, "", http.StatusOK), &cur)
	if !cur.Expired {
		t.Error("expired flag not set")
	}
	expectErr(t, e, u.Cookie, http.MethodPost, "/v1/quote-requests/"+r.ID+"/accept", `{"revision":1}`, http.StatusConflict, "quote_expired")
	// Rejecting an expired quote is still fine.
	as(t, e, u.Cookie, http.MethodPost, "/v1/quote-requests/"+r.ID+"/reject", "", http.StatusOK)
}

func TestSnapshotIsImmutable(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	ch := chair(t, e, a, "Original Name", 10000)
	u := e.RegisterCustomer()
	r := directRequest(t, e, u, ch, 2)

	sellerDo(t, e, a, http.MethodPatch, "/v1/companies/"+a.ID+"/products/"+ch.ID, `{"name":"Renamed","price_minor":99999}`, http.StatusOK)
	sellerDo(t, e, a, http.MethodPost, "/v1/companies/"+a.ID+"/products/"+ch.ID+"/archive", "", http.StatusOK)

	var cur request
	decode(t, as(t, e, u.Cookie, http.MethodGet, "/v1/quote-requests/"+r.ID, "", http.StatusOK), &cur)
	if cur.Lines[0].ProductName != "Original Name" || cur.Lines[0].ListUnit != 10000 || cur.ListTotal != 20000 {
		t.Errorf("snapshot changed: %+v", cur.Lines[0])
	}
	// The company can still quote it, even though the product is archived.
	as(t, e, a.AdminCookie, http.MethodPut, companyPath(a, r.ID)+"/quote", priceAll(r, 9500), http.StatusOK)
}

func TestQuoteIsolation(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	c := e.RegisterCompany()
	ch := chair(t, e, a, "Isolated Chair", 100)
	alice := e.RegisterCustomer()
	bob := e.RegisterCustomer()
	r := directRequest(t, e, alice, ch, 1)
	up := "/v1/quote-requests/" + r.ID

	for _, rt := range [][3]string{
		{http.MethodGet, up, ""},
		{http.MethodPost, up + "/accept", `{"revision":0}`},
		{http.MethodPost, up + "/reject", ""},
		{http.MethodPost, up + "/cancel", ""},
	} {
		if w := e.As(bob.Cookie, rt[0], rt[1], rt[2]); w.Code != http.StatusNotFound {
			t.Errorf("bob %s %s: %d", rt[0], rt[1], w.Code)
		}
	}
	// Another company, via its own path or seller A's path.
	for _, base := range []string{companyPath(c, r.ID), companyPath(a, r.ID)} {
		for _, rt := range [][3]string{
			{http.MethodGet, base, ""},
			{http.MethodPost, base + "/review", ""},
			{http.MethodPut, base + "/quote", priceAll(r, 1)},
			{http.MethodPost, base + "/decline", `{}`},
		} {
			if w := e.As(c.AdminCookie, rt[0], rt[1], rt[2]); w.Code != http.StatusNotFound {
				t.Errorf("company C %s %s: %d", rt[0], rt[1], w.Code)
			}
		}
	}
	// The seller's admin is not the customer: customer routes are 404.
	if w := e.As(a.AdminCookie, http.MethodPost, up+"/cancel", ""); w.Code != http.StatusNotFound {
		t.Errorf("seller using customer route: %d", w.Code)
	}
	// The customer is not a member: company routes are 404.
	if w := e.As(alice.Cookie, http.MethodPost, companyPath(a, r.ID)+"/review", ""); w.Code != http.StatusNotFound {
		t.Errorf("customer using company route: %d", w.Code)
	}
	if n := e.Count(`SELECT count(*) FROM quote_requests WHERE id = $1 AND status = 'pending'`, r.ID); n != 1 {
		t.Error("request changed")
	}
}

// A customer cancels while the company sends a quote. The row lock
// serializes them: exactly one succeeds, and the final state matches it.
func TestConcurrentCancelAndQuote(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	ch := chair(t, e, a, "Contested Chair", 100)
	u := e.RegisterCustomer()

	for round := 0; round < 8; round++ {
		r := directRequest(t, e, u, ch, 1)
		var wg sync.WaitGroup
		var cancelCode, quoteCode int
		wg.Add(2)
		go func() {
			defer wg.Done()
			cancelCode = e.As(u.Cookie, http.MethodPost, "/v1/quote-requests/"+r.ID+"/cancel", "").Code
		}()
		go func() {
			defer wg.Done()
			quoteCode = e.As(a.AdminCookie, http.MethodPut, companyPath(a, r.ID)+"/quote", priceAll(r, 90)).Code
		}()
		wg.Wait()

		var cur request
		decode(t, as(t, e, u.Cookie, http.MethodGet, "/v1/quote-requests/"+r.ID, "", http.StatusOK), &cur)
		switch {
		case cancelCode == http.StatusOK && quoteCode == http.StatusOK:
			// Quote first, then cancel (allowed from quoted): both succeed.
			if cur.Status != "cancelled" || cur.Revision != 1 {
				t.Errorf("round %d: both succeeded but state is %s rev %d", round, cur.Status, cur.Revision)
			}
		case cancelCode == http.StatusOK && quoteCode == http.StatusConflict:
			if cur.Status != "cancelled" || cur.Revision != 0 {
				t.Errorf("round %d: cancel won but state is %s rev %d", round, cur.Status, cur.Revision)
			}
		default:
			t.Errorf("round %d: cancel %d, quote %d", round, cancelCode, quoteCode)
		}
	}
}

func TestListAndFilter(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	ch := chair(t, e, a, "Listed Chair", 100)
	u := e.RegisterCustomer()
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, directRequest(t, e, u, ch, 1).ID)
	}
	as(t, e, u.Cookie, http.MethodPost, "/v1/quote-requests/"+ids[0]+"/cancel", "", http.StatusOK)

	var all []string
	path := "/v1/quote-requests?limit=2"
	for i := 0; i < 5; i++ {
		var pg struct {
			Requests []request `json:"requests"`
			Next     *string   `json:"next_cursor"`
		}
		decode(t, as(t, e, u.Cookie, http.MethodGet, path, "", http.StatusOK), &pg)
		for _, r := range pg.Requests {
			all = append(all, r.ID)
			if len(r.Lines) != 0 {
				t.Error("list entries should not embed lines")
			}
		}
		if pg.Next == nil {
			break
		}
		path = "/v1/quote-requests?limit=2&cursor=" + *pg.Next
	}
	if len(all) != 5 || all[0] != ids[4] {
		t.Errorf("walked %v", all)
	}
	var cancelled struct{ Requests []request }
	decode(t, as(t, e, u.Cookie, http.MethodGet, "/v1/quote-requests?status=cancelled", "", http.StatusOK), &cancelled)
	if len(cancelled.Requests) != 1 || cancelled.Requests[0].ID != ids[0] {
		t.Errorf("status filter: %+v", cancelled.Requests)
	}
	expectErr(t, e, u.Cookie, http.MethodGet, "/v1/quote-requests?status=lost", "", http.StatusBadRequest, "validation_failed")
}
