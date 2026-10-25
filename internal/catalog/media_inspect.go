package catalog

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"strings"

	// Blank imports register decoders with the image package, so
	// image.DecodeConfig recognizes these formats.
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

const (
	ContentTypeJPEG = "image/jpeg"
	ContentTypePNG  = "image/png"
	ContentTypeWebP = "image/webp"
	ContentTypeGLB  = "model/gltf-binary"

	maxImageSide = 20_000 // pixels; far beyond any product photo
)

// ErrUnsupportedFile means the bytes are not an accepted format, whatever
// the file name or the client's Content-Type claimed.
var ErrUnsupportedFile = errors.New("unsupported or invalid file")

// FileProblem explains why a file was rejected, for the client.
type FileProblem struct{ Reason string }

func (e *FileProblem) Error() string { return "invalid file: " + e.Reason }

func (e *FileProblem) Unwrap() error { return ErrUnsupportedFile }

func reject(format string, args ...any) error {
	return &FileProblem{Reason: fmt.Sprintf(format, args...)}
}

// sniff identifies a file by its leading "magic" bytes. File extensions
// and client Content-Type headers are trivially faked; these are not.
func sniff(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return ContentTypeJPEG
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return ContentTypePNG
	case len(head) >= 12 && bytes.Equal(head[0:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")):
		return ContentTypeWebP
	case bytes.HasPrefix(head, []byte("glTF")):
		return ContentTypeGLB
	}
	return ""
}

// ImageInfo is what we learn from an image without decoding its pixels.
type ImageInfo struct {
	ContentType   string
	Width, Height int
}

// inspectImage checks the magic bytes and reads only the header
// (image.DecodeConfig), so even a huge image is cheap to validate.
func inspectImage(f io.ReadSeeker) (ImageInfo, error) {
	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	ct := sniff(head[:n])
	if ct != ContentTypeJPEG && ct != ContentTypePNG && ct != ContentTypeWebP {
		return ImageInfo{}, reject("images must be JPEG, PNG or WebP")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ImageInfo{}, err
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return ImageInfo{}, reject("the image header could not be read (corrupt file?)")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxImageSide || cfg.Height > maxImageSide {
		return ImageInfo{}, reject("image dimensions %dx%d are out of range", cfg.Width, cfg.Height)
	}
	return ImageInfo{ContentType: ct, Width: cfg.Width, Height: cfg.Height}, nil
}

// ModelInfo is what we learn from a GLB.
type ModelInfo struct {
	Materials []string // material names, in file order, deduplicated
	Variants  []string // KHR_materials_variants names, if any
}

const (
	glbMagic     = 0x46546C67 // "glTF" read as a little-endian uint32
	glbChunkJSON = 0x4E4F534A // "JSON"
	maxGLBJSON   = 16 << 20   // the JSON part of any real model is far smaller
)

// gltfDoc is the subset of the glTF JSON we inspect.
type gltfDoc struct {
	Asset struct {
		Version string `json:"version"`
	} `json:"asset"`
	Materials []struct {
		Name string `json:"name"`
	} `json:"materials"`
	Buffers []struct {
		URI *string `json:"uri"`
	} `json:"buffers"`
	Images []struct {
		URI *string `json:"uri"`
	} `json:"images"`
	Extensions struct {
		Variants struct {
			Variants []struct {
				Name string `json:"name"`
			} `json:"variants"`
		} `json:"KHR_materials_variants"`
	} `json:"extensions"`
}

// inspectGLB validates a binary glTF 2.0 file of the given size:
//
//	header: magic "glTF" | version 2 | total length   (3 x uint32 LE)
//	chunk 0: length | type "JSON" | JSON bytes
//	chunk 1: length | type "BIN\0" | binary data       (optional)
//
// It rejects files whose JSON points at external resources: a GLB must be
// self-contained, and the frontend should never be told to fetch
// arbitrary URLs embedded in an uploaded file.
func inspectGLB(f io.ReadSeeker, size int64) (ModelInfo, error) {
	var header struct {
		Magic, Version, Length uint32
	}
	// binary.Read fills the struct fields in order from little-endian bytes.
	if err := binary.Read(f, binary.LittleEndian, &header); err != nil || header.Magic != glbMagic {
		return ModelInfo{}, reject("3D models must be GLB (binary glTF) files")
	}
	if header.Version != 2 {
		return ModelInfo{}, reject("glTF version %d is not supported; export glTF 2.0", header.Version)
	}
	if int64(header.Length) != size {
		return ModelInfo{}, reject("GLB header says %d bytes but the file has %d (truncated?)", header.Length, size)
	}

	var chunk struct {
		Length, Type uint32
	}
	if err := binary.Read(f, binary.LittleEndian, &chunk); err != nil || chunk.Type != glbChunkJSON {
		return ModelInfo{}, reject("GLB is missing its JSON chunk")
	}
	if chunk.Length == 0 || chunk.Length > maxGLBJSON || int64(chunk.Length) > size-20 {
		return ModelInfo{}, reject("GLB JSON chunk has an invalid length")
	}
	raw := make([]byte, chunk.Length)
	if _, err := io.ReadFull(f, raw); err != nil {
		return ModelInfo{}, reject("GLB JSON chunk is truncated")
	}

	var doc gltfDoc
	if err := json.Unmarshal(bytes.TrimRight(raw, " \x00"), &doc); err != nil {
		return ModelInfo{}, reject("GLB JSON is not valid")
	}
	if doc.Asset.Version != "2.0" {
		return ModelInfo{}, reject("glTF asset version %q is not supported; export glTF 2.0", doc.Asset.Version)
	}
	for _, b := range doc.Buffers {
		if b.URI != nil && !strings.HasPrefix(*b.URI, "data:") {
			return ModelInfo{}, reject("the model references an external buffer; export a self-contained GLB")
		}
	}
	for _, im := range doc.Images {
		if im.URI != nil && !strings.HasPrefix(*im.URI, "data:") {
			return ModelInfo{}, reject("the model references an external texture; export a self-contained GLB with embedded textures")
		}
	}

	info := ModelInfo{Materials: []string{}, Variants: []string{}}
	seen := map[string]bool{}
	for _, m := range doc.Materials {
		if m.Name != "" && !seen[m.Name] {
			seen[m.Name] = true
			info.Materials = append(info.Materials, m.Name)
		}
	}
	for _, v := range doc.Extensions.Variants.Variants {
		if v.Name != "" {
			info.Variants = append(info.Variants, v.Name)
		}
	}
	return info, nil
}
