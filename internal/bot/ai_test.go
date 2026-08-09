package bot

import (
	rand "math/rand/v2"
	"testing"

	"arena/internal/protocol"
)

// TestNavPathAvoidsWall: путь из точки слева от стены в точку справа огибает стену —
// ни одна точка пути не попадает в заблокированную клетку.
func TestNavPathAvoidsWall(t *testing.T) {
	nav := NewNav([]Rect{{MinX: 1500, MinY: 1500, MaxX: 1620, MaxY: 1900}})
	path := nav.Path(1400, 1700, 1700, 1700) // слева направо сквозь стену
	if len(path) == 0 {
		t.Fatal("no path found around the wall")
	}
	for i, wp := range path[:len(path)-1] { // последняя точка — точная позиция цели
		cx, cy := nav.cellAt(wp[0], wp[1])
		if nav.isBlocked(cx, cy) {
			t.Fatalf("waypoint %d (%.0f,%.0f) is inside a blocked cell", i, wp[0], wp[1])
		}
	}
	// Путь обязан отклониться от прямой (y=1700) — иначе он прошёл бы сквозь стену.
	straight := true
	for _, wp := range path[:len(path)-1] {
		if wp[1] < 1600 || wp[1] > 1800 {
			straight = false
			break
		}
	}
	if straight {
		t.Fatal("path did not deviate to go around the wall")
	}
}

// TestNavPathNoObstacles: без препятствий путь находится и ведёт к цели.
func TestNavPathNoObstacles(t *testing.T) {
	nav := NewNav(nil)
	path := nav.Path(500, 500, 3500, 3500)
	if len(path) == 0 {
		t.Fatal("expected a path across an empty map")
	}
	last := path[len(path)-1]
	if last[0] != 3500 || last[1] != 3500 {
		t.Fatalf("path should end exactly at the target, got (%.0f,%.0f)", last[0], last[1])
	}
}

// TestDirToButtons: вектор направления даёт верную 8-направленную маску.
func TestDirToButtons(t *testing.T) {
	cases := []struct {
		dx, dy float32
		want   uint8
	}{
		{1, 0, protocol.BtnRight},
		{-1, 0, protocol.BtnLeft},
		{0, 1, protocol.BtnDown}, // +Y вниз
		{0, -1, protocol.BtnUp},  // −Y вверх
		{1, 1, protocol.BtnRight | protocol.BtnDown},
		{-1, -1, protocol.BtnLeft | protocol.BtnUp},
		{0, 0, 0}, // нулевой вектор — стоим
	}
	for _, c := range cases {
		if got := dirToButtons(c.dx, c.dy); got != c.want {
			t.Fatalf("dirToButtons(%.0f,%.0f)=%08b, want %08b", c.dx, c.dy, got, c.want)
		}
	}
}

// TestBrainChasesAndFires: видя врага справа в пределах дальности, бот движется к нему
// и стреляет, целясь в его сторону.
func TestBrainChasesAndFires(t *testing.T) {
	b := NewBrain(NewNav(nil))
	b.view.Store(&View{SelfX: 2048, SelfY: 2048, SelfAlive: true, Enemies: []Enemy{{X: 2400, Y: 2048}}})
	buttons, aim := b.think(rand.New(rand.NewPCG(1, 1)))
	if buttons&protocol.BtnRight == 0 {
		t.Fatalf("bot should move right toward the enemy, buttons=%08b", buttons)
	}
	if buttons&protocol.BtnFire == 0 {
		t.Fatal("bot should fire at an enemy in range")
	}
	if aim != protocol.AimFromRadians(0) { // враг ровно справа → угол 0
		t.Fatalf("aim=%d, want %d (straight right)", aim, protocol.AimFromRadians(0))
	}
}

// TestBrainHoldsFireOutOfRange: далёкий враг — бот преследует, но не стреляет.
func TestBrainHoldsFireOutOfRange(t *testing.T) {
	b := NewBrain(NewNav(nil))
	b.view.Store(&View{SelfX: 500, SelfY: 500, SelfAlive: true, Enemies: []Enemy{{X: 3000, Y: 500}}})
	buttons, _ := b.think(rand.New(rand.NewPCG(1, 1)))
	if buttons&protocol.BtnFire != 0 {
		t.Fatal("bot should not fire at an out-of-range enemy")
	}
	if buttons&protocol.BtnRight == 0 {
		t.Fatalf("bot should still chase toward the enemy, buttons=%08b", buttons)
	}
}

// TestBrainIdleWhenDead: без своей сущности (мертвы/нет данных) бот стоит.
func TestBrainIdleWhenDead(t *testing.T) {
	b := NewBrain(NewNav(nil))
	b.view.Store(&View{SelfAlive: false, Enemies: []Enemy{{X: 100, Y: 100}}})
	if buttons, _ := b.think(rand.New(rand.NewPCG(1, 1))); buttons != 0 {
		t.Fatalf("dead bot should send no movement, buttons=%08b", buttons)
	}
}
