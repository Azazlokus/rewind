package game

import (
	"math"
	"testing"

	"arena/internal/protocol"
)

// fireAt возвращает ввод «выстрел» из точки (fromX,fromY) в точку (toX,toY).
func fireAt(seq uint32, fromX, fromY, toX, toY float32) protocol.Input {
	ang := math.Atan2(float64(toY-fromY), float64(toX-fromX))
	return fireInput(seq, ang)
}

// TestSpawnInvulnBlocksDamage: снаряд проходит сквозь неуязвимого игрока — урона и
// смерти нет, пока действует окно неуязвимости.
func TestSpawnInvulnBlocksDamage(t *testing.T) {
	w := NewWorld(1)
	shooter, _ := w.AddPlayer("s")
	target, _ := w.AddPlayer("t")
	place(shooter, 1000, 1000)
	place(target, 1000, 1120) // прямо под стрелком
	target.invulnUntil = w.Tick + spawnInvulnTicks

	w.EnqueueInput(shooter.ID, fireAt(1, 1000, 1000, 1000, 1120))
	for range 12 { // снаряду хватит долететь ~120 юнитов (скорость 700)
		w.Step(testDt)
	}
	if target.HP != 100 || target.dead {
		t.Fatalf("invulnerable target damaged: HP=%d dead=%v", target.HP, target.dead)
	}

	// Контроль: без неуязвимости тот же выстрел ранит.
	w2 := NewWorld(1)
	s2, _ := w2.AddPlayer("s")
	t2, _ := w2.AddPlayer("t")
	place(s2, 1000, 1000)
	place(t2, 1000, 1120)
	w2.EnqueueInput(s2.ID, fireAt(1, 1000, 1000, 1000, 1120))
	for range 12 {
		w2.Step(testDt)
	}
	if t2.HP == 100 {
		t.Fatal("control: target without invuln took no damage (bad test geometry)")
	}
}

// TestFiringDropsShield: выстрел снимает окно неуязвимости — нельзя бить из-под щита.
func TestFiringDropsShield(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	place(p, 1000, 1000)
	p.invulnUntil = w.Tick + spawnInvulnTicks
	w.EnqueueInput(p.ID, fireInput(1, 0))
	w.Step(testDt)
	if p.invulnerable(w.Tick) {
		t.Fatalf("shield survived firing: invulnUntil=%d tick=%d", p.invulnUntil, w.Tick)
	}
}

// TestRespawnGrantsInvuln: возрождение даёт окно неуязвимости.
func TestRespawnGrantsInvuln(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	p.dead = true
	p.respawnAt = w.Tick
	w.Step(testDt)
	if p.dead {
		t.Fatal("player should have respawned")
	}
	if !p.invulnerable(w.Tick) {
		t.Fatalf("respawn did not grant invuln: invulnUntil=%d tick=%d", p.invulnUntil, w.Tick)
	}
}

// TestStreakResetsOnDeath: смерть обнуляет серию убийств.
func TestStreakResetsOnDeath(t *testing.T) {
	w := NewWorld(1)
	victim, _ := w.AddPlayer("v")
	killer, _ := w.AddPlayer("k")
	victim.streak = 5
	w.applyDamage(victim, killer.ID, 100)
	if !victim.dead {
		t.Fatal("victim should be dead")
	}
	if victim.streak != 0 {
		t.Fatalf("streak not reset on death: %d", victim.streak)
	}
}

// TestKillstreakMilestoneRewards: на каждой killstreakStep-й фраге игрок долечивается,
// получает щит и порождает EventKillstreak. Промежуточные фраги награды не дают.
func TestKillstreakMilestoneRewards(t *testing.T) {
	w := NewWorld(1)
	a, _ := w.AddPlayer("a")
	v, _ := w.AddPlayer("v")
	a.HP = 40 // повреждён — веха долечит до 100

	for i := 1; i <= killstreakStep; i++ {
		v.HP = 100 // оживляем жертву для следующего фрага (в обход respawn)
		v.dead = false
		w.events = w.events[:0]
		w.applyDamage(v, a.ID, 100)

		if a.streak != uint16(i) {
			t.Fatalf("after %d kills: streak %d, want %d", i, a.streak, i)
		}
		milestone := i%killstreakStep == 0
		hasEvent := false
		for _, ev := range w.events {
			if ev.Kind == EventKillstreak && ev.Target == a.ID && ev.Streak == uint16(i) {
				hasEvent = true
			}
		}
		if hasEvent != milestone {
			t.Fatalf("kill %d: EventKillstreak=%v, want %v", i, hasEvent, milestone)
		}
		if !milestone && a.HP != 40 {
			t.Fatalf("non-milestone kill %d healed attacker: HP=%d", i, a.HP)
		}
	}
	if a.HP != 100 {
		t.Fatalf("milestone did not heal attacker: HP=%d", a.HP)
	}
	if a.invulnUntil != w.Tick+killstreakInvulnTicks {
		t.Fatalf("milestone shield: invulnUntil=%d, want %d", a.invulnUntil, w.Tick+killstreakInvulnTicks)
	}
}

// TestSuicideDoesNotStreak: суицид/окружение (killer == victim) не растит серию.
func TestSuicideDoesNotStreak(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	p.streak = 2
	w.applyDamage(p, p.ID, 100) // сам себя
	if p.streak != 0 {
		t.Fatalf("suicide changed streak to %d, want 0 (death reset, no self-award)", p.streak)
	}
}

// TestKillstreakStateInChecksum: неуязвимость и серия убийств входят в Checksum.
func TestKillstreakStateInChecksum(t *testing.T) {
	build := func() *World {
		w := NewWorld(9)
		_, _ = w.AddPlayer("p")
		return w
	}
	base := build()
	for name, mut := range map[string]func(*World){
		"invulnUntil": func(w *World) { w.players[w.order[0]].invulnUntil = 5 },
		"streak":      func(w *World) { w.players[w.order[0]].streak = 3 },
	} {
		w := build()
		mut(w)
		if w.Checksum() == base.Checksum() {
			t.Fatalf("%s not covered by Checksum", name)
		}
	}
}

// TestKillstreakDeterminism: два мира с одним seed и одной лентой, где два соседних
// игрока непрерывно стреляют друг в друга (кражи фрагов, смерти, респауны с окном
// неуязвимости, вехи стрика), совпадают по Checksum каждый тик.
func TestKillstreakDeterminism(t *testing.T) {
	build := func() (*World, []PlayerID) {
		w := NewWorld(42)
		a, _ := w.AddPlayer("a")
		b, _ := w.AddPlayer("b")
		place(a, 1000, 1000)
		place(b, 1000, 1100)
		return w, []PlayerID{a.ID, b.ID}
	}
	a, aids := build()
	b, bids := build()

	const ticks = 800
	seq := uint32(0)
	for tick := range ticks {
		seq++
		// Оба целятся друг в друга (по стартовым позициям — грубо, но воспроизводимо).
		inA := fireAt(seq, 1000, 1000, 1000, 1100)
		inB := fireAt(seq, 1000, 1100, 1000, 1000)
		a.EnqueueInput(aids[0], inA)
		a.EnqueueInput(aids[1], inB)
		b.EnqueueInput(bids[0], inA)
		b.EnqueueInput(bids[1], inB)
		a.Step(testDt)
		b.Step(testDt)
		if a.Checksum() != b.Checksum() {
			t.Fatalf("determinism broke at tick %d", tick)
		}
	}
}
