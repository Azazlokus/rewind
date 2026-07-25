package game

import "arena/internal/protocol"

// Пространственная сетка для interest management (итерация 6).
//
// aoiCellSize — сторона клетки сетки в мировых юнитах; MapSize кратна ей
// (4096/256 = 16), поэтому клеток ровно aoiCols на сторону.
const (
	aoiCellSize = 256
	aoiCols     = protocol.MapSize / aoiCellSize // 16
	aoiNumCells = aoiCols * aoiCols              // 256
)

// aoiGrid — равномерная пространственная сетка над картой.
//
// Это networking-структура, а не состояние симуляции: комнатная горутина строит
// её из позиций сущностей после Step и запрашивает окрестность каждого зрителя,
// чтобы слать ему только близкие сущности. В Checksum сетка НЕ входит — это
// производный индекс уже-хешируемых позиций, а не самостоятельное состояние (два
// равных мира дают равные сетки). Клетки — переиспользуемые срезы: после прогрева
// build/queryBox не аллоцируют.
type aoiGrid struct {
	// cells[cy*aoiCols+cx] держит индексы сущностей (в исходном срезе снапшота),
	// попавших в клетку. Сущность — точка, поэтому лежит ровно в одной клетке.
	cells [aoiNumCells][]int32
}

// cellCoord переводит координату в индекс клетки, зажимая в [0, aoiCols).
// Позиции игроков зажаты в границы карты, снаряды за границей удаляются, поэтому
// клампа хватает и как страховки от значения ровно на дальней кромке.
func cellCoord(v float32) int {
	c := int(v) / aoiCellSize
	if c < 0 {
		return 0
	}
	if c >= aoiCols {
		return aoiCols - 1
	}
	return c
}

// build раскладывает сущности ents по клеткам. Индексы в клетках указывают в ents,
// поэтому срез должен жить, пока по сетке идут запросы (в пределах одного тика).
func (g *aoiGrid) build(ents []protocol.Entity) {
	for i := range g.cells {
		g.cells[i] = g.cells[i][:0]
	}
	for i := range ents {
		cx := cellCoord(ents[i].X)
		cy := cellCoord(ents[i].Y)
		g.cells[cy*aoiCols+cx] = append(g.cells[cy*aoiCols+cx], int32(i))
	}
}

// queryBox дописывает в dst индексы сущностей всех клеток, пересекающих коробку
// [px-r, px+r] × [py-r, py+r], и возвращает расширенный срез. Это широкая фаза:
// вернувшиеся кандидаты — целые клетки, вызывающий уточняет попадание в коробку
// сам. dst передаётся переиспользуемым (обычно dst[:0]).
func (g *aoiGrid) queryBox(px, py, r float32, dst []int32) []int32 {
	x0, x1 := cellCoord(px-r), cellCoord(px+r)
	y0, y1 := cellCoord(py-r), cellCoord(py+r)
	for cy := y0; cy <= y1; cy++ {
		row := cy * aoiCols
		for cx := x0; cx <= x1; cx++ {
			dst = append(dst, g.cells[row+cx]...)
		}
	}
	return dst
}

// abs32 — модуль float32 для точного фильтра коробки AOI.
func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
