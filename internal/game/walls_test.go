package game

import (
	"math"
	"testing"

	"arena/internal/protocol"
)

// wallXLeft — X центра игрока, вплотную прижатого к левой грани стены wl.
func wallXLeft(wl wall) float32 { return wl.minX - PlayerRadius }

// TestPlayerStopsAtWall: игрок, упирающийся в стену, останавливается у её грани и
// не проникает внутрь. Едет вправо в левый столб {1500,...}.
func TestPlayerStopsAtWall(t *testing.T) {
	w := NewWorld(1)
	p, err := w.AddPlayer("pusher")
	if err != nil {
		t.Fatal(err)
	}
	place(p, 1400, 1700) // слева от стены, на её высоте

	for i := range 200 {
		w.EnqueueInput(p.ID, protocol.Input{Seq: uint32(i + 1), Buttons: protocol.BtnRight})
		w.Step(1.0 / 30)
	}
	want := wallXLeft(walls[0]) // 1500 - PlayerRadius
	if math.Abs(float64(p.X-want)) > 0.01 {
		t.Fatalf("player did not settle at the wall face: x=%.3f, want %.3f", p.X, want)
	}
	if p.Y != 1700 {
		t.Fatalf("frontal push should not move Y: y=%.3f, want 1700", p.Y)
	}
	if insideAnyWall(p.X, p.Y, PlayerRadius) {
		t.Fatal("player ended up overlapping a wall")
	}
}

// TestPlayerPushedOutOfWall: игрок, оказавшийся внутри стены (туннелирование),
// выталкивается наружу за один шаг через ближайшую грань.
func TestPlayerPushedOutOfWall(t *testing.T) {
	wl := walls[0]
	cx := (wl.minX + wl.maxX) / 2
	cy := (wl.minY + wl.maxY) / 2

	w := NewWorld(1)
	p, err := w.AddPlayer("stuck")
	if err != nil {
		t.Fatal(err)
	}
	place(p, cx, cy) // ровно в центре стены
	if !insideAnyWall(p.X, p.Y, PlayerRadius) {
		t.Fatal("test setup broken: player not inside the wall")
	}

	// Пустой ввод: движения нет, но Step всё равно разрешает коллизию.
	w.EnqueueInput(p.ID, protocol.Input{Seq: 1})
	w.Step(1.0 / 30)

	if insideAnyWall(p.X, p.Y, PlayerRadius) {
		t.Fatalf("player still inside wall after step: (%.1f, %.1f)", p.X, p.Y)
	}
}

// TestProjectileBlockedByWall: снаряд гибнет на стене — цель ЗА стеной урона не
// получает, а цель ПЕРЕД стеной получает.
func TestProjectileBlockedByWall(t *testing.T) {
	fireAt := func(shooterY float32, targetX, targetY float32) uint8 {
		w := NewWorld(1)
		s, _ := w.AddPlayer("s")
		tg, _ := w.AddPlayer("t")
		place(s, 1400, shooterY) // слева от левого столба {1500,...}
		place(tg, targetX, targetY)

		w.EnqueueInput(s.ID, fireInput(1, 0)) // выстрел вправо
		w.Step(1.0 / 30)
		for range 20 {
			w.Step(1.0 / 30)
		}
		return tg.HP
	}

	// Цель за стеной (x=1700 правее столба [1500,1620]) — снаряд гасится стеной.
	if hp := fireAt(1700, 1700, 1700); hp != 100 {
		t.Fatalf("target behind wall took damage: HP=%d, want 100", hp)
	}
	// Контроль: цель перед стеной (x=1460, между стрелком и стеной) — попадание.
	if hp := fireAt(1700, 1460, 1700); hp != 100-ProjectileDamage {
		t.Fatalf("target in front of wall not hit: HP=%d, want %d", hp, 100-ProjectileDamage)
	}
}

// TestSpawnNotInsideWall: точки спавна не попадают внутрь стен ни на одном из
// нескольких seed'ов (детерминированный перебор в spawnPoint).
func TestSpawnNotInsideWall(t *testing.T) {
	for _, seed := range []int64{0, 1, 2, 7, 42, 99, 1234} {
		w := NewWorld(seed)
		for i := range 20 {
			p, err := w.AddPlayer("p")
			if err != nil {
				t.Fatalf("seed %d add %d: %v", seed, i, err)
			}
			if insideAnyWall(p.X, p.Y, PlayerRadius) {
				t.Fatalf("seed %d: player %d spawned inside a wall at (%.1f, %.1f)", seed, i, p.X, p.Y)
			}
		}
	}
}

// TestWallCollisionDeterminism: два мира, игроки давят в стены одной лентой, идут
// к байт-в-байт равному Checksum. Стережёт, что resolveWalls не привносит
// недетерминизма (фикс. порядок стен, чистая float32-арифметика). Проба в конце
// сверяется с гранью стены — лента реально упражняет коллизию, а не молчит.
func TestWallCollisionDeterminism(t *testing.T) {
	build := func() (*World, *Player) {
		w := NewWorld(5)
		probe, _ := w.AddPlayer("probe")
		other, _ := w.AddPlayer("other")
		place(probe, 1400, 1700) // упрётся в левый столб
		place(other, 2100, 1700) // упрётся в правый столб {2200,...}
		return w, probe
	}
	wa, probeA := build()
	wb, _ := build()

	for tick := range 200 {
		seq := uint32(tick + 1)
		in := protocol.Input{Seq: seq, Buttons: protocol.BtnRight}
		wa.EnqueueInput(1, in)
		wa.EnqueueInput(2, in)
		wb.EnqueueInput(1, in)
		wb.EnqueueInput(2, in)
		wa.Step(1.0 / 30)
		wb.Step(1.0 / 30)
		if wa.Checksum() != wb.Checksum() {
			t.Fatalf("wall-collision divergence at tick %d", tick)
		}
	}
	if want := wallXLeft(walls[0]); math.Abs(float64(probeA.X-want)) > 0.01 {
		t.Fatalf("tape did not exercise the wall: probe x=%.3f, want %.3f", probeA.X, want)
	}
}
