package game

import "math"

// Статичные препятствия (итерация 10).
//
// Стена — выровненная по осям коробка (AABB) в мировых координатах. Раскладка
// ФИКСИРОВАНА и зеркалится клиентом (web/game.js: WALLS), потому что предсказание
// своего игрока обязано повторять коллизию тик-в-тик — иначе позиция задрейфит и
// реконсиляция будет дёргать картинку. Игрок сталкивается со стеной как круг vs
// AABB, снаряд — как отрезок vs AABB.
//
// Стены статичны и одинаковы во всех мирах, поэтому в Checksum они НЕ входят: в
// хэш попадает уже исправленная коллизией позиция игрока (X/Y) и факт удаления
// снаряда (w.projectiles) — само́ следствие, а не сами препятствия.

// wall — статичное препятствие, коробка [minX,minY]×[maxX,maxY].
type wall struct{ minX, minY, maxX, maxY float32 }

// walls — фиксированная раскладка арены. Обход всегда идёт по индексу (срез, не
// map), поэтому порядок разрешения коллизий детерминирован. Раскладка выбрана так,
// чтобы оставить свободными зоны, на которые опираются тесты (полоса боя вокруг
// y≈1000, диагональ к верхне-левому углу, окрестности точек спавна). Значения
// обязаны совпадать с WALLS в web/game.js.
var walls = []wall{
	{1500, 1500, 1620, 1900}, // левый центральный столб
	{2200, 1400, 2320, 2000}, // правый центральный столб
	{1600, 2300, 2400, 2420}, // нижняя перекладина
	{2600, 1900, 3100, 2020}, // правая перекладина
}

const (
	// spawnTries — сколько раз перебросить точку спавна, если она угодила в стену.
	spawnTries = 16
	// spawnClearance — свободный зазор вокруг игрока при спавне сверх его радиуса.
	// Даёт свежему игроку место, чтобы сразу двинуться, а не упереться в стену.
	spawnClearance = 48
)

// resolveWalls выталкивает круг радиуса r с центром (cx,cy) из всех стен, с
// которыми он пересекается, и возвращает исправленный центр. Стены обходятся в
// фиксированном порядке; за тик игрок сдвигается на единицы юнитов (стены толще
// шага), поэтому одного прохода достаточно — остаток исправит следующий тик.
// Зеркалится в web/game.js (resolveWalls); держать идентичным.
func resolveWalls(cx, cy, r float32) (float32, float32) {
	for i := range walls {
		cx, cy = resolveWall(cx, cy, r, &walls[i])
	}
	return cx, cy
}

// resolveWall выталкивает круг из одной стены. Обычный случай — центр снаружи
// коробки: сдвигаем по нормали от ближайшей точки коробки. Вырожденный — центр
// внутри (туннелирование, за тик почти не случается): выталкиваем по оси
// наименьшего проникновения. Обрабатываем и его ради устойчивости и совпадения с
// клиентом.
func resolveWall(cx, cy, r float32, wl *wall) (float32, float32) {
	// Ближайшая к центру круга точка коробки.
	qx := clamp(cx, wl.minX, wl.maxX)
	qy := clamp(cy, wl.minY, wl.maxY)
	dx, dy := cx-qx, cy-qy
	d2 := dx*dx + dy*dy
	if d2 >= r*r {
		return cx, cy // круг не касается стены
	}
	if d2 > 0 {
		// Центр снаружи: толкаем наружу по нормали (от ближайшей точки).
		d := float32(math.Sqrt(float64(d2)))
		push := r - d
		return cx + dx/d*push, cy + dy/d*push
	}
	// Центр внутри коробки: выходим через ближайшую грань.
	left := cx - wl.minX
	right := wl.maxX - cx
	top := cy - wl.minY
	bottom := wl.maxY - cy
	m := left
	if right < m {
		m = right
	}
	if top < m {
		m = top
	}
	if bottom < m {
		m = bottom
	}
	switch m {
	case left:
		return wl.minX - r, cy
	case right:
		return wl.maxX + r, cy
	case top:
		return cx, wl.minY - r
	default:
		return cx, wl.maxY + r
	}
}

// insideAnyWall сообщает, пересекает ли круг (cx,cy,r) хоть одну стену. Служит
// фильтром точек спавна: свежий игрок не должен появиться внутри препятствия.
func insideAnyWall(cx, cy, r float32) bool {
	for i := range walls {
		wl := &walls[i]
		qx := clamp(cx, wl.minX, wl.maxX)
		qy := clamp(cy, wl.minY, wl.maxY)
		dx, dy := cx-qx, cy-qy
		if dx*dx+dy*dy < r*r {
			return true
		}
	}
	return false
}

// segmentWallHit сообщает, пересекает ли отрезок A(ax,ay)→B(bx,by) какую-либо
// стену (расширенную на радиус снаряда), и возвращает точку ВХОДА — ближайшую к A
// точку самой ранней задетой стены. На этом стоит коллизия снаряда: задевший стену
// снаряд гибнет, а сегмент проверки попадания подрезается до этой точки, чтобы
// цель за стеной урона не получала, а перед стеной — получала. Только сервер
// (снаряды не предсказываются), поэтому в web/game.js не зеркалится.
func segmentWallHit(ax, ay, bx, by float32) (bool, float32, float32) {
	const r = ProjectileRadius
	dx, dy := bx-ax, by-ay
	bestT := float32(math.MaxFloat32)
	hit := false
	for i := range walls {
		wl := &walls[i]
		if t, ok := segAABBEntry(ax, ay, dx, dy, wl.minX-r, wl.minY-r, wl.maxX+r, wl.maxY+r); ok && t < bestT {
			bestT = t
			hit = true
		}
	}
	if !hit {
		return false, 0, 0
	}
	return true, ax + dx*bestT, ay + dy*bestT
}

// segAABBEntry — метод слэбов: параметр входа t∈[0,1] отрезка (ax,ay)+t·(dx,dy) в
// коробку [minX,minY]×[maxX,maxY]. ok=false, если пересечения на отрезке нет.
// Начало внутри коробки даёт t=0.
func segAABBEntry(ax, ay, dx, dy, minX, minY, maxX, maxY float32) (float32, bool) {
	tmin, tmax := float32(0), float32(1)
	if dx != 0 {
		t1 := (minX - ax) / dx
		t2 := (maxX - ax) / dx
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		if t1 > tmin {
			tmin = t1
		}
		if t2 < tmax {
			tmax = t2
		}
	} else if ax < minX || ax > maxX {
		return 0, false
	}
	if dy != 0 {
		t1 := (minY - ay) / dy
		t2 := (maxY - ay) / dy
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		if t1 > tmin {
			tmin = t1
		}
		if t2 < tmax {
			tmax = t2
		}
	} else if ay < minY || ay > maxY {
		return 0, false
	}
	if tmin > tmax {
		return 0, false
	}
	return tmin, true
}
