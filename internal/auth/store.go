package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Joedee43/interior_api/internal/postgres"
)

// Store functions are unexported: only Service calls them, and Service is
// the package's public API. They take a postgres.DBTX so Service can run
// them inside a caller's transaction (see InsertUser, StartSession).

const userColumns = `id, email, full_name, status, is_platform_admin, created_at, updated_at`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.FullName, &u.Status, &u.IsPlatformAdmin, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

func insertUser(ctx context.Context, db postgres.DBTX, email, passwordHash, fullName string) (User, error) {
	u, err := scanUser(db.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, full_name)
		VALUES ($1, $2, $3)
		RETURNING `+userColumns,
		email, passwordHash, fullName,
	))
	if postgres.IsUniqueViolation(err, "users_email_key") {
		return User{}, ErrEmailTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

// credentials is what login needs; the hash never leaves this package.
type credentials struct {
	UserID       string
	PasswordHash string
	Status       string
}

// findCredentials returns ok=false (and no error) when the email is unknown.
func findCredentials(ctx context.Context, db postgres.DBTX, email string) (credentials, bool, error) {
	var c credentials
	err := db.QueryRow(ctx,
		`SELECT id, password_hash, status FROM users WHERE email = $1`, email,
	).Scan(&c.UserID, &c.PasswordHash, &c.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return credentials{}, false, nil
	}
	if err != nil {
		return credentials{}, false, fmt.Errorf("query credentials: %w", err)
	}
	return c, true, nil
}

func getUser(ctx context.Context, db postgres.DBTX, id string) (User, error) {
	u, err := scanUser(db.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

func insertSession(ctx context.Context, db postgres.DBTX, userID string, tokenHash []byte, ttl time.Duration, meta SessionMeta) error {
	// inet column: pass a parsed address, or NULL if it does not parse.
	var ip any
	if addr, err := netip.ParseAddr(meta.IP); err == nil {
		ip = addr
	}
	var userAgent any
	if meta.UserAgent != "" {
		ua := meta.UserAgent
		if len(ua) > 512 {
			ua = ua[:512]
		}
		userAgent = ua
	}
	_, err := db.Exec(ctx, `
		INSERT INTO sessions (user_id, token_hash, expires_at, user_agent, ip)
		VALUES ($1, $2, now() + $3::interval, $4, $5)`,
		userID, tokenHash, ttl, userAgent, ip,
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// sessionPrincipal resolves a token hash to its user, but only if the
// session is unexpired, unrevoked and the user is still active. All of
// that is checked in one query, using the database clock.
func sessionPrincipal(ctx context.Context, db postgres.DBTX, tokenHash []byte) (Principal, time.Time, error) {
	var p Principal
	var lastSeen time.Time
	err := db.QueryRow(ctx, `
		SELECT s.id, s.user_id, u.is_platform_admin, s.last_seen_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1
		  AND s.revoked_at IS NULL
		  AND s.expires_at > now()
		  AND u.status = 'active'`,
		tokenHash,
	).Scan(&p.SessionID, &p.UserID, &p.IsPlatformAdmin, &lastSeen)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, time.Time{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, time.Time{}, fmt.Errorf("query session: %w", err)
	}
	return p, lastSeen, nil
}

func touchSession(ctx context.Context, db postgres.DBTX, sessionID string) error {
	_, err := db.Exec(ctx, `UPDATE sessions SET last_seen_at = now() WHERE id = $1`, sessionID)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

func revokeSession(ctx context.Context, db postgres.DBTX, tokenHash []byte) error {
	_, err := db.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`,
		tokenHash,
	)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}
