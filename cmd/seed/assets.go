package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
)

const (
	greySKU = "DEMO-CHAIR-GREY"
	blueSKU = "DEMO-CHAIR-BLUE"
)

func samplePNG(c color.RGBA) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			shade := uint8((x + y) / 8)
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(min(255, int(c.R)+int(shade))),
				G: uint8(min(255, int(c.G)+int(shade))),
				B: uint8(min(255, int(c.B)+int(shade))),
				A: 255,
			})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// sampleGLB is a valid, self-contained 0.8 × 0.9 × 0.8 metre box. It is
// intentionally simple: frontend development needs a deterministic model
// that exercises real KHR_materials_variants mappings, not production art.
func sampleGLB() ([]byte, error) {
	positions := [][3]float32{
		{-0.4, 0, -0.4}, {0.4, 0, -0.4}, {0.4, 0.9, -0.4}, {-0.4, 0.9, -0.4},
		{-0.4, 0, 0.4}, {0.4, 0, 0.4}, {0.4, 0.9, 0.4}, {-0.4, 0.9, 0.4},
	}
	indices := []uint16{
		0, 1, 2, 0, 2, 3, 5, 4, 7, 5, 7, 6,
		4, 0, 3, 4, 3, 7, 1, 5, 6, 1, 6, 2,
		3, 2, 6, 3, 6, 7, 4, 5, 1, 4, 1, 0,
	}

	var bin bytes.Buffer
	for _, p := range positions {
		for _, v := range p {
			if err := binary.Write(&bin, binary.LittleEndian, v); err != nil {
				return nil, err
			}
		}
	}
	positionBytes := bin.Len()
	for _, i := range indices {
		if err := binary.Write(&bin, binary.LittleEndian, i); err != nil {
			return nil, err
		}
	}
	for bin.Len()%4 != 0 {
		bin.WriteByte(0)
	}

	doc := map[string]any{
		"asset":          map[string]any{"version": "2.0", "generator": "interior_api cmd/seed"},
		"extensionsUsed": []string{"KHR_materials_variants"},
		"extensions": map[string]any{"KHR_materials_variants": map[string]any{
			"variants": []map[string]string{{"name": greySKU}, {"name": blueSKU}},
		}},
		"scene":  0,
		"scenes": []map[string]any{{"nodes": []int{0}}},
		"nodes":  []map[string]any{{"mesh": 0, "name": "Demo chair"}},
		"meshes": []map[string]any{{"primitives": []map[string]any{{
			"attributes": map[string]int{"POSITION": 0},
			"indices":    1,
			"material":   0,
			"extensions": map[string]any{"KHR_materials_variants": map[string]any{
				"mappings": []map[string]any{
					{"material": 0, "variants": []int{0}},
					{"material": 1, "variants": []int{1}},
				},
			}},
		}}}},
		"materials": []map[string]any{
			{"name": "fabric_grey", "pbrMetallicRoughness": map[string]any{"baseColorFactor": []float64{0.45, 0.48, 0.5, 1}, "roughnessFactor": 0.9}},
			{"name": "fabric_blue", "pbrMetallicRoughness": map[string]any{"baseColorFactor": []float64{0.16, 0.32, 0.55, 1}, "roughnessFactor": 0.9}},
		},
		"buffers": []map[string]any{{"byteLength": bin.Len()}},
		"bufferViews": []map[string]any{
			{"buffer": 0, "byteOffset": 0, "byteLength": positionBytes, "target": 34962},
			{"buffer": 0, "byteOffset": positionBytes, "byteLength": len(indices) * 2, "target": 34963},
		},
		"accessors": []map[string]any{
			{"bufferView": 0, "componentType": 5126, "count": len(positions), "type": "VEC3", "min": []float64{-0.4, 0, -0.4}, "max": []float64{0.4, 0.9, 0.4}},
			{"bufferView": 1, "componentType": 5123, "count": len(indices), "type": "SCALAR", "min": []int{0}, "max": []int{7}},
		},
	}
	jsonBytes, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	for len(jsonBytes)%4 != 0 {
		jsonBytes = append(jsonBytes, ' ')
	}

	const (
		jsonChunk = uint32(0x4E4F534A)
		binChunk  = uint32(0x004E4942)
		magic     = uint32(0x46546C67)
	)
	total := 12 + 8 + len(jsonBytes) + 8 + bin.Len()
	var glb bytes.Buffer
	for _, v := range []uint32{magic, 2, uint32(total), uint32(len(jsonBytes)), jsonChunk} {
		if err := binary.Write(&glb, binary.LittleEndian, v); err != nil {
			return nil, err
		}
	}
	glb.Write(jsonBytes)
	for _, v := range []uint32{uint32(bin.Len()), binChunk} {
		if err := binary.Write(&glb, binary.LittleEndian, v); err != nil {
			return nil, err
		}
	}
	glb.Write(bin.Bytes())
	return glb.Bytes(), nil
}
