package design_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Joedee43/interior_api/internal/apitest"
)

// ---- fixtures: sellers and products -------------------------------------------

type product struct {
	ID, Slug string
	Variants map[string]string // SKU -> variant id
}

func productBody(name string, price int64) string {
	return fmt.Sprintf(`{"category":"chairs","name":%q,"price_minor":%d,"width_mm":600,"depth_mm":550,"height_mm":900}`, name, price)
}

func mustDo(t *testing.T, e *apitest.Env, co apitest.Company, method, path, body string, want int) []byte {
	t.Helper()
	w := e.As(co.AdminCookie, method, path, body)
	if w.Code != want {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
	}
	return w.Body.Bytes()
}

// publishedChair is a published product without options.
func publishedChair(t *testing.T, e *apitest.Env, co apitest.Company, name string, price int64) product {
	t.Helper()
	w := e.As(co.AdminCookie, http.MethodPost, "/v1/companies/"+co.ID+"/products", productBody(name, price))
	var p product
	apitest.Decode(t, w, &p)
	mustDo(t, e, co, http.MethodPost, "/v1/companies/"+co.ID+"/products/"+p.ID+"/publish", "", http.StatusOK)
	return p
}

// publishedSofa has Fabric [Linen, Velvet]; variants LIN (available) and
// VEL (unavailable).
func publishedSofa(t *testing.T, e *apitest.Env, co apitest.Company, name string) product {
	t.Helper()
	w := e.As(co.AdminCookie, http.MethodPost, "/v1/companies/"+co.ID+"/products", productBody(name, 1))
	var p product
	apitest.Decode(t, w, &p)
	base := "/v1/companies/" + co.ID + "/products/" + p.ID
	mustDo(t, e, co, http.MethodPost, base+"/options", `{"name":"Fabric","values":[{"name":"Linen"},{"name":"Velvet"}]}`, http.StatusCreated)
	mustDo(t, e, co, http.MethodPost, base+"/variants", `{"sku":"`+p.Slug+`-LIN","price_minor":50000,"options":{"Fabric":"Linen"}}`, http.StatusCreated)
	body := mustDo(t, e, co, http.MethodPost, base+"/variants", `{"sku":"`+p.Slug+`-VEL","price_minor":70000,"available":false,"options":{"Fabric":"Velvet"}}`, http.StatusCreated)
	var cfg struct {
		Variants []struct{ ID, SKU string } `json:"variants"`
	}
	if err := jsonUnmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	p.Variants = map[string]string{}
	for _, v := range cfg.Variants {
		p.Variants[strings.TrimPrefix(v.SKU, strings.ToUpper(p.Slug)+"-")] = v.ID
	}
	mustDo(t, e, co, http.MethodPost, base+"/publish", "", http.StatusOK)
	return p
}

// ---- design helpers -------------------------------------------------------------

type itemResp struct {
	ID          string  `json:"id"`
	RotationDeg float64 `json:"rotation_deg"`
	Available   bool    `json:"available"`
	Position    struct {
		X int32 `json:"x_mm"`
		Y int32 `json:"y_mm"`
		Z int32 `json:"z_mm"`
	} `json:"position"`
	Product struct {
		ID          string            `json:"id"`
		Name        string            `json:"name"`
		CompanySlug string            `json:"company_slug"`
		VariantID   *string           `json:"variant_id"`
		SKU         *string           `json:"sku"`
		Options     map[string]string `json:"options"`
		Price       struct {
			AmountMinor int64  `json:"amount_minor"`
			Currency    string `json:"currency"`
		} `json:"price"`
	} `json:"product"`
}

type designResp struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	TemplateID *string `json:"template_id"`
	Room       struct {
		W int32 `json:"width_mm"`
		D int32 `json:"depth_mm"`
		H int32 `json:"height_mm"`
	} `json:"room"`
	ItemCount int `json:"item_count"`
	Features  struct {
		Floor    string           `json:"floor"`
		Openings []map[string]any `json:"openings"`
	} `json:"features"`
	Items    []itemResp `json:"items"`
	Estimate struct {
		ItemCount        int `json:"item_count"`
		UnavailableCount int `json:"unavailable_count"`
		Totals           []struct {
			CompanySlug string `json:"company_slug"`
			Currency    string `json:"currency"`
			AmountMinor int64  `json:"amount_minor"`
			Items       int    `json:"items"`
		} `json:"totals"`
	} `json:"estimate"`
}

func call(t *testing.T, e *apitest.Env, u apitest.User, method, path, body string, want int) []byte {
	t.Helper()
	w := e.As(u.Cookie, method, path, body)
	if w.Code != want {
		t.Fatalf("%s %s %s: %d %s, want %d", method, path, body, w.Code, w.Body, want)
	}
	return w.Body.Bytes()
}

func newDesign(t *testing.T, e *apitest.Env, u apitest.User, body string) designResp {
	t.Helper()
	var d designResp
	if err := jsonUnmarshal(call(t, e, u, http.MethodPost, "/v1/designs", body, http.StatusCreated), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func getDesign(t *testing.T, e *apitest.Env, u apitest.User, id string) designResp {
	t.Helper()
	var d designResp
	if err := jsonUnmarshal(call(t, e, u, http.MethodGet, "/v1/designs/"+id, "", http.StatusOK), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func place(t *testing.T, e *apitest.Env, u apitest.User, designID, body string) itemResp {
	t.Helper()
	var it itemResp
	if err := jsonUnmarshal(call(t, e, u, http.MethodPost, "/v1/designs/"+designID+"/items", body, http.StatusCreated), &it); err != nil {
		t.Fatal(err)
	}
	return it
}

func expect(t *testing.T, e *apitest.Env, u apitest.User, method, path, body string, status int, code, field string) {
	t.Helper()
	w := e.As(u.Cookie, method, path, body)
	if w.Code != status || apitest.ErrorCode(t, w) != code || (field != "" && !strings.Contains(w.Body.String(), `"`+field+`"`)) {
		t.Errorf("%s %s %s: %d %s, want %d %s on %q", method, path, body, w.Code, w.Body, status, code, field)
	}
}

func itemBody(productID string, variantID string, x, z int, extra ...string) string {
	fields := []string{`"product_id":"` + productID + `"`, fmt.Sprintf(`"x_mm":%d`, x), fmt.Sprintf(`"z_mm":%d`, z)}
	if variantID != "" {
		fields = append(fields, `"variant_id":"`+variantID+`"`)
	}
	return "{" + strings.Join(append(fields, extra...), ",") + "}"
}

// ---- tests ----------------------------------------------------------------------

func TestCustomerRegistration(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	c := e.RegisterCustomer()
	w := e.As(c.Cookie, http.MethodGet, "/v1/auth/me", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"memberships":[]`) {
		t.Errorf("customer /me: %d %s", w.Code, w.Body)
	}
	if w := e.Do(http.MethodPost, "/v1/auth/register", `{"full_name":"X","email":"`+c.Email+`","password":"another long password"}`); w.Code != http.StatusConflict {
		t.Errorf("duplicate email: %d %s", w.Code, w.Body)
	}
	if w := e.Do(http.MethodPost, "/v1/auth/register", `{"full_name":"","email":"bad","password":"short"}`); w.Code != http.StatusBadRequest {
		t.Errorf("invalid registration: %d %s", w.Code, w.Body)
	}
	e.Login(c.Email, apitest.Password)
}

func TestRoomTemplates(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	w := e.Do(http.MethodGet, "/v1/room-templates", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"home-office-3x4"`) || strings.Contains(w.Body.String(), `"status"`) {
		t.Fatalf("public templates: %d %s", w.Code, w.Body)
	}
	if w := e.Do(http.MethodGet, "/v1/room-templates/open-office-8x10", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"floor":"carpet"`) {
		t.Errorf("one template: %d %s", w.Code, w.Body)
	}

	customer := e.RegisterCustomer()
	expect(t, e, customer, http.MethodPost, "/v1/admin/room-templates", `{}`, http.StatusForbidden, "forbidden", "")
	if w := e.Do(http.MethodGet, "/v1/admin/room-templates", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous admin route: %d", w.Code)
	}

	admin := e.RegisterCustomer()
	e.MakePlatformAdmin(admin.ID)
	slug := "studio-" + strings.ToLower(admin.ID[:8])
	body := `{"slug":"` + slug + `","name":"Studio","width_mm":4000,"depth_mm":3000,"height_mm":2600,
		"features":{"floor":"tile","openings":[{"type":"door","wall":"north","offset_mm":100,"width_mm":900,"height_mm":2100}]}}`
	var tpl struct{ ID, Status string }
	if err := jsonUnmarshal(call(t, e, admin, http.MethodPost, "/v1/admin/room-templates", body, http.StatusCreated), &tpl); err != nil {
		t.Fatal(err)
	}
	if tpl.Status != "draft" {
		t.Errorf("new template status %q", tpl.Status)
	}
	expect(t, e, admin, http.MethodPost, "/v1/admin/room-templates", body, http.StatusConflict, "slug_taken", "")
	if w := e.Do(http.MethodGet, "/v1/room-templates/"+slug, ""); w.Code != http.StatusNotFound {
		t.Errorf("draft template is public: %d", w.Code)
	}

	// Shrinking so the door no longer fits is refused (it is 2100 mm high);
	// unknown keys too.
	expect(t, e, admin, http.MethodPatch, "/v1/admin/room-templates/"+tpl.ID, `{"height_mm":2000}`, http.StatusBadRequest, "validation_failed", "features.openings[0]")
	expect(t, e, admin, http.MethodPatch, "/v1/admin/room-templates/"+tpl.ID, `{"features":{"floor":"oak","skylights":2}}`, http.StatusBadRequest, "bad_request", "")
	call(t, e, admin, http.MethodPatch, "/v1/admin/room-templates/"+tpl.ID, `{"status":"published"}`, http.StatusOK)
	if w := e.Do(http.MethodGet, "/v1/room-templates/"+slug, ""); w.Code != http.StatusOK {
		t.Errorf("published template: %d", w.Code)
	}
}

func TestCreateDesign(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	u := e.RegisterCustomer()

	d := newDesign(t, e, u, `{"name":"My office","template":"home-office-3x4"}`)
	if d.TemplateID == nil || d.Room.W != 3000 || d.Room.D != 4000 || d.Room.H != 2700 || d.Features.Floor != "oak" || len(d.Features.Openings) != 2 {
		t.Errorf("from template: %+v", d)
	}
	c := newDesign(t, e, u, `{"name":"Garage","width_mm":6000,"depth_mm":6000,"height_mm":2500,"features":{"floor":"concrete"}}`)
	if c.TemplateID != nil || c.Room.W != 6000 || c.Features.Floor != "concrete" || c.Features.Openings == nil {
		t.Errorf("custom: %+v", c)
	}

	for body, field := range map[string]string{
		`{"name":"X"}`: "template",
		`{"name":"X","template":"home-office-3x4","width_mm":5000}`:    "template",
		`{"name":"X","template":"no-such-room"}`:                       "template",
		`{"name":"X","width_mm":500,"depth_mm":4000,"height_mm":2700}`: "width_mm",
		`{"name":"","template":"home-office-3x4"}`:                     "name",
		`{"name":"X","width_mm":3000,"depth_mm":3000,"height_mm":2500,"features":{"openings":[{"type":"door","wall":"north","offset_mm":2500,"width_mm":900,"height_mm":2000}]}}`: "features.openings[0]",
	} {
		expect(t, e, u, http.MethodPost, "/v1/designs", body, http.StatusBadRequest, "validation_failed", field)
	}
	if w := e.Do(http.MethodPost, "/v1/designs", `{"name":"X","template":"home-office-3x4"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous create: %d", w.Code)
	}
}

func TestPlaceItems(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	sellerA := e.RegisterCompany()
	sellerB := e.RegisterCompany()
	chair := publishedChair(t, e, sellerA, "Desk Chair", 12000)
	sofa := publishedSofa(t, e, sellerB, "Corner Sofa")
	otherSofa := publishedSofa(t, e, sellerB, "Other Sofa")
	draft := struct{ ID string }{}
	apitest.Decode(t, e.As(sellerA.AdminCookie, http.MethodPost, "/v1/companies/"+sellerA.ID+"/products", productBody("Draft Chair", 1)), &draft)

	u := e.RegisterCustomer()
	d := newDesign(t, e, u, `{"name":"Lounge","template":"living-room-5x6"}`)
	items := "/v1/designs/" + d.ID + "/items"

	it := place(t, e, u, d.ID, itemBody(chair.ID, "", 1500, 2000, `"rotation_deg":-90`))
	if it.RotationDeg != 270 || it.Position.X != 1500 || !it.Available || it.Product.Price.AmountMinor != 12000 {
		t.Errorf("placed chair: %+v", it)
	}
	if it.Product.VariantID != nil || it.Product.SKU != nil {
		t.Errorf("plain product variant identity: variant=%v sku=%v, want nulls", it.Product.VariantID, it.Product.SKU)
	}
	place(t, e, u, d.ID, itemBody(chair.ID, "", 2500, 2000))
	lin := place(t, e, u, d.ID, itemBody(sofa.ID, sofa.Variants["LIN"], 4000, 5000))
	if lin.Product.Options["Fabric"] != "Linen" || lin.Product.Price.AmountMinor != 50000 || lin.Product.SKU == nil || *lin.Product.SKU != strings.ToUpper(sofa.Slug)+"-LIN" {
		t.Errorf("placed sofa: %+v", lin)
	}

	for name, tc := range map[string]struct{ body, field string }{
		"variant missing":         {itemBody(sofa.ID, "", 100, 100), "variant_id"},
		"variant of another sofa": {itemBody(sofa.ID, otherSofa.Variants["LIN"], 100, 100), "variant_id"},
		"unavailable variant":     {itemBody(sofa.ID, sofa.Variants["VEL"], 100, 100), "variant_id"},
		"variant on plain chair":  {itemBody(chair.ID, sofa.Variants["LIN"], 100, 100), "variant_id"},
		"draft product":           {itemBody(draft.ID, "", 100, 100), "product_id"},
		"unknown product":         {itemBody("00000000-0000-0000-0000-000000000000", "", 100, 100), "product_id"},
		"malformed product":       {`{"product_id":"nope","x_mm":1,"z_mm":1}`, "product_id"},
		"outside the room":        {itemBody(chair.ID, "", 5001, 100), "x_mm"},
		"above the ceiling":       {itemBody(chair.ID, "", 100, 100, `"y_mm":2801`), "y_mm"},
		"missing coordinates":     {`{"product_id":"` + chair.ID + `"}`, "z_mm"},
	} {
		w := e.As(u.Cookie, http.MethodPost, items, tc.body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"`+tc.field+`"`) {
			t.Errorf("%s: %d %s, want 400 on %s", name, w.Code, w.Body, tc.field)
		}
	}

	got := getDesign(t, e, u, d.ID)
	if len(got.Items) != 3 || got.ItemCount != 3 || got.Estimate.ItemCount != 3 || len(got.Estimate.Totals) != 2 {
		t.Fatalf("design: %+v", got)
	}
	for _, tot := range got.Estimate.Totals {
		switch tot.CompanySlug {
		case sellerA.Slug:
			if tot.AmountMinor != 24000 || tot.Items != 2 || tot.Currency != "ETB" {
				t.Errorf("seller A total: %+v", tot)
			}
		case sellerB.Slug:
			if tot.AmountMinor != 50000 || tot.Items != 1 {
				t.Errorf("seller B total: %+v", tot)
			}
		default:
			t.Errorf("unexpected seller %s", tot.CompanySlug)
		}
	}
}

func TestUpdateAndDeleteItems(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	seller := e.RegisterCompany()
	sofa := publishedSofa(t, e, seller, "Movable Sofa")
	u := e.RegisterCustomer()
	d := newDesign(t, e, u, `{"name":"Office","template":"home-office-3x4"}`)
	it := place(t, e, u, d.ID, itemBody(sofa.ID, sofa.Variants["LIN"], 1000, 1000))
	path := "/v1/designs/" + d.ID + "/items/" + it.ID

	var moved itemResp
	jsonUnmarshal(call(t, e, u, http.MethodPatch, path, `{"x_mm":2000,"rotation_deg":45.5}`, http.StatusOK), &moved)
	if moved.Position.X != 2000 || moved.Position.Z != 1000 || moved.RotationDeg != 45.5 || moved.Product.Options["Fabric"] != "Linen" {
		t.Errorf("moved: %+v", moved)
	}
	expect(t, e, u, http.MethodPatch, path, `{"z_mm":4001}`, http.StatusBadRequest, "validation_failed", "z_mm")
	expect(t, e, u, http.MethodPatch, path, `{"variant_id":"`+sofa.Variants["VEL"]+`"}`, http.StatusBadRequest, "validation_failed", "variant_id")
	expect(t, e, u, http.MethodPatch, path, `{}`, http.StatusBadRequest, "validation_failed", "body")
	expect(t, e, u, http.MethodPatch, path, `{"product_id":"`+sofa.ID+`"}`, http.StatusBadRequest, "bad_request", "")

	// Make Velvet available, then switch the item to it.
	cfgPath := "/v1/companies/" + seller.ID + "/products/" + sofa.ID + "/variants/" + sofa.Variants["VEL"]
	mustDo(t, e, seller, http.MethodPatch, cfgPath, `{"available":true}`, http.StatusOK)
	var switched itemResp
	jsonUnmarshal(call(t, e, u, http.MethodPatch, path, `{"variant_id":"`+sofa.Variants["VEL"]+`"}`, http.StatusOK), &switched)
	if switched.Product.Options["Fabric"] != "Velvet" || switched.Product.Price.AmountMinor != 70000 {
		t.Errorf("switched variant: %+v", switched)
	}

	call(t, e, u, http.MethodDelete, path, "", http.StatusNoContent)
	expect(t, e, u, http.MethodDelete, path, "", http.StatusNotFound, "not_found", "")
	if got := getDesign(t, e, u, d.ID); len(got.Items) != 0 {
		t.Errorf("items after delete: %d", len(got.Items))
	}
}

func TestUnavailableAfterArchive(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	seller := e.RegisterCompany()
	chair := publishedChair(t, e, seller, "Discontinued Chair", 9000)
	u := e.RegisterCustomer()
	d := newDesign(t, e, u, `{"name":"Den","template":"home-office-3x4"}`)
	place(t, e, u, d.ID, itemBody(chair.ID, "", 500, 500))

	mustDo(t, e, seller, http.MethodPost, "/v1/companies/"+seller.ID+"/products/"+chair.ID+"/archive", "", http.StatusOK)
	got := getDesign(t, e, u, d.ID)
	if len(got.Items) != 1 || got.Items[0].Available || got.Estimate.UnavailableCount != 1 || len(got.Estimate.Totals) != 0 {
		t.Errorf("after archive: %+v", got)
	}
	// It cannot be placed again while archived.
	expect(t, e, u, http.MethodPost, "/v1/designs/"+d.ID+"/items", itemBody(chair.ID, "", 600, 600), http.StatusBadRequest, "validation_failed", "product_id")
}

func TestResizeRoom(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	seller := e.RegisterCompany()
	chair := publishedChair(t, e, seller, "Corner Chair", 1)
	u := e.RegisterCustomer()
	d := newDesign(t, e, u, `{"name":"Room","width_mm":3000,"depth_mm":3000,"height_mm":2600}`)
	it := place(t, e, u, d.ID, itemBody(chair.ID, "", 2900, 1000))
	path := "/v1/designs/" + d.ID

	expect(t, e, u, http.MethodPatch, path, `{"width_mm":2500}`, http.StatusConflict, "items_outside_room", "")
	call(t, e, u, http.MethodPatch, path+"/items/"+it.ID, `{"x_mm":2000}`, http.StatusOK)
	var resized designResp
	jsonUnmarshal(call(t, e, u, http.MethodPatch, path, `{"width_mm":2500,"name":"Smaller room"}`, http.StatusOK), &resized)
	if resized.Room.W != 2500 || resized.Name != "Smaller room" {
		t.Errorf("resized: %+v", resized)
	}

	// Features are re-checked against the new size.
	call(t, e, u, http.MethodPatch, path, `{"features":{"openings":[{"type":"window","wall":"north","offset_mm":1000,"width_mm":1400,"height_mm":1000,"sill_mm":900}]}}`, http.StatusOK)
	expect(t, e, u, http.MethodPatch, path, `{"width_mm":2000}`, http.StatusBadRequest, "validation_failed", "features.openings[0]")
}

func TestDesignRevisionPreconditions(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	seller := e.RegisterCompany()
	chair := publishedChair(t, e, seller, "Revision Chair", 12000)
	owner := e.RegisterCustomer()
	other := e.RegisterCustomer()

	created := e.As(owner.Cookie, http.MethodPost, "/v1/designs", `{"name":"Versioned","template":"home-office-3x4"}`)
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != `"design-1"` {
		t.Fatalf("create: %d etag=%q body=%s", created.Code, created.Header().Get("ETag"), created.Body)
	}
	var d designResp
	apitest.Decode(t, created, &d)
	path := "/v1/designs/" + d.ID

	missing := e.Do(http.MethodPatch, path, `{"name":"Missing"}`, apitest.WithCookie(owner.Cookie))
	if missing.Code != http.StatusPreconditionRequired || apitest.ErrorCode(t, missing) != "precondition_required" {
		t.Fatalf("missing precondition: %d %s", missing.Code, missing.Body)
	}

	mutate := func(method, target, body, tag string) *httptest.ResponseRecorder {
		return e.Do(method, target, body, apitest.WithCookie(owner.Cookie), apitest.WithHeader("If-Match", tag))
	}
	updated := mutate(http.MethodPatch, path, `{"name":"Version two"}`, `"design-1"`)
	if updated.Code != http.StatusOK || updated.Header().Get("ETag") != `"design-2"` {
		t.Fatalf("update: %d etag=%q body=%s", updated.Code, updated.Header().Get("ETag"), updated.Body)
	}
	stale := mutate(http.MethodPatch, path, `{"name":"Stale"}`, `"design-1"`)
	if stale.Code != http.StatusConflict || apitest.ErrorCode(t, stale) != "stale_revision" || stale.Header().Get("ETag") != `"design-2"` {
		t.Fatalf("stale: %d etag=%q body=%s", stale.Code, stale.Header().Get("ETag"), stale.Body)
	}
	rejected := mutate(http.MethodPatch, path, `{"width_mm":500}`, `"design-2"`)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("rejected mutation: %d %s", rejected.Code, rejected.Body)
	}
	placed := mutate(http.MethodPost, path+"/items", itemBody(chair.ID, "", 1000, 1000), `"design-2"`)
	if placed.Code != http.StatusCreated || placed.Header().Get("ETag") != `"design-3"` {
		t.Fatalf("place after rollback: %d etag=%q body=%s", placed.Code, placed.Header().Get("ETag"), placed.Body)
	}
	var item itemResp
	apitest.Decode(t, placed, &item)

	foreign := e.Do(http.MethodPatch, path, `{"name":"Not mine"}`, apitest.WithCookie(other.Cookie), apitest.WithHeader("If-Match", `"design-3"`))
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign design: %d %s", foreign.Code, foreign.Body)
	}
	removed := mutate(http.MethodDelete, path+"/items/"+item.ID, "", `"design-3"`)
	if removed.Code != http.StatusNoContent || removed.Header().Get("ETag") != `"design-4"` {
		t.Fatalf("remove: %d etag=%q body=%s", removed.Code, removed.Header().Get("ETag"), removed.Body)
	}
	deleted := mutate(http.MethodDelete, path, "", `"design-4"`)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body)
	}
}

func TestDesignIsolation(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	seller := e.RegisterCompany()
	chair := publishedChair(t, e, seller, "Private Chair", 1)
	alice := e.RegisterCustomer()
	bob := e.RegisterCustomer()
	ad := newDesign(t, e, alice, `{"name":"Alice's office","template":"home-office-3x4"}`)
	ai := place(t, e, alice, ad.ID, itemBody(chair.ID, "", 1000, 1000))
	bd := newDesign(t, e, bob, `{"name":"Bob's office","template":"home-office-3x4"}`)

	for _, r := range [][3]string{
		{http.MethodGet, "/v1/designs/" + ad.ID, ""},
		{http.MethodPatch, "/v1/designs/" + ad.ID, `{"name":"Mine now"}`},
		{http.MethodDelete, "/v1/designs/" + ad.ID, ""},
		{http.MethodPost, "/v1/designs/" + ad.ID + "/items", itemBody(chair.ID, "", 10, 10)},
		{http.MethodPatch, "/v1/designs/" + ad.ID + "/items/" + ai.ID, `{"x_mm":0}`},
		{http.MethodDelete, "/v1/designs/" + ad.ID + "/items/" + ai.ID, ""},
		// Alice's item id through Bob's own design.
		{http.MethodPatch, "/v1/designs/" + bd.ID + "/items/" + ai.ID, `{"x_mm":0}`},
		{http.MethodDelete, "/v1/designs/" + bd.ID + "/items/" + ai.ID, ""},
	} {
		if w := e.As(bob.Cookie, r[0], r[1], r[2]); w.Code != http.StatusNotFound {
			t.Errorf("bob %s %s: %d %s, want 404", r[0], r[1], w.Code, w.Body)
		}
	}
	w := e.As(bob.Cookie, http.MethodGet, "/v1/designs", "")
	if strings.Contains(w.Body.String(), ad.ID) {
		t.Error("bob's list includes alice's design")
	}
	got := getDesign(t, e, alice, ad.ID)
	if got.Name != "Alice's office" || len(got.Items) != 1 || got.Items[0].Position.X != 1000 {
		t.Errorf("alice's design changed: %+v", got)
	}
}

func TestItemLimit(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	seller := e.RegisterCompany()
	chair := publishedChair(t, e, seller, "Stacking Chair", 1)
	u := e.RegisterCustomer()
	d := newDesign(t, e, u, `{"name":"Hall","template":"open-office-8x10"}`)
	place(t, e, u, d.ID, itemBody(chair.ID, "", 100, 100))
	e.Exec(`INSERT INTO design_items (design_id, product_id, x_mm, z_mm)
		SELECT $1, $2, 100, 100 FROM generate_series(1, 199)`, d.ID, chair.ID)

	expect(t, e, u, http.MethodPost, "/v1/designs/"+d.ID+"/items", itemBody(chair.ID, "", 200, 200), http.StatusConflict, "limit_reached", "")
}

func TestPlacedProductsCannotBeDeleted(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	seller := e.RegisterCompany()
	sofa := publishedSofa(t, e, seller, "Beloved Sofa")
	u := e.RegisterCustomer()
	d := newDesign(t, e, u, `{"name":"Lounge","template":"living-room-5x6"}`)
	place(t, e, u, d.ID, itemBody(sofa.ID, sofa.Variants["LIN"], 1000, 1000))

	base := "/v1/companies/" + seller.ID + "/products/" + sofa.ID
	mustDo(t, e, seller, http.MethodPost, base+"/archive", "", http.StatusOK)
	mustDo(t, e, seller, http.MethodPost, base+"/restore", "", http.StatusOK) // draft again
	if w := e.As(seller.AdminCookie, http.MethodDelete, base+"/variants/"+sofa.Variants["LIN"], ""); w.Code != http.StatusConflict || apitest.ErrorCode(t, w) != "variant_in_use" {
		t.Errorf("delete placed variant: %d %s", w.Code, w.Body)
	}
	if w := e.As(seller.AdminCookie, http.MethodDelete, base, ""); w.Code != http.StatusConflict || apitest.ErrorCode(t, w) != "product_in_use" {
		t.Errorf("delete placed product: %d %s", w.Code, w.Body)
	}
	// Unplaced variants can still be deleted.
	mustDo(t, e, seller, http.MethodDelete, base+"/variants/"+sofa.Variants["VEL"], "", http.StatusOK)
}

func TestListDesigns(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	seller := e.RegisterCompany()
	chair := publishedChair(t, e, seller, "List Chair", 1)
	u := e.RegisterCustomer()
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, newDesign(t, e, u, fmt.Sprintf(`{"name":"Design %d","template":"home-office-3x4"}`, i)).ID)
	}
	// Editing the oldest design (adding an item) moves it to the top.
	place(t, e, u, ids[0], itemBody(chair.ID, "", 10, 10))

	var all []string
	q := url.Values{"limit": {"2"}}
	for page := 0; page < 5; page++ {
		var resp struct {
			Designs []struct {
				ID        string `json:"id"`
				ItemCount int    `json:"item_count"`
			} `json:"designs"`
			Next *string `json:"next_cursor"`
		}
		jsonUnmarshal(call(t, e, u, http.MethodGet, "/v1/designs?"+q.Encode(), "", http.StatusOK), &resp)
		for _, d := range resp.Designs {
			all = append(all, d.ID)
		}
		if resp.Next == nil {
			break
		}
		q.Set("cursor", *resp.Next)
	}
	if len(all) != 5 || all[0] != ids[0] {
		t.Errorf("walked %v; want 5 designs with the edited one first (%s)", all, ids[0])
	}
	expect(t, e, u, http.MethodGet, "/v1/designs?cursor=garbage", "", http.StatusBadRequest, "validation_failed", "cursor")
}
