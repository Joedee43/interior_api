package company_test

// Phase 3 security tests: company isolation, role boundaries, membership
// lifecycle and platform-admin access, against the real database.

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Joedee43/interior_api/internal/apitest"
	"github.com/Joedee43/interior_api/internal/auth"
	"github.com/Joedee43/interior_api/internal/company"
	"github.com/Joedee43/interior_api/internal/httpapi"
)

func companyPath(id string) string { return "/v1/companies/" + id }

func membersPath(id string) string { return companyPath(id) + "/members" }

func memberPath(companyID, userID string) string { return membersPath(companyID) + "/" + userID }

func newMemberBody(email, role string) string {
	return `{"email":"` + email + `","full_name":"New Person","password":"` + apitest.Password + `","role":"` + role + `"}`
}

// route is one company-scoped endpoint, used to sweep every endpoint in
// the isolation tests so a newly added route cannot be forgotten.
type route struct {
	method, path, body string
}

func companyRoutes(companyID, someUserID string) []route {
	return []route{
		{http.MethodGet, companyPath(companyID), ""},
		{http.MethodPatch, companyPath(companyID), `{"name":"Hijacked"}`},
		{http.MethodGet, membersPath(companyID), ""},
		{http.MethodPost, membersPath(companyID), newMemberBody("intruder@example.com", company.RoleAdmin)},
		{http.MethodPatch, memberPath(companyID, someUserID), `{"role":"company_staff"}`},
	}
}

func TestUnauthenticatedIsRejected(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()

	for _, r := range companyRoutes(co.ID, co.AdminID) {
		w := e.Do(r.method, r.path, r.body)
		if w.Code != http.StatusUnauthorized || apitest.ErrorCode(t, w) != httpapi.CodeUnauthenticated {
			t.Errorf("%s %s without session: %d %s", r.method, r.path, w.Code, w.Body)
		}
	}
}

func TestCrossCompanyIsolation(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	b := e.RegisterCompany()

	// A's admin attacks every one of B's endpoints by swapping the ID.
	for _, r := range companyRoutes(b.ID, b.AdminID) {
		w := e.As(a.AdminCookie, r.method, r.path, r.body)
		if w.Code != http.StatusNotFound || apitest.ErrorCode(t, w) != httpapi.CodeNotFound {
			t.Errorf("A -> %s %s: %d %s, want 404", r.method, r.path, w.Code, w.Body)
		}
	}

	// Through A's own company path, A must not reach B's member either.
	w := e.As(a.AdminCookie, http.MethodPatch, memberPath(a.ID, b.AdminID), `{"status":"disabled"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("A patching B's admin via A's path: %d %s, want 404", w.Code, w.Body)
	}

	// Nothing about B changed.
	if n := e.Count(`SELECT count(*) FROM companies WHERE id = $1 AND name = 'Acme Furniture'`, b.ID); n != 1 {
		t.Error("company B was renamed")
	}
	if n := e.Count(`SELECT count(*) FROM company_members WHERE company_id = $1`, b.ID); n != 1 {
		t.Errorf("company B has %d members, want 1", n)
	}
	if n := e.Count(`SELECT count(*) FROM company_members WHERE company_id = $1 AND user_id = $2 AND role = 'company_admin' AND status = 'active'`, b.ID, b.AdminID); n != 1 {
		t.Error("company B's admin was modified")
	}
	if n := e.Count(`SELECT count(*) FROM users WHERE email = 'intruder@example.com'`); n != 0 {
		t.Error("A created a user through B's endpoint")
	}

	// B's members list never shows A's people, and vice versa.
	w = e.As(b.AdminCookie, http.MethodGet, membersPath(b.ID), "")
	if strings.Contains(w.Body.String(), a.AdminEmail) {
		t.Errorf("B's member list contains A's admin: %s", w.Body)
	}
}

func TestStaffBoundaries(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	staff := e.AddMember(co, company.RoleStaff)

	allowed := []route{
		{http.MethodGet, companyPath(co.ID), ""},
		{http.MethodGet, membersPath(co.ID), ""},
	}
	for _, r := range allowed {
		if w := e.As(staff.Cookie, r.method, r.path, r.body); w.Code != http.StatusOK {
			t.Errorf("staff %s %s: %d %s, want 200", r.method, r.path, w.Code, w.Body)
		}
	}

	denied := []route{
		{http.MethodPatch, companyPath(co.ID), `{"name":"Staff Rename"}`},
		{http.MethodPost, membersPath(co.ID), newMemberBody("staff-made@example.com", company.RoleAdmin)},
		{http.MethodPatch, memberPath(co.ID, staff.ID), `{"role":"company_admin"}`}, // self-promotion
		{http.MethodPatch, memberPath(co.ID, co.AdminID), `{"status":"disabled"}`},
	}
	for _, r := range denied {
		w := e.As(staff.Cookie, r.method, r.path, r.body)
		if w.Code != http.StatusForbidden || apitest.ErrorCode(t, w) != httpapi.CodeForbidden {
			t.Errorf("staff %s %s: %d %s, want 403", r.method, r.path, w.Code, w.Body)
		}
	}
	if n := e.Count(`SELECT count(*) FROM company_members WHERE user_id = $1 AND role = 'company_staff'`, staff.ID); n != 1 {
		t.Error("staff role changed despite 403")
	}
}

func TestAdminManagesMembers(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	staff := e.AddMember(co, company.RoleStaff)

	// The new member sees the company on /auth/me with their role.
	w := e.As(staff.Cookie, http.MethodGet, "/v1/auth/me", "")
	var me auth.AccountResponse
	apitest.Decode(t, w, &me)
	if len(me.Memberships) != 1 || me.Memberships[0].Role != company.RoleStaff || me.Memberships[0].Company.ID != co.ID {
		t.Errorf("staff /me: %s", w.Body)
	}

	// An email that already exists is rejected, not silently attached.
	w = e.As(co.AdminCookie, http.MethodPost, membersPath(co.ID), newMemberBody(co.AdminEmail, company.RoleStaff))
	if w.Code != http.StatusConflict || apitest.ErrorCode(t, w) != "email_taken" {
		t.Errorf("duplicate member email: %d %s", w.Code, w.Body)
	}

	// Validation covers both user fields and role.
	w = e.As(co.AdminCookie, http.MethodPost, membersPath(co.ID), `{"email":"x","full_name":"","password":"short","role":"owner"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid member: %d %s", w.Code, w.Body)
	}
	for _, f := range []string{"email", "full_name", "password", "role"} {
		if !strings.Contains(w.Body.String(), `"`+f+`"`) {
			t.Errorf("validation details missing %s: %s", f, w.Body)
		}
	}

	// Disabling a member cuts off company access immediately, while their
	// login session itself stays valid.
	if w := e.As(co.AdminCookie, http.MethodPatch, memberPath(co.ID, staff.ID), `{"status":"disabled"}`); w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	if w := e.As(staff.Cookie, http.MethodGet, companyPath(co.ID), ""); w.Code != http.StatusNotFound {
		t.Errorf("disabled member reading company: %d, want 404", w.Code)
	}
	if w := e.As(staff.Cookie, http.MethodGet, "/v1/auth/me", ""); w.Code != http.StatusOK {
		t.Errorf("disabled member's session should still authenticate: %d", w.Code)
	}

	// Re-enable and promote.
	w = e.As(co.AdminCookie, http.MethodPatch, memberPath(co.ID, staff.ID), `{"status":"active","role":"company_admin"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"role":"company_admin"`) {
		t.Fatalf("re-enable + promote: %d %s", w.Code, w.Body)
	}
	if w := e.As(staff.Cookie, http.MethodPatch, companyPath(co.ID), `{"name":"Promoted Rename"}`); w.Code != http.StatusOK {
		t.Errorf("promoted member renaming: %d %s", w.Code, w.Body)
	}

	// Unknown member in own company.
	if w := e.As(co.AdminCookie, http.MethodPatch, memberPath(co.ID, "00000000-0000-0000-0000-000000000000"), `{"role":"company_staff"}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown member: %d", w.Code)
	}
	// Empty PATCH.
	if w := e.As(co.AdminCookie, http.MethodPatch, memberPath(co.ID, staff.ID), `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty patch: %d %s", w.Code, w.Body)
	}
}

func TestLastAdminProtection(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()

	for _, body := range []string{`{"role":"company_staff"}`, `{"status":"disabled"}`} {
		w := e.As(co.AdminCookie, http.MethodPatch, memberPath(co.ID, co.AdminID), body)
		if w.Code != http.StatusConflict || apitest.ErrorCode(t, w) != "last_admin" {
			t.Errorf("sole admin %s: %d %s, want 409 last_admin", body, w.Code, w.Body)
		}
	}

	// With a second admin, the first may step down.
	e.AddMember(co, company.RoleAdmin)
	if w := e.As(co.AdminCookie, http.MethodPatch, memberPath(co.ID, co.AdminID), `{"role":"company_staff"}`); w.Code != http.StatusOK {
		t.Errorf("stepping down with another admin present: %d %s", w.Code, w.Body)
	}
}

// Two admins demote each other at the same instant. Without the company
// lock and the re-check of the actor's role inside it, both could succeed
// and leave the company with no admin.
func TestConcurrentMutualDemotion(t *testing.T) {
	e := apitest.New(t, apitest.Options{})

	for round := 0; round < 5; round++ {
		co := e.RegisterCompany()
		other := e.AddMember(co, company.RoleAdmin)

		var wg sync.WaitGroup
		codes := make([]int, 2)
		requests := []struct {
			cookie *http.Cookie
			target string
		}{
			{co.AdminCookie, other.ID},
			{other.Cookie, co.AdminID},
		}
		for i, r := range requests {
			wg.Add(1)
			// i and r are per-iteration variables (Go 1.22+), so each
			// goroutine sees its own values.
			go func() {
				defer wg.Done()
				w := e.As(r.cookie, http.MethodPatch, memberPath(co.ID, r.target), `{"role":"company_staff"}`)
				codes[i] = w.Code // each goroutine writes only its own index: no data race
			}()
		}
		wg.Wait()

		ok := 0
		for _, c := range codes {
			switch c {
			case http.StatusOK:
				ok++
			case http.StatusForbidden, http.StatusConflict:
			default:
				t.Errorf("round %d: unexpected status %d", round, c)
			}
		}
		if ok != 1 {
			t.Errorf("round %d: %d demotions succeeded (codes %v), want exactly 1", round, ok, codes)
		}
		if n := e.Count(`SELECT count(*) FROM company_members WHERE company_id = $1 AND role = 'company_admin' AND status = 'active'`, co.ID); n != 1 {
			t.Errorf("round %d: %d active admins remain, want 1", round, n)
		}
	}
}

func TestSuspendedCompany(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	e.Exec(`UPDATE companies SET status = 'suspended' WHERE id = $1`, co.ID)

	if w := e.As(co.AdminCookie, http.MethodGet, companyPath(co.ID), ""); w.Code != http.StatusNotFound {
		t.Errorf("suspended company readable: %d", w.Code)
	}
	w := e.As(co.AdminCookie, http.MethodGet, "/v1/auth/me", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"suspended"`) {
		t.Errorf("/me should still list the suspended company: %d %s", w.Code, w.Body)
	}
}

func TestInvalidAndUnknownIDs(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()

	for _, path := range []string{
		companyPath("not-a-uuid"),
		companyPath("00000000-0000-0000-0000-000000000000"),
		companyPath(co.ID + "x"),
		membersPath(url.PathEscape("'; DROP TABLE companies; --")), // escaped as a real client would send it
	} {
		if w := e.As(co.AdminCookie, http.MethodGet, path, ""); w.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d %s, want 404", path, w.Code, w.Body)
		}
	}
	if w := e.As(co.AdminCookie, http.MethodPatch, memberPath(co.ID, "nope"), `{"role":"company_staff"}`); w.Code != http.StatusNotFound {
		t.Errorf("malformed member id: %d", w.Code)
	}
}

func TestRevokedSessionLosesAccess(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	if w := e.As(co.AdminCookie, http.MethodPost, "/v1/auth/logout", ""); w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := e.As(co.AdminCookie, http.MethodGet, companyPath(co.ID), ""); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked session: %d, want 401", w.Code)
	}
}

func TestRenameCompany(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()

	w := e.As(co.AdminCookie, http.MethodPatch, companyPath(co.ID), `{"name":"  Acme Office Supply  "}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"Acme Office Supply"`) || !strings.Contains(w.Body.String(), `"slug":"`+co.Slug+`"`) {
		t.Errorf("rename: %d %s", w.Code, w.Body)
	}
	if w := e.As(co.AdminCookie, http.MethodPatch, companyPath(co.ID), `{"name":"x"}`); w.Code != http.StatusBadRequest {
		t.Errorf("too-short name: %d", w.Code)
	}
	if w := e.As(co.AdminCookie, http.MethodPatch, companyPath(co.ID), `{"slug":"new-slug"}`); w.Code != http.StatusBadRequest {
		t.Errorf("slug must not be patchable: %d %s", w.Code, w.Body)
	}
}

func TestPlatformAdmin(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	a := e.RegisterCompany()
	b := e.RegisterCompany()

	if w := e.Do(http.MethodGet, "/v1/admin/companies", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous admin route: %d", w.Code)
	}
	if w := e.As(a.AdminCookie, http.MethodGet, "/v1/admin/companies", ""); w.Code != http.StatusForbidden {
		t.Errorf("company admin on platform route: %d, want 403", w.Code)
	}

	// Granting takes effect on the next request, no re-login.
	e.Exec(`UPDATE users SET is_platform_admin = true WHERE id = $1`, a.AdminID)
	w := e.As(a.AdminCookie, http.MethodGet, "/v1/admin/companies?limit=100", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), b.ID) {
		t.Errorf("platform admin list: %d (contains B: %v)", w.Code, strings.Contains(w.Body.String(), b.ID))
	}
	if w := e.As(a.AdminCookie, http.MethodGet, "/v1/admin/companies?limit=1000", ""); w.Code != http.StatusBadRequest {
		t.Errorf("limit over max: %d", w.Code)
	}

	// Platform admin does not imply access inside other companies.
	if w := e.As(a.AdminCookie, http.MethodGet, companyPath(b.ID), ""); w.Code != http.StatusNotFound {
		t.Errorf("platform admin reading company B: %d, want 404", w.Code)
	}

	// Revoking also applies immediately.
	e.Exec(`UPDATE users SET is_platform_admin = false WHERE id = $1`, a.AdminID)
	if w := e.As(a.AdminCookie, http.MethodGet, "/v1/admin/companies", ""); w.Code != http.StatusForbidden {
		t.Errorf("after revoke: %d, want 403", w.Code)
	}
}
