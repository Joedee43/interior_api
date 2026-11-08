package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

// The frontend requires is_primary on every image and the materials and
// variants arrays on every model, even when false or empty. No database
// needed.
func TestMediaDTOAlwaysHasPrimaryAndArrays(t *testing.T) {
	cases := map[string]struct {
		media Media
		want  []string
	}{
		"non-primary image": {
			media: Media{ID: "m1", Kind: "image", ContentType: "image/png", SizeBytes: 10, Position: 1},
			want:  []string{`"is_primary":false`, `"materials":[]`, `"variants":[]`},
		},
		"model without materials or variants": {
			media: Media{ID: "m2", Kind: "model", ContentType: "model/gltf-binary", SizeBytes: 10},
			want:  []string{`"is_primary":false`, `"materials":[]`, `"variants":[]`},
		},
		"model with materials": {
			media: Media{ID: "m3", Kind: "model", ContentType: "model/gltf-binary", SizeBytes: 10, Materials: []string{"fabric"}, Variants: []string{"GREY"}},
			want:  []string{`"materials":["fabric"]`, `"variants":["GREY"]`},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := json.Marshal(toMediaDTO(tc.media))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(string(got), want) {
					t.Errorf("JSON lacks %s: %s", want, got)
				}
			}
		})
	}
}
