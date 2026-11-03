package main

import (
	"bytes"
	"context"
	"image/color"
	"io"
	"log/slog"
	"testing"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/catalog"
	"github.com/Joedee43/interior_api/internal/config"
	"github.com/Joedee43/interior_api/internal/postgres/pgtest"
	"github.com/Joedee43/interior_api/internal/storage"
)

func TestProductionRefusal(t *testing.T) {
	if err := allowSeed(config.EnvProduction); err == nil {
		t.Fatal("production seed was allowed")
	}
	if err := allowSeed(config.EnvDevelopment); err != nil {
		t.Fatalf("development seed was refused: %v", err)
	}
}

func TestGeneratedAssetsPassUploadInspection(t *testing.T) {
	files, err := storage.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := catalog.NewService(nil, files, log)

	glb, err := sampleGLB()
	if err != nil {
		t.Fatal(err)
	}
	staged, err := svc.Stage(context.Background(), catalog.MediaModel, bytes.NewReader(glb))
	if err != nil {
		t.Fatalf("sample GLB rejected: %v", err)
	}
	svc.Discard(context.Background(), staged)

	image, err := samplePNG(color.RGBA{R: 80, G: 90, B: 100, A: 255})
	if err != nil {
		t.Fatal(err)
	}
	staged, err = svc.Stage(context.Background(), catalog.MediaImage, bytes.NewReader(image))
	if err != nil {
		t.Fatalf("sample PNG rejected: %v", err)
	}
	svc.Discard(context.Background(), staged)
}

func TestSeedIsRepeatable(t *testing.T) {
	pool := pgtest.New(t)
	files, err := storage.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hasher := auth.NewPasswordHasher(auth.Argon2Params{
		MemoryKiB: 1024, Iterations: 1, Parallelism: 1, SaltLen: 8, KeyLen: 16,
	})
	ctx := context.Background()
	t.Cleanup(func() {
		keys, cleanupErr := resetDemoRows(ctx, pool)
		if cleanupErr != nil {
			t.Errorf("cleanup demo rows: %v", cleanupErr)
		}
		for _, key := range keys {
			if cleanupErr := files.Delete(ctx, key); cleanupErr != nil {
				t.Errorf("cleanup demo file: %v", cleanupErr)
			}
		}
	})

	for i := 0; i < 2; i++ {
		if err := seedData(ctx, pool, files, log, hasher); err != nil {
			t.Fatalf("seed run %d: %v", i+1, err)
		}
	}

	var companies, users, products, variants, media, designs, items int
	err = pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM companies WHERE slug = $1),
			(SELECT count(*) FROM users WHERE email = ANY($2)),
			(SELECT count(*) FROM products p JOIN companies c ON c.id = p.company_id WHERE c.slug = $1),
			(SELECT count(*) FROM product_variants v JOIN companies c ON c.id = v.company_id WHERE c.slug = $1),
			(SELECT count(*) FROM product_media m JOIN companies c ON c.id = m.company_id WHERE c.slug = $1),
			(SELECT count(*) FROM designs d JOIN users u ON u.id = d.user_id WHERE u.email = $3),
			(SELECT count(*) FROM design_items i JOIN designs d ON d.id = i.design_id JOIN users u ON u.id = d.user_id WHERE u.email = $3)`,
		seedCompanySlug, []string{sellerEmail, staffEmail, customerEmail}, customerEmail,
	).Scan(&companies, &users, &products, &variants, &media, &designs, &items)
	if err != nil {
		t.Fatal(err)
	}
	if companies != 1 || users != 3 || products != 2 || variants != 2 || media != 3 || designs != 1 || items != 1 {
		t.Fatalf("unexpected seed counts: companies=%d users=%d products=%d variants=%d media=%d designs=%d items=%d",
			companies, users, products, variants, media, designs, items)
	}

	var materials, modelVariants []string
	err = pool.QueryRow(ctx, `
		SELECT m.model_materials, m.model_variants
		FROM product_media m
		JOIN products p ON p.id = m.product_id
		JOIN companies c ON c.id = p.company_id
		WHERE c.slug = $1 AND m.kind = 'model'`, seedCompanySlug).Scan(&materials, &modelVariants)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(materials, []string{"fabric_grey", "fabric_blue"}) || !sameStrings(modelVariants, []string{greySKU, blueSKU}) {
		t.Errorf("GLB contract metadata: materials=%v variants=%v", materials, modelVariants)
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
