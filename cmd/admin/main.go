// Command admin performs operator tasks that must never be reachable over
// HTTP, such as granting platform-admin rights. Running it requires
// access to the server and the database URL, which is the authorization.
//
//	go run ./cmd/admin grant-platform-admin  -email you@example.com
//	go run ./cmd/admin revoke-platform-admin -email you@example.com
//	go run ./cmd/admin list-platform-admins
//
// Changes take effect on the user's next request: the flag is read from
// the users table on every authenticated request, so no re-login is needed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/Joedee43/interior_api/internal/auth"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: admin grant-platform-admin|revoke-platform-admin -email <email>")
	fmt.Fprintln(os.Stderr, "       admin list-platform-admins")
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("missing command")
	}
	cmd := args[0]

	// Each subcommand gets its own FlagSet, the standard library's way of
	// doing `tool <command> -flags` (like `go build -o`).
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	email := fs.String("email", "", "user email")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load .env: %w", err)
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	switch cmd {
	case "grant-platform-admin", "revoke-platform-admin":
		if *email == "" {
			return errors.New("-email is required")
		}
		grant := cmd == "grant-platform-admin"
		tag, err := pool.Exec(ctx,
			`UPDATE users SET is_platform_admin = $2 WHERE email = $1`,
			auth.NormalizeEmail(*email), grant)
		if err != nil {
			return fmt.Errorf("update user: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("no user with email %s", auth.NormalizeEmail(*email))
		}
		fmt.Printf("%s: is_platform_admin = %v\n", auth.NormalizeEmail(*email), grant)
		return nil

	case "list-platform-admins":
		rows, err := pool.Query(ctx, `SELECT email FROM users WHERE is_platform_admin ORDER BY email`)
		if err != nil {
			return fmt.Errorf("query: %w", err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var e string
			if err := rows.Scan(&e); err != nil {
				return err
			}
			fmt.Println(e)
			n++
		}
		if err := rows.Err(); err != nil { // errors during iteration surface here
			return err
		}
		if n == 0 {
			fmt.Println("(no platform admins)")
		}
		return nil

	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}
