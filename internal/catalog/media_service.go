package catalog

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/company"
	"github.com/Joedee43/interior_api/internal/storage"
)

// Uploads happen in two steps:
//
//  1. Stage: stream the bytes into storage under a fresh key, hashing as
//     they go, then validate the stored file by its content.
//  2. Attach: in a transaction holding the product lock, enforce limits
//     and insert the media row.
//
// Staging first means a 50 MB upload is never held in memory, and the
// transaction (which holds a lock) never waits on a slow client. If
// attaching fails, the staged file is deleted.

// Staged is a validated file in storage that has no media row yet.
type Staged struct {
	kind  string
	key   string
	size  int64
	sum   []byte
	image ImageInfo
	model ModelInfo
}

// Stage stores and validates one uploaded file of the given kind.
func (s *Service) Stage(ctx context.Context, kind string, r io.Reader) (*Staged, error) {
	limit := int64(MaxImageBytes)
	if kind == MediaModel {
		limit = MaxModelBytes
	}
	st := &Staged{kind: kind, key: newStorageKey()}

	h := sha256.New()
	// LimitReader stops after limit+1 bytes: reading exactly one byte more
	// than allowed is how we know the file was too large, without
	// reading (or storing) the rest of an oversized upload.
	body := io.TeeReader(io.LimitReader(r, limit+1), h) // a hash.Hash is an io.Writer: hashing happens as bytes stream past
	n, err := s.files.Put(ctx, st.key, body)
	if err != nil {
		s.removeFiles(ctx, st.key)
		var tooLarge *http.MaxBytesError // the request body cap fired first
		if errors.As(err, &tooLarge) {
			return nil, ErrFileTooLarge
		}
		return nil, err
	}
	if n > limit {
		s.removeFiles(ctx, st.key)
		return nil, ErrFileTooLarge
	}
	if n == 0 {
		s.removeFiles(ctx, st.key)
		return nil, reject("the file is empty")
	}
	st.size, st.sum = n, h.Sum(nil)

	if err := s.inspect(ctx, st); err != nil {
		s.removeFiles(ctx, st.key)
		return nil, err
	}
	return st, nil
}

func (s *Service) inspect(ctx context.Context, st *Staged) error {
	f, err := s.files.Open(ctx, st.key)
	if err != nil {
		return err
	}
	defer f.Close()
	if st.kind == MediaModel {
		st.model, err = inspectGLB(f, st.size)
	} else {
		st.image, err = inspectImage(f)
	}
	return err
}

// Discard deletes a staged file that will not be attached (e.g. the
// request failed after the file part was read).
func (s *Service) Discard(ctx context.Context, st *Staged) {
	if st != nil {
		s.removeFiles(ctx, st.key)
	}
}

// removeFiles deletes stored files, logging failures instead of failing
// the request: by now the database no longer references them, so a
// leftover file is harmless clutter for a future cleanup job.
func (s *Service) removeFiles(ctx context.Context, keys ...string) {
	// A cancelled request context must not stop the cleanup.
	ctx = context.WithoutCancel(ctx)
	for _, k := range keys {
		if err := s.files.Delete(ctx, k); err != nil {
			s.log.WarnContext(ctx, "could not delete stored file", slog.String("key", k), slog.Any("error", err))
		}
	}
}

// AttachImage adds a staged image to the product's gallery. The first
// image becomes the primary one. The staged file is discarded on error.
func (s *Service) AttachImage(ctx context.Context, actor company.Member, productID string, st *Staged, altText, filename string) (Media, error) {
	altText = strings.TrimSpace(altText)
	p := auth.Problems{}
	p.Add("alt_text", validateAltText(altText))
	if err := p.Err(); err != nil {
		s.Discard(ctx, st)
		return Media{}, err
	}

	var out Media
	err := s.productTx(ctx, actor, productID, func(tx pgx.Tx, _, _ string) error {
		pm, err := listMedia(ctx, tx, productID)
		if err != nil {
			return err
		}
		if len(pm.Images) >= MaxImagesPerProduct {
			return &LimitError{What: "images per product", Max: MaxImagesPerProduct}
		}
		w, h := int32(st.image.Width), int32(st.image.Height)
		out, err = insertMedia(ctx, tx, actor.CompanyID, productID, mediaRow{
			Kind: MediaImage, StorageKey: st.key, ContentType: st.image.ContentType,
			SizeBytes: st.size, SHA256: st.sum, OriginalFilename: cleanFilename(filename),
			Width: &w, Height: &h, AltText: altText,
			Position: len(pm.Images), IsPrimary: len(pm.Images) == 0,
		})
		return err
	})
	if err != nil {
		s.Discard(ctx, st)
		return Media{}, err
	}
	return out, nil
}

// ReplaceModel sets the product's 3D model, replacing any previous one.
// The old file is deleted only after the new row has committed, so a
// failure never leaves the product without its previous model.
func (s *Service) ReplaceModel(ctx context.Context, actor company.Member, productID string, st *Staged, filename string) (Media, error) {
	var (
		out    Media
		oldKey string
	)
	err := s.productTx(ctx, actor, productID, func(tx pgx.Tx, _, _ string) error {
		pm, err := listMedia(ctx, tx, productID)
		if err != nil {
			return err
		}
		if pm.Model != nil {
			if _, err := deleteMedia(ctx, tx, productID, pm.Model.ID, MediaModel); err != nil {
				return err
			}
			oldKey = pm.Model.StorageKey
		}
		out, err = insertMedia(ctx, tx, actor.CompanyID, productID, mediaRow{
			Kind: MediaModel, StorageKey: st.key, ContentType: ContentTypeGLB,
			SizeBytes: st.size, SHA256: st.sum, OriginalFilename: cleanFilename(filename),
			Materials: st.model.Materials, Variants: st.model.Variants,
		})
		return err
	})
	if err != nil {
		s.Discard(ctx, st)
		return Media{}, err
	}
	if oldKey != "" {
		s.removeFiles(ctx, oldKey)
	}
	return out, nil
}

func (s *Service) DeleteModel(ctx context.Context, actor company.Member, productID string) error {
	var key string
	err := s.productTx(ctx, actor, productID, func(tx pgx.Tx, _, _ string) error {
		pm, err := listMedia(ctx, tx, productID)
		if err != nil {
			return err
		}
		if pm.Model == nil {
			return ErrMediaNotFound
		}
		m, err := deleteMedia(ctx, tx, productID, pm.Model.ID, MediaModel)
		key = m.StorageKey
		return err
	})
	if err != nil {
		return err
	}
	s.removeFiles(ctx, key)
	return nil
}

type ImageUpdate struct {
	AltText   *string
	Position  *int
	IsPrimary *bool
}

func (s *Service) UpdateImage(ctx context.Context, actor company.Member, productID, mediaID string, upd ImageUpdate) (Media, error) {
	set := map[string]any{}
	p := auth.Problems{}
	if upd.AltText != nil {
		a := strings.TrimSpace(*upd.AltText)
		p.Add("alt_text", validateAltText(a))
		set["alt_text"] = a
	}
	if upd.Position != nil {
		p.Add("position", validatePosition(*upd.Position))
		set["position"] = *upd.Position
	}
	if upd.IsPrimary != nil && !*upd.IsPrimary {
		p.Add("is_primary", "can only be set to true; make another image primary instead")
	}
	if len(set) == 0 && upd.IsPrimary == nil {
		p.Add("body", "provide alt_text, position and/or is_primary")
	}
	if err := p.Err(); err != nil {
		return Media{}, err
	}

	var out Media
	err := s.productTx(ctx, actor, productID, func(tx pgx.Tx, _, _ string) error {
		if err := updateMediaFields(ctx, tx, productID, mediaID, set); err != nil {
			return err
		}
		if upd.IsPrimary != nil {
			if err := setPrimary(ctx, tx, productID, mediaID); err != nil {
				return err
			}
		}
		pm, err := listMedia(ctx, tx, productID)
		if err != nil {
			return err
		}
		for _, im := range pm.Images {
			if im.ID == mediaID {
				out = im
				return nil
			}
		}
		return ErrMediaNotFound
	})
	return out, err
}

// DeleteImage removes an image; if it was primary, the next image (by
// position) takes over.
func (s *Service) DeleteImage(ctx context.Context, actor company.Member, productID, mediaID string) error {
	var key string
	err := s.productTx(ctx, actor, productID, func(tx pgx.Tx, _, _ string) error {
		m, err := deleteMedia(ctx, tx, productID, mediaID, MediaImage)
		if err != nil {
			return err
		}
		key = m.StorageKey
		if m.IsPrimary {
			return promoteFirstImage(ctx, tx, productID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.removeFiles(ctx, key)
	return nil
}

// Media returns a product's media and the material check for members.
func (s *Service) Media(ctx context.Context, actor company.Member, productID string) (ProductMedia, MaterialCheck, error) {
	if _, err := getCompanyProduct(ctx, s.pool, actor.CompanyID, productID); err != nil {
		return ProductMedia{}, MaterialCheck{}, err
	}
	pm, err := listMedia(ctx, s.pool, productID)
	if err != nil {
		return ProductMedia{}, MaterialCheck{}, err
	}
	cfg, err := loadConfiguration(ctx, s.pool, productID)
	if err != nil {
		return ProductMedia{}, MaterialCheck{}, err
	}
	return pm, materialCheck(cfg, pm.Model), nil
}

// OpenPublicFile opens a media file visible in the public catalog.
func (s *Service) OpenPublicFile(ctx context.Context, mediaID string) (Media, storage.File, error) {
	m, err := getPublicMedia(ctx, s.pool, mediaID)
	if err != nil {
		return Media{}, nil, err
	}
	return s.open(ctx, m)
}

// OpenCompanyFile opens any media file of the actor's company.
func (s *Service) OpenCompanyFile(ctx context.Context, actor company.Member, mediaID string) (Media, storage.File, error) {
	m, err := getCompanyMedia(ctx, s.pool, actor.CompanyID, mediaID)
	if err != nil {
		return Media{}, nil, err
	}
	return s.open(ctx, m)
}

func (s *Service) open(ctx context.Context, m Media) (Media, storage.File, error) {
	f, err := s.files.Open(ctx, m.StorageKey)
	if errors.Is(err, storage.ErrNotFound) {
		// Row without file: report as missing rather than a server error,
		// and log it, because it means storage and database disagree.
		s.log.ErrorContext(ctx, "media row has no stored file", slog.String("media_id", m.ID), slog.String("key", m.StorageKey))
		return Media{}, nil, ErrMediaNotFound
	}
	if err != nil {
		return Media{}, nil, err
	}
	return m, f, nil
}

// publicMedia returns a published product's media (caller has already
// established visibility via PublicGet).
func (s *Service) publicMedia(ctx context.Context, productID string) (ProductMedia, error) {
	return listMedia(ctx, s.pool, productID)
}
