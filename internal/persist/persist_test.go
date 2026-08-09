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
	acc, err := st.CreateAccount(context.Background(), name, "hash:"+name)
	if err != nil {
		t.Fatalf("create account %s: %v", name, err)
	}
	return acc.ID
}

// drain прогоняет persister до конца: шлёт msgs, закрывает канал, ждёт выхода Run.
func drain(t *testing.T, st store.Store, msgs []game.PersistMsg) {
	t.Helper()
	ch := make(chan game.PersistMsg, len(msgs)+1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		New(st, nil).Run(ch)
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
