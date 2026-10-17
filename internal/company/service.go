package company

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/postgres"
)

// MaxListLimit caps list endpoints until proper pagination arrives with
// the catalog (Phase 4).
const MaxListLimit = 100

type Service struct {
	pool  *pgxpool.Pool
	users *auth.Service
}

func NewService(pool *pgxpool.Pool, users *auth.Service) *Service {
	return &Service{pool: pool, users: users}
}

// Compile-time check that *Service satisfies the interface auth consumes.
var _ auth.MembershipLister = (*Service)(nil)

// ListMemberships implements auth.MembershipLister for /auth/me.
func (s *Service) ListMemberships(ctx context.Context, userID string) ([]auth.Membership, error) {
	return listMembershipsForUser(ctx, s.pool, userID)
}

// ---- registration -------------------------------------------------------

type RegisterInput struct {
	CompanyName string
	CompanySlug string
	User        auth.NewUserInput
}

// RegisterCompany creates a user, an active company, the user's
// company_admin membership and a login session in one transaction, so a
// failure at any step (e.g. slug taken) leaves nothing behind. It returns
// the raw session token for the cookie.
func (s *Service) RegisterCompany(ctx context.Context, in RegisterInput, meta auth.SessionMeta) (auth.User, []auth.Membership, string, error) {
	in.CompanyName = strings.TrimSpace(in.CompanyName)
	in.CompanySlug = NormalizeSlug(in.CompanySlug)
	in.User.Normalize()

	p := auth.Problems{}
	p.Add("company_name", ValidateName(in.CompanyName))
	p.Add("company_slug", ValidateSlug(in.CompanySlug))
	in.User.Validate(p)
	if err := p.Err(); err != nil {
		return auth.User{}, nil, "", err
	}

	// Slow password hashing happens before the transaction opens.
	prepared, err := s.users.PrepareUser(in.User)
	if err != nil {
		return auth.User{}, nil, "", err
	}

	var (
		user  auth.User
		co    Company
		token string
	)
	err = postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if user, err = s.users.InsertUser(ctx, tx, prepared); err != nil {
			return err
		}
		if co, err = createCompany(ctx, tx, in.CompanyName, in.CompanySlug, StatusActive); err != nil {
			return err
		}
		if err = addMember(ctx, tx, co.ID, user.ID, RoleAdmin); err != nil {
			return err
		}
		token, err = s.users.StartSession(ctx, tx, user.ID, meta)
		return err
	})
	if err != nil {
		return auth.User{}, nil, "", err
	}
	return user, []auth.Membership{toAuthMembership(co, RoleAdmin, MemberActive)}, token, nil
}

// ---- authorization ------------------------------------------------------

// Authorize resolves the user's active membership in an active company.
// It returns ErrNotFound for "not a member", "company suspended" and
// "no such company" alike. Role checks are separate (see RequireRole) so
// that a member lacking a role gets 403, while a non-member gets 404.
func (s *Service) Authorize(ctx context.Context, userID, companyID string) (Member, error) {
	return findActiveMember(ctx, s.pool, companyID, userID)
}

func requireAdmin(actor Member) error {
	if !actor.IsAdmin() {
		return ErrForbidden
	}
	return nil
}

// adminTx runs fn in a transaction that holds the company lock and has
// re-verified, under that lock, that actor is still an active admin. The
// middleware check happened before the lock; without re-checking, an admin
// demoted a moment ago by someone else could still complete a change.
func (s *Service) adminTx(ctx context.Context, actor Member, fn func(tx pgx.Tx) error) error {
	if err := requireAdmin(actor); err != nil {
		return err
	}
	return postgres.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockCompany(ctx, tx, actor.CompanyID); err != nil {
			return err
		}
		current, err := findActiveMember(ctx, tx, actor.CompanyID, actor.UserID)
		if err != nil {
			return err
		}
		if !current.IsAdmin() {
			return ErrForbidden
		}
		return fn(tx)
	})
}

// ---- company profile ----------------------------------------------------

func (s *Service) Get(ctx context.Context, actor Member) (Company, error) {
	return getCompany(ctx, s.pool, actor.CompanyID)
}

func (s *Service) Rename(ctx context.Context, actor Member, name string) (Company, error) {
	name = strings.TrimSpace(name)
	p := auth.Problems{}
	p.Add("name", ValidateName(name))
	if err := p.Err(); err != nil {
		return Company{}, err
	}
	var c Company
	err := s.adminTx(ctx, actor, func(tx pgx.Tx) error {
		var err error
		c, err = renameCompany(ctx, tx, actor.CompanyID, name)
		return err
	})
	return c, err
}

// ---- members --------------------------------------------------------------

func (s *Service) ListMembers(ctx context.Context, actor Member) ([]MemberInfo, error) {
	return listMembers(ctx, s.pool, actor.CompanyID)
}

type NewMemberInput struct {
	User auth.NewUserInput
	Role string
}

// CreateMember creates a brand-new user account and adds it to the
// actor's company. An email that already belongs to any user is rejected
// (auth.ErrEmailTaken): adding existing accounts needs their consent,
// which waits for email invitations.
func (s *Service) CreateMember(ctx context.Context, actor Member, in NewMemberInput) (MemberInfo, error) {
	if err := requireAdmin(actor); err != nil {
		return MemberInfo{}, err
	}
	in.User.Normalize()
	in.Role = strings.TrimSpace(in.Role)
	p := auth.Problems{}
	in.User.Validate(p)
	p.Add("role", validateRole(in.Role))
	if err := p.Err(); err != nil {
		return MemberInfo{}, err
	}
	prepared, err := s.users.PrepareUser(in.User)
	if err != nil {
		return MemberInfo{}, err
	}

	var mi MemberInfo
	err = s.adminTx(ctx, actor, func(tx pgx.Tx) error {
		user, err := s.users.InsertUser(ctx, tx, prepared)
		if err != nil {
			return err
		}
		if err := addMember(ctx, tx, actor.CompanyID, user.ID, in.Role); err != nil {
			return err
		}
		mi, err = getMemberInfo(ctx, tx, actor.CompanyID, user.ID)
		return err
	})
	return mi, err
}

// MemberUpdate fields are pointers: nil means "leave unchanged", which a
// plain string could not express (its zero value "" is ambiguous).
type MemberUpdate struct {
	Role   *string
	Status *string
}

func (s *Service) UpdateMember(ctx context.Context, actor Member, userID string, upd MemberUpdate) (MemberInfo, error) {
	if err := requireAdmin(actor); err != nil {
		return MemberInfo{}, err
	}
	p := auth.Problems{}
	if upd.Role == nil && upd.Status == nil {
		p.Add("role", "provide role and/or status")
	}
	if upd.Role != nil {
		p.Add("role", validateRole(*upd.Role))
	}
	if upd.Status != nil {
		p.Add("status", validateMemberStatus(*upd.Status))
	}
	if err := p.Err(); err != nil {
		return MemberInfo{}, err
	}

	var mi MemberInfo
	err := s.adminTx(ctx, actor, func(tx pgx.Tx) error {
		cur, err := getMemberState(ctx, tx, actor.CompanyID, userID)
		if err != nil {
			return err
		}
		next := cur
		if upd.Role != nil {
			next.Role = *upd.Role
		}
		if upd.Status != nil {
			next.Status = *upd.Status
		}

		wasActiveAdmin := cur.Role == RoleAdmin && cur.Status == MemberActive
		staysActiveAdmin := next.Role == RoleAdmin && next.Status == MemberActive
		if wasActiveAdmin && !staysActiveAdmin {
			// Safe to count without further locks: we hold the company
			// lock, so no other membership change in this company can run.
			n, err := countActiveAdmins(ctx, tx, actor.CompanyID)
			if err != nil {
				return err
			}
			if n <= 1 {
				return ErrLastAdmin
			}
		}

		if err := updateMember(ctx, tx, actor.CompanyID, userID, next.Role, next.Status); err != nil {
			return err
		}
		mi, err = getMemberInfo(ctx, tx, actor.CompanyID, userID)
		return err
	})
	return mi, err
}

// ---- platform admin -------------------------------------------------------

// ListAll returns companies across all tenants. It re-checks the platform
// admin flag itself rather than trusting that a middleware ran.
func (s *Service) ListAll(ctx context.Context, p auth.Principal, limit int) ([]Company, error) {
	if !p.IsPlatformAdmin {
		return nil, auth.ErrForbidden
	}
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	return listCompanies(ctx, s.pool, limit)
}
