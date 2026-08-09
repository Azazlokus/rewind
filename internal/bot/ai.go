package bot

import (
	"context"
	"math"
	rand "math/rand/v2"
	"sync/atomic"
	"time"

	"arena/internal/protocol"
)

// Умный ИИ бота (итерация 28): в отличие от простого случайного Autopilot (нагрузка),
// умный бот ВИДИТ мир из снапшотов, идёт к ближайшему врагу по A* вокруг стен и целится
// в него. Он остаётся обычным клиентом: симуляцию/провод не трогает, только читает
// снапшоты и шлёт вводы — поэтому живёт в пакете bot, а геометрию стен получает
// параметром (Rect), не завися от internal/game.
//
// Две горутины на бота, как у Drain/Autopilot: Observe читает снапшоты и публикует
// снимок мира (atomic.Pointer), Drive на 60 Гц читает снимок и рулит. Обе завершаются
// по отмене ctx или закрытию соединения — владелец тот же, что и раньше.

const (
	navCell     float32 = 128 // сторона клетки навигационной сетки, юниты
	navInflate  float32 = 22  // на сколько раздуть стены (≈ радиус игрока + зазор)
	fireRange   float32 = 680 // дальность, с которой бот открывает огонь
	reachDist   float32 = 64  // насколько близко к точке пути, чтобы считать её достигнутой
	repathTicks         = 30  // как часто пересчитывать путь (тиков ввода, ~0.5 с)
)

// Rect — препятствие-AABB для навигации (развязка от game.Obstacle).
type Rect struct{ MinX, MinY, MaxX, MaxY float32 }

// Enemy — видимый противник (позиция из снапшота).
type Enemy struct{ X, Y float32 }

// View — снимок мира глазами бота на последнем снапшоте. Публикуется Observe,
// читается Drive через atomic.Pointer (перекрёстные горутины).
type View struct {
	SelfX, SelfY float32
	SelfAlive    bool
	Enemies      []Enemy
}

// Nav — навигационная сетка над картой: клетка заблокирована, если пересекает
// раздутую стену. Строится один раз и делится (read-only) между всеми ботами.
type Nav struct {
	cols    int
	cell    float32
	blocked []bool
}

// NewNav строит сетку 32×32 (при карте 4096 и клетке 128), помечая заблокированными
// клетки, пересекающие любое препятствие, раздутое на navInflate (чтобы бот огибал
// стену с зазором под свой радиус).
func NewNav(obstacles []Rect) *Nav {
	cols := int(math.Ceil(float64(protocol.MapSize) / float64(navCell)))
	n := &Nav{cols: cols, cell: navCell, blocked: make([]bool, cols*cols)}
	for cy := 0; cy < cols; cy++ {
		for cx := 0; cx < cols; cx++ {
			x0, y0 := float32(cx)*navCell, float32(cy)*navCell
			x1, y1 := x0+navCell, y0+navCell
			for _, o := range obstacles {
				if x1 > o.MinX-navInflate && x0 < o.MaxX+navInflate &&
					y1 > o.MinY-navInflate && y0 < o.MaxY+navInflate {
					n.blocked[cy*cols+cx] = true
					break
				}
			}
		}
	}
	return n
}

func (n *Nav) inBounds(cx, cy int) bool  { return cx >= 0 && cy >= 0 && cx < n.cols && cy < n.cols }
func (n *Nav) idx(cx, cy int) int        { return cy*n.cols + cx }
func (n *Nav) isBlocked(cx, cy int) bool { return !n.inBounds(cx, cy) || n.blocked[n.idx(cx, cy)] }

func (n *Nav) cellAt(x, y float32) (int, int) {
	cx := int(x / n.cell)
	cy := int(y / n.cell)
	if cx < 0 {
		cx = 0
	} else if cx >= n.cols {
		cx = n.cols - 1
	}
	if cy < 0 {
		cy = 0
	} else if cy >= n.cols {
		cy = n.cols - 1
	}
	return cx, cy
}

func (n *Nav) center(cx, cy int) (float32, float32) {
	return (float32(cx) + 0.5) * n.cell, (float32(cy) + 0.5) * n.cell
}

// nearestFree возвращает ближайшую свободную клетку к (cx,cy) поиском по расширяющимся
// кольцам (если сама клетка занята — цель/старт внутри стены). При неудаче — исходную.
func (n *Nav) nearestFree(cx, cy int) (int, int) {
	if !n.isBlocked(cx, cy) {
		return cx, cy
	}
	for r := 1; r < n.cols; r++ {
		for dy := -r; dy <= r; dy++ {
			for dx := -r; dx <= r; dx++ {
				if dx > -r && dx < r && dy > -r && dy < r {
					continue // только рамка кольца
				}
				if !n.isBlocked(cx+dx, cy+dy) {
					return cx + dx, cy + dy
				}
			}
		}
	}
	return cx, cy
}

// randomFree возвращает центр случайной свободной клетки — цель для блуждания, когда
// врагов не видно.
func (n *Nav) randomFree(rng *rand.Rand) (float32, float32) {
	for range 32 {
		cx, cy := rng.IntN(n.cols), rng.IntN(n.cols)
		if !n.isBlocked(cx, cy) {
			return n.center(cx, cy)
		}
	}
	return protocol.MapSize / 2, protocol.MapSize / 2
}

// Path прокладывает путь из (sx,sy) в (tx,ty) по A* (8 связностей, без срезки углов
// сквозь стены) и возвращает точки-центры клеток плюс финальную точку цели. nil, если
// пути нет. Старт/цель во внутренностях стены сносятся на ближайшую свободную клетку.
func (n *Nav) Path(sx, sy, tx, ty float32) [][2]float32 {
	scx, scy := n.nearestFree(n.cellAt(sx, sy))
	tcx, tcy := n.nearestFree(n.cellAt(tx, ty))
	start, goal := n.idx(scx, scy), n.idx(tcx, tcy)
	if start == goal {
		return [][2]float32{{tx, ty}}
	}

	const inf = math.MaxInt32
	nCells := n.cols * n.cols
	gScore := make([]int32, nCells)
	came := make([]int32, nCells)
	for i := range gScore {
		gScore[i] = inf
		came[i] = -1
	}
	gScore[start] = 0
	h := &cellHeap{}
	h.push(int32(start), n.heuristic(scx, scy, tcx, tcy))

	// 8 направлений: орто (стоимость 10) и диагонали (14 ≈ 10*√2).
	dirs := [8][3]int{{1, 0, 10}, {-1, 0, 10}, {0, 1, 10}, {0, -1, 10},
		{1, 1, 14}, {1, -1, 14}, {-1, 1, 14}, {-1, -1, 14}}

	for h.len() > 0 {
		cur := int(h.pop())
		if cur == goal {
			return n.reconstruct(came, cur, tx, ty)
		}
		cx, cy := cur%n.cols, cur/n.cols
		for _, d := range dirs {
			nx, ny := cx+d[0], cy+d[1]
			if n.isBlocked(nx, ny) {
				continue
			}
			if d[0] != 0 && d[1] != 0 { // диагональ — не срезаем угол сквозь стену
				if n.isBlocked(cx+d[0], cy) || n.isBlocked(cx, cy+d[1]) {
					continue
				}
			}
			ni := n.idx(nx, ny)
			ng := gScore[cur] + int32(d[2])
			if ng < gScore[ni] {
				gScore[ni] = ng
				came[ni] = int32(cur)
				h.push(int32(ni), ng+n.heuristic(nx, ny, tcx, tcy))
			}
		}
	}
	return nil
}

// heuristic — октальное расстояние (масштаб 10/14, как стоимости шагов).
func (n *Nav) heuristic(cx, cy, tx, ty int) int32 {
	dx, dy := abs(cx-tx), abs(cy-ty)
	if dx > dy {
		return int32(14*dy + 10*(dx-dy))
	}
	return int32(14*dx + 10*(dy-dx))
}

func (n *Nav) reconstruct(came []int32, cur int, tx, ty float32) [][2]float32 {
	var cells []int
	for cur != -1 {
		cells = append(cells, cur)
		cur = int(came[cur])
	}
	// cells идут от цели к старту — разворачиваем и берём центры, старт пропускаем.
	out := make([][2]float32, 0, len(cells))
	for i := len(cells) - 2; i >= 0; i-- {
		cx, cy := cells[i]%n.cols, cells[i]/n.cols
		wx, wy := n.center(cx, cy)
		out = append(out, [2]float32{wx, wy})
	}
	if k := len(out); k > 0 { // последнюю точку заменяем на точную позицию цели
		out[k-1] = [2]float32{tx, ty}
	}
	return out
}

// Brain — состояние умного бота: общая сетка nav, публикуемый снимок view и локальные
// поля рулёжки (принадлежат горутине Drive).
type Brain struct {
	nav  *Nav
	view atomic.Pointer[View]

	path     [][2]float32
	pathIdx  int
	repathIn int
	aim      uint16
	roamX    float32
	roamY    float32
	haveRoam bool
}

// NewBrain создаёт мозг бота поверх общей навигационной сетки (nav делится между
// ботами — он read-only).
func NewBrain(nav *Nav) *Brain { return &Brain{nav: nav} }

// Observe читает снапшоты и публикует снимок мира, пока живо соединение (замена Drain
// для умного бота: тоже осушает снапшоты и подтверждает тики). Возвращается по закрытию
// соединения/отмене ctx.
func (b *Brain) Observe(ctx context.Context, c *Client) {
	for {
		snap, err := c.ReadSnapshot(ctx)
		if err != nil {
			return
		}
		v := &View{}
		for i := range snap.Entities {
			e := &snap.Entities[i]
			if e.Kind != protocol.KindPlayer {
				continue
			}
			if e.ID == c.ID() {
				v.SelfX, v.SelfY, v.SelfAlive = e.X, e.Y, true
				continue
			}
			v.Enemies = append(v.Enemies, Enemy{X: e.X, Y: e.Y})
		}
		b.view.Store(v)
	}
}

// Drive на 60 Гц читает снимок мира и шлёт рассчитанный ввод. Возвращается по отмене
// ctx или закрытию соединения; владелец — вызывающий (botfill).
func (b *Brain) Drive(ctx context.Context, c *Client, rng *rand.Rand) {
	tk := time.NewTicker(time.Second / 60)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			buttons, aim := b.think(rng)
			if err := c.SendInput(ctx, buttons, aim); err != nil {
				return
			}
		}
	}
}

// think — чистая логика одного шага: по текущему снимку выбирает цель (ближайший враг
// или точка блуждания), держит путь A*, рулит к следующей точке и целится/стреляет.
// Вынесена отдельно, чтобы тестировать без сети (подставив view).
func (b *Brain) think(rng *rand.Rand) (uint8, uint16) {
	v := b.view.Load()
	if v == nil || !v.SelfAlive {
		return 0, b.aim // мертвы/нет данных — стоим (сервер ввод всё равно игнорит)
	}

	// Цель: ближайший враг, иначе блуждание к случайной свободной клетке.
	var enemy *Enemy
	var goalX, goalY float32
	if len(v.Enemies) > 0 {
		enemy = nearestEnemy(v.SelfX, v.SelfY, v.Enemies)
		goalX, goalY = enemy.X, enemy.Y
		b.haveRoam = false
		b.aim = angleTo(v.SelfX, v.SelfY, enemy.X, enemy.Y)
	} else {
		if !b.haveRoam || dist2(v.SelfX, v.SelfY, b.roamX, b.roamY) < reachDist*reachDist {
			b.roamX, b.roamY = b.nav.randomFree(rng)
			b.haveRoam = true
			b.repathIn = 0
		}
		goalX, goalY = b.roamX, b.roamY
	}

	// Пересчёт пути изредка (или если пути нет).
	b.repathIn--
	if b.repathIn <= 0 || len(b.path) == 0 {
		b.path = b.nav.Path(v.SelfX, v.SelfY, goalX, goalY)
		b.pathIdx = 0
		b.repathIn = repathTicks
	}

	// Рулим к следующей точке пути (или прямо к цели, если путь пройден/пуст).
	tx, ty := goalX, goalY
	if b.pathIdx < len(b.path) {
		wp := b.path[b.pathIdx]
		if dist2(v.SelfX, v.SelfY, wp[0], wp[1]) < reachDist*reachDist {
			b.pathIdx++
		}
		if b.pathIdx < len(b.path) {
			tx, ty = b.path[b.pathIdx][0], b.path[b.pathIdx][1]
		}
	}
	buttons := dirToButtons(tx-v.SelfX, ty-v.SelfY)

	// Огонь по врагу в пределах дальности (мы уже смотрим на него). Стена на пути
	// погасит снаряд на сервере — промах безвреден.
	if enemy != nil && dist2(v.SelfX, v.SelfY, enemy.X, enemy.Y) < fireRange*fireRange {
		buttons |= protocol.BtnFire
	}
	return buttons, b.aim
}

// dirToButtons переводит вектор направления в 8-направленную маску WASD. Порог ≈
// cos(67.5°): ось включается, если её нормированная компонента заметна (иначе чистая
// диагональ/орто). BtnUp — это −Y (экранные координаты), как в game.Step.
func dirToButtons(dx, dy float32) uint8 {
	m := float32(math.Hypot(float64(dx), float64(dy)))
	if m < 1e-3 {
		return 0
	}
	nx, ny := dx/m, dy/m
	const t = 0.38
	var b uint8
	if nx > t {
		b |= protocol.BtnRight
	} else if nx < -t {
		b |= protocol.BtnLeft
	}
	if ny > t {
		b |= protocol.BtnDown
	} else if ny < -t {
		b |= protocol.BtnUp
	}
	return b
}

func nearestEnemy(x, y float32, es []Enemy) *Enemy {
	best := 0
	bestD := dist2(x, y, es[0].X, es[0].Y)
	for i := 1; i < len(es); i++ {
		if d := dist2(x, y, es[i].X, es[i].Y); d < bestD {
			bestD, best = d, i
		}
	}
	return &es[best]
}

func angleTo(x, y, tx, ty float32) uint16 {
	return protocol.AimFromRadians(math.Atan2(float64(ty-y), float64(tx-x)))
}

func dist2(ax, ay, bx, by float32) float32 {
	dx, dy := ax-bx, ay-by
	return dx*dx + dy*dy
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// cellHeap — минимальная двоичная куча клеток по f-оценке (для A*).
type cellHeap struct {
	cells []int32
	fs    []int32
}

func (h *cellHeap) len() int { return len(h.cells) }

func (h *cellHeap) push(cell, f int32) {
	h.cells = append(h.cells, cell)
	h.fs = append(h.fs, f)
	i := len(h.cells) - 1
	for i > 0 {
		p := (i - 1) / 2
		if h.fs[p] <= h.fs[i] {
			break
		}
		h.swap(i, p)
		i = p
	}
}

func (h *cellHeap) pop() int32 {
	top := h.cells[0]
	last := len(h.cells) - 1
	h.swap(0, last)
	h.cells = h.cells[:last]
	h.fs = h.fs[:last]
	i, m := 0, last
	for {
		l, r, s := 2*i+1, 2*i+2, i
		if l < m && h.fs[l] < h.fs[s] {
			s = l
		}
		if r < m && h.fs[r] < h.fs[s] {
			s = r
		}
		if s == i {
			break
		}
		h.swap(i, s)
		i = s
	}
	return top
}

func (h *cellHeap) swap(i, j int) {
	h.cells[i], h.cells[j] = h.cells[j], h.cells[i]
	h.fs[i], h.fs[j] = h.fs[j], h.fs[i]
}
