package auth_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/Joedee43/interior_api/internal/apitest"
	"github.com/Joedee43/interior_api/internal/auth"
)

func TestChangePassword(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	otherDevice := e.Login(co.AdminEmail, apitest.Password)

	for body, field := range map[string]string{
		`{"current_password":"wrong password here","new_password":"brand new password"}`:            "current_password",
		`{"current_password":"` + apitest.Password + `","new_password":"short"}`:                    "new_password",
		`{"current_password":"` + apitest.Password + `","new_password":"` + apitest.Password + `"}`: "new_password",
	} {
		w := e.As(co.AdminCookie, http.MethodPost, "/v1/auth/password", body)
		if w.Code != http.StatusBadRequest || apitest.ErrorCode(t, w) != "validation_failed" {
			t.Errorf("%s: %d %s (want 400 on %s)", body, w.Code, w.Body, field)
		}
	}

	w := e.As(co.AdminCookie, http.MethodPost, "/v1/auth/password",
		`{"current_password":"`+apitest.Password+`","new_password":"brand new password"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("change password: %d %s", w.Code, w.Body)
	}
	fresh := apitest.SessionCookie(t, w)

	// Every earlier session is gone, on every device; the new one works.
	for name, c := range map[string]*http.Cookie{"this device (old)": co.AdminCookie, "other device": otherDevice} {
		if w := e.As(c, http.MethodGet, "/v1/auth/me", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s still signed in: %d", name, w.Code)
		}
	}
	if w := e.As(fresh, http.MethodGet, "/v1/auth/me", ""); w.Code != http.StatusOK {
		t.Errorf("new session: %d", w.Code)
	}
	if w := e.Do(http.MethodPost, "/v1/auth/login", `{"email":"`+co.AdminEmail+`","password":"`+apitest.Password+`"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("old password still works: %d", w.Code)
	}
	e.Login(co.AdminEmail, "brand new password")

	if w := e.Do(http.MethodPost, "/v1/auth/password", `{}`); w.Code != http.StatusUnauthorized {
		t.Errorf("anonymous password change: %d", w.Code)
	}
}

func TestLogoutAll(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	phone := e.Login(co.AdminEmail, apitest.Password)

	w := e.As(phone, http.MethodPost, "/v1/auth/logout-all", "")
	if w.Code != http.StatusNoContent || apitest.SessionCookie(t, w).MaxAge >= 0 {
		t.Fatalf("logout-all: %d", w.Code)
	}
	for _, c := range []*http.Cookie{phone, co.AdminCookie} {
		if w := e.As(c, http.MethodGet, "/v1/auth/me", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("session survived logout-all: %d", w.Code)
		}
	}
}

func TestDeleteDeadSessions(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	co := e.RegisterCompany()
	// Three extra sessions for this user, aged by hand.
	for i := 0; i < 3; i++ {
		e.Login(co.AdminEmail, apitest.Password)
	}
	e.Exec(`WITH s AS (SELECT id, row_number() OVER (ORDER BY created_at) AS n FROM sessions WHERE user_id = $1)
		UPDATE sessions SET
		  created_at   = CASE s.n WHEN 1 THEN now() - interval '60 days' ELSE sessions.created_at END,
		  expires_at   = CASE s.n WHEN 1 THEN now() - interval '40 days' ELSE sessions.expires_at END,
		  revoked_at   = CASE s.n WHEN 2 THEN now() - interval '40 days' WHEN 3 THEN now() - interval '1 day' ELSE sessions.revoked_at END
		FROM s WHERE sessions.id = s.id`, co.AdminID)

	before := e.Count(`SELECT count(*) FROM sessions WHERE user_id = $1`, co.AdminID)
	if _, err := auth.DeleteDeadSessions(context.Background(), e.Pool, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	after := e.Count(`SELECT count(*) FROM sessions WHERE user_id = $1`, co.AdminID)
	// Long-expired and long-revoked go; recently revoked and live stay.
	if before-after != 2 {
		t.Errorf("deleted %d of this user's sessions, want 2 (before %d, after %d)", before-after, before, after)
	}
}

func TestRunSessionCleanupStopsOnCancel(t *testing.T) {
	e := apitest.New(t, apitest.Options{})
	hasher := auth.NewPasswordHasher(auth.Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32})
	svc, err := auth.NewService(e.Pool, hasher, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.RunSessionCleanup(ctx, 10*time.Millisecond, 30*24*time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done) // closing a channel is how a goroutine announces "finished"
	}()
	time.Sleep(50 * time.Millisecond) // let it run a few ticks
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup goroutine did not stop after cancel (goroutine leak)")
	}
}
