package catalog_test

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Joedee43/interior_api/internal/apitest"
	"github.com/Joedee43/interior_api/internal/company"
	"github.com/Joedee43/interior_api/internal/httpapi"
)

// ---- helpers ------------------------------------------------------------

type productResp struct {
	ID              string  `json:"id"`
	Slug            string  `json:"slug"`
	Name            string  `json:"name"`
	Status          string  `json:"status"`
	WeightG         *int32  `json:"weight_g"`
	PublishedAt     *string `json:"published_at"`
	PrimaryImageURL *string `json:"primary_image_url"`
	Price           struct {
		AmountMinor int64  `json:"amount_minor"`
		Currency    string `json:"currency"`
	} `json:"price"`
	Category struct{ Slug string } `json:"category"`
	Company  struct{ Slug string } `json:"company"`
}

type pageResp struct {
	Products   []productResp `json:"products"`
	NextCursor *string       `json:"next_cursor"`
}

func productsPath(companyID string) string { return "/v1/companies/" + companyID + "/products" }

func productPath(companyID, productID string) string {
	return productsPath(companyID) + "/" + productID
}

func body(name string, price int64, extra ...string) string {
	fields := []string{
		`"category":"chairs"`,
		`"name":"` + name + `"`,
		fmt.Sprintf(`"price_minor":%d`, price),
		`"width_mm":600`, `"depth_mm":550`, `"height_mm":900`,
	}
	return "{" + strings.Join(append(fields, extra...), ",") + "}"
}

func create(t *testing.T, e *apitest.Env, co apitest.Company, json string) productResp {
	t.Helper()
	w := e.As(co.AdminCookie, http.MethodPost, productsPath(co.ID), json)
	if w.Code != http.StatusCreated {
		t.Fatalf("create product: %d %s", w.Code, w.Body)
	}
	var p productResp
	apitest.Decode(t, w, &p)
	return p
}

func act(t *testing.T, e *apitest.Env, co apitest.Company, id, action string, want int) {
	t.Helper()
	w := e.As(co.AdminCookie, http.MethodPost, productPath(co.ID, id)+"/"+action, "")
	if w.Code != want {
		t.Fatalf("%s %s: %d %s, want %d", action, id, w.Code, w.Body, want)
	}
}

func published(t *testing.T, e *apitest.Env, co apitest.Company, name string, price int64) productResp {
	t.Helper()
	p := create(t, e, co, body(name, price))
	act(t, e, co, p.ID, "publish", http.StatusOK)
	return p
}

func publicList(t *testing.T, e *apitest.Env, query url.Values) pageResp {
	t.Helper()
	w := e.Do(http.MethodGet, "/v1/catalog/products?"+query.Encode(), "")
	if w.Code != http.StatusOK {
		t.Fatalf("public list %s: %d %s", query.Encode(), w.Code, w.Body)
	}
	var pg pageResp
	apitest.Decode(t, w, &pg)
	return pg
}

// ---- lifecycle and validation -------------------------------------------

func TestProductLifecycle(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()

	p := create(t, e, co, body("Oslo Dining Chair", 1250000, `"weight_g":7500`))
	if p.Status != "draft" || p.Slug != "oslo-dining-chair" || p.Price.Currency != "ETB" || p.PublishedAt != nil {
		t.Errorf("new product: %+v", p)
	}

	act(t, e, co, p.ID, "archive", http.StatusConflict) // draft cannot be archived
	act(t, e, co, p.ID, "restore", http.StatusConflict) // nor restored
	act(t, e, co, p.ID, "publish", http.StatusOK)
	act(t, e, co, p.ID, "publish", http.StatusConflict) // already published
	act(t, e, co, p.ID, "archive", http.StatusOK)
	act(t, e, co, p.ID, "restore", http.StatusOK)
	act(t, e, co, p.ID, "publish", http.StatusOK)

	w := e.As(co.AdminCookie, http.MethodGet, productPath(co.ID, p.ID), "")
	var got productResp
	apitest.Decode(t, w, &got)
	if got.Status != "published" || got.PublishedAt == nil {
		t.Errorf("after lifecycle: %+v", got)
	}

	w = e.As(co.AdminCookie, http.MethodPost, productPath(co.ID, p.ID)+"/archive", "")
	if w.Code != http.StatusOK {
		t.Fatal(w.Body)
	}
	w = e.As(co.AdminCookie, http.MethodPost, productPath(co.ID, p.ID)+"/archive", "")
	if apitest.ErrorCode(t, w) != "invalid_transition" {
		t.Errorf("error code: %s", w.Body)
	}
}

func TestCreateValidation(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	post := func(json string) (int, string) {
		w := e.As(co.AdminCookie, http.MethodPost, productsPath(co.ID), json)
		return w.Code, w.Body.String()
	}

	code, b := post(`{"name":"X"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("missing fields: %d %s", code, b)
	}
	for _, f := range []string{"name", "category", "price_minor", "width_mm", "depth_mm", "height_mm"} {
		if !strings.Contains(b, `"`+f+`"`) {
			t.Errorf("missing-field details lack %s: %s", f, b)
		}
	}

	for name, tc := range map[string]struct {
		json  string
		field string
	}{
		"negative price":     {body("Chair A", -1), "price_minor"},
		"zero width":         {strings.Replace(body("Chair A", 1), `"width_mm":600`, `"width_mm":0`, 1), "width_mm"},
		"too tall":           {strings.Replace(body("Chair A", 1), `"height_mm":900`, `"height_mm":10001`, 1), "height_mm"},
		"bad weight":         {body("Chair A", 1, `"weight_g":0`), "weight_g"},
		"unknown category":   {strings.Replace(body("Chair A", 1), `"chairs"`, `"spaceships"`, 1), "category"},
		"bad slug":           {body("Chair A", 1, `"slug":"Not A Slug"`), "slug"},
		"amharic, no slug":   {body("ወንበር", 1), "slug"},
		"description length": {body("Chair A", 1, `"description":"`+strings.Repeat("x", 5001)+`"`), "description"},
	} {
		code, b := post(tc.json)
		if code != http.StatusBadRequest || !strings.Contains(b, `"`+tc.field+`"`) {
			t.Errorf("%s: %d %s, want 400 on %s", name, code, b, tc.field)
		}
	}

	// Money must be an integer in minor units; 12.50 is not accepted.
	if code, b := post(strings.Replace(body("Chair A", 1), `"price_minor":1`, `"price_minor":12.5`, 1)); code != http.StatusBadRequest {
		t.Errorf("fractional price: %d %s", code, b)
	}

	// Amharic name with an explicit slug is fine.
	if code, b := post(body("ወንበር", 1, `"slug":"wenber-chair"`)); code != http.StatusCreated {
		t.Errorf("amharic with slug: %d %s", code, b)
	}

	// Slugs are unique per company, not globally.
	create(t, e, co, body("Unique Chair", 1))
	if code, b := post(body("Unique Chair", 1)); code != http.StatusConflict || !strings.Contains(b, "slug_taken") {
		t.Errorf("duplicate slug: %d %s", code, b)
	}
	other := e.RegisterCompany()
	create(t, e, other, body("Unique Chair", 1))
}

func TestUpdateProduct(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p := create(t, e, co, body("Plain Stool", 100000, `"weight_g":3000`))
	patch := func(json string) (int, productResp, string) {
		w := e.As(co.AdminCookie, http.MethodPatch, productPath(co.ID, p.ID), json)
		var r productResp
		if w.Code == http.StatusOK {
			apitest.Decode(t, w, &r)
		}
		return w.Code, r, w.Body.String()
	}

	if code, r, b := patch(`{"name":"Bar Stool","price_minor":150000}`); code != http.StatusOK || r.Name != "Bar Stool" || r.Price.AmountMinor != 150000 || r.Slug != "plain-stool" {
		t.Errorf("patch name/price: %d %s", code, b)
	}
	if code, r, b := patch(`{"weight_g":null}`); code != http.StatusOK || r.WeightG != nil {
		t.Errorf("clear weight: %d %s", code, b)
	}
	if code, r, b := patch(`{"weight_g":4200}`); code != http.StatusOK || r.WeightG == nil || *r.WeightG != 4200 {
		t.Errorf("set weight: %d %s", code, b)
	}
	if code, r, b := patch(`{"slug":"bar-stool"}`); code != http.StatusOK || r.Slug != "bar-stool" {
		t.Errorf("draft slug change: %d %s", code, b)
	}
	if code, _, b := patch(`{}`); code != http.StatusBadRequest {
		t.Errorf("empty patch: %d %s", code, b)
	}
	if code, _, b := patch(`{"category":"nope"}`); code != http.StatusBadRequest {
		t.Errorf("unknown category: %d %s", code, b)
	}
	if code, r, b := patch(`{"category":"office-chairs"}`); code != http.StatusOK || r.Category.Slug != "office-chairs" {
		t.Errorf("change category: %d %s", code, b)
	}

	act(t, e, co, p.ID, "publish", http.StatusOK)
	if code, _, b := patch(`{"slug":"renamed-after-publish"}`); code != http.StatusConflict || !strings.Contains(b, "slug_locked") {
		t.Errorf("published slug change: %d %s", code, b)
	}
	if code, _, b := patch(`{"slug":"bar-stool","price_minor":160000}`); code != http.StatusOK {
		t.Errorf("unchanged slug on published product should be allowed: %d %s", code, b)
	}
}

func TestDeleteOnlyDrafts(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	draft := create(t, e, co, body("Draft Chair", 1))
	live := published(t, e, co, "Live Chair", 1)

	if w := e.As(co.AdminCookie, http.MethodDelete, productPath(co.ID, draft.ID), ""); w.Code != http.StatusNoContent {
		t.Errorf("delete draft: %d %s", w.Code, w.Body)
	}
	if w := e.As(co.AdminCookie, http.MethodGet, productPath(co.ID, draft.ID), ""); w.Code != http.StatusNotFound {
		t.Errorf("deleted draft still readable: %d", w.Code)
	}
	w := e.As(co.AdminCookie, http.MethodDelete, productPath(co.ID, live.ID), "")
	if w.Code != http.StatusConflict || apitest.ErrorCode(t, w) != "product_not_draft" {
		t.Errorf("delete published: %d %s", w.Code, w.Body)
	}
}

// ---- isolation and roles ------------------------------------------------

func TestProductIsolation(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	b := e.RegisterCompany()
	bProduct := create(t, e, b, body("B Secret Chair", 999))

	type route struct{ method, suffix, body string }
	routes := []route{
		{http.MethodGet, "", ""},
		{http.MethodPatch, "", `{"name":"Hijacked"}`},
		{http.MethodDelete, "", ""},
		{http.MethodPost, "/publish", ""},
		{http.MethodPost, "/archive", ""},
		{http.MethodPost, "/restore", ""},
	}
	for _, r := range routes {
		// Via B's company path: A is not a member -> 404.
		if w := e.As(a.AdminCookie, r.method, productPath(b.ID, bProduct.ID)+r.suffix, r.body); w.Code != http.StatusNotFound {
			t.Errorf("A via B path %s%s: %d", r.method, r.suffix, w.Code)
		}
		// Via A's own path with B's product id: still 404.
		if w := e.As(a.AdminCookie, r.method, productPath(a.ID, bProduct.ID)+r.suffix, r.body); w.Code != http.StatusNotFound {
			t.Errorf("A via own path %s%s: %d %s", r.method, r.suffix, w.Code, w.Body)
		}
	}
	if w := e.As(a.AdminCookie, http.MethodGet, productsPath(b.ID), ""); w.Code != http.StatusNotFound {
		t.Errorf("A listing B's products: %d", w.Code)
	}
	w := e.As(a.AdminCookie, http.MethodGet, productsPath(a.ID), "")
	if strings.Contains(w.Body.String(), bProduct.ID) {
		t.Error("A's list contains B's product")
	}
	if n := e.Count(`SELECT count(*) FROM products WHERE id = $1 AND name = 'B Secret Chair' AND status = 'draft'`, bProduct.ID); n != 1 {
		t.Error("B's product was modified")
	}
	if w := e.Do(http.MethodGet, productPath(b.ID, bProduct.ID), ""); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous private read: %d", w.Code)
	}
}

func TestStaffManageProducts(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	staff := e.AddMember(co, company.RoleStaff)

	w := e.As(staff.Cookie, http.MethodPost, productsPath(co.ID), body("Staff Chair", 5))
	if w.Code != http.StatusCreated {
		t.Fatalf("staff create: %d %s", w.Code, w.Body)
	}
	var p productResp
	apitest.Decode(t, w, &p)
	if w := e.As(staff.Cookie, http.MethodPost, productPath(co.ID, p.ID)+"/publish", ""); w.Code != http.StatusOK {
		t.Errorf("staff publish: %d %s", w.Code, w.Body)
	}
}

// ---- currency -----------------------------------------------------------

func TestCompanyCurrency(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	companyPath := "/v1/companies/" + co.ID
	patch := func(json string) (int, string) {
		w := e.As(co.AdminCookie, http.MethodPatch, companyPath, json)
		return w.Code, w.Body.String()
	}

	if code, b := patch(`{"currency":"us"}`); code != http.StatusBadRequest {
		t.Errorf("invalid currency: %d %s", code, b)
	}
	if code, b := patch(`{"currency":"usd"}`); code != http.StatusOK || !strings.Contains(b, `"currency":"USD"`) {
		t.Errorf("set USD with no products: %d %s", code, b)
	}
	p := create(t, e, co, body("Priced Chair", 4999))
	if p.Price.Currency != "USD" {
		t.Errorf("product currency = %s, want USD", p.Price.Currency)
	}
	if code, b := patch(`{"currency":"EUR"}`); code != http.StatusConflict || !strings.Contains(b, "currency_locked") {
		t.Errorf("change with products: %d %s", code, b)
	}
	if code, b := patch(`{"currency":"USD","name":"Same Currency Rename"}`); code != http.StatusOK {
		t.Errorf("same currency + rename: %d %s", code, b)
	}
	staff := e.AddMember(co, company.RoleStaff)
	if w := e.As(staff.Cookie, http.MethodPatch, companyPath, `{"currency":"USD"}`); w.Code != http.StatusForbidden {
		t.Errorf("staff currency change: %d", w.Code)
	}
}

// ---- public catalog -----------------------------------------------------

func TestPublicVisibility(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	draft := create(t, e, co, body("Hidden Draft", 1))
	live := published(t, e, co, "Visible Chair", 1)
	archived := published(t, e, co, "Old Chair", 1)
	act(t, e, co, archived.ID, "archive", http.StatusOK)

	pg := publicList(t, e, url.Values{"company": {co.Slug}})
	if len(pg.Products) != 1 || pg.Products[0].ID != live.ID {
		t.Fatalf("public list: %+v", pg.Products)
	}

	get := func(slug string) int {
		return e.Do(http.MethodGet, "/v1/catalog/products/"+co.Slug+"/"+slug, "").Code
	}
	if code := get(live.Slug); code != http.StatusOK {
		t.Errorf("published: %d", code)
	}
	for _, slug := range []string{draft.Slug, archived.Slug, "does-not-exist"} {
		if code := get(slug); code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", slug, code)
		}
	}

	// The public shape never includes lifecycle fields.
	w := e.Do(http.MethodGet, "/v1/catalog/products/"+co.Slug+"/"+live.Slug, "")
	if strings.Contains(w.Body.String(), `"status"`) || strings.Contains(w.Body.String(), `"created_at"`) {
		t.Errorf("public product leaks internal fields: %s", w.Body)
	}

	e.Exec(`UPDATE companies SET status = 'suspended' WHERE id = $1`, co.ID)
	if pg := publicList(t, e, url.Values{"company": {co.Slug}}); len(pg.Products) != 0 {
		t.Errorf("suspended company still listed: %d products", len(pg.Products))
	}
	if code := get(live.Slug); code != http.StatusNotFound {
		t.Errorf("suspended company product: %d", code)
	}
}

// walk follows next_cursor until the end and returns ids in page order.
func walk(t *testing.T, e *apitest.Env, q url.Values, between func(page int)) []productResp {
	t.Helper()
	var all []productResp
	for page := 0; ; page++ {
		pg := publicList(t, e, q)
		all = append(all, pg.Products...)
		if between != nil {
			between(page)
		}
		if pg.NextCursor == nil {
			return all
		}
		q.Set("cursor", *pg.NextCursor)
		if page > 20 {
			t.Fatal("pagination did not terminate")
		}
	}
}

func TestPublicPagination(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	// Duplicate prices check that the id tie-breaker keeps order total.
	prices := []int64{500, 300, 300, 900, 100, 300, 700}
	want := map[string]int64{}
	for i, price := range prices {
		p := published(t, e, co, fmt.Sprintf("Pager Chair %d", i), price)
		want[p.ID] = price
	}

	for _, s := range []string{"newest", "price_asc", "price_desc"} {
		t.Run(s, func(t *testing.T) {
			got := walk(t, e, url.Values{"company": {co.Slug}, "sort": {s}, "limit": {"3"}}, nil)
			if len(got) != len(prices) {
				t.Fatalf("walked %d products, want %d", len(got), len(prices))
			}
			seen := map[string]bool{}
			for _, p := range got {
				if seen[p.ID] {
					t.Errorf("duplicate %s", p.ID)
				}
				seen[p.ID] = true
			}
			ok := sort.SliceIsSorted(got, func(i, j int) bool {
				switch s {
				case "price_asc":
					return got[i].Price.AmountMinor < got[j].Price.AmountMinor
				case "price_desc":
					return got[i].Price.AmountMinor > got[j].Price.AmountMinor
				default:
					// Parse rather than compare strings: RFC 3339 output drops
					// trailing zeros in fractions, so string order can lie.
					ti, _ := time.Parse(time.RFC3339Nano, *got[i].PublishedAt)
					tj, _ := time.Parse(time.RFC3339Nano, *got[j].PublishedAt)
					return ti.After(tj)
				}
			})
			if !ok {
				t.Errorf("not sorted by %s", s)
			}
		})
	}

	t.Run("inserts during the walk do not cause skips or repeats", func(t *testing.T) {
		got := walk(t, e, url.Values{"company": {co.Slug}, "sort": {"price_asc"}, "limit": {"2"}}, func(page int) {
			if page == 0 {
				published(t, e, co, "Cheap Newcomer", 50) // sorts before the cursor
				published(t, e, co, "Pricey Newcomer", 5000)
			}
		})
		seen := map[string]int{}
		for _, p := range got {
			seen[p.ID]++
		}
		for id := range want {
			if seen[id] != 1 {
				t.Errorf("original product %s seen %d times", id, seen[id])
			}
		}
	})
}

func TestPublicFilters(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	published(t, e, co, "Walnut Desk", 800000) // category chairs via helper; desk below
	desk := create(t, e, co, strings.Replace(body("Standing Desk 100%", 2000000), `"chairs"`, `"desks"`, 1))
	act(t, e, co, desk.ID, "publish", http.StatusOK)

	q := func(kv ...string) url.Values {
		v := url.Values{"company": {co.Slug}}
		for i := 0; i < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}
	if pg := publicList(t, e, q("category", "desks")); len(pg.Products) != 1 || pg.Products[0].ID != desk.ID {
		t.Errorf("category filter: %+v", pg.Products)
	}
	if pg := publicList(t, e, q("q", "walnut")); len(pg.Products) != 1 {
		t.Errorf("name search: %+v", pg.Products)
	}
	// "%" is a literal character in search, not a wildcard.
	if pg := publicList(t, e, q("q", "100%")); len(pg.Products) != 1 || pg.Products[0].ID != desk.ID {
		t.Errorf("literal %% search: %+v", pg.Products)
	}
	if pg := publicList(t, e, q("currency", "etb", "min_price", "1000000")); len(pg.Products) != 1 || pg.Products[0].ID != desk.ID {
		t.Errorf("min_price: %+v", pg.Products)
	}
	if pg := publicList(t, e, q("currency", "USD")); len(pg.Products) != 0 {
		t.Errorf("currency filter: %+v", pg.Products)
	}

	newest := publicList(t, e, q("limit", "1"))
	for name, query := range map[string]url.Values{
		"price without currency": q("min_price", "1"),
		"min above max":          q("currency", "ETB", "min_price", "5", "max_price", "1"),
		"bad sort":               q("sort", "random"),
		"limit too big":          q("limit", "101"),
		"limit not a number":     q("limit", "ten"),
		"cursor from other sort": q("sort", "price_asc", "cursor", *newest.NextCursor),
		"garbage cursor":         q("cursor", "garbage"),
	} {
		w := e.Do(http.MethodGet, "/v1/catalog/products?"+query.Encode(), "")
		if w.Code != http.StatusBadRequest || apitest.ErrorCode(t, w) != httpapi.CodeValidation {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
}

func TestCompanyListIncludesAllStatuses(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	create(t, e, co, body("List Draft", 1))
	published(t, e, co, "List Live", 1)

	list := func(query string) pageResp {
		w := e.As(co.AdminCookie, http.MethodGet, productsPath(co.ID)+query, "")
		if w.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", query, w.Code, w.Body)
		}
		var pg pageResp
		apitest.Decode(t, w, &pg)
		return pg
	}
	if pg := list(""); len(pg.Products) != 2 {
		t.Errorf("all statuses: %d", len(pg.Products))
	}
	if pg := list("?status=draft"); len(pg.Products) != 1 || pg.Products[0].Status != "draft" {
		t.Errorf("status filter: %+v", pg.Products)
	}
	first := list("?limit=1")
	if first.NextCursor == nil {
		t.Fatal("expected a next page")
	}
	second := list("?limit=1&cursor=" + url.QueryEscape(*first.NextCursor))
	if len(second.Products) != 1 || second.Products[0].ID == first.Products[0].ID || second.NextCursor != nil {
		t.Errorf("second page: %+v", second)
	}
	if w := e.As(co.AdminCookie, http.MethodGet, productsPath(co.ID)+"?status=deleted", ""); w.Code != http.StatusBadRequest {
		t.Errorf("bad status filter: %d", w.Code)
	}
}

func TestCategories(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	w := e.Do(http.MethodGet, "/v1/catalog/categories", "")
	if w.Code != http.StatusOK {
		t.Fatalf("categories: %d", w.Code)
	}
	var resp struct {
		Categories []struct{ Slug string } `json:"categories"`
	}
	apitest.Decode(t, w, &resp)
	if len(resp.Categories) < 9 || resp.Categories[0].Slug != "chairs" {
		t.Errorf("categories: %+v", resp.Categories)
	}
}
