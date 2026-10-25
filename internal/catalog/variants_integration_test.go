package catalog_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Joedee43/interior_api/internal/apitest"
)

type configResp struct {
	Options []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Values []struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			SwatchHex   *string `json:"swatch_hex"`
			MaterialKey *string `json:"material_key"`
		} `json:"values"`
	} `json:"options"`
	Variants []struct {
		ID        string            `json:"id"`
		SKU       string            `json:"sku"`
		Available bool              `json:"available"`
		Options   map[string]string `json:"options"`
		ValueIDs  []string          `json:"value_ids"`
		Price     struct {
			AmountMinor int64  `json:"amount_minor"`
			Currency    string `json:"currency"`
		} `json:"price"`
	} `json:"variants"`
}

// cfgCall sends a configuration request and decodes the response when it
// has the wanted status.
func cfgCall(t *testing.T, e *apitest.Env, co apitest.Company, method, path, body string, want int) configResp {
	t.Helper()
	w := e.As(co.AdminCookie, method, path, body)
	if w.Code != want {
		t.Fatalf("%s %s: %d %s, want %d", method, path, w.Code, w.Body, want)
	}
	var cfg configResp
	if w.Code < 300 {
		apitest.Decode(t, w, &cfg)
	}
	return cfg
}

// expectCode asserts an error response code.
func expectCode(t *testing.T, e *apitest.Env, co apitest.Company, method, path, body string, status int, code string) {
	t.Helper()
	w := e.As(co.AdminCookie, method, path, body)
	if w.Code != status || apitest.ErrorCode(t, w) != code {
		t.Errorf("%s %s %s: %d %s, want %d %s", method, path, body, w.Code, w.Body, status, code)
	}
}

// sofa creates a draft with Fabric [Linen, Leather] and Colour [Grey, Black, Sage].
func sofa(t *testing.T, e *apitest.Env, co apitest.Company) (productResp, configResp) {
	t.Helper()
	p := create(t, e, co, body("Oslo Sofa "+fmt.Sprint(len(co.ID)), 100))
	base := productPath(co.ID, p.ID)
	cfgCall(t, e, co, http.MethodPost, base+"/options",
		`{"name":"Fabric","values":[{"name":"Linen","material_key":"Fabric_Linen"},{"name":"Leather"}]}`, http.StatusCreated)
	cfg := cfgCall(t, e, co, http.MethodPost, base+"/options",
		`{"name":"Colour","values":[{"name":"Grey","swatch_hex":"#808080"},{"name":"Black"},{"name":"Sage","swatch_hex":"#a3b18a"}]}`, http.StatusCreated)
	return p, cfg
}

func variantBody(sku string, price int64, fabric, colour string) string {
	return fmt.Sprintf(`{"sku":%q,"price_minor":%d,"options":{"Fabric":%q,"Colour":%q}}`, sku, price, fabric, colour)
}

func TestOptionsAndVariants(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p, cfg := sofa(t, e, co)
	base := productPath(co.ID, p.ID)

	if len(cfg.Options) != 2 || len(cfg.Options[1].Values) != 3 {
		t.Fatalf("configuration: %+v", cfg)
	}
	if v := cfg.Options[0].Values[0]; v.MaterialKey == nil || *v.MaterialKey != "fabric_linen" {
		t.Errorf("material_key not normalized: %+v", v)
	}
	if v := cfg.Options[1].Values[2]; v.SwatchHex == nil || *v.SwatchHex != "#A3B18A" {
		t.Errorf("swatch not normalized: %+v", v)
	}

	// Names are matched case-insensitively; SKUs are stored uppercase.
	cfg = cfgCall(t, e, co, http.MethodPost, base+"/variants",
		`{"sku":"oslo-lin-gry","price_minor":4850000,"options":{"fabric":"linen","COLOUR":"Grey"}}`, http.StatusCreated)
	v := cfg.Variants[0]
	if v.SKU != "OSLO-LIN-GRY" || v.Options["Fabric"] != "Linen" || v.Options["Colour"] != "Grey" || len(v.ValueIDs) != 2 || !v.Available {
		t.Errorf("variant: %+v", v)
	}

	expectCode(t, e, co, http.MethodPost, base+"/variants", variantBody("OTHER", 1, "Linen", "Grey"), http.StatusConflict, "duplicate_combination")
	expectCode(t, e, co, http.MethodPost, base+"/variants", variantBody("OSLO-LIN-GRY", 1, "Linen", "Black"), http.StatusConflict, "sku_taken")
	expectCode(t, e, co, http.MethodPost, base+"/variants", `{"sku":"X1","price_minor":1,"options":{"Fabric":"Linen"}}`, http.StatusBadRequest, "validation_failed")
	expectCode(t, e, co, http.MethodPost, base+"/variants", variantBody("X2", 1, "Velvet", "Grey"), http.StatusBadRequest, "validation_failed")
	expectCode(t, e, co, http.MethodPost, base+"/variants", `{"sku":"bad sku!","price_minor":1,"options":{}}`, http.StatusBadRequest, "validation_failed")

	// The same SKU is fine in another company.
	other := e.RegisterCompany()
	op, _ := sofa(t, e, other)
	cfgCall(t, e, other, http.MethodPost, productPath(other.ID, op.ID)+"/variants", variantBody("OSLO-LIN-GRY", 1, "Linen", "Grey"), http.StatusCreated)

	// Database backstop: even raw SQL cannot attach a Colour value to the
	// Fabric option (composite foreign key on (value_id, option_id)).
	_, err := e.Pool.Exec(t.Context(), `
		INSERT INTO product_variant_values (variant_id, product_id, option_id, value_id)
		VALUES ($1, $2, $3, $4)`, v.ID, p.ID, cfg.Options[0].ID, cfg.Options[1].Values[1].ID)
	if err == nil || !strings.Contains(err.Error(), "product_variant_values") {
		t.Errorf("cross-option value insert was not rejected: %v", err)
	}

	// Options with no variants are rejected for variant creation.
	plain := create(t, e, co, body("Plain Bench", 100))
	expectCode(t, e, co, http.MethodPost, productPath(co.ID, plain.ID)+"/variants", variantBody("B1", 1, "Linen", "Grey"), http.StatusBadRequest, "validation_failed")

	// Option and value names are unique per product / option.
	expectCode(t, e, co, http.MethodPost, base+"/options/"+cfg.Options[0].ID+"/values", `{"name":"linen"}`, http.StatusConflict, "value_name_taken")
	expectCode(t, e, co, http.MethodPatch, base+"/options/"+cfg.Options[1].ID, `{"name":"FABRIC"}`, http.StatusConflict, "option_name_taken")
}

func TestStructureLocks(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p, cfg := sofa(t, e, co)
	base := productPath(co.ID, p.ID)
	fabric, colour := cfg.Options[0], cfg.Options[1]

	cfgCall(t, e, co, http.MethodPost, base+"/variants", variantBody("V1", 100, "Linen", "Grey"), http.StatusCreated)

	expectCode(t, e, co, http.MethodPost, base+"/options", `{"name":"Legs","values":[{"name":"Oak"}]}`, http.StatusConflict, "options_locked")
	expectCode(t, e, co, http.MethodDelete, base+"/options/"+fabric.ID, "", http.StatusConflict, "options_locked")
	expectCode(t, e, co, http.MethodDelete, base+"/options/"+fabric.ID+"/values/"+fabric.Values[0].ID, "", http.StatusConflict, "value_in_use")
	// Database backstop: the deferred foreign key still refuses (at commit)
	// a raw delete of a value that a variant uses.
	if _, err := e.Pool.Exec(t.Context(), `DELETE FROM product_option_values WHERE id = $1`, fabric.Values[0].ID); err == nil ||
		!strings.Contains(err.Error(), "product_variant_values_value_fkey") {
		t.Errorf("raw delete of a used value was not refused: %v", err)
	}

	// Renaming, adding values and removing unused values stay allowed.
	cfgCall(t, e, co, http.MethodPatch, base+"/options/"+colour.ID, `{"name":"Color"}`, http.StatusOK)
	cfgCall(t, e, co, http.MethodPost, base+"/options/"+colour.ID+"/values", `{"name":"Navy","swatch_hex":"#000080"}`, http.StatusCreated)
	cfg = cfgCall(t, e, co, http.MethodDelete, base+"/options/"+colour.ID+"/values/"+colour.Values[2].ID, "", http.StatusOK)
	if names := len(cfg.Options[1].Values); names != 3 { // Grey, Black, Navy
		t.Errorf("colour values after add+delete: %d", names)
	}
	cfg = cfgCall(t, e, co, http.MethodPatch, base+"/options/"+colour.ID+"/values/"+colour.Values[0].ID, `{"swatch_hex":null,"material_key":"paint_grey"}`, http.StatusOK)
	if v := cfg.Options[1].Values[0]; v.SwatchHex != nil || v.MaterialKey == nil {
		t.Errorf("value patch: %+v", v)
	}

	// Last value of an option cannot go.
	single := create(t, e, co, body("Single Value Stool", 1))
	sbase := productPath(co.ID, single.ID)
	scfg := cfgCall(t, e, co, http.MethodPost, sbase+"/options", `{"name":"Finish","values":[{"name":"Oak"}]}`, http.StatusCreated)
	expectCode(t, e, co, http.MethodDelete, sbase+"/options/"+scfg.Options[0].ID+"/values/"+scfg.Options[0].Values[0].ID, "", http.StatusConflict, "last_value")
	// Without variants, the option itself can be deleted.
	cfgCall(t, e, co, http.MethodDelete, sbase+"/options/"+scfg.Options[0].ID, "", http.StatusOK)

	// A live product cannot gain options (it would show pickers with no
	// variants to buy); archive -> restore makes it a draft again.
	act(t, e, co, single.ID, "publish", http.StatusOK)
	expectCode(t, e, co, http.MethodPost, sbase+"/options", `{"name":"Finish","values":[{"name":"Oak"}]}`, http.StatusConflict, "options_locked")
	act(t, e, co, single.ID, "archive", http.StatusOK)
	act(t, e, co, single.ID, "restore", http.StatusOK)
	cfgCall(t, e, co, http.MethodPost, sbase+"/options", `{"name":"Finish","values":[{"name":"Oak"}]}`, http.StatusCreated)
}

func TestLimits(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p := create(t, e, co, body("Limit Chair", 1))
	base := productPath(co.ID, p.ID)

	for i := 0; i < 3; i++ {
		cfgCall(t, e, co, http.MethodPost, base+"/options", fmt.Sprintf(`{"name":"Opt%d","values":[{"name":"A"},{"name":"B"},{"name":"C"},{"name":"D"},{"name":"E"}]}`, i), http.StatusCreated)
	}
	expectCode(t, e, co, http.MethodPost, base+"/options", `{"name":"Opt4","values":[{"name":"A"}]}`, http.StatusConflict, "limit_reached")

	// 5 x 5 x 5 = 125 combinations > 100: generate refuses and creates nothing.
	expectCode(t, e, co, http.MethodPost, base+"/variants/generate", `{"price_minor":100}`, http.StatusConflict, "limit_reached")
	if n := e.Count(`SELECT count(*) FROM product_variants WHERE product_id = $1`, p.ID); n != 0 {
		t.Errorf("generate created %d variants despite refusing", n)
	}

	vals := make([]string, 31)
	for i := range vals {
		vals[i] = fmt.Sprintf(`{"name":"V%d"}`, i)
	}
	other := create(t, e, co, body("Many Values Chair", 1))
	expectCode(t, e, co, http.MethodPost, productPath(co.ID, other.ID)+"/options", `{"name":"Big","values":[`+strings.Join(vals, ",")+`]}`, http.StatusBadRequest, "validation_failed")
}

func TestGenerateVariants(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p, _ := sofa(t, e, co)
	base := productPath(co.ID, p.ID)

	cfgCall(t, e, co, http.MethodPost, base+"/variants", variantBody("HANDMADE", 9000, "Leather", "Black"), http.StatusCreated)

	w := e.As(co.AdminCookie, http.MethodPost, base+"/variants/generate", `{"price_minor":5000}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("generate: %d %s", w.Code, w.Body)
	}
	var resp struct {
		Created       int        `json:"created"`
		Configuration configResp `json:"configuration"`
	}
	apitest.Decode(t, w, &resp)
	if resp.Created != 5 || len(resp.Configuration.Variants) != 6 {
		t.Fatalf("created %d, total %d; want 5 and 6", resp.Created, len(resp.Configuration.Variants))
	}
	skus := map[string]bool{}
	for _, v := range resp.Configuration.Variants {
		skus[v.SKU] = true
	}
	want := strings.ToUpper(p.Slug) + "-LINEN-GREY"
	if !skus[want] || !skus["HANDMADE"] {
		t.Errorf("SKUs %v missing %s or HANDMADE", skus, want)
	}

	w = e.As(co.AdminCookie, http.MethodPost, base+"/variants/generate", `{"price_minor":5000}`)
	apitest.Decode(t, w, &resp)
	if resp.Created != 0 {
		t.Errorf("second generate created %d, want 0", resp.Created)
	}
}

func TestFromPrice(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p, _ := sofa(t, e, co)
	base := productPath(co.ID, p.ID)
	price := func() int64 {
		w := e.As(co.AdminCookie, http.MethodGet, base, "")
		var r productResp
		apitest.Decode(t, w, &r)
		return r.Price.AmountMinor
	}

	cfg := cfgCall(t, e, co, http.MethodPost, base+"/variants", variantBody("P500", 500, "Linen", "Grey"), http.StatusCreated)
	cfg = cfgCall(t, e, co, http.MethodPost, base+"/variants", variantBody("P300", 300, "Linen", "Black"), http.StatusCreated)
	if got := price(); got != 300 {
		t.Errorf("from price = %d, want 300", got)
	}
	var cheap, dear string
	for _, v := range cfg.Variants {
		if v.SKU == "P300" {
			cheap = v.ID
		} else {
			dear = v.ID
		}
	}
	cfgCall(t, e, co, http.MethodPatch, base+"/variants/"+cheap, `{"available":false}`, http.StatusOK)
	if got := price(); got != 500 {
		t.Errorf("after disabling cheapest: %d, want 500", got)
	}
	cfgCall(t, e, co, http.MethodPatch, base+"/variants/"+dear, `{"price_minor":450}`, http.StatusOK)
	if got := price(); got != 450 {
		t.Errorf("after repricing: %d, want 450", got)
	}
	cfgCall(t, e, co, http.MethodDelete, base+"/variants/"+dear, "", http.StatusOK)
	if got := price(); got != 450 {
		t.Errorf("with no available variants the last price stays: %d", got)
	}

	expectCode(t, e, co, http.MethodPatch, base, `{"price_minor":1}`, http.StatusConflict, "price_from_variants")
	// Other product fields remain editable.
	if w := e.As(co.AdminCookie, http.MethodPatch, base, `{"name":"Oslo Sofa Renamed"}`); w.Code != http.StatusOK {
		t.Errorf("rename with variants: %d %s", w.Code, w.Body)
	}
}

// Two variants of one product are repriced at the same moment. Each
// transaction recomputes the from-price; without the product row lock,
// each could compute the minimum without seeing the other's change.
func TestConcurrentRepricingKeepsFromPrice(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p, _ := sofa(t, e, co)
	base := productPath(co.ID, p.ID)
	cfg := cfgCall(t, e, co, http.MethodPost, base+"/variants", variantBody("A", 1000, "Linen", "Grey"), http.StatusCreated)
	cfg = cfgCall(t, e, co, http.MethodPost, base+"/variants", variantBody("B", 1000, "Linen", "Black"), http.StatusCreated)
	a, b := cfg.Variants[0].ID, cfg.Variants[1].ID

	for round := 0; round < 10; round++ {
		pa, pb := int64(100+round), int64(200+round)
		if round%2 == 1 {
			pa, pb = pb, pa
		}
		var wg sync.WaitGroup
		for _, u := range []struct {
			id    string
			price int64
		}{{a, pa}, {b, pb}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				e.As(co.AdminCookie, http.MethodPatch, base+"/variants/"+u.id, fmt.Sprintf(`{"price_minor":%d}`, u.price))
			}()
		}
		wg.Wait()
		want := min(pa, pb)
		if n := e.Count(`SELECT count(*) FROM products WHERE id = $1 AND price_minor = $2`, p.ID, want); n != 1 {
			t.Errorf("round %d: from price is not %d", round, want)
		}
	}
}

func TestPublishNeedsAvailableVariant(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p, _ := sofa(t, e, co)
	base := productPath(co.ID, p.ID)

	expectCode(t, e, co, http.MethodPost, base+"/publish", "", http.StatusConflict, "no_available_variants")
	cfg := cfgCall(t, e, co, http.MethodPost, base+"/variants", `{"sku":"OFF","price_minor":1,"available":false,"options":{"Fabric":"Linen","Colour":"Grey"}}`, http.StatusCreated)
	expectCode(t, e, co, http.MethodPost, base+"/publish", "", http.StatusConflict, "no_available_variants")
	cfgCall(t, e, co, http.MethodPatch, base+"/variants/"+cfg.Variants[0].ID, `{"available":true}`, http.StatusOK)
	act(t, e, co, p.ID, "publish", http.StatusOK)

	// Once published, variants are switched off rather than deleted.
	expectCode(t, e, co, http.MethodDelete, base+"/variants/"+cfg.Variants[0].ID, "", http.StatusConflict, "product_not_draft")
	cfgCall(t, e, co, http.MethodPatch, base+"/variants/"+cfg.Variants[0].ID, `{"available":false}`, http.StatusOK)
}

func TestConfigurationIsolation(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	b := e.RegisterCompany()
	bp, bcfg := sofa(t, e, b)
	bcfg = cfgCall(t, e, b, http.MethodPost, productPath(b.ID, bp.ID)+"/variants", variantBody("B-ONLY", 1, "Linen", "Grey"), http.StatusCreated)
	ap, acfg := sofa(t, e, a)

	bOpt, bVal, bVar := bcfg.Options[0].ID, bcfg.Options[0].Values[1].ID, bcfg.Variants[0].ID
	routes := func(companyID, productID, optionID, valueID, variantID string) [][3]string {
		base := productPath(companyID, productID)
		return [][3]string{
			{http.MethodGet, base + "/configuration", ""},
			{http.MethodPost, base + "/options", `{"name":"Evil","values":[{"name":"X"}]}`},
			{http.MethodPatch, base + "/options/" + optionID, `{"name":"Hacked"}`},
			{http.MethodDelete, base + "/options/" + optionID, ""},
			{http.MethodPost, base + "/options/" + optionID + "/values", `{"name":"Evil"}`},
			{http.MethodPatch, base + "/options/" + optionID + "/values/" + valueID, `{"name":"Hacked"}`},
			{http.MethodDelete, base + "/options/" + optionID + "/values/" + valueID, ""},
			{http.MethodPost, base + "/variants", variantBody("EVIL", 1, "Leather", "Black")},
			{http.MethodPost, base + "/variants/generate", `{"price_minor":1}`},
			{http.MethodPatch, base + "/variants/" + variantID, `{"price_minor":0}`},
			{http.MethodDelete, base + "/variants/" + variantID, ""},
		}
	}

	// B's ids through B's path (A not a member) and B's product through A's path.
	for _, set := range [][][3]string{
		routes(b.ID, bp.ID, bOpt, bVal, bVar),
		routes(a.ID, bp.ID, bOpt, bVal, bVar),
	} {
		for _, r := range set {
			if w := e.As(a.AdminCookie, r[0], r[1], r[2]); w.Code != http.StatusNotFound {
				t.Errorf("%s %s: %d %s, want 404", r[0], r[1], w.Code, w.Body)
			}
		}
	}
	// A's own product with B's option/value/variant ids: must not touch B.
	aBase := productPath(a.ID, ap.ID)
	for _, r := range [][3]string{
		{http.MethodPatch, aBase + "/options/" + bOpt, `{"name":"Hacked"}`},
		{http.MethodDelete, aBase + "/options/" + bOpt + "/values/" + bVal, ""},
		{http.MethodPatch, aBase + "/options/" + bOpt + "/values/" + bVal, `{"name":"Hacked"}`},
		{http.MethodPatch, aBase + "/variants/" + bVar, `{"price_minor":0}`},
		{http.MethodDelete, aBase + "/variants/" + bVar, ""},
	} {
		if w := e.As(a.AdminCookie, r[0], r[1], r[2]); w.Code != http.StatusNotFound {
			t.Errorf("A product + B id %s %s: %d %s", r[0], r[1], w.Code, w.Body)
		}
	}
	_ = acfg

	if n := e.Count(`SELECT count(*) FROM product_variants WHERE id = $1 AND price_minor = 1 AND sku = 'B-ONLY'`, bVar); n != 1 {
		t.Error("B's variant changed")
	}
	if n := e.Count(`SELECT count(*) FROM product_options WHERE product_id = $1 AND name IN ('Fabric','Colour')`, bp.ID); n != 2 {
		t.Error("B's options changed")
	}
	if n := e.Count(`SELECT count(*) FROM product_option_values WHERE option_id = $1`, bOpt); n != 2 {
		t.Error("B's values changed")
	}
}

func TestPublicDetailIncludesConfiguration(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p, _ := sofa(t, e, co)
	base := productPath(co.ID, p.ID)
	cfgCall(t, e, co, http.MethodPost, base+"/variants", variantBody("LIVE", 4850000, "Linen", "Grey"), http.StatusCreated)
	cfgCall(t, e, co, http.MethodPost, base+"/variants", `{"sku":"SOLD-OUT","price_minor":7200000,"available":false,"options":{"Fabric":"Leather","Colour":"Black"}}`, http.StatusCreated)
	act(t, e, co, p.ID, "publish", http.StatusOK)

	w := e.Do(http.MethodGet, "/v1/catalog/products/"+co.Slug+"/"+p.Slug, "")
	if w.Code != http.StatusOK {
		t.Fatalf("public detail: %d %s", w.Code, w.Body)
	}
	var detail struct {
		productResp
		configResp
	}
	apitest.Decode(t, w, &detail)
	if len(detail.Options) != 2 || len(detail.Variants) != 2 || detail.Price.AmountMinor != 4850000 {
		t.Fatalf("detail: %s", w.Body)
	}
	for _, v := range detail.Variants {
		if v.SKU == "SOLD-OUT" && v.Available {
			t.Error("unavailable variant shown as available")
		}
		if v.Price.Currency != "ETB" {
			t.Errorf("variant currency %q", v.Price.Currency)
		}
	}
	if !strings.Contains(w.Body.String(), `"material_key":"fabric_linen"`) {
		t.Errorf("public detail lacks material_key: %s", w.Body)
	}

	// The list shows the from price (cheapest available).
	pg := publicList(t, e, url.Values{"company": {co.Slug}})
	if len(pg.Products) != 1 || pg.Products[0].Price.AmountMinor != 4850000 {
		t.Errorf("list from price: %+v", pg.Products)
	}
}

func TestDeletingDraftCascadesConfiguration(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p, _ := sofa(t, e, co)
	base := productPath(co.ID, p.ID)
	cfgCall(t, e, co, http.MethodPost, base+"/variants/generate", `{"price_minor":100}`, http.StatusCreated)

	if w := e.As(co.AdminCookie, http.MethodDelete, base, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete draft with variants: %d %s", w.Code, w.Body)
	}
	for _, table := range []string{"product_options", "product_variants", "product_variant_values"} {
		if n := e.Count(`SELECT count(*) FROM `+table+` WHERE product_id = $1`, p.ID); n != 0 {
			t.Errorf("%s still has %d rows", table, n)
		}
	}
}
