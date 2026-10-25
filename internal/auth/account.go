package auth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Joedee43/interior_api/internal/postgres"
)

// ChangePassword checks the current password, stores the new one and
// signs the user out everywhere else: every session is revoked and a new
// one is started for the caller (whose cookie is replaced).
func (s *Service) ChangePassword(ctx context.Context, userID, current, next string, meta SessionMeta) (string, error) {
	p := Problems{}
	p.Add("new_password", validatePassword(next))
	if current == next {
		p.Add("new_password", "must differ from the current password")
	}
	if err := p.Err(); err != nil {
		return "", err
	}
	hash, err := passwordHash(ctx, s.pool, userID)
	if err != nil {
		return "", err
	}
	ok, err := s.hasher.Verify(current, hash)
	if err != nil {
		return "", fmt.Errorf("verify password for user %s: %w", userID, err)
	}
	if !ok {
		return "", &ValidationError{Fields: map[string]string{"current_password": "is incorrect"}}
	}
	newHash, err := s.hasher.Hash(next) // slow: before the transaction
	if err != nil {
		return "", err
	}

	var token string
	err = postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := setPasswordHash(ctx, tx, userID, newHash); err != nil {
			return err
		}
		if _, err := revokeUserSessions(ctx, tx, userID); err != nil {
			return err
		}
		var err error
		token, err = s.StartSession(ctx, tx, userID, meta)
		return err
	})
	return token, err
}

// LogoutAll revokes every session of the user, including the caller's.
func (s *Service) LogoutAll(ctx context.Context, userID string) (int64, error) {
	return revokeUserSessions(ctx, s.pool, userID)
}

// DeleteDeadSessions removes sessions expired or revoked more than retain
// ago. It is a plain function (not a Service method) so `cmd/admin
// cleanup-sessions` can call it with just a pool.
func DeleteDeadSessions(ctx context.Context, db postgres.DBTX, retain time.Duration) (int64, error) {
	return deleteDeadSessions(ctx, db, retain)
}

func (s *Service) DeleteDeadSessions(ctx context.Context, retain time.Duration) (int64, error) {
	return DeleteDeadSessions(ctx, s.pool, retain)
}

// RunSessionCleanup deletes dead sessions once immediately and then every
// interval, until ctx is cancelled. It blocks: run it in its own goroutine
// and cancel ctx to stop it. A failed run is logged, never fatal.
//
//	go func() { defer wg.Done(); svc.RunSessionCleanup(ctx, time.Hour, 30*24*time.Hour, log) }()
func (s *Service) RunSessionCleanup(ctx context.Context, interval, retain time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop() // release the ticker's resources when we return

	for {
		n, err := s.DeleteDeadSessions(ctx, retain)
		switch {
		case err != nil && ctx.Err() == nil:
			log.WarnContext(ctx, "session cleanup failed", slog.Any("error", err))
		case n > 0:
			log.InfoContext(ctx, "deleted dead sessions", slog.Int64("count", n))
		}

		// select waits for whichever happens first: shutdown or next tick.
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
