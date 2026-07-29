package botfill

import (
	"context"
	"testing"
	"time"

	"arena/internal/bot"
	"arena/internal/game"
	"arena/internal/transport"
)

func TestTargetBots(t *testing.T) {
	const max = 8
	cases := []struct {
		name                         string
		players, currentBots, target int
		want                         int
	}{
		{"empty room stays empty", 0, 0, 4, 0},
		{"lone human filled to target", 1, 0, 4, 3},
		{"already filled holds", 4, 3, 4, 3},
		{"second human yields one bot", 5, 3, 4, 2},
		{"humans at target need no bots", 4, 0, 4, 0},
		{"humans over target need no bots", 6, 0, 4, 0},
		{"bots never exceed room cap", 1, 0, 100, max - 1},
		{"near-full room tops up to cap", 7, 0, 100, 1},
		{"full room of humans stays", max, 0, 100, 0},
		{"disabled target adds nothing", 1, 0, 0, 0},
		{"all bots, no humans, drains", 3, 3, 4, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := targetBots(c.players, c.currentBots, c.target, max); got != c.want {
				t.Fatalf("targetBots(%d,%d,%d,%d) = %d, want %d",
					c.players, c.currentBots, c.target, max, got, c.want)
			}
		})
	}
}

// TestFillerDisabled: при Target<=0 Run сразу возвращается, ботов нет.
func TestFillerDisabled(t *testing.T) {
	f := New(staticProvider{}, Config{Target: 0, MaxPlayers: 8})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() { defer close(done); f.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disabled filler did not return promptly")
	}
	f.Wait()
	if f.ActiveBots() != 0 {
		t.Fatalf("disabled filler kept %d bots", f.ActiveBots())
	}
}

// TestFillerFillsYieldsAndDrains: наполнитель добавляет ботов к одинокому человеку,
// уступает место второму и осушает комнату, когда люди уходят. Комната на RealClock —
// это компонентный тест наполнителя (сеть/тайминг), а не harness детерминизма.
func TestFillerFillsYieldsAndDrains(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	room := game.NewRoom("test", game.Config{
		TickRate: 30, SnapshotRate: 20, MaxPlayers: 8, Seed: 1,
	})
	go room.Run(ctx)

	f := New(staticProvider{rooms: []*game.Room{room}}, Config{
		Target: 4, MaxPlayers: 8, Seed: 1, Interval: 5 * time.Millisecond,
	})
	go f.Run(ctx)

	// Один человек — наполнитель дотягивает комнату до 4 (человек + 3 бота).
	h1 := joinPlayer(t, ctx, room, "human-1")
	eventually(t, 5*time.Second, func() bool {
		return room.Players() == 4 && f.ActiveBots() == 3
	}, "fill to target for lone human")

	// Второй человек — наполнитель снимает одного бота (уступает место).
	h2 := joinPlayer(t, ctx, room, "human-2")
	eventually(t, 5*time.Second, func() bool {
		return room.Players() == 4 && f.ActiveBots() == 2
	}, "yield one bot to second human")

	// Люди ушли — наполнитель осушает комнату (пустую ботами не оживляем).
	_ = h1.Close()
	_ = h2.Close()
	eventually(t, 5*time.Second, func() bool {
		return room.Players() == 0 && f.ActiveBots() == 0
	}, "drain bots when humans leave")

	cancel()
	f.Wait()
	<-room.Done()
}

// joinPlayer подключает «человека» через Pipe (как cmd/loadtest) и вычерпывает его
// снапшоты, чтобы комната не сочла его отставшим. Сессия/Drain гаснут по отмене ctx
// или закрытию клиента.
func joinPlayer(t *testing.T, ctx context.Context, room *game.Room, name string) *bot.Client {
	t.Helper()
	server, client := transport.Pipe(64)
	sess, err := room.Join(ctx, server, name, 0)
	if err != nil {
		_ = server.Close("join failed")
		_ = client.Close("join failed")
		t.Fatalf("join %s: %v", name, err)
	}
	go func() { _ = sess.Run(ctx) }()
	c, err := bot.Attach(ctx, client)
	if err != nil {
		t.Fatalf("attach %s: %v", name, err)
	}
	go bot.Drain(ctx, c)
	return c
}

// eventually опрашивает cond до timeout; проваливает тест, если условие не наступило.
func eventually(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition never held: %s", what)
}

// staticProvider отдаёт фиксированный набор комнат.
type staticProvider struct{ rooms []*game.Room }

func (p staticProvider) Rooms() []*game.Room { return p.rooms }
