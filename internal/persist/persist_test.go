package persist

import (
	"context"
	"testing"
	"time"

	"arena/internal/game"
	"arena/internal/store"
)

// newStore поднимает in-memory SQLite (миграции применяются на открытии).
func newStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func mkAccount(t *testing.T, st store.Store, name string) int64 {
	t.Helper()
	acc, err := st.CreateAccount(context.Background(), name, "hash:"+name, "")
	if err != nil {
		t.Fatalf("create account %s: %v", name, err)
	}
	return acc.ID
}

// drain прогоняет persister (автобан выключен) до конца.
func drain(t *testing.T, st store.Store, msgs []game.PersistMsg) {
	t.Helper()
	drainCfg(t, st, Config{}, msgs)
}

// drainCfg — то же, но с заданной конфигурацией (для проверки автобана).
func drainCfg(t *testing.T, st store.Store, cfg Config, msgs []game.PersistMsg) {
	t.Helper()
	ch := make(chan game.PersistMsg, len(msgs)+1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(st, nil, cfg).Run(ch)
	}()
	for _, m := range msgs {
		ch <- m
	}
	close(ch)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("persister did not drain")
	}
}

// TestPersisterKillsAndMatch: смерти копят kills/deaths вживую, итог матча — games/wins
// и историю. Суицид даёт только death, гашение окружением (killer 0) — death жертве.
// Гости (accountID 0) в статистику и историю не попадают.
func TestPersisterKillsAndMatch(t *testing.T) {
	st := newStore(t)
	alice := mkAccount(t, st, "alice")
	bob := mkAccount(t, st, "bob")

	now := time.Now()
	drain(t, st, []game.PersistMsg{
		{Kind: game.PersistKill, Killer: alice, Victim: bob},   // alice +kill, bob +death
		{Kind: game.PersistKill, Killer: alice, Victim: bob},   // alice +kill, bob +death
		{Kind: game.PersistKill, Killer: bob, Victim: alice},   // bob +kill, alice +death
		{Kind: game.PersistKill, Killer: alice, Victim: alice}, // суицид: только alice +death
		{Kind: game.PersistKill, Killer: 0, Victim: bob},       // окружение: только bob +death
		{Kind: game.PersistKill, Killer: 0, Victim: 0},         // оба гости: no-op
		{Kind: game.PersistMatch, Match: game.MatchResult{
			Mode: "ffa", Seed: 1, StartedAt: now.Add(-3 * time.Minute), EndedAt: now,
			Players: []game.MatchResultPlayer{
				{AccountID: alice, Kills: 2, Deaths: 2, Won: true},
				{AccountID: bob, Kills: 1, Deaths: 3, Won: false},
				{AccountID: 0, Kills: 9, Deaths: 0, Won: false}, // гость — отфильтровать
			},
		}},
	})

	// Статистика: kills/deaths — из смертей, games/wins — из матча.
	assertStats(t, st, alice, store.Stats{AccountID: alice, Kills: 2, Deaths: 2, Games: 1, Wins: 1})
	assertStats(t, st, bob, store.Stats{AccountID: bob, Kills: 1, Deaths: 3, Games: 1, Wins: 0})

	// История: по одному матчу у каждого; вклад — из результата матча, не из live-статы.
	am := matches(t, st, alice)
	if len(am) != 1 || !am[0].Won || am[0].Kills != 2 || am[0].Deaths != 2 || am[0].Mode != "ffa" {
		t.Fatalf("alice match history = %+v", am)
	}
	bm := matches(t, st, bob)
	if len(bm) != 1 || bm[0].Won || bm[0].Kills != 1 || bm[0].Deaths != 3 {
		t.Fatalf("bob match history = %+v", bm)
	}
}

// TestPersisterSkipsGuestOnlyMatch: матч без зарегистрированных участников не пишется
// (пустая строка истории бесполезна).
func TestPersisterSkipsGuestOnlyMatch(t *testing.T) {
	st := newStore(t)
	drain(t, st, []game.PersistMsg{
		{Kind: game.PersistMatch, Match: game.MatchResult{
			Mode: "ffa", EndedAt: time.Now(),
			Players: []game.MatchResultPlayer{{AccountID: 0, Kills: 3, Won: true}},
		}},
	})
	// Лидерборд пуст: никакой аккаунт не появился.
	lb, err := st.Leaderboard(context.Background(), 10)
	if err != nil {
		t.Fatalf("leaderboard: %v", err)
	}
	if len(lb) != 0 {
		t.Fatalf("guest-only match leaked into store: %+v", lb)
	}
}

func assertStats(t *testing.T, st store.Store, id int64, want store.Stats) {
	t.Helper()
	got, err := st.Stats(context.Background(), id)
	if err != nil {
		t.Fatalf("stats %d: %v", id, err)
	}
	if got != want {
		t.Fatalf("stats %d = %+v, want %+v", id, got, want)
	}
}

func matches(t *testing.T, st store.Store, id int64) []store.Match {
	t.Helper()
	m, err := st.MatchesByAccount(context.Background(), id, 10)
	if err != nil {
		t.Fatalf("matches %d: %v", id, err)
	}
	return m
}

// TestPersisterAntiCheat: события копятся по (аккаунт,вид); автобан выключен (порог 0) —
// бана нет; гости (id 0) игнорируются (итер. 40).
func TestPersisterAntiCheat(t *testing.T) {
	st := newStore(t)
	alice := mkAccount(t, st, "alice")

	drain(t, st, []game.PersistMsg{
		{Kind: game.PersistAntiCheat, AntiCheatAccount: alice, AntiCheatKind: "rewind_stale", AntiCheatCount: 1},
		{Kind: game.PersistAntiCheat, AntiCheatAccount: alice, AntiCheatKind: "rewind_stale", AntiCheatCount: 1},
		{Kind: game.PersistAntiCheat, AntiCheatAccount: alice, AntiCheatKind: "rewind_future", AntiCheatCount: 1},
		{Kind: game.PersistAntiCheat, AntiCheatAccount: 0, AntiCheatKind: "rewind_stale", AntiCheatCount: 1}, // гость — no-op
	})

	stats, err := st.AntiCheatByAccount(context.Background(), alice)
	if err != nil {
		t.Fatalf("anticheat by account: %v", err)
	}
	got := map[string]int64{}
	for _, s := range stats {
		got[s.Kind] = s.Count
	}
	if got["rewind_stale"] != 2 || got["rewind_future"] != 1 {
		t.Fatalf("anticheat counts = %v, want stale=2 future=1", got)
	}
	// Порог 0 → бана нет.
	if _, err := st.ActiveBan(context.Background(), alice, time.Now()); err != store.ErrNotFound {
		t.Fatalf("threshold 0 must not ban: err=%v", err)
	}
	// Топ-обзор видит аккаунт с суммой 3.
	top, err := st.TopAntiCheat(context.Background(), 10)
	if err != nil || len(top) != 1 || top[0].AccountID != alice || top[0].Total != 3 {
		t.Fatalf("top anticheat = %+v err=%v", top, err)
	}
}

// TestPersisterAntiCheatAutoBan: при пороге автобан срабатывает по превышению суммы,
// отзывает сессии, и не банит того, кто ниже порога (итер. 40).
func TestPersisterAntiCheatAutoBan(t *testing.T) {
	st := newStore(t)
	cheater := mkAccount(t, st, "cheater")
	clean := mkAccount(t, st, "clean")
	// Дадим cheater refresh-токен — автобан обязан его отозвать.
	if err := st.CreateRefreshToken(context.Background(), store.RefreshToken{
		AccountID: cheater, FamilyID: "f1", TokenHash: "rt-cheater",
		IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	drainCfg(t, st, Config{AntiCheatBanThreshold: 3, AntiCheatBanDuration: time.Hour}, []game.PersistMsg{
		{Kind: game.PersistAntiCheat, AntiCheatAccount: cheater, AntiCheatKind: "rewind_stale", AntiCheatCount: 1},
		{Kind: game.PersistAntiCheat, AntiCheatAccount: cheater, AntiCheatKind: "rewind_stale", AntiCheatCount: 1},
		{Kind: game.PersistAntiCheat, AntiCheatAccount: cheater, AntiCheatKind: "rewind_future", AntiCheatCount: 1}, // сумма 3 ≥ порог
		{Kind: game.PersistAntiCheat, AntiCheatAccount: clean, AntiCheatKind: "rewind_stale", AntiCheatCount: 1},    // 1 < порог
	})

	ban, err := st.ActiveBan(context.Background(), cheater, time.Now())
	if err != nil {
		t.Fatalf("cheater should be auto-banned: %v", err)
	}
	if ban.CreatedBy != 0 {
		t.Fatalf("auto-ban CreatedBy = %d, want 0 (system)", ban.CreatedBy)
	}
	// Сессии отозваны.
	if rt, _ := st.RefreshTokenByHash(context.Background(), "rt-cheater"); rt.RevokedAt.IsZero() {
		t.Fatalf("auto-ban must revoke refresh tokens")
	}
	// Ниже порога — без бана.
	if _, err := st.ActiveBan(context.Background(), clean, time.Now()); err != store.ErrNotFound {
		t.Fatalf("below-threshold account must not be banned: %v", err)
	}
}
