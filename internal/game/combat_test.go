package game

import (
	rand "math/rand/v2"
	"testing"

	"arena/internal/protocol"
)

// place ставит игрока в точную позицию — в тестах пакета game поля доступны.
func place(p *Player, x, y float32) { p.X, p.Y, p.VX, p.VY = x, y, 0, 0 }

// fireInput — ввод «выстрел» под углом rad (радианы), с номером seq.
func fireInput(seq uint32, rad float64) protocol.Input {
	return protocol.Input{Seq: seq, Buttons: protocol.BtnFire, Aim: protocol.AimFromRadians(rad)}
}

// TestFireSpawnsProjectileWithCooldown: выстрел порождает снаряд, а повторный до
// истечения кулдауна — нет.
func TestFireSpawnsProjectileWithCooldown(t *testing.T) {
	w := NewWorld(1)
	p, err := w.AddPlayer("shooter")
	if err != nil {
		t.Fatal(err)
	}
	place(p, 1000, 1000)

	w.EnqueueInput(p.ID, fireInput(1, 0))
	w.Step(1.0 / 30)
	if len(w.projectiles) != 1 {
		t.Fatalf("first fire: got %d projectiles, want 1", len(w.projectiles))
	}
	// Сразу жмём снова — кулдаун ещё не истёк, нового снаряда быть не должно.
	w.EnqueueInput(p.ID, fireInput(2, 0))
	w.Step(1.0 / 30)
	if len(w.projectiles) != 1 {
		t.Fatalf("fire during cooldown: got %d projectiles, want 1", len(w.projectiles))
	}
}

// TestProjectileHitsTargetAndEmitsHit: снаряд долетает до цели, снимает HP и
// эмитит Hit с верными участниками.
func TestProjectileHitsTargetAndEmitsHit(t *testing.T) {
	w := NewWorld(1)
	shooter, _ := w.AddPlayer("s")
	target, _ := w.AddPlayer("t")
	place(shooter, 1000, 1000)
	place(target, 1100, 1000)

	w.EnqueueInput(shooter.ID, fireInput(1, 0)) // стреляем вправо, в цель
	w.Step(1.0 / 30)

	var hit *Event
	for range 20 {
		w.Step(1.0 / 30)
		for _, e := range w.Events() {
			if e.Kind == EventHit {
				ev := e
				hit = &ev
			}
		}
		if hit != nil {
			break
		}
	}
	if hit == nil {
		t.Fatal("projectile never hit the target")
	}
	if hit.Attacker != shooter.ID || hit.Target != target.ID {
		t.Fatalf("hit event attacker=%d target=%d, want %d/%d", hit.Attacker, hit.Target, shooter.ID, target.ID)
	}
	if target.HP != 100-ProjectileDamage {
		t.Fatalf("target HP=%d, want %d", target.HP, 100-ProjectileDamage)
	}
	if len(w.projectiles) != 0 {
		t.Fatalf("projectile should be consumed on hit, %d remain", len(w.projectiles))
	}
}

// TestKillDeathAndRespawn: смертельное попадание убивает цель (Death, исчезает из
// снапшота), а через respawnDelayTicks она возрождается (Spawn, полный HP).
func TestKillDeathAndRespawn(t *testing.T) {
	w := NewWorld(1)
	shooter, _ := w.AddPlayer("s")
	target, _ := w.AddPlayer("t")
	place(shooter, 1000, 1000)
	place(target, 1100, 1000)
	target.HP = ProjectileDamage // одного попадания хватит на смерть

	w.EnqueueInput(shooter.ID, fireInput(1, 0))
	w.Step(1.0 / 30)

	death := false
	for range 20 {
		w.Step(1.0 / 30)
		for _, e := range w.Events() {
			if e.Kind == EventDeath && e.Target == target.ID {
				death = true
			}
		}
		if target.dead {
			break
		}
	}
	if !death {
		t.Fatal("no death event emitted")
	}
	if !target.dead {
		t.Fatal("target not marked dead")
	}
	for _, e := range w.AppendEntities(nil) {
		if e.Kind == protocol.KindPlayer && e.ID == uint16(target.ID) {
			t.Fatal("dead player still present in snapshot")
		}
	}

	spawn := false
	for range respawnDelayTicks + 2 {
		w.Step(1.0 / 30)
		for _, e := range w.Events() {
			if e.Kind == EventSpawn && e.Target == target.ID {
				spawn = true
			}
		}
		if !target.dead {
			break
		}
	}
	if !spawn {
		t.Fatal("no spawn event emitted")
	}
	if target.dead || target.HP != 100 {
		t.Fatalf("after respawn: dead=%v hp=%d, want alive/100", target.dead, target.HP)
	}
}

// TestOwnerNotHitByOwnProjectile: снаряд не наносит урон своему владельцу.
func TestOwnerNotHitByOwnProjectile(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("s")
	place(p, 2000, 2000)

	w.EnqueueInput(p.ID, fireInput(1, 0))
	w.Step(1.0 / 30)
	for range 5 {
		w.Step(1.0 / 30)
	}
	if p.HP != 100 {
		t.Fatalf("owner damaged by own projectile: HP=%d", p.HP)
	}
}

// TestProjectileExpires: снаряд без цели живёт ограниченное время и исчезает.
func TestProjectileExpires(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("s")
	place(p, 200, 200)

	w.EnqueueInput(p.ID, fireInput(1, 0))
	w.Step(1.0 / 30)
	if len(w.projectiles) != 1 {
		t.Fatal("no projectile spawned")
	}
	for range projectileLifeTicks + 2 {
		w.Step(1.0 / 30)
	}
	if len(w.projectiles) != 0 {
		t.Fatalf("projectile did not expire: %d remain", len(w.projectiles))
	}
}

// TestCombatDeterminism: два мира с одной лентой стрельбы (включая смерти и
// респауны) приходят к байт-в-байт равному Checksum.
func TestCombatDeterminism(t *testing.T) {
	build := func() *World {
		w := NewWorld(99)
		a, _ := w.AddPlayer("a")
		b, _ := w.AddPlayer("b")
		place(a, 1000, 1000)
		place(b, 1200, 1000) // на линии огня, стоит на месте
		return w
	}
	wa, wb := build(), build()
	deaths, spawns := 0, 0
	for tick := 0; tick < 300; tick++ {
		seq := uint32(tick + 1)
		fireRight := protocol.Input{Seq: seq, Buttons: protocol.BtnFire, Aim: protocol.AimFromRadians(0)}
		hold := protocol.Input{Seq: seq, Buttons: 0}
		wa.EnqueueInput(1, fireRight)
		wb.EnqueueInput(1, fireRight)
		wa.EnqueueInput(2, hold)
		wb.EnqueueInput(2, hold)
		wa.Step(1.0 / 30)
		wb.Step(1.0 / 30)
		for _, e := range wa.Events() {
			switch e.Kind {
			case EventDeath:
				deaths++
			case EventSpawn:
				spawns++
			}
		}
		if wa.Checksum() != wb.Checksum() {
			t.Fatalf("combat divergence at tick %d", tick)
		}
	}
	// Тест обязан реально упражнять бой: попадания -> смерть -> респаун (и путь
	// w.rng респауна). Иначе он ослепнет к боевому коду при сдвиге констант/позиций.
	if deaths == 0 || spawns == 0 {
		t.Fatalf("combat tape trivial: deaths=%d spawns=%d, want both > 0", deaths, spawns)
	}
}

// findHitBruteforce — эталон O(игроки): линейный проход по order, первый (младший
// id) геометрический хит с учётом перемотки. Такой была коллизия до итерации 8;
// stepProjectiles теперь ходит через широкофазный World.findHit, а
// TestBroadPhaseAgreesWithBruteforce сверяет их исход байт-в-байт.
func (w *World) findHitBruteforce(pr *projectile, nx, ny float32) *Player {
	for _, id := range w.order {
		tgt := w.players[id]
		if tgt.dead || tgt.invulnerable(w.Tick) || tgt.ID == pr.owner {
			continue // паритет с findHit: мёртвых и неуязвимых (итер. 20) пропускаем
		}
		tx, ty := w.targetPos(tgt, pr.rewind)
		if segmentCircleHit(pr.x, pr.y, nx, ny, tx, ty, PlayerRadius+ProjectileRadius) {
			return tgt
		}
	}
	return nil
}

func hitID(p *Player) PlayerID {
	if p == nil {
		return 0
	}
	return p.ID
}

// TestBroadPhaseAgreesWithBruteforce стережёт центральный инвариант итерации 8:
// широкофазный findHit выбирает ту же жертву, что эталонный брутфорс, на батарее
// случайных сцен. Нарочно рассинхронизируем текущую позицию и историю (независимый
// рандом) и крутим w.Tick — то есть перемотанная позиция расходится с текущей на
// произвольную величину, вплоть до через всю карту. Это ломало бы широкую фазу с
// гадательным константным запасом, но rewindAABB покрывает окно точно, поэтому
// совпадение обязано держаться безусловно.
func TestBroadPhaseAgreesWithBruteforce(t *testing.T) {
	for seed := uint64(0); seed < 300; seed++ {
		rng := rand.New(rand.NewPCG(seed, 0x2545f4914f6cdd1d))
		w := NewWorld(int64(seed))
		// Нетривиальная индексация кольца истории. Первые maxRewindTicks сидов держим
		// на малом тике — там (tick-r) заворачивается по uint32, поэтому wrap
		// упражняется ДЕТЕРМИНИРОВАННО, а не только статистически (nit determinism-guard).
		if seed < maxRewindTicks {
			w.Tick = uint32(seed)
		} else {
			w.Tick = rng.Uint32N(5000)
		}
		n := 1 + rng.IntN(14)
		for i := 0; i < n; i++ {
			p, err := w.AddPlayer("p")
			if err != nil {
				t.Fatal(err)
			}
			p.X, p.Y = rng.Float32()*MapSize, rng.Float32()*MapSize
			for k := range p.posHist {
				p.posHist[k] = histPos{rng.Float32() * MapSize, rng.Float32() * MapSize}
			}
			if rng.IntN(5) == 0 {
				p.dead = true // часть целей мертва — обе стороны их пропускают
			}
			if rng.IntN(4) == 0 {
				// Часть целей под окном неуязвимости (итер. 20) — обе стороны их
				// пропускают. Ставим invulnUntil вокруг w.Tick (со сдвигом ±), чтобы
				// попадать и в «активен», и в «истёк», упражняя true-ветку invuln-скипа
				// под сверкой findHit ↔ bruteforce (совет determinism-guard). Клампим
				// снизу нулём — без underflow uint32 на малых тиках.
				iu := int64(w.Tick) + int64(rng.IntN(120)-40)
				if iu < 0 {
					iu = 0
				}
				p.invulnUntil = uint32(iu)
			}
		}
		w.hitGrid.build(w)

		for shot := 0; shot < 50; shot++ {
			pr := projectile{
				owner:  PlayerID(1 + rng.IntN(n)),
				x:      rng.Float32() * MapSize,
				y:      rng.Float32() * MapSize,
				rewind: int32(rng.IntN(maxRewindTicks + 1)),
			}
			// Сегмент за тик: ±40 юнитов по каждой оси (снаряд летит ~23/тик).
			nx := pr.x + (rng.Float32()*2-1)*40
			ny := pr.y + (rng.Float32()*2-1)*40
			got := hitID(w.findHit(&pr, nx, ny))
			want := hitID(w.findHitBruteforce(&pr, nx, ny))
			if got != want {
				t.Fatalf("seed %d shot %d: grid picked %d, bruteforce picked %d", seed, shot, got, want)
			}
		}
	}
}

// TestBroadPhaseSkipsMidTickKilledTarget прямо стережёт обработку смерти в середине
// тика (nit determinism-guard): ранний снаряд убивает цель с младшим id, поздний
// снаряд ТОГО ЖЕ тика обязан уйти к следующей — findHit читает живой tgt.dead, а не
// снимок из сетки. Идёт через реальный Step, а не через статичную сцену.
func TestBroadPhaseSkipsMidTickKilledTarget(t *testing.T) {
	w := NewWorld(1)
	low, err := w.AddPlayer("low") // id 1 — младший, гибнет первым
	if err != nil {
		t.Fatal(err)
	}
	high, err := w.AddPlayer("high") // id 2
	if err != nil {
		t.Fatal(err)
	}
	place(low, 1000, 1000)
	place(high, 1000, 1000) // та же точка — оба на пути снарядов
	low.initHistory()
	high.initHistory()
	low.HP = ProjectileDamage // одно попадание убивает

	// Два снаряда в одном тике проходят сквозь (1000,1000). owner=999 — среди целей
	// такого нет, самопопадание не мешает.
	for range 2 {
		w.projectiles = append(w.projectiles, projectile{
			owner: 999,
			x:     985, y: 1000, // за тик долетает до x≈1008, накрывая цель
			vx: ProjectileSpeed, vy: 0,
			life: projectileLifeTicks,
		})
	}
	w.Step(1.0 / 30)

	if !low.dead {
		t.Fatalf("младший id должен погибнуть от первого снаряда: HP=%d dead=%v", low.HP, low.dead)
	}
	if high.HP != 100-uint8(ProjectileDamage) {
		t.Fatalf("второй снаряд должен уйти к следующей цели: high HP=%d, want %d", high.HP, 100-ProjectileDamage)
	}
	if len(w.projectiles) != 0 {
		t.Fatalf("оба снаряда должны попасть и исчезнуть, осталось %d", len(w.projectiles))
	}
}

// combatScene расставляет n игроков сеткой (с заполненной историей) и maxProjectiles
// снарядов, разбросанных по карте, — общая статичная сцена для бенчей широкой фазы.
func combatScene(tb testing.TB, n int) (*World, []projectile) {
	tb.Helper()
	w := NewWorld(1)
	cols := 1
	for cols*cols < n {
		cols++
	}
	step := (MapSize - 400) / float32(cols)
	for i := range n {
		p, err := w.AddPlayer("p")
		if err != nil {
			tb.Fatal(err)
		}
		place(p, 200+float32(i%cols)*step, 200+float32(i/cols)*step)
		p.initHistory()
	}
	rng := rand.New(rand.NewPCG(42, 7))
	prs := make([]projectile, 0, maxProjectiles)
	for range maxProjectiles {
		prs = append(prs, projectile{
			owner: PlayerID(1 + rng.IntN(n)),
			x:     rng.Float32() * MapSize,
			y:     rng.Float32() * MapSize,
		})
	}
	return w, prs
}

// BenchmarkProjectileHitGrid / Bruteforce — стоимость коллизии снаряд×игрок при
// плотном бое. Сравнение показывает выигрыш широкой фазы (итерация 8). Прямые (не
// через функцию-значение) вызовы поиска цели держат &pr на стеке — путь zero-alloc,
// как в проде. applyDamage не зовём: сцена статична между итерациями, меряется
// только поиск (для грида — плюс построение сетки; честно относим к его стоимости).
func BenchmarkProjectileHitGrid(b *testing.B) {
	for _, n := range []int{50, 200} {
		b.Run(sizeName(n), func(b *testing.B) {
			w, prs := combatScene(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				w.hitGrid.build(w)
				for i := range prs {
					pr := prs[i]
					_ = w.findHit(&pr, pr.x+16, pr.y+16)
				}
			}
		})
	}
}

func BenchmarkProjectileHitBruteforce(b *testing.B) {
	for _, n := range []int{50, 200} {
		b.Run(sizeName(n), func(b *testing.B) {
			w, prs := combatScene(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				for i := range prs {
					pr := prs[i]
					_ = w.findHitBruteforce(&pr, pr.x+16, pr.y+16)
				}
			}
		})
	}
}
