package catalog_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Joedee43/interior_api/internal/apitest"
)

// ---- fixtures -----------------------------------------------------------

func pngFile(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegFile(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var webpFile, _ = base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")

func glbFile(materials ...string) []byte {
	names := make([]string, len(materials))
	for i, m := range materials {
		names[i] = fmt.Sprintf(`{"name":%q}`, m)
	}
	js := []byte(`{"asset":{"version":"2.0"},"materials":[` + strings.Join(names, ",") + `]}`)
	for len(js)%4 != 0 {
		js = append(js, ' ')
	}
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, []uint32{0x46546C67, 2, uint32(20 + len(js))})
	binary.Write(&buf, binary.LittleEndian, []uint32{uint32(len(js)), 0x4E4F534A})
	buf.Write(js)
	return buf.Bytes()
}

type mediaResp struct {
	ID          string   `json:"id"`
	URL         string   `json:"url"`
	FileURL     string   `json:"file_url"`
	ContentType string   `json:"content_type"`
	SizeBytes   int64    `json:"size_bytes"`
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	AltText     string   `json:"alt_text"`
	IsPrimary   bool     `json:"is_primary"`
	Materials   []string `json:"materials"`
	SHA256      string   `json:"sha256"`
	Filename    string   `json:"original_filename"`
}

type mediaListResp struct {
	Images        []mediaResp `json:"images"`
	Model         *mediaResp  `json:"model"`
	MaterialCheck struct {
		ModelPresent bool     `json:"model_present"`
		Missing      []string `json:"missing_material_keys"`
	} `json:"material_check"`
}

func uploadImage(t *testing.T, e *apitest.Env, co apitest.Company, productID, name string, data []byte, fields map[string]string) mediaResp {
	t.Helper()
	w := e.Upload(co.AdminCookie, http.MethodPost, productPath(co.ID, productID)+"/images", name, data, fields)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload %s: %d %s", name, w.Code, w.Body)
	}
	var m mediaResp
	apitest.Decode(t, w, &m)
	return m
}

func listMedia(t *testing.T, e *apitest.Env, co apitest.Company, productID string) mediaListResp {
	t.Helper()
	w := e.As(co.AdminCookie, http.MethodGet, productPath(co.ID, productID)+"/media", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list media: %d %s", w.Code, w.Body)
	}
	var r mediaListResp
	apitest.Decode(t, w, &r)
	return r
}

// ---- images ---------------------------------------------------------------

func TestImageGallery(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p := create(t, e, co, body("Gallery Chair", 100))

	first := uploadImage(t, e, co, p.ID, "front.png", pngFile(t, 40, 30), map[string]string{"alt_text": " Front view "})
	if !first.IsPrimary || first.ContentType != "image/png" || first.Width != 40 || first.Height != 30 ||
		first.AltText != "Front view" || first.Filename != "front.png" || len(first.SHA256) != 64 {
		t.Errorf("first image: %+v", first)
	}
	// The type comes from the bytes: a JPEG named .png is stored as JPEG.
	second := uploadImage(t, e, co, p.ID, "liar.png", jpegFile(t, 8, 6), nil)
	if second.ContentType != "image/jpeg" || second.IsPrimary {
		t.Errorf("second image: %+v", second)
	}
	third := uploadImage(t, e, co, p.ID, `C:\fakepath\..\side.webp`, webpFile, nil)
	if third.ContentType != "image/webp" || third.Filename != "side.webp" {
		t.Errorf("third image: %+v", third)
	}

	// Switch primary.
	w := e.As(co.AdminCookie, http.MethodPatch, productPath(co.ID, p.ID)+"/images/"+third.ID, `{"is_primary":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("set primary: %d %s", w.Code, w.Body)
	}
	got := listMedia(t, e, co, p.ID)
	primaries := 0
	for _, im := range got.Images {
		if im.IsPrimary {
			primaries++
			if im.ID != third.ID {
				t.Errorf("wrong primary %s", im.ID)
			}
		}
	}
	if len(got.Images) != 3 || primaries != 1 {
		t.Errorf("%d images, %d primary", len(got.Images), primaries)
	}
	if w := e.As(co.AdminCookie, http.MethodPatch, productPath(co.ID, p.ID)+"/images/"+third.ID, `{"is_primary":false}`); w.Code != http.StatusBadRequest {
		t.Errorf("is_primary false: %d", w.Code)
	}

	// The product list shows the primary image.
	act(t, e, co, p.ID, "publish", http.StatusOK)
	pg := publicList(t, e, url.Values{"company": {co.Slug}})
	if len(pg.Products) != 1 || pg.Products[0].PrimaryImageURL == nil || *pg.Products[0].PrimaryImageURL != "/v1/media/"+third.ID {
		t.Errorf("list lacks primary_image_url for %s", third.ID)
	}

	// Deleting the primary promotes the first remaining image.
	if w := e.As(co.AdminCookie, http.MethodDelete, productPath(co.ID, p.ID)+"/images/"+third.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	got = listMedia(t, e, co, p.ID)
	if len(got.Images) != 2 || !got.Images[0].IsPrimary || got.Images[0].ID != first.ID {
		t.Errorf("after deleting primary: %+v", got.Images)
	}
	if n := e.StoredFiles(); n != 2 {
		t.Errorf("%d files on disk, want 2", n)
	}
}

func TestRejectsBadUploads(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p := create(t, e, co, body("Strict Chair", 100))
	images := productPath(co.ID, p.ID) + "/images"

	for name, data := range map[string][]byte{
		"html renamed png": []byte("<html><script>alert(document.cookie)</script></html>"),
		"glb as image":     glbFile("x"),
		"empty":            {},
		"truncated png":    pngFile(t, 10, 10)[:30],
	} {
		w := e.Upload(co.AdminCookie, http.MethodPost, images, "photo.png", data, nil)
		if w.Code != http.StatusBadRequest || apitest.ErrorCode(t, w) != "invalid_file" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if w := e.Upload(co.AdminCookie, http.MethodPost, images, "x.png", pngFile(t, 2, 2), map[string]string{"color": "red"}); w.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d %s", w.Code, w.Body)
	}
	if w := e.As(co.AdminCookie, http.MethodPost, images, `{"file":"base64..."}`); w.Code != http.StatusBadRequest {
		t.Errorf("JSON instead of multipart: %d %s", w.Code, w.Body)
	}
	if w := e.Upload(co.AdminCookie, http.MethodPost, images, "x.png", pngFile(t, 2, 2), map[string]string{"alt_text": strings.Repeat("a", 301)}); w.Code != http.StatusBadRequest {
		t.Errorf("long alt text: %d %s", w.Code, w.Body)
	}
	// A bad model upload must not be accepted on the model route either.
	if w := e.Upload(co.AdminCookie, http.MethodPut, productPath(co.ID, p.ID)+"/model", "chair.glb", pngFile(t, 2, 2), nil); w.Code != http.StatusBadRequest {
		t.Errorf("png as model: %d %s", w.Code, w.Body)
	}

	// Every rejected upload was cleaned out of storage.
	if n := e.StoredFiles(); n != 0 {
		t.Errorf("%d files left on disk after rejected uploads", n)
	}
	if n := e.Count(`SELECT count(*) FROM product_media WHERE product_id = $1`, p.ID); n != 0 {
		t.Errorf("%d media rows after rejected uploads", n)
	}
}

func TestUploadSizeLimit(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p := create(t, e, co, body("Huge Photo Chair", 100))

	big := make([]byte, 10<<20+1) // one byte over 10 MiB
	copy(big, pngFile(t, 2, 2))
	w := e.Upload(co.AdminCookie, http.MethodPost, productPath(co.ID, p.ID)+"/images", "big.png", big, nil)
	if w.Code != http.StatusRequestEntityTooLarge || apitest.ErrorCode(t, w) != "payload_too_large" {
		t.Errorf("oversized image: %d %s", w.Code, w.Body)
	}
	if n := e.StoredFiles(); n != 0 {
		t.Errorf("%d files left after oversized upload", n)
	}
}

func TestImageCountLimit(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p := create(t, e, co, body("Many Photos Chair", 100))
	img := pngFile(t, 2, 2)
	for i := 0; i < 20; i++ {
		uploadImage(t, e, co, p.ID, fmt.Sprintf("p%d.png", i), img, nil)
	}
	w := e.Upload(co.AdminCookie, http.MethodPost, productPath(co.ID, p.ID)+"/images", "p21.png", img, nil)
	if w.Code != http.StatusConflict || apitest.ErrorCode(t, w) != "limit_reached" {
		t.Errorf("21st image: %d %s", w.Code, w.Body)
	}
	if n := e.StoredFiles(); n != 20 {
		t.Errorf("%d files on disk, want 20 (the refused one cleaned up)", n)
	}
}

// ---- 3D model ---------------------------------------------------------------

func TestModelUploadAndMaterialCheck(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p := create(t, e, co, body("Model Chair", 100))
	base := productPath(co.ID, p.ID)
	cfgCall(t, e, co, http.MethodPost, base+"/options",
		`{"name":"Fabric","values":[{"name":"Linen","material_key":"fabric_linen"},{"name":"Velvet","material_key":"fabric_velvet"},{"name":"Plain"}]}`, http.StatusCreated)

	if got := listMedia(t, e, co, p.ID); got.Model != nil || got.MaterialCheck.ModelPresent {
		t.Errorf("no model yet: %+v", got)
	}

	w := e.Upload(co.AdminCookie, http.MethodPut, base+"/model", "chair.glb", glbFile("Fabric_Linen", "Wood"), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("upload model: %d %s", w.Code, w.Body)
	}
	got := listMedia(t, e, co, p.ID)
	if got.Model == nil || got.Model.ContentType != "model/gltf-binary" || len(got.Model.Materials) != 2 {
		t.Fatalf("model: %+v", got.Model)
	}
	// Matching is case-insensitive; fabric_velvet has no material in the model.
	if !got.MaterialCheck.ModelPresent || len(got.MaterialCheck.Missing) != 1 || got.MaterialCheck.Missing[0] != "fabric_velvet" {
		t.Errorf("material check: %+v", got.MaterialCheck)
	}
	firstModel := got.Model.ID

	// A broken replacement is rejected and the current model stays.
	if w := e.Upload(co.AdminCookie, http.MethodPut, base+"/model", "broken.glb", glbFile("x")[:30], nil); w.Code != http.StatusBadRequest {
		t.Errorf("broken model: %d %s", w.Code, w.Body)
	}
	if got := listMedia(t, e, co, p.ID); got.Model == nil || got.Model.ID != firstModel {
		t.Error("failed replacement lost the existing model")
	}

	// A good replacement swaps the model and deletes the old file.
	w = e.Upload(co.AdminCookie, http.MethodPut, base+"/model", "chair-v2.glb", glbFile("fabric_linen", "fabric_velvet"), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", w.Code, w.Body)
	}
	got = listMedia(t, e, co, p.ID)
	if got.Model.ID == firstModel || len(got.MaterialCheck.Missing) != 0 {
		t.Errorf("after replace: model %s, missing %v", got.Model.ID, got.MaterialCheck.Missing)
	}
	if n := e.StoredFiles(); n != 1 {
		t.Errorf("%d files on disk after replace, want 1", n)
	}

	if w := e.As(co.AdminCookie, http.MethodDelete, base+"/model", ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete model: %d", w.Code)
	}
	if w := e.As(co.AdminCookie, http.MethodDelete, base+"/model", ""); w.Code != http.StatusNotFound {
		t.Errorf("delete missing model: %d", w.Code)
	}
	if n := e.StoredFiles(); n != 0 {
		t.Errorf("%d files on disk after delete", n)
	}
}

// ---- serving and visibility -------------------------------------------------

func TestPublicMediaVisibility(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p := create(t, e, co, body("Visible Media Chair", 100))
	data := pngFile(t, 64, 48)
	img := uploadImage(t, e, co, p.ID, "a.png", data, nil)
	e.Upload(co.AdminCookie, http.MethodPut, productPath(co.ID, p.ID)+"/model", "m.glb", glbFile("fabric_linen"), nil)

	public := func() int { return e.Do(http.MethodGet, img.URL, "").Code }
	if code := public(); code != http.StatusNotFound {
		t.Errorf("draft image public: %d", code)
	}
	// Members can still fetch their draft's file, never cached publicly.
	w := e.As(co.AdminCookie, http.MethodGet, img.FileURL, "")
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), data) || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("member file: %d cache=%q", w.Code, w.Header().Get("Cache-Control"))
	}

	act(t, e, co, p.ID, "publish", http.StatusOK)
	w = e.Do(http.MethodGet, img.URL, "")
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatalf("published image: %d", w.Code)
	}
	for header, want := range map[string]string{
		"Content-Type":           "image/png",
		"Cache-Control":          "public, max-age=31536000, immutable",
		"X-Content-Type-Options": "nosniff",
		"ETag":                   `"` + img.SHA256 + `"`,
	} {
		if got := w.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("CSP = %q", w.Header().Get("Content-Security-Policy"))
	}

	// Range request: partial content.
	req := func(h map[string]string) *http.Response {
		r, _ := http.NewRequest(http.MethodGet, img.URL, nil)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		rw := httptest.NewRecorder()
		e.Router.ServeHTTP(rw, r)
		return rw.Result()
	}
	if res := req(map[string]string{"Range": "bytes=0-9"}); res.StatusCode != http.StatusPartialContent || res.ContentLength != 10 {
		t.Errorf("range: %d len %d", res.StatusCode, res.ContentLength)
	}
	if res := req(map[string]string{"If-None-Match": `"` + img.SHA256 + `"`}); res.StatusCode != http.StatusNotModified {
		t.Errorf("conditional GET: %d", res.StatusCode)
	}

	// Public product page lists images and model.
	w = e.Do(http.MethodGet, "/v1/catalog/products/"+co.Slug+"/"+p.Slug, "")
	if !strings.Contains(w.Body.String(), img.URL) || !strings.Contains(w.Body.String(), `"model":{`) ||
		strings.Contains(w.Body.String(), "original_filename") || strings.Contains(w.Body.String(), "file_url") {
		t.Errorf("public detail media: %s", w.Body)
	}

	act(t, e, co, p.ID, "archive", http.StatusOK)
	if code := public(); code != http.StatusNotFound {
		t.Errorf("archived product image: %d", code)
	}
	act(t, e, co, p.ID, "restore", http.StatusOK)
	act(t, e, co, p.ID, "publish", http.StatusOK)
	e.Exec(`UPDATE companies SET status = 'suspended' WHERE id = $1`, co.ID)
	if code := public(); code != http.StatusNotFound {
		t.Errorf("suspended company image: %d", code)
	}
	if w := e.Do(http.MethodGet, "/v1/media/not-a-uuid", ""); w.Code != http.StatusNotFound {
		t.Errorf("malformed id: %d", w.Code)
	}
}

func TestMediaIsolation(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	b := e.RegisterCompany()
	bp := create(t, e, b, body("B Media Chair", 100))
	bImg := uploadImage(t, e, b, bp.ID, "secret.png", pngFile(t, 4, 4), nil)
	ap := create(t, e, a, body("A Media Chair", 100))

	img := pngFile(t, 2, 2)
	for _, path := range []string{
		productPath(b.ID, bp.ID) + "/images", // not a member of B
		productPath(a.ID, bp.ID) + "/images", // B's product via A's path
	} {
		if w := e.Upload(a.AdminCookie, http.MethodPost, path, "x.png", img, nil); w.Code != http.StatusNotFound {
			t.Errorf("upload to %s: %d", path, w.Code)
		}
		modelPath := strings.TrimSuffix(path, "/images") + "/model"
		if w := e.Upload(a.AdminCookie, http.MethodPut, modelPath, "x.glb", glbFile("x"), nil); w.Code != http.StatusNotFound {
			t.Errorf("model to %s: %d", modelPath, w.Code)
		}
	}
	for _, r := range [][3]string{
		{http.MethodGet, productPath(a.ID, bp.ID) + "/media", ""},
		{http.MethodPatch, productPath(a.ID, ap.ID) + "/images/" + bImg.ID, `{"alt_text":"pwned"}`},
		{http.MethodPatch, productPath(a.ID, ap.ID) + "/images/" + bImg.ID, `{"is_primary":true}`},
		{http.MethodDelete, productPath(a.ID, ap.ID) + "/images/" + bImg.ID, ""},
		{http.MethodGet, "/v1/companies/" + a.ID + "/media/" + bImg.ID + "/file", ""},
		{http.MethodGet, "/v1/companies/" + b.ID + "/media/" + bImg.ID + "/file", ""},
	} {
		if w := e.As(a.AdminCookie, r[0], r[1], r[2]); w.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d %s", r[0], r[1], w.Code, w.Body)
		}
	}
	if w := e.Upload(nil, http.MethodPost, productPath(b.ID, bp.ID)+"/images", "x.png", img, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous upload: %d", w.Code)
	}

	if n := e.Count(`SELECT count(*) FROM product_media WHERE id = $1 AND alt_text = '' AND is_primary`, bImg.ID); n != 1 {
		t.Error("B's image was modified")
	}
	if n := e.Count(`SELECT count(*) FROM product_media WHERE product_id IN ($1, $2)`, ap.ID, bp.ID); n != 1 {
		t.Errorf("unexpected media rows: %d", n)
	}
	if n := e.StoredFiles(); n != 1 {
		t.Errorf("%d files on disk, want only B's 1", n)
	}
}

func TestDeletingDraftRemovesFiles(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	p := create(t, e, co, body("Doomed Chair", 100))
	uploadImage(t, e, co, p.ID, "1.png", pngFile(t, 2, 2), nil)
	uploadImage(t, e, co, p.ID, "2.png", pngFile(t, 3, 3), nil)
	e.Upload(co.AdminCookie, http.MethodPut, productPath(co.ID, p.ID)+"/model", "m.glb", glbFile("a"), nil)
	if n := e.StoredFiles(); n != 3 {
		t.Fatalf("%d files before delete", n)
	}

	if w := e.As(co.AdminCookie, http.MethodDelete, productPath(co.ID, p.ID), ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete product: %d %s", w.Code, w.Body)
	}
	if n := e.StoredFiles(); n != 0 {
		t.Errorf("%d files left after deleting the draft", n)
	}
	if n := e.Count(`SELECT count(*) FROM product_media WHERE product_id = $1`, p.ID); n != 0 {
		t.Errorf("%d media rows left", n)
	}
}
