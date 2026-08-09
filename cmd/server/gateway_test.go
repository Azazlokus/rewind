package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"arena/internal/account"
	"arena/internal/protocol"
	"arena/internal/store"
)

// newTestGateway строит шлюз с реальным сервисом аккаунтов поверх in-memory SQLite.
func newTestGateway(t *testing.T) (*gateway, *account.Service) {
	t.Helper()
	st, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := account.NewService(st, []byte("test-secret-0123456789"), time.Hour)
	g := &gateway{accounts: svc, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return g, svc
}

// TestResolveIdentity: валидный токен авторитетен (имя из токена, аккаунт привязан),
// пустой/битый токен деградирует до гостя с именем из Join.
func TestResolveIdentity(t *testing.T) {
	g, svc := newTestGateway(t)
	ctx := context.Background()

	id, regTok, err := svc.Register(ctx, "alice", "hunter2pass")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	_, guestTok, err := svc.Guest("Bob")
	if err != nil {
		t.Fatalf("guest: %v", err)
	}

	t.Run("registered token binds account and uses token name", func(t *testing.T) {
		// Имя в Join умышленно чужое — токен обязан победить (анти-имперсонация).
		name, acc := g.resolveIdentity(protocol.Join{Name: "impostor", Token: regTok})
		if name != "alice" || acc != id.AccountID {
			t.Fatalf("got (%q, %d), want (alice, %d)", name, acc, id.AccountID)
		}
	})

	t.Run("guest token keeps name, no account", func(t *testing.T) {
		name, acc := g.resolveIdentity(protocol.Join{Name: "ignored", Token: guestTok})
		if name != "Bob" || acc != 0 {
			t.Fatalf("got (%q, %d), want (Bob, 0)", name, acc)
		}
	})

	t.Run("no token → guest with join name", func(t *testing.T) {
		name, acc := g.resolveIdentity(protocol.Join{Name: "anon"})
		if name != "anon" || acc != 0 {
			t.Fatalf("got (%q, %d), want (anon, 0)", name, acc)
		}
	})

	t.Run("bad token → guest fallback", func(t *testing.T) {
		name, acc := g.resolveIdentity(protocol.Join{Name: "anon", Token: "garbage.sig"})
		if name != "anon" || acc != 0 {
			t.Fatalf("got (%q, %d), want (anon, 0)", name, acc)
		}
	})
}
