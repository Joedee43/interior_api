package catalog

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func pngBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{200, 100, 50, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A 1x1 lossless WebP (the standard library cannot encode WebP).
var webp1x1, _ = base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")

// glbBytes builds a GLB around the given JSON, padding the JSON chunk to a
// 4-byte boundary as the spec requires.
func glbBytes(json string) []byte {
	js := []byte(json)
	for len(js)%4 != 0 {
		js = append(js, ' ')
	}
	var buf bytes.Buffer
	total := uint32(12 + 8 + len(js))
	binary.Write(&buf, binary.LittleEndian, []uint32{0x46546C67, 2, total})
	binary.Write(&buf, binary.LittleEndian, []uint32{uint32(len(js)), 0x4E4F534A})
	buf.Write(js)
	return buf.Bytes()
}

const sampleGLTF = `{"asset":{"version":"2.0"},
 "materials":[{"name":"fabric_linen"},{"name":"fabric_velvet"},{"name":"fabric_linen"},{"name":""}],
 "extensions":{"KHR_materials_variants":{"variants":[{"name":"Linen"},{"name":"Velvet"}]}},
 "buffers":[{"byteLength":0}]}`

func TestInspectImage(t *testing.T) {
	for name, tc := range map[string]struct {
		data   []byte
		ct     string
		w, h   int
		reject bool
	}{
		"png":           {pngBytes(t, 40, 30), ContentTypePNG, 40, 30, false},
		"jpeg":          {jpegBytes(t, 8, 6), ContentTypeJPEG, 8, 6, false},
		"webp":          {webp1x1, ContentTypeWebP, 1, 1, false},
		"html as image": {[]byte("<html><script>alert(1)</script></html>"), "", 0, 0, true},
		"glb as image":  {glbBytes(sampleGLTF), "", 0, 0, true},
		"truncated png": {pngBytes(t, 4, 4)[:20], "", 0, 0, true},
		"empty":         {nil, "", 0, 0, true},
	} {
		t.Run(name, func(t *testing.T) {
			info, err := inspectImage(bytes.NewReader(tc.data))
			if tc.reject {
				if !errors.Is(err, ErrUnsupportedFile) {
					t.Fatalf("want rejection, got %+v, %v", info, err)
				}
				return
			}
			if err != nil || info.ContentType != tc.ct || info.Width != tc.w || info.Height != tc.h {
				t.Errorf("got %+v, %v", info, err)
			}
		})
	}
}

func TestInspectGLB(t *testing.T) {
	good := glbBytes(sampleGLTF)
	info, err := inspectGLB(bytes.NewReader(good), int64(len(good)))
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Materials) != 2 || info.Materials[0] != "fabric_linen" || info.Materials[1] != "fabric_velvet" {
		t.Errorf("materials = %v (want deduplicated, empty names dropped)", info.Materials)
	}
	if len(info.Variants) != 2 {
		t.Errorf("variants = %v", info.Variants)
	}

	wrongLength := append([]byte{}, good...)
	binary.LittleEndian.PutUint32(wrongLength[8:], uint32(len(good)+100))

	for name, data := range map[string][]byte{
		"png bytes":        pngBytes(t, 2, 2),
		"glTF 1":           append([]byte("glTF\x01\x00\x00\x00"), good[8:]...),
		"length mismatch":  wrongLength,
		"truncated":        good[:len(good)-4],
		"bad json":         glbBytes(`{"asset":`),
		"asset 1.0":        glbBytes(`{"asset":{"version":"1.0"}}`),
		"external buffer":  glbBytes(`{"asset":{"version":"2.0"},"buffers":[{"uri":"https://evil.example/x.bin","byteLength":4}]}`),
		"external texture": glbBytes(`{"asset":{"version":"2.0"},"images":[{"uri":"../../etc/passwd"}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := inspectGLB(bytes.NewReader(data), int64(len(data))); !errors.Is(err, ErrUnsupportedFile) {
				t.Errorf("want rejection, got %v", err)
			}
		})
	}

	// Embedded (data:) URIs are self-contained and allowed.
	embedded := glbBytes(`{"asset":{"version":"2.0"},"images":[{"uri":"data:image/png;base64,AAAA"}]}`)
	if _, err := inspectGLB(bytes.NewReader(embedded), int64(len(embedded))); err != nil {
		t.Errorf("data: URI rejected: %v", err)
	}
}
