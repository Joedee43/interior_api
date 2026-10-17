// Command migrate applies and reverts SQL migrations in ./migrations using
// goose. It is a separate binary from the API so schema changes are always
// an explicit step, never a side effect of starting the server.
//
//	go run ./cmd/migrate status
//	go run ./cmd/migrate up
//	go run ./cmd/migrate down          # reverts exactly one migration
//	go run ./cmd/migrate create add_users
//
// The database comes from DATABASE_URL (or .env); pass -test to use
// TEST_DATABASE_URL instead.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/joho/godotenv"
	"github.com/pressly/goose/v3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	dir := flag.String("dir", "migrations", "migrations directory")
	useTest := flag.Bool("test", false, "use TEST_DATABASE_URL instead of DATABASE_URL")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: migrate [-dir migrations] [-test] up|down|status|create <name>")
		flag.PrintDefaults()
	}
	flag.Parse()

	cmd := flag.Arg(0)
	if cmd == "create" {
		name := flag.Arg(1)
		if name == "" {
			return errors.New("create needs a name, e.g. create add_users")
		}
		// Sequential 00001_ numbering instead of timestamps: easier to read
		// in a small team, and goose rejects duplicate versions anyway.
		goose.SetSequential(true)
		return goose.Create(nil, *dir, name, "sql")
	}

	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load .env: %w", err)
	}
	envKey := "DATABASE_URL"
	if *useTest {
		envKey = "TEST_DATABASE_URL"
	}
	url := os.Getenv(envKey)
	if url == "" {
		return fmt.Errorf("%s is not set", envKey)
	}

	// goose works on database/sql, so here (and only here) we open pgx
	// through its database/sql adapter instead of pgxpool.
	db, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS(*dir))
	if err != nil {
		return fmt.Errorf("load migrations from %s: %w", *dir, err)
	}

	ctx := context.Background()
	switch cmd {
	case "up":
		results, err := provider.Up(ctx)
		printResults(results)
		if len(results) == 0 && err == nil {
			fmt.Println("already up to date")
		}
		return err
	case "down":
		result, err := provider.Down(ctx)
		if errors.Is(err, goose.ErrNoNextVersion) {
			fmt.Println("nothing to revert")
			return nil
		}
		if result != nil {
			printResults([]*goose.MigrationResult{result})
		}
		return err
	case "status":
		statuses, err := provider.Status(ctx)
		if err != nil {
			return err
		}
		for _, s := range statuses {
			applied := "pending"
			if s.State == goose.StateApplied {
				applied = "applied " + s.AppliedAt.Format("2006-01-02 15:04:05")
			}
			fmt.Printf("%-40s %s\n", s.Source.Path, applied)
		}
		return nil
	default:
		flag.Usage()
		return fmt.Errorf("unknown command %q", strings.TrimSpace(cmd))
	}
}

func printResults(results []*goose.MigrationResult) {
	for _, r := range results {
		fmt.Printf("%-5s %-40s %v\n", r.Direction, r.Source.Path, r.Duration.Round(1e6))
	}
}
