// Command api is the composition root: it reads config, builds every
// dependency explicitly and wires them together. No package below this
// one creates its own database pool, logger or config.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/gin-gonic/gin"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/catalog"
	"github.com/Joedee43/interior_api/internal/company"
	"github.com/Joedee43/interior_api/internal/config"
	"github.com/Joedee43/interior_api/internal/design"
	"github.com/Joedee43/interior_api/internal/httpapi"
	"github.com/Joedee43/interior_api/internal/postgres"
	"github.com/Joedee43/interior_api/internal/quote"
	"github.com/Joedee43/interior_api/internal/storage"
)

func main() {
	// main only reports the error and sets the exit code. All real work
	// is in run, so its deferred cleanup (closing the pool) always runs —
	// os.Exit and log.Fatal skip deferred calls.
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg.Env)
	if cfg.Env == config.EnvProduction {
		gin.SetMode(gin.ReleaseMode)
	}

	// ctx is cancelled on Ctrl+C (SIGINT) or SIGTERM, which starts shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, postgres.DefaultPoolOptions())
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()
	log.Info("database connected")

	authSvc, err := auth.NewService(pool, auth.NewPasswordHasher(auth.DefaultArgon2Params), cfg.SessionTTL)
	if err != nil {
		return err
	}
	companySvc := company.NewService(pool, authSvc)

	cookies := auth.Cookies{Secure: cfg.CookieSecure(), MaxAge: cfg.SessionTTL}
	authHandler := auth.NewHandler(authSvc, companySvc, cookies, auth.DefaultLimits())
	companyHandler := company.NewHandler(companySvc, authHandler.RequireSession(), cookies, company.DefaultRegisterLimit())
	files, err := storage.NewLocalStore(cfg.StorageDir)
	if err != nil {
		return err
	}
	log.Info("media storage ready", slog.String("dir", cfg.StorageDir))
	catalogSvc := catalog.NewService(pool, files, log)
	catalogHandler := catalog.NewHandler(catalogSvc, authHandler.RequireSession(), companyHandler.RequireMember())
	// design depends on the catalog only through its Catalog interface.
	designSvc := design.NewService(pool, catalogSvc)
	designHandler := design.NewHandler(designSvc, authHandler.RequireSession())
	quoteHandler := quote.NewHandler(quote.NewService(pool, catalogSvc, designSvc, authSvc),
		authHandler.RequireSession(), companyHandler.RequireMember(), quote.DefaultSubmitLimit())

	router := httpapi.NewRouter(httpapi.Deps{
		Logger:         log,
		DB:             pool,
		AllowedOrigins: cfg.AllowedOrigins,
		V1Routes: []func(*gin.RouterGroup){
			authHandler.Mount, companyHandler.Mount, catalogHandler.Mount, designHandler.Mount, quoteHandler.Mount,
		},
	})

	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           router,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	// Bind the port before logging "listening", so a port already in use
	// is reported as a startup error instead of after a success message.
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", srv.Addr, err)
	}
	log.Info("http server listening", slog.String("addr", ln.Addr().String()), slog.String("env", string(cfg.Env)))

	// Serve blocks, so it runs in its own goroutine and reports back
	// through a buffered channel (buffered so the goroutine can always
	// send and exit, even if nobody is receiving any more).
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	select {
	case err := <-serveErr:
		// The server stopped on its own, without a shutdown signal.
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	// Stop accepting new connections and let in-flight requests finish,
	// but no longer than ShutdownTimeout. A fresh context is needed
	// because ctx is already cancelled.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}

	log.Info("server stopped cleanly")
	return nil // deferred pool.Close() runs now, after HTTP is fully drained
}

// newLogger uses human-readable text locally and JSON in production, where
// logs are usually shipped to a collector that parses them.
func newLogger(env config.Env) *slog.Logger {
	if env == config.EnvProduction {
		return slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
