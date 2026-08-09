package game

import (
	"fmt"
	"math"
	"testing"

	"arena/internal/protocol"
)

// stepN прогоняет n тиков фиксированным шагом.
func stepN(w *World, n int) {
	for range n {
		w.Step(1.0 / 30)
	}
}

// TestMatchScoringOnKill: убийство даёт фраг атакующему и смерть жертве; суицид —
// только смерть (без самофрага).
func TestMatchScoringOnKill(t *testing.T) {
	w := NewWorld(1)
	a, _ := w.AddPlayer("a")
	b, _ := w.AddPlayer("b")

	w.applyDamage(b, a.ID, 255) // a убивает b
	if a.Kills != 1 || a.Deaths != 0 {
		t.Fatalf("attacker score = %d/%d, want 1/0", a.Kills, a.Deaths)
	}
	if b.Kills != 0 || b.Deaths != 1 {
		t.Fatalf("victim score = %d/%d, want 0/1", b.Kills, b.Deaths)
	}

	// Суицид: жертва == атакующий, фраг никому не идёт.
	a.HP = 100
	a.dead = false
	w.applyDamage(a, a.ID, 255)
	if a.Kills != 1 {
		t.Fatalf("suicide granted a frag: kills = %d, want 1", a.Kills)
	}
	if a.Deaths != 1 {
		t.Fatalf("suicide death not counted: deaths = %d, want 1", a.Deaths)
	}
}

// TestMatchLifecycle: матч стартует активным, по таймеру уходит в антракт с
// зафиксированным победителем, затем стартует новый матч со сброшенным счётом.
func TestMatchLifecycle(t *testing.T) {
	w := NewWorld(1)
	a, _ := w.AddPlayer("a")
	b, _ := w.AddPlayer("b")
	if w.matchPhase != matchActive {
		t.Fatalf("new world phase = %d, want active", w.matchPhase)
	}

	// a лидирует по фрагам к концу матча.
	a.Kills = 5
	b.Kills = 2

	// Досчитываем до конца матча (matchAt == matchDurationTicks).
	stepN(w, matchDurationTicks-int(w.Tick))
	if w.matchPhase != matchIntermission {
		t.Fatalf("phase after duration = %d, want intermission", w.matchPhase)
	}
	if w.winner != a.ID {
		t.Fatalf("winner = %d, want a=%d", w.winner, a.ID)
	}
	if w.matchAt != w.Tick+intermissionTicks {
		t.Fatalf("intermission deadline = %d, want %d", w.matchAt, w.Tick+intermissionTicks)
	}

	// Досчитываем до конца антракта — новый матч, счёт обнулён.
	stepN(w, int(w.matchAt-w.Tick))
	if w.matchPhase != matchActive {
		t.Fatalf("phase after intermission = %d, want active", w.matchPhase)
	}
	if a.Kills != 0 || b.Kills != 0 || a.Deaths != 0 || b.Deaths != 0 {
		t.Fatalf("scores not reset: a=%d/%d b=%d/%d", a.Kills, a.Deaths, b.Kills, b.Deaths)
	}
	if w.winner != 0 {
		t.Fatalf("winner not cleared on new match: %d", w.winner)
	}
}

// TestMatchStateSorted: табло отсортировано по убыванию убийств, при равенстве — по
// возрастанию id.
func TestMatchStateSorted(t *testing.T) {
	w := NewWorld(1)
	p1, _ := w.AddPlayer("p1") // id 1
	p2, _ := w.AddPlayer("p2") // id 2
	p3, _ := w.AddPlayer("p3") // id 3
	p1.Kills = 2
	p2.Kills = 5
	p3.Kills = 2 // равен p1 — tiebreak по id: p1 раньше p3

	snap := w.MatchState(nil)
	got := make([]PlayerID, len(snap.Scores))
	for i, s := range snap.Scores {
		got[i] = s.ID
	}
	want := []PlayerID{p2.ID, p1.ID, p3.ID}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scoreboard order = %v, want %v", got, want)
		}
	}
}

// TestMatchDeterminismAcrossFullCycle: два мира с одним seed и одной лентой вводов
// (со стрельбой и движением) обязаны совпадать по Checksum на КАЖДОМ тике, включая
// переход матч → антракт → новый матч. Обычный TestWorldDeterminism не жмёт BtnFire
// и крутит &lt; matchDurationTicks, поэтому не упражняет под равенством хэша ни скоринг
// (Kills/Deaths), ни переходы фаз с rng-респаунами. Этот тест закрывает именно эти
// самые рискованные на рассинхрон пути: endMatch (обход order), startMatch (розыгрыши
// w.rng в respawn + сброс счёта) и winner. Регресс вида «range w.players вместо
// w.order» разошёл бы миры на первом же переходе фазы и был бы пойман здесь.
func TestMatchDeterminismAcrossFullCycle(t *testing.T) {
	const seed = 20240729
	build := func() (*World, []PlayerID) {
		w := NewWorld(seed)
		ids := make([]PlayerID, 0, 4)
		for i := range 4 {
			p, err := w.AddPlayer(fmt.Sprintf("p%d", i))
			if err != nil {
				t.Fatalf("add player: %v", err)
			}
			// Кучно вокруг (2020,2020): выстрелы в центр пересекают чужие круги —
			// счёт реально копится, а не остаётся нулевым.
			place(p, 2000+float32(i%2)*40, 2000+float32(i/2)*40)
			ids = append(ids, p.ID)
		}
		return w, ids
	}
	a, aIDs := build()
	b, bIDs := build()

	// Лента детерминирована и одинакова для обоих миров: зависит только от tick/индекса
	// и текущей позиции игрока. Каждый тик — стрельба в центр кучи + смена направления.
	feed := func(w *World, ids []PlayerID, tick int) {
		for i, id := range ids {
			p := w.players[id]
			btn := protocol.BtnFire
			switch (tick + i) % 4 {
			case 0:
				btn |= protocol.BtnRight
			case 1:
				btn |= protocol.BtnDown
			case 2:
				btn |= protocol.BtnLeft
			case 3:
				btn |= protocol.BtnUp
			}
			aim := protocol.AimFromRadians(math.Atan2(float64(2020-p.Y), float64(2020-p.X)))
			w.EnqueueInput(id, protocol.Input{Seq: uint32(tick + 1), Buttons: btn, Aim: aim})
		}
	}

	total := matchDurationTicks + intermissionTicks + 120
	sawKill := false
	sawIntermission := false
	for tick := range total {
		feed(a, aIDs, tick)
		feed(b, bIDs, tick)
		a.Step(1.0 / 30)
		b.Step(1.0 / 30)
		if a.Checksum() != b.Checksum() {
			t.Fatalf("desync at tick %d (phase %d)", a.Tick, a.matchPhase)
		}
		if a.matchPhase == matchIntermission {
			sawIntermission = true
		}
		for _, id := range aIDs {
			if a.players[id].Kills > 0 {
				sawKill = true
			}
		}
	}
	// Тест обязан реально упражнять скоринг и переходы, иначе он слеп к новому коду.
	if !sawKill {
		t.Fatal("input feed produced no kills — scoring path not exercised")
	}
	if !sawIntermission {
		t.Fatal("match never reached intermission — phase transition not exercised")
	}
	if a.matchPhase != matchActive {
		t.Fatalf("match cycle did not restart to active: phase %d", a.matchPhase)
	}
}

// TestMatchStateInChecksum: счёт и состояние матча входят в Checksum — иначе
// реплеи/предсказание разошлись бы по невидимому состоянию.
func TestMatchStateInChecksum(t *testing.T) {
	build := func() (*World, *Player) {
		w := NewWorld(7)
		p, _ := w.AddPlayer("p")
		_, _ = w.AddPlayer("q")
		return w, p
	}

	base, _ := build()
	same, _ := build()
	if base.Checksum() != same.Checksum() {
		t.Fatal("identical worlds disagree on checksum")
	}

	// Разный счёт игрока → разный Checksum.
	w1, p1 := build()
	p1.Kills++
	if w1.Checksum() == base.Checksum() {
		t.Fatal("player Kills not covered by Checksum")
	}

	// Разная фаза/дедлайн/победитель матча → разный Checksum.
	for _, mut := range []func(*World){
		func(w *World) { w.matchPhase = matchIntermission },
		func(w *World) { w.matchAt++ },
		func(w *World) { w.winner = 1 },
	} {
		w, _ := build()
		mut(w)
		if w.Checksum() == base.Checksum() {
			t.Fatal("match world state not covered by Checksum")
		}
	}
}
