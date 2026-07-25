package game

import (
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
