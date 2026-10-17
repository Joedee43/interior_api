package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Joedee43/interior_api/internal/postgres"
)

// touchInterval limits last_seen_at writes to one per session per interval
// instead of one per request.
const touchInterval = 5 * time.Minute

type Service struct {
	pool       *pgxpool.Pool
	hasher     PasswordHasher
	sessionTTL time.Duration
	// dummyHash is verified against when the email is unknown, so a login
	// for a missing account costs the same time as one for a real account
	// and response timing does not reveal which emails are registered.
	dummyHash string
}

func NewService(pool *pgxpool.Pool, hasher PasswordHasher, sessionTTL time.Duration) (*Service, error) {
	dummy, err := hasher.Hash("dummy password for timing equalization")
	if err != nil {
		return nil, fmt.Errorf("prepare dummy hash: %w", err)
	}
	return &Service{pool: pool, hasher: hasher, sessionTTL: sessionTTL, dummyHash: dummy}, nil
}

// SessionTTL is exposed so handlers can set a matching cookie Max-Age.
func (s *Service) SessionTTL() time.Duration { return s.sessionTTL }

// PreparedUser is a validated user with its password already hashed.
// Its fields are unexported, so the only way to get one is PrepareUser:
// code outside this package cannot insert a user with an unhashed or
// unvalidated password even by mistake.
type PreparedUser struct {
	email        string
	fullName     string
	passwordHash string
}

// PrepareUser normalizes, validates and hashes. It is separate from
// InsertUser because hashing is deliberately slow (~50 ms) and should
// happen before a caller opens a transaction, not while holding one.
func (s *Service) PrepareUser(in NewUserInput) (PreparedUser, error) {
	in.Normalize()
	p := Problems{}
	in.Validate(p)
	if err := p.Err(); err != nil {
		return PreparedUser{}, err
	}
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return PreparedUser{}, err
	}
	return PreparedUser{email: in.Email, fullName: in.FullName, passwordHash: hash}, nil
}

// InsertUser stores a prepared user using db, which may be a transaction
// owned by the caller. Returns ErrEmailTaken on a duplicate email.
func (s *Service) InsertUser(ctx context.Context, db postgres.DBTX, u PreparedUser) (User, error) {
	return insertUser(ctx, db, u.email, u.passwordHash, u.fullName)
}

// StartSession creates a session for userID using db and returns the raw
// token for the cookie.
func (s *Service) StartSession(ctx context.Context, db postgres.DBTX, userID string, meta SessionMeta) (string, error) {
	token, err := newSessionToken()
	if err != nil {
		return "", err
	}
	if err := insertSession(ctx, db, userID, hashToken(token), s.sessionTTL, meta); err != nil {
		return "", err
	}
	return token, nil
}

// Login checks credentials and starts a new session. If the client sent
// an existing session token, that session is revoked: every login gets a
// fresh token (prevents session fixation).
func (s *Service) Login(ctx context.Context, email, password, previousToken string, meta SessionMeta) (User, string, error) {
	email = NormalizeEmail(email)
	if email == "" || password == "" {
		return User{}, "", ErrInvalidCredentials
	}

	creds, found, err := findCredentials(ctx, s.pool, email)
	if err != nil {
		return User{}, "", err
	}
	if !found {
		_, _ = s.hasher.Verify(password, s.dummyHash) // same cost as a real check
		return User{}, "", ErrInvalidCredentials
	}
	ok, err := s.hasher.Verify(password, creds.PasswordHash)
	if err != nil {
		return User{}, "", fmt.Errorf("verify password for user %s: %w", creds.UserID, err)
	}
	// Status is checked after verifying, so a disabled account takes the
	// same time and gives the same answer as a wrong password.
	if !ok || creds.Status != UserActive {
		return User{}, "", ErrInvalidCredentials
	}

	var token string
	err = postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if previousToken != "" {
			if err := revokeSession(ctx, tx, hashToken(previousToken)); err != nil {
				return err
			}
		}
		var err error
		token, err = s.StartSession(ctx, tx, creds.UserID, meta)
		return err
	})
	if err != nil {
		return User{}, "", err
	}

	user, err := s.GetUser(ctx, creds.UserID)
	if err != nil {
		return User{}, "", err
	}
	return user, token, nil
}

// Logout revokes the session for token. Unknown or already-revoked tokens
// are not an error: logging out twice is fine.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return revokeSession(ctx, s.pool, hashToken(token))
}

// Authenticate resolves a raw session token to a Principal, or returns
// ErrUnauthenticated.
func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrUnauthenticated
	}
	p, lastSeen, err := sessionPrincipal(ctx, s.pool, hashToken(token))
	if err != nil {
		return Principal{}, err
	}
	if time.Since(lastSeen) > touchInterval {
		if err := touchSession(ctx, s.pool, p.SessionID); err != nil {
			return Principal{}, err
		}
	}
	return p, nil
}

func (s *Service) GetUser(ctx context.Context, userID string) (User, error) {
	return getUser(ctx, s.pool, userID)
}
