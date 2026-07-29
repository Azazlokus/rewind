package game

import (
	"testing"
	"time"
)

// newPersistRoom строит комнату с каналом персиста (цикл не запускаем — дёргаем
// методы эмита напрямую, они принадлежат горутине цикла и её не требуют).
func newPersistRoom(t *testing.T, sink chan<- PersistMsg) *Room {
	t.Helper()
	return NewRoom("t", Config{Seed: 42, PersistSink: sink})
}

// TestRoomPersistKill: смерть шлёт инкремент статы с аккаунтами убийцы и жертвы;
// суицид — тот же аккаунт дважды (persister не даст фраг); оба гостя — не шлём;
// ушедший стрелок (nil-игрок) даёт killer 0.
func TestRoomPersistKill(t *testing.T) {
	sink := make(chan PersistMsg, 8)
	r := newPersistRoom(t, sink)
	p1, _ := r.world.AddPlayer("a")
	p1.AccountID = 100
	p2, _ := r.world.AddPlayer("b")
	p2.AccountID = 200
	g1, _ := r.world.AddPlayer("g1") // гость (AccountID 0)
	g2, _ := r.world.AddPlayer("g2")

	wantKill := func(tag string, killer, victim int64) {
		t.Helper()
		got := <-sink
		if got.Kind != PersistKill || got.Killer != killer || got.Victim != victim {
			t.Fatalf("%s = %+v, want killer %d victim %d", tag, got, killer, victim)
		}
	}
	r.persistKill(p1.ID, p2.ID)
	wantKill("kill", 100, 200)
	r.persistKill(p1.ID, p1.ID) // суицид
	wantKill("suicide", 100, 100)
	r.persistKill(999, p2.ID) // стрелок уже ушёл → killer 0
	wantKill("orphan-killer", 0, 200)
	r.persistKill(g1.ID, g2.ID) // оба гости → нечего копить
	select {
	case got := <-sink:
		t.Fatalf("guests should not emit, got %+v", got)
	default:
	}
}

// TestRoomPersistMatchResult: на итог матча шлётся результат с участниками, победителем
// и временами; StartedAt отстоит от EndedAt ровно на длительность матча.
func TestRoomPersistMatchResult(t *testing.T) {
	sink := make(chan PersistMsg, 4)
	r := newPersistRoom(t, sink)
	p1, _ := r.world.AddPlayer("a") // id 1
	p1.AccountID = 100
	p1.Kills, p1.Deaths = 5, 1
	p2, _ := r.world.AddPlayer("b") // id 2
	p2.AccountID = 200
	p2.Kills, p2.Deaths = 5, 4 // равен по фрагам, но id больше → не победитель
	r.world.winner = p1.ID

	r.persistMatchResult()
	got := <-sink
	if got.Kind != PersistMatch {
		t.Fatalf("kind = %d", got.Kind)
	}
	m := got.Match
	if m.Mode != "ffa" || m.Seed != 42 || m.Winner != p1.ID {
		t.Fatalf("meta = %+v", m)
	}
	if len(m.Players) != 2 {
		t.Fatalf("players = %+v", m.Players)
	}
	if p := m.Players[0]; p.AccountID != 100 || p.Kills != 5 || p.Deaths != 1 || !p.Won {
		t.Fatalf("winner row = %+v", p)
	}
	if p := m.Players[1]; p.AccountID != 200 || p.Won {
		t.Fatalf("loser row = %+v", p)
	}
	if d := m.EndedAt.Sub(m.StartedAt); d != time.Duration(matchDurationTicks)*r.cfg.TickInterval() {
		t.Fatalf("match span = %v, want %v", d, time.Duration(matchDurationTicks)*r.cfg.TickInterval())
	}
}

// TestRoomPersistNilSinkNoop: без PersistSink эмит — no-op (не паникует, ничего не шлёт).
func TestRoomPersistNilSinkNoop(t *testing.T) {
	r := newPersistRoom(t, nil)
	p, _ := r.world.AddPlayer("a")
	p.AccountID = 1
	r.persistKill(p.ID, p.ID) // не паникует
	r.persistMatchResult()    // не паникует
}

// TestRoomPersistDropsWhenFull: переполненный канал роняет сообщение и считает дроп,
// но sendPersist не блокируется (тик никогда не стоит на персисте).
func TestRoomPersistDropsWhenFull(t *testing.T) {
	sink := make(chan PersistMsg) // без буфера и без читателя → send всегда мимо
	r := newPersistRoom(t, sink)
	r.sendPersist(PersistMsg{Kind: PersistKill, Killer: 1})
	if r.persistDrops != 1 {
		t.Fatalf("drops = %d, want 1", r.persistDrops)
	}
}
