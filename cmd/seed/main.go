// Command seed replaces the documented demo data in a development database.
// It is deliberately separate from migrations: production schemas never gain
// sample accounts or files as a side effect of deployment.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/color"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/catalog"
	"github.com/Joedee43/interior_api/internal/company"
	"github.com/Joedee43/interior_api/internal/config"
	"github.com/Joedee43/interior_api/internal/design"
	"github.com/Joedee43/interior_api/internal/postgres"
	"github.com/Joedee43/interior_api/internal/storage"
)

const (
	seedCompanySlug = "demo-furniture"
	sellerEmail     = "demo@example.com"
	staffEmail      = "demo-staff@example.com"
	customerEmail   = "demo-customer@example.com"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	// Check the real variable before Load: production intentionally does not
	// load .env, and refusal must happen even when DATABASE_URL is absent.
	if config.Env(os.Getenv("APP_ENV")) == config.EnvProduction {
		return errors.New("seed refuses to run with APP_ENV=production")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := allowSeed(cfg.Env); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, postgres.DefaultPoolOptions())
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()
	files, err := storage.NewLocalStore(cfg.StorageDir)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	return seedData(ctx, pool, files, log, auth.NewPasswordHasher(auth.DefaultArgon2Params))
}

func allowSeed(env config.Env) error {
	if env == config.EnvProduction {
		return errors.New("seed refuses to run with APP_ENV=production")
	}
	return nil
}

func seedData(ctx context.Context, pool *pgxpool.Pool, files storage.Store, log *slog.Logger, hasher auth.PasswordHasher) error {
	oldKeys, err := resetDemoRows(ctx, pool)
	if err != nil {
		return fmt.Errorf("reset prior demo data: %w", err)
	}
	for _, key := range oldKeys {
		if err := files.Delete(context.WithoutCancel(ctx), key); err != nil {
			return fmt.Errorf("remove old demo file %s: %w", key, err)
		}
	}

	authSvc, err := auth.NewService(pool, hasher, 7*24*time.Hour)
	if err != nil {
		return err
	}
	companySvc := company.NewService(pool, authSvc)
	seller, memberships, _, err := companySvc.RegisterCompany(ctx, company.RegisterInput{
		CompanyName: "Demo Furniture",
		CompanySlug: seedCompanySlug,
		User: auth.NewUserInput{
			FullName: "Demo Admin",
			Email:    sellerEmail,
			Password: "demo password 123",
		},
	}, auth.SessionMeta{UserAgent: "cmd/seed", IP: "127.0.0.1"})
	if err != nil {
		return fmt.Errorf("create demo company: %w", err)
	}
	actor := company.Member{CompanyID: memberships[0].CompanyID, UserID: seller.ID, Role: company.RoleAdmin}
	if _, err := companySvc.CreateMember(ctx, actor, company.NewMemberInput{
		Role: company.RoleStaff,
		User: auth.NewUserInput{FullName: "Demo Staff", Email: staffEmail, Password: "staff password 123"},
	}); err != nil {
		return fmt.Errorf("create demo staff: %w", err)
	}

	customer, _, err := authSvc.RegisterCustomer(ctx, auth.NewUserInput{
		FullName: "Demo Customer",
		Email:    customerEmail,
		Password: "customer password 1",
	}, auth.SessionMeta{UserAgent: "cmd/seed", IP: "127.0.0.1"})
	if err != nil {
		return fmt.Errorf("create demo customer: %w", err)
	}

	catalogSvc := catalog.NewService(pool, files, log)
	chair, chairVariantID, err := seedChair(ctx, catalogSvc, actor)
	if err != nil {
		return err
	}
	if _, err := seedSofa(ctx, catalogSvc, actor); err != nil {
		return err
	}

	designSvc := design.NewService(pool, catalogSvc)
	d, err := designSvc.Create(ctx, customer.ID, design.CreateInput{Name: "My living room", Template: "living-room-5x6"})
	if err != nil {
		return fmt.Errorf("create demo design: %w", err)
	}
	x, z, rotation := int32(1600), int32(1800), 30.0
	if _, _, err := designSvc.AddItem(ctx, customer.ID, d.ID, d.Revision, design.ItemInput{
		ProductID: chair.ID, VariantID: &chairVariantID, XMM: &x, ZMM: &z, RotationDeg: &rotation,
	}); err != nil {
		return fmt.Errorf("place demo chair: %w", err)
	}

	log.Info("development seed ready",
		slog.String("company", seedCompanySlug),
		slog.String("seller", sellerEmail),
		slog.String("customer", customerEmail),
		slog.String("design_id", d.ID))
	return nil
}

func resetDemoRows(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	var keys []string
	err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT m.storage_key
			FROM product_media m
			JOIN products p ON p.id = m.product_id
			JOIN companies c ON c.id = p.company_id
			WHERE c.slug = $1`, seedCompanySlug)
		if err != nil {
			return err
		}
		keys, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		statements := []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM quote_requests WHERE company_id IN (SELECT id FROM companies WHERE slug = $1) OR customer_id IN (SELECT id FROM users WHERE email = ANY($2))`, []any{seedCompanySlug, []string{sellerEmail, staffEmail, customerEmail}}},
			{`DELETE FROM designs WHERE user_id IN (SELECT id FROM users WHERE email = ANY($1))`, []any{[]string{sellerEmail, staffEmail, customerEmail}}},
			{`DELETE FROM products WHERE company_id IN (SELECT id FROM companies WHERE slug = $1)`, []any{seedCompanySlug}},
			{`DELETE FROM company_members WHERE company_id IN (SELECT id FROM companies WHERE slug = $1) OR user_id IN (SELECT id FROM users WHERE email = ANY($2))`, []any{seedCompanySlug, []string{sellerEmail, staffEmail, customerEmail}}},
			{`DELETE FROM companies WHERE slug = $1`, []any{seedCompanySlug}},
			{`DELETE FROM users WHERE email = ANY($1)`, []any{[]string{sellerEmail, staffEmail, customerEmail}}},
		}
		for _, statement := range statements {
			if _, err := tx.Exec(ctx, statement.sql, statement.args...); err != nil {
				return err
			}
		}
		return nil
	})
	return keys, err
}

func seedChair(ctx context.Context, svc *catalog.Service, actor company.Member) (catalog.Product, string, error) {
	price, width, depth, height, weight := int64(18_500_00), int32(800), int32(800), int32(900), int32(12_000)
	p, err := svc.Create(ctx, actor, catalog.ProductInput{
		Category: "chairs", Name: "Addis Lounge Chair", Slug: "addis-lounge-chair",
		Description: "A configurable lounge chair used by the Next.js and Three.js development flows.",
		PriceMinor:  &price, WidthMM: &width, DepthMM: &depth, HeightMM: &height, WeightG: &weight,
	})
	if err != nil {
		return catalog.Product{}, "", fmt.Errorf("create demo chair: %w", err)
	}
	grey, blue := "#737A80", "#29528C"
	greyMaterial, blueMaterial := "fabric_grey", "fabric_blue"
	if _, err := svc.AddOption(ctx, actor, p.ID, catalog.OptionInput{Name: "Colour", Values: []catalog.ValueInput{
		{Name: "Grey", SwatchHex: &grey, MaterialKey: &greyMaterial},
		{Name: "Blue", SwatchHex: &blue, MaterialKey: &blueMaterial},
	}}); err != nil {
		return catalog.Product{}, "", fmt.Errorf("add chair option: %w", err)
	}
	greyPrice, bluePrice := price, int64(19_250_00)
	cfg, err := svc.CreateVariant(ctx, actor, p.ID, catalog.VariantInput{SKU: greySKU, PriceMinor: &greyPrice, Options: map[string]string{"Colour": "Grey"}})
	if err != nil {
		return catalog.Product{}, "", fmt.Errorf("create grey chair variant: %w", err)
	}
	if _, err := svc.CreateVariant(ctx, actor, p.ID, catalog.VariantInput{SKU: blueSKU, PriceMinor: &bluePrice, Options: map[string]string{"Colour": "Blue"}}); err != nil {
		return catalog.Product{}, "", fmt.Errorf("create blue chair variant: %w", err)
	}
	var greyVariantID string
	for _, variant := range cfg.Variants {
		if variant.SKU == greySKU {
			greyVariantID = variant.ID
		}
	}
	if greyVariantID == "" {
		return catalog.Product{}, "", errors.New("grey demo variant was not returned")
	}
	img, err := samplePNG(color.RGBA{R: 80, G: 90, B: 100, A: 255})
	if err != nil {
		return catalog.Product{}, "", err
	}
	if err := attachImage(ctx, svc, actor, p.ID, img, "Addis lounge chair"); err != nil {
		return catalog.Product{}, "", err
	}
	model, err := sampleGLB()
	if err != nil {
		return catalog.Product{}, "", err
	}
	staged, err := svc.Stage(ctx, catalog.MediaModel, bytes.NewReader(model))
	if err != nil {
		return catalog.Product{}, "", fmt.Errorf("stage demo GLB: %w", err)
	}
	if _, err := svc.ReplaceModel(ctx, actor, p.ID, staged, "addis-lounge-chair.glb"); err != nil {
		return catalog.Product{}, "", fmt.Errorf("attach demo GLB: %w", err)
	}
	p, err = svc.Transition(ctx, actor, p.ID, "publish")
	if err != nil {
		return catalog.Product{}, "", fmt.Errorf("publish demo chair: %w", err)
	}
	return p, greyVariantID, nil
}

func seedSofa(ctx context.Context, svc *catalog.Service, actor company.Member) (catalog.Product, error) {
	price, width, depth, height, weight := int64(48_500_00), int32(2100), int32(900), int32(820), int32(42_000)
	p, err := svc.Create(ctx, actor, catalog.ProductInput{
		Category: "sofas", Name: "Oslo 3-Seat Sofa", Slug: "oslo-3-seat-sofa",
		Description: "A clean three-seat sofa for catalog and room-layout development.",
		PriceMinor:  &price, WidthMM: &width, DepthMM: &depth, HeightMM: &height, WeightG: &weight,
	})
	if err != nil {
		return catalog.Product{}, fmt.Errorf("create demo sofa: %w", err)
	}
	img, err := samplePNG(color.RGBA{R: 150, G: 120, B: 90, A: 255})
	if err != nil {
		return catalog.Product{}, err
	}
	if err := attachImage(ctx, svc, actor, p.ID, img, "Oslo three-seat sofa"); err != nil {
		return catalog.Product{}, err
	}
	p, err = svc.Transition(ctx, actor, p.ID, "publish")
	if err != nil {
		return catalog.Product{}, fmt.Errorf("publish demo sofa: %w", err)
	}
	return p, nil
}

func attachImage(ctx context.Context, svc *catalog.Service, actor company.Member, productID string, data []byte, alt string) error {
	staged, err := svc.Stage(ctx, catalog.MediaImage, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("stage demo image: %w", err)
	}
	if _, err := svc.AttachImage(ctx, actor, productID, staged, alt, "demo.png"); err != nil {
		return fmt.Errorf("attach demo image: %w", err)
	}
	return nil
}
