package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"arena/internal/store"
)

func newService(t *testing.T) *Service {
	t.Helper()
	st, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewService(st, []byte("test-secret-0123456789"), time.Hour, 24*time.Hour)
}

func TestRegisterLoginVerify(t *testing.T) {
	ctx := context.Background()
	s := newService(t)

	id, toks, err := s.Register(ctx, "alice", "hunter2pass")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if id.AccountID == 0 || id.Guest || id.Name != "alice" {
		t.Fatalf("bad identity: %+v", id)
	}
	if toks.Access == "" || toks.Refresh == "" || toks.ExpiresIn <= 0 {
		t.Fatalf("register tokens incomplete: %+v", toks)
	}

	got, err := s.Verify(toks.Access)
	if err != nil || got != id {
		t.Fatalf("verify: %+v err=%v", got, err)
	}

	// Логин верным паролем.
	if _, _, err := s.Login(ctx, "alice", "hunter2pass"); err != nil {
		t.Fatalf("login ok: %v", err)
	}
	// Неверный пароль и неизвестный пользователь — одинаковая ошибка.
	if _, _, err := s.Login(ctx, "alice", "wrongpass1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: want ErrInvalidCredentials, got %v", err)
	}
	if _, _, err := s.Login(ctx, "nobody", "whatever1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user: want ErrInvalidCredentials, got %v", err)
	}
	// Повторная регистрация — занято.
	if _, _, err := s.Register(ctx, "alice", "anotherpass"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("dup register: want ErrUsernameTaken, got %v", err)
	}
}

func TestGuest(t *testing.T) {
	s := newService(t)
	id, toks, err := s.Guest("  Bob  ")
	if err != nil {
		t.Fatalf("guest: %v", err)
	}
	if !id.Guest || id.AccountID != 0 || id.Name != "Bob" {
		t.Fatalf("bad guest identity: %+v", id)
	}
	// Гость эфемерен: refresh-токена нет (обновлять в БД нечего).
	if toks.Refresh != "" {
		t.Fatalf("guest should have no refresh token, got %q", toks.Refresh)
	}
	got, err := s.Verify(toks.Access)
	if err != nil || !got.Guest || got.Name != "Bob" {
		t.Fatalf("verify guest: %+v err=%v", got, err)
	}
}

func TestValidation(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	cases := []struct {
		name      string
		u, p, gnm string
		guest     bool
	}{
		{name: "short username", u: "ab", p: "longenough1"},
		{name: "bad username char", u: "bad name", p: "longenough1"},
		{name: "short password", u: "gooduser", p: "short"},
		{name: "empty guest name", guest: true, gnm: "   "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var err error
			if c.guest {
				_, _, err = s.Guest(c.gnm)
			} else {
				_, _, err = s.Register(ctx, c.u, c.p)
			}
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("want ErrValidation, got %v", err)
			}
		})
	}
}

func TestTokenTamperAndExpiry(t *testing.T) {
	s := newService(t)
	_, toks, err := s.Guest("x")
	if err != nil {
		t.Fatalf("guest: %v", err)
	}
	tok := toks.Access

	// Подделка подписи.
	if _, err := s.Verify(tok + "x"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("tampered token: want ErrBadToken, got %v", err)
	}
	// Чужой секрет не проверяет.
	other := NewService(s.store, []byte("different-secret-value"), time.Hour, 24*time.Hour)
	if _, err := other.Verify(tok); !errors.Is(err, ErrBadToken) {
		t.Fatalf("foreign secret: want ErrBadToken, got %v", err)
	}
	// Просроченный access-токен.
	past := NewService(s.store, s.secret, time.Hour, 24*time.Hour)
	past.clock = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	_, expiredToks, _ := past.Guest("y")
	if _, err := s.Verify(expiredToks.Access); !errors.Is(err, ErrBadToken) {
		t.Fatalf("expired token: want ErrBadToken, got %v", err)
	}
}

// TestRefreshRotation: refresh обменивается на СВЕЖУЮ пару, старый refresh перестаёт
// действовать (ротация), а новый access валиден.
func TestRefreshRotation(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	_, toks, err := s.Register(ctx, "alice", "hunter2pass")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	id2, toks2, err := s.Refresh(ctx, toks.Refresh)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if id2.Name != "alice" || id2.AccountID == 0 {
		t.Fatalf("bad refreshed identity: %+v", id2)
	}
	if toks2.Access == "" || toks2.Refresh == "" || toks2.Refresh == toks.Refresh {
		t.Fatalf("refresh must rotate to a new token: old=%q new=%q", toks.Refresh, toks2.Refresh)
	}
	if _, err := s.Verify(toks2.Access); err != nil {
		t.Fatalf("verify rotated access: %v", err)
	}
	// Новый refresh работает и дальше.
	if _, _, err := s.Refresh(ctx, toks2.Refresh); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
}

// TestRefreshReuseRevokesFamily: повторное предъявление уже ротированного refresh-токена
// гасит ВСЁ семейство — даже действующий на тот момент новый токен становится мёртв.
func TestRefreshReuseRevokesFamily(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	_, toks, err := s.Register(ctx, "alice", "hunter2pass")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	_, toks1, err := s.Refresh(ctx, toks.Refresh) // r0 → r1
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	// Повторное использование ротированного r0 — компрометация.
	if _, _, err := s.Refresh(ctx, toks.Refresh); !errors.Is(err, ErrBadToken) {
		t.Fatalf("reused token: want ErrBadToken, got %v", err)
	}
	// Семейство погашено — действующий r1 тоже больше не работает.
	if _, _, err := s.Refresh(ctx, toks1.Refresh); !errors.Is(err, ErrBadToken) {
		t.Fatalf("family should be revoked after reuse, got %v", err)
	}
}

// TestLogoutRevokesRefresh: logout гасит refresh-семейство; повторный/пустой/неизвестный
// logout идемпотентен.
func TestLogoutRevokesRefresh(t *testing.T) {
	ctx := context.Background()
	s := newService(t)
	_, toks, err := s.Register(ctx, "alice", "hunter2pass")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := s.Logout(ctx, toks.Refresh); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, _, err := s.Refresh(ctx, toks.Refresh); !errors.Is(err, ErrBadToken) {
		t.Fatalf("refresh after logout: want ErrBadToken, got %v", err)
	}
	if err := s.Logout(ctx, toks.Refresh); err != nil {
		t.Fatalf("repeat logout: %v", err)
	}
	if err := s.Logout(ctx, ""); err != nil {
		t.Fatalf("empty logout: %v", err)
	}
	if err := s.Logout(ctx, "unknown-token"); err != nil {
		t.Fatalf("unknown logout: %v", err)
	}
}

// TestRefreshInvalidAndExpired: пустой/мусорный refresh и refresh с истёкшим сроком — 401.
func TestRefreshInvalidAndExpired(t *testing.T) {
	ctx := context.Background()
	s := newService(t)

	if _, _, err := s.Refresh(ctx, ""); !errors.Is(err, ErrBadToken) {
		t.Fatalf("empty refresh: want ErrBadToken, got %v", err)
	}
	if _, _, err := s.Refresh(ctx, "garbage-token"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("garbage refresh: want ErrBadToken, got %v", err)
	}

	// Просроченный refresh: сервис с часами в прошлом и refreshTTL=1h выдаёт токен,
	// истёкший к «настоящему» времени; реальный сервис его отвергает.
	past := NewService(s.store, s.secret, time.Hour, time.Hour)
	past.clock = func() time.Time { return time.Now().Add(-3 * time.Hour) }
	_, ptoks, err := past.Register(ctx, "bob", "hunter2pass")
	if err != nil {
		t.Fatalf("past register: %v", err)
	}
	if _, _, err := s.Refresh(ctx, ptoks.Refresh); !errors.Is(err, ErrBadToken) {
		t.Fatalf("expired refresh: want ErrBadToken, got %v", err)
	}
}
