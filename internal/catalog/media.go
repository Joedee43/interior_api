package catalog

import (
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MediaImage = "image"
	MediaModel = "model"

	MaxImagesPerProduct = 20
	MaxImageBytes       = 10 << 20 // 10 MiB
	MaxModelBytes       = 50 << 20 // 50 MiB
)

var (
	ErrMediaNotFound = errors.New("media not found")
	ErrFileTooLarge  = errors.New("file too large")
)

// Media is one stored image or 3D model of a product.
type Media struct {
	ID               string
	ProductID        string
	CompanyID        string
	Kind             string
	StorageKey       string
	ContentType      string
	SizeBytes        int64
	SHA256           []byte
	OriginalFilename string
	Width            *int32
	Height           *int32
	AltText          string
	Position         int
	IsPrimary        bool
	Materials        []string
	Variants         []string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// ProductMedia is a product's gallery plus its (optional) model.
type ProductMedia struct {
	Images []Media
	Model  *Media
}

// MaterialCheck compares option values' material_key with the materials
// inside the product's GLB, so a company can spot names that the 3D
// viewer will not find. It is advisory: nothing is blocked by it.
type MaterialCheck struct {
	ModelPresent bool
	// Missing lists material_keys with no material of that name (case-
	// insensitive) in the model. Empty when no model is uploaded.
	Missing []string
}

func materialCheck(cfg Configuration, model *Media) MaterialCheck {
	mc := MaterialCheck{ModelPresent: model != nil, Missing: []string{}}
	if model == nil {
		return mc
	}
	have := make(map[string]bool, len(model.Materials))
	for _, m := range model.Materials {
		have[strings.ToLower(m)] = true
	}
	seen := map[string]bool{}
	for _, o := range cfg.Options {
		for _, v := range o.Values {
			if v.MaterialKey == nil || seen[*v.MaterialKey] {
				continue
			}
			seen[*v.MaterialKey] = true
			if !have[*v.MaterialKey] {
				mc.Missing = append(mc.Missing, *v.MaterialKey)
			}
		}
	}
	return mc
}

// newStorageKey returns "media/<random uuid v4>". Keys are generated, never
// derived from the uploaded file name.
func newStorageKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("media/%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// cleanFilename keeps the uploaded name for display only: base name, no
// control characters, at most 255 characters. It is never used as a path.
func cleanFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == "/" {
		return ""
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1 // dropping the rune
		}
		return r
	}, name)
	for utf8.RuneCountInString(name) > 255 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}

func validateAltText(s string) string {
	if utf8.RuneCountInString(s) > 300 {
		return "must be at most 300 characters"
	}
	return ""
}
