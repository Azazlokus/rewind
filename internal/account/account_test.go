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
	return NewService(st, []byte("test-secret-0123456789"), time.Hour)
}

func TestRegisterLoginVerify(t *testing.T) {
	ctx := context.Background()
	s := newService(t)

	id, tok, err := s.Register(ctx, "alice", "hunter2pass")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if id.AccountID == 0 || id.Guest || id.Name != "alice" {
		t.Fatalf("bad identity: %+v", id)
	}

	got, err := s.Verify(tok)
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
	id, tok, err := s.Guest("  Bob  ")
	if err != nil {
		t.Fatalf("guest: %v", err)
	}
	if !id.Guest || id.AccountID != 0 || id.Name != "Bob" {
		t.Fatalf("bad guest identity: %+v", id)
	}
	got, err := s.Verify(tok)
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
	_, tok, err := s.Guest("x")
	if err != nil {
		t.Fatalf("guest: %v", err)
	}

	// Подделка подписи.
	if _, err := s.Verify(tok + "x"); !errors.Is(err, ErrBadToken) {
		t.Fatalf("tampered token: want ErrBadToken, got %v", err)
	}
	// Чужой секрет не проверяет.
	other := NewService(s.store, []byte("different-secret-value"), time.Hour)
	if _, err := other.Verify(tok); !errors.Is(err, ErrBadToken) {
		t.Fatalf("foreign secret: want ErrBadToken, got %v", err)
	}
	// Просроченный токен.
	past := NewService(s.store, s.secret, time.Hour)
	past.clock = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	_, expiredTok, _ := past.Guest("y")
	if _, err := s.Verify(expiredTok); !errors.Is(err, ErrBadToken) {
		t.Fatalf("expired token: want ErrBadToken, got %v", err)
	}
}
