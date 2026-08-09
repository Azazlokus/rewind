package game

import (
	rand "math/rand/v2"
	"testing"

	"arena/internal/protocol"
)

// moveRight — ввод «движение вправо»; dashRight — то же с запросом рывка.
func moveRight(seq uint32) protocol.Input {
	return protocol.Input{Seq: seq, Buttons: protocol.BtnRight}
}
func dashRight(seq uint32) protocol.Input {
	return protocol.Input{Seq: seq, Buttons: protocol.BtnRight, Actions: protocol.ActDash}
}

// TestDashBurstSpeeds: рывок при движении даёт ускорение на dashDurationSec, затем
// скорость возвращается к обычной.
func TestDashBurstSpeeds(t *testing.T) {
	var s MoveState
	s.X, s.Y = 1000, 1000

	Step(&s, moveRight(1), inputDt)
	if s.VX != PlayerSpeed {
		t.Fatalf("normal move vx=%v, want %v", s.VX, PlayerSpeed)
	}
	Step(&s, dashRight(2), inputDt)
	if s.VX != PlayerSpeed*dashSpeedMult {
		t.Fatalf("dash vx=%v, want %v", s.VX, PlayerSpeed*dashSpeedMult)
	}
	if s.dashCD <= 0 {
		t.Fatal("dash should start the cooldown")
	}
	// В середине рывка скорость ещё повышена.
	Step(&s, moveRight(3), inputDt)
	if s.VX != PlayerSpeed*dashSpeedMult {
		t.Fatalf("mid-dash vx=%v, want boosted", s.VX)
	}
	// Досматриваем рывок до конца — скорость падает к обычной.
	for i := uint32(0); s.dashT > 0 && i < 100; i++ {
		Step(&s, moveRight(10+i), inputDt)
	}
	Step(&s, moveRight(200), inputDt)
	if s.VX != PlayerSpeed {
		t.Fatalf("post-dash vx=%v, want %v", s.VX, PlayerSpeed)
	}
}

// TestDashRequiresMovement: рывок без направления движения не срабатывает (кулдаун не
// тратится, ускорения нет).
func TestDashRequiresMovement(t *testing.T) {
	var s MoveState
	s.X, s.Y = 1000, 1000
	Step(&s, protocol.Input{Seq: 1, Actions: protocol.ActDash}, inputDt) // без WASD
	if s.dashCD != 0 || s.dashT != 0 {
		t.Fatalf("dash triggered without movement: cd=%v t=%v", s.dashCD, s.dashT)
	}
	if s.VX != 0 || s.VY != 0 {
		t.Fatalf("no movement expected, got vx=%v vy=%v", s.VX, s.VY)
	}
}

// TestDashCooldown: пока идёт кулдаун, повторный рывок не срабатывает; после его
// истечения — снова доступен.
func TestDashCooldown(t *testing.T) {
	var s MoveState
	s.X, s.Y = 1000, 1000
	Step(&s, dashRight(1), inputDt) // старт рывка + кулдаун
	// Досматриваем ускорение до конца.
	var seq uint32 = 2
	for s.dashT > 0 {
		Step(&s, moveRight(seq), inputDt)
		seq++
	}
	// Кулдаун ещё идёт (2.5 с >> 0.18 с рывка) — повторный запрос игнорируется.
	Step(&s, dashRight(seq), inputDt)
	seq++
	if s.dashT > 0 {
		t.Fatal("dash re-triggered during cooldown")
	}
	// Ждём снятия кулдауна и пробуем снова.
	for s.dashCD > 0 {
		Step(&s, moveRight(seq), inputDt)
		seq++
	}
	Step(&s, dashRight(seq), inputDt)
	if s.dashT <= 0 {
		t.Fatal("dash should be available after cooldown elapsed")
	}
}

// TestDashResetOnRespawn: респаун обнуляет таймеры рывка (свежая жизнь).
func TestDashResetOnRespawn(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	place(p, 1000, 1000)
	p.dashCD, p.dashT = 2.0, 0.1
	p.dead = true
	p.respawnAt = w.Tick
	w.Step(dt)
	if p.dead {
		t.Fatal("player should have respawned")
	}
	if p.dashCD != 0 || p.dashT != 0 {
		t.Fatalf("dash timers after respawn: cd=%v t=%v, want 0/0", p.dashCD, p.dashT)
	}
}

// TestDashInChecksum: таймеры рывка входят в Checksum (будущее состояние движения).
func TestDashInChecksum(t *testing.T) {
	w := NewWorld(1)
	p, _ := w.AddPlayer("p")
	place(p, 1000, 1000)
	w.Step(dt)
	before := w.Checksum()
	p.dashT = 0.1
	if w.Checksum() == before {
		t.Fatal("changing dashT must change Checksum")
	}
}

// TestDashDeterminism: два мира с одним seed и одной лентой вводов (движение + рывки)
// дают равный Checksum на каждом тике.
func TestDashDeterminism(t *testing.T) {
	build := func() *World {
		w := NewWorld(9)
		for range 3 {
			if _, err := w.AddPlayer("p"); err != nil {
				t.Fatal(err)
			}
		}
		return w
	}
	wa, wb := build(), build()
	src := rand.New(rand.NewPCG(3, 3))
	ids := wa.order
	for tick := range 300 {
		for _, id := range ids {
			var act uint8
			if src.UintN(6) == 0 { // изредка жмём рывок
				act = protocol.ActDash
			}
			in := protocol.Input{
				Seq:     uint32(tick + 1),
				Buttons: uint8(src.UintN(16)),
				Aim:     uint16(src.UintN(65536)),
				Actions: act,
			}
			wa.EnqueueInput(id, in)
			wb.EnqueueInput(id, in)
		}
		wa.Step(dt)
		wb.Step(dt)
		if wa.Checksum() != wb.Checksum() {
			t.Fatalf("worlds diverged at tick %d", tick)
		}
	}
}
