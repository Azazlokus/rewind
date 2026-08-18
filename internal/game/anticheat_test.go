package game

import (
	"testing"
	"time"

	"arena/internal/protocol"
)

// TestAntiCheatRewindClamps: tryFire считает выход ViewTick за окно перемотки —
// будущее (d<0) и слишком далёкое прошлое (d>maxRewindTicks); ViewTick в окне не
// считается. DrainAntiCheat возвращает накопленное и обнуляет.
func TestAntiCheatRewindClamps(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	w.Tick = 100

	fire := func(viewTick uint32) {
		p.nextFireTick = 0 // снять кулдаун, чтобы выстрел прошёл до клампа
		w.tryFire(p, protocol.Input{Buttons: protocol.BtnFire, ViewTick: viewTick})
	}
	fire(150) // из будущего: d = 100-150 = -50 < 0 → future
	fire(80)  // далёкое прошлое: d = 20 > maxRewindTicks(10) → stale
	fire(95)  // в окне: d = 5 → не считается
	fire(0)   // «снапшотов ещё не было» — перемотки нет, не считается

	c := w.DrainAntiCheat()
	if c[ACRewindFuture] != 1 {
		t.Fatalf("ACRewindFuture = %d, want 1", c[ACRewindFuture])
	}
	if c[ACRewindStale] != 1 {
		t.Fatalf("ACRewindStale = %d, want 1", c[ACRewindStale])
	}
	// Слив обнулил счётчики.
	c2 := w.DrainAntiCheat()
	if c2[ACRewindFuture] != 0 || c2[ACRewindStale] != 0 {
		t.Fatalf("DrainAntiCheat did not reset: %v", c2)
	}
}

// TestAntiCheatCountersNotInChecksum: античит-счётчики — наблюдение, не состояние
// симуляции; в Checksum не входят (иначе сломали бы реплей/предсказание).
func TestAntiCheatCountersNotInChecksum(t *testing.T) {
	w := NewWorld(1)
	_, _ = w.AddPlayer("p")
	before := w.Checksum()
	w.ac[ACRewindStale] = 7
	w.ac[ACRewindFuture] = 3
	if w.Checksum() != before {
		t.Fatal("anti-cheat counters leaked into Checksum")
	}
}

// TestAntiCheatKindString: метки стабильны (значения лейбла Prometheus).
func TestAntiCheatKindString(t *testing.T) {
	if ACRewindStale.String() != "rewind_stale" || ACRewindFuture.String() != "rewind_future" {
		t.Fatalf("labels drifted: %q %q", ACRewindStale, ACRewindFuture)
	}
	if AntiCheatKind(200).String() != "unknown" {
		t.Fatal("out-of-range kind must be 'unknown'")
	}
}

// capRec — Recorder, запоминающий вызовы AntiCheat (прочие методы — no-op через
// вложенный NopRecorder).
type capRec struct {
	NopRecorder
	events map[string]int
}

func (c *capRec) AntiCheat(kind string, n int) { c.events[kind] += n }

// TestRoomReportsAntiCheat: комната сливает счётчики мира в Recorder метками, и
// после слива не задваивает (DrainAntiCheat обнулил).
func TestRoomReportsAntiCheat(t *testing.T) {
	rec := &capRec{events: map[string]int{}}
	r := NewRoom("t", Config{Metrics: rec, Clock: NewManualClock(time.Time{})})
	r.world.ac[ACRewindStale] = 3
	r.world.ac[ACRewindFuture] = 1

	r.reportAntiCheat()
	if rec.events["rewind_stale"] != 3 || rec.events["rewind_future"] != 1 {
		t.Fatalf("recorder got %v, want stale=3 future=1", rec.events)
	}
	// Второй слив: счётчики уже нулевые — ничего не добавилось.
	r.reportAntiCheat()
	if rec.events["rewind_stale"] != 3 || rec.events["rewind_future"] != 1 {
		t.Fatalf("second report double-counted: %v", rec.events)
	}
}

// TestAntiCheatAttributedToAccount: tryFire привязывает клампнутое событие к AccountID
// игрока (итер. 40); DrainAntiCheatEvents отдаёт и обнуляет.
func TestAntiCheatAttributedToAccount(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	p.AccountID = 42
	w.Tick = 100
	p.nextFireTick = 0
	w.tryFire(p, protocol.Input{Buttons: protocol.BtnFire, ViewTick: 150}) // future
	p.nextFireTick = 0
	w.tryFire(p, protocol.Input{Buttons: protocol.BtnFire, ViewTick: 80}) // stale

	ev := w.DrainAntiCheatEvents()
	if len(ev) != 2 {
		t.Fatalf("want 2 attributed events, got %d", len(ev))
	}
	kinds := map[AntiCheatKind]bool{}
	for _, e := range ev {
		if e.accountID != 42 {
			t.Fatalf("event not attributed to account: %+v", e)
		}
		kinds[e.kind] = true
	}
	if !kinds[ACRewindFuture] || !kinds[ACRewindStale] {
		t.Fatalf("both kinds expected, got %v", kinds)
	}
	if len(w.DrainAntiCheatEvents()) != 0 {
		t.Fatal("DrainAntiCheatEvents did not reset")
	}
}

// TestAntiCheatGuestNotAttributed: у гостя (AccountID 0) привязки нет, но глобальный
// счётчик метрики (итер. 25) событие всё равно считает.
func TestAntiCheatGuestNotAttributed(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("guest") // AccountID 0
	w.Tick = 100
	p.nextFireTick = 0
	w.tryFire(p, protocol.Input{Buttons: protocol.BtnFire, ViewTick: 150}) // future

	if len(w.DrainAntiCheatEvents()) != 0 {
		t.Fatal("guest events must not be attributed to an account")
	}
	if w.DrainAntiCheat()[ACRewindFuture] != 1 {
		t.Fatal("global metric counter should still count guest events")
	}
}

// TestAntiCheatEventsNotInChecksum: привязанные события — наблюдение, не состояние
// симуляции; в Checksum не входят (как и счётчики ac).
func TestAntiCheatEventsNotInChecksum(t *testing.T) {
	w := NewWorld(1)
	_, _ = w.AddPlayer("p")
	before := w.Checksum()
	w.recordAC(7, ACRewindStale)
	w.recordAC(7, ACRewindFuture)
	if w.Checksum() != before {
		t.Fatal("attributed anti-cheat events leaked into Checksum")
	}
}

// TestRoomPersistsAntiCheat: комната шлёт привязанные события в PersistSink (итер. 40),
// по сообщению на событие; после слива буфер пуст.
func TestRoomPersistsAntiCheat(t *testing.T) {
	ch := make(chan PersistMsg, 8)
	r := NewRoom("t", Config{Metrics: NopRecorder{}, Clock: NewManualClock(time.Time{}), PersistSink: ch})
	r.world.recordAC(42, ACRewindStale)
	r.world.recordAC(42, ACRewindFuture)

	r.reportAntiCheat()
	close(ch)
	var got []PersistMsg
	for m := range ch {
		got = append(got, m)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 persist messages, got %d", len(got))
	}
	for _, m := range got {
		if m.Kind != PersistAntiCheat || m.AntiCheatAccount != 42 || m.AntiCheatCount != 1 {
			t.Fatalf("bad persist msg: %+v", m)
		}
	}
}
