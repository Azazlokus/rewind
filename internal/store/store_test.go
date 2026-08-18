package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// newSQLite открывает свежий in-memory SQLite Store для теста.
func newSQLite(t *testing.T) Store {
	t.Helper()
	s, err := OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestStoreSQLite гоняет общий сьют на SQLite (всегда).
func TestStoreSQLite(t *testing.T) {
	runStoreSuite(t, func() Store { return newSQLite(t) })
}

// TestStorePostgres гоняет тот же сьют на PostgreSQL, если задан
// ARENA_TEST_POSTGRES_DSN (в CI — сервис postgres). Иначе — skip.
func TestStorePostgres(t *testing.T) {
	dsn := os.Getenv("ARENA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("ARENA_TEST_POSTGRES_DSN not set — skipping Postgres store suite")
	}
	runStoreSuite(t, func() Store {
		s, err := OpenPostgres(context.Background(), dsn)
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}
		// Чистим таблицы между подтестами: у Postgres БД общая, не in-memory.
		sq := s.(*sqlStore)
		if _, err := sq.db.ExecContext(context.Background(),
			`TRUNCATE refresh_tokens, match_participants, matches, stats, accounts RESTART IDENTITY CASCADE`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

// runStoreSuite — набор проверок контракта Store, общий для всех реализаций.
func runStoreSuite(t *testing.T, newStore func() Store) {
	ctx := context.Background()

	t.Run("account lifecycle", func(t *testing.T) {
		s := newStore()
		a, err := s.CreateAccount(ctx, "alice", "hash-a")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if a.ID == 0 || a.Username != "alice" {
			t.Fatalf("bad account: %+v", a)
		}

		got, hash, err := s.CredentialsByUsername(ctx, "alice")
		if err != nil {
			t.Fatalf("credentials: %v", err)
		}
		if got.ID != a.ID || hash != "hash-a" {
			t.Fatalf("credentials mismatch: %+v hash=%q", got, hash)
		}

		byID, err := s.AccountByID(ctx, a.ID)
		if err != nil || byID.Username != "alice" {
			t.Fatalf("by id: %+v err=%v", byID, err)
		}

		if _, err := s.CreateAccount(ctx, "alice", "hash-b"); !errors.Is(err, ErrUsernameTaken) {
			t.Fatalf("duplicate username: want ErrUsernameTaken, got %v", err)
		}
		if _, _, err := s.CredentialsByUsername(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown username: want ErrNotFound, got %v", err)
		}
		if _, err := s.AccountByID(ctx, 99999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown id: want ErrNotFound, got %v", err)
		}
	})

	t.Run("stats accumulate", func(t *testing.T) {
		s := newStore()
		a, _ := s.CreateAccount(ctx, "bob", "h")

		// Нет строки stats — нули, не ошибка.
		if st, err := s.Stats(ctx, a.ID); err != nil || st != (Stats{AccountID: a.ID}) {
			t.Fatalf("empty stats: %+v err=%v", st, err)
		}

		if err := s.AddStats(ctx, a.ID, StatsDelta{Kills: 2, Deaths: 1}); err != nil {
			t.Fatalf("add stats 1: %v", err)
		}
		if err := s.AddStats(ctx, a.ID, StatsDelta{Kills: 3, Games: 1}); err != nil {
			t.Fatalf("add stats 2: %v", err)
		}
		st, err := s.Stats(ctx, a.ID)
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		if st.Kills != 5 || st.Deaths != 1 || st.Games != 1 {
			t.Fatalf("accumulated wrong: %+v", st)
		}
	})

	t.Run("leaderboard order and limit", func(t *testing.T) {
		s := newStore()
		low, _ := s.CreateAccount(ctx, "low", "h")
		high, _ := s.CreateAccount(ctx, "high", "h")
		mid, _ := s.CreateAccount(ctx, "mid", "h")
		_ = s.AddStats(ctx, low.ID, StatsDelta{Kills: 1})
		_ = s.AddStats(ctx, high.ID, StatsDelta{Kills: 10})
		_ = s.AddStats(ctx, mid.ID, StatsDelta{Kills: 5})

		top, err := s.Leaderboard(ctx, 2)
		if err != nil {
			t.Fatalf("leaderboard: %v", err)
		}
		if len(top) != 2 {
			t.Fatalf("limit not applied: got %d", len(top))
		}
		if top[0].Username != "high" || top[1].Username != "mid" {
			t.Fatalf("wrong order: %v", []string{top[0].Username, top[1].Username})
		}
	})

	t.Run("record match and history", func(t *testing.T) {
		s := newStore()
		p1, _ := s.CreateAccount(ctx, "p1", "h")
		p2, _ := s.CreateAccount(ctx, "p2", "h")

		id, err := s.RecordMatch(ctx, MatchResult{
			Mode: "tdm", Seed: 42,
			Participants: []MatchParticipant{
				{AccountID: p1.ID, Kills: 5, Deaths: 2, Won: true},
				{AccountID: p2.ID, Kills: 2, Deaths: 5, Won: false},
				{AccountID: 0, Kills: 9, Deaths: 9}, // гость — игнорируется
			},
		})
		if err != nil || id == 0 {
			t.Fatalf("record match: id=%d err=%v", id, err)
		}

		hist, err := s.MatchesByAccount(ctx, p1.ID, 10)
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		if len(hist) != 1 || hist[0].Mode != "tdm" || hist[0].Kills != 5 || !hist[0].Won {
			t.Fatalf("bad history: %+v", hist)
		}

		// Матч кладёт в stats games/wins (kills/deaths копятся вживую отдельно).
		st, _ := s.Stats(ctx, p1.ID)
		if st.Games != 1 || st.Wins != 1 {
			t.Fatalf("match stats: %+v", st)
		}
		st2, _ := s.Stats(ctx, p2.ID)
		if st2.Games != 1 || st2.Wins != 0 {
			t.Fatalf("loser stats: %+v", st2)
		}
	})

	t.Run("refresh token lifecycle", func(t *testing.T) {
		s := newStore()
		a, _ := s.CreateAccount(ctx, "rt", "h")
		now := time.Now().UTC()

		// Создать и найти по хешу.
		if err := s.CreateRefreshToken(ctx, RefreshToken{
			AccountID: a.ID, FamilyID: "fam1", TokenHash: "hash1",
			IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatalf("create refresh: %v", err)
		}
		got, err := s.RefreshTokenByHash(ctx, "hash1")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if got.AccountID != a.ID || got.FamilyID != "fam1" || !got.RevokedAt.IsZero() {
			t.Fatalf("bad refresh row: %+v", got)
		}
		if _, err := s.RefreshTokenByHash(ctx, "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing hash: want ErrNotFound, got %v", err)
		}

		// Ротация: старый отзывается, новый активен.
		if err := s.RotateRefreshToken(ctx, got.ID, RefreshToken{
			AccountID: a.ID, FamilyID: "fam1", TokenHash: "hash2",
			IssuedAt: now.Add(time.Minute), ExpiresAt: now.Add(2 * time.Hour),
		}); err != nil {
			t.Fatalf("rotate: %v", err)
		}
		if old, _ := s.RefreshTokenByHash(ctx, "hash1"); old.RevokedAt.IsZero() {
			t.Fatalf("rotated old token still active")
		}
		if fresh, _ := s.RefreshTokenByHash(ctx, "hash2"); !fresh.RevokedAt.IsZero() {
			t.Fatalf("rotated new token should be active")
		}

		// Повторная ротация уже отозванного — ErrTokenRevoked, без вставки ребёнка.
		if err := s.RotateRefreshToken(ctx, got.ID, RefreshToken{
			AccountID: a.ID, FamilyID: "fam1", TokenHash: "hash3",
			IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		}); !errors.Is(err, ErrTokenRevoked) {
			t.Fatalf("re-rotate revoked: want ErrTokenRevoked, got %v", err)
		}
		if _, err := s.RefreshTokenByHash(ctx, "hash3"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("failed rotation must not insert child")
		}

		// Ротация чистит просроченные токены этого аккаунта.
		if err := s.CreateRefreshToken(ctx, RefreshToken{
			AccountID: a.ID, FamilyID: "fam2", TokenHash: "expired",
			IssuedAt: now.Add(-3 * time.Hour), ExpiresAt: now.Add(-time.Hour),
		}); err != nil {
			t.Fatalf("create expired: %v", err)
		}
		h2, _ := s.RefreshTokenByHash(ctx, "hash2")
		if err := s.RotateRefreshToken(ctx, h2.ID, RefreshToken{
			AccountID: a.ID, FamilyID: "fam1", TokenHash: "hash4",
			IssuedAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(3 * time.Hour),
		}); err != nil {
			t.Fatalf("rotate again: %v", err)
		}
		if _, err := s.RefreshTokenByHash(ctx, "expired"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("expired token should be pruned on rotation, got %v", err)
		}

		// Отзыв семейства гасит активные токены семейства.
		if err := s.RevokeRefreshFamily(ctx, "fam1"); err != nil {
			t.Fatalf("revoke family: %v", err)
		}
		if fresh, _ := s.RefreshTokenByHash(ctx, "hash4"); fresh.RevokedAt.IsZero() {
			t.Fatalf("family revoke left active token")
		}
	})
}
