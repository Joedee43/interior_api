package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Joedee43/interior_api/internal/company"
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

// SessionTTL is exposed so the handler can set a matching cookie Max-Age.
func (s *Service) SessionTTL() time.Duration { return s.sessionTTL }

// RegisterCompany creates a user, an active company, the user's
// company_admin membership and a login session — all in one transaction,
// so a failure at any step (e.g. slug taken) leaves nothing behind.
// It returns the new account and the raw session token for the cookie.
func (s *Service) RegisterCompany(ctx context.Context, in RegisterCompanyInput, meta SessionMeta) (Account, string, error) {
	in.normalize()
	if err := in.validate(); err != nil {
		return Account{}, "", err
	}

	// Hash before opening the transaction: hashing is deliberately slow,
	// and a transaction should not hold a connection while we wait.
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return Account{}, "", err
	}
	token, err := newSessionToken()
	if err != nil {
		return Account{}, "", err
	}

	var acct Account
	err = postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		user, err := insertUser(ctx, tx, in.Email, hash, in.FullName)
		if err != nil {
			return err
		}
		co, err := company.Create(ctx, tx, in.CompanyName, in.CompanySlug, company.StatusActive)
		if err != nil {
			return err
		}
		m, err := company.AddMember(ctx, tx, co.ID, user.ID, company.RoleAdmin)
		if err != nil {
			return err
		}
		if err := insertSession(ctx, tx, user.ID, hashToken(token), s.sessionTTL, meta); err != nil {
			return err
		}
		acct = Account{
			User:        user,
			Memberships: []company.UserMembership{{Company: co, Role: m.Role, Status: m.Status}},
		}
		return nil
	})
	if err != nil {
		return Account{}, "", err
	}
	return acct, token, nil
}

// Login checks credentials and starts a new session. If the client sent
// an existing session token, that session is revoked: every login gets a
// fresh token (prevents session fixation).
func (s *Service) Login(ctx context.Context, email, password, previousToken string, meta SessionMeta) (Account, string, error) {
	email = NormalizeEmail(email)
	if email == "" || password == "" {
		return Account{}, "", ErrInvalidCredentials
	}

	creds, found, err := findCredentials(ctx, s.pool, email)
	if err != nil {
		return Account{}, "", err
	}
	if !found {
		_, _ = s.hasher.Verify(password, s.dummyHash) // same cost as a real check
		return Account{}, "", ErrInvalidCredentials
	}
	ok, err := s.hasher.Verify(password, creds.PasswordHash)
	if err != nil {
		return Account{}, "", fmt.Errorf("verify password for user %s: %w", creds.UserID, err)
	}
	// Status is checked after verifying, so a disabled account takes the
	// same time and gives the same answer as a wrong password.
	if !ok || creds.Status != UserActive {
		return Account{}, "", ErrInvalidCredentials
	}

	token, err := newSessionToken()
	if err != nil {
		return Account{}, "", err
	}
	err = postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if previousToken != "" {
			if err := revokeSession(ctx, tx, hashToken(previousToken)); err != nil {
				return err
			}
		}
		return insertSession(ctx, tx, creds.UserID, hashToken(token), s.sessionTTL, meta)
	})
	if err != nil {
		return Account{}, "", err
	}

	acct, err := s.Account(ctx, creds.UserID)
	if err != nil {
		return Account{}, "", err
	}
	return acct, token, nil
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

// Account loads a user and all their memberships.
func (s *Service) Account(ctx context.Context, userID string) (Account, error) {
	user, err := getUser(ctx, s.pool, userID)
	if err != nil {
		return Account{}, err
	}
	ms, err := company.ListForUser(ctx, s.pool, userID)
	if err != nil {
		return Account{}, err
	}
	return Account{User: user, Memberships: ms}, nil
}
