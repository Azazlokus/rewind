package game

import (
	"slices"
	"testing"

	"arena/internal/protocol"
)

// queryPrecise повторяет то, что делает broadcastAOI: широкая фаза по сетке плюс
// точный фильтр коробки. Возвращает id попавших сущностей по возрастанию.
func queryPrecise(g *aoiGrid, ents []protocol.Entity, px, py, r float32) []uint16 {
	cand := g.queryBox(px, py, r, nil)
	var ids []uint16
	for _, idx := range cand {
		e := ents[idx]
		if abs32(e.X-px) <= r && abs32(e.Y-py) <= r {
			ids = append(ids, e.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// TestGridQuerySelectsBox проверяет, что сетка отдаёт сущности внутри коробки и
// не отдаёт дальние.
func TestGridQuerySelectsBox(t *testing.T) {
	ents := []protocol.Entity{
		{ID: 1, X: 100, Y: 100}, // клетка (0,0)
		{ID: 2, X: 300, Y: 300}, // клетка (1,1)
		{ID: 3, X: 2000, Y: 2000},
	}
	var g aoiGrid
	g.build(ents)

	// Тесная коробка вокруг первой — только она.
	if got := queryPrecise(&g, ents, 100, 100, 50); !slices.Equal(got, []uint16{1}) {
		t.Fatalf("tight box: got %v, want [1]", got)
	}
	// Коробка, накрывающая первых двух, но не третью.
	if got := queryPrecise(&g, ents, 200, 200, 200); !slices.Equal(got, []uint16{1, 2}) {
		t.Fatalf("wide box: got %v, want [1 2]", got)
	}
	// Далёкая точка — пусто.
	if got := queryPrecise(&g, ents, 4000, 200, 100); len(got) != 0 {
		t.Fatalf("far box: got %v, want empty", got)
	}
}

// TestGridPreciseFilterRejectsCellNeighbours проверяет, что точный фильтр режет
// сущность, попавшую в клетку широкой фазы, но лежащую вне коробки. Сущность на
// (300,100) в клетке (1,0); запрос вокруг (250,100) r=40 захватывает клетку 1, но
// |300-250|=50 > 40 — сущность обязана отсеяться.
func TestGridPreciseFilterRejectsCellNeighbours(t *testing.T) {
	ents := []protocol.Entity{{ID: 7, X: 300, Y: 100}}
	var g aoiGrid
	g.build(ents)

	// Широкая фаза её вернёт (одна клетка), точный фильтр — нет.
	if raw := g.queryBox(250, 100, 40, nil); len(raw) == 0 {
		t.Fatal("broad phase should return the cell candidate")
	}
	if got := queryPrecise(&g, ents, 250, 100, 40); len(got) != 0 {
		t.Fatalf("precise filter: got %v, want empty", got)
	}
	// Расширяем коробку — теперь попадает.
	if got := queryPrecise(&g, ents, 250, 100, 60); !slices.Equal(got, []uint16{7}) {
		t.Fatalf("widened box: got %v, want [7]", got)
	}
}

// TestGridEdgeClamp проверяет кламп координат на дальней кромке карты.
func TestGridEdgeClamp(t *testing.T) {
	if c := cellCoord(protocol.MapSize); c != aoiCols-1 {
		t.Fatalf("cellCoord(MapSize)=%d, want %d", c, aoiCols-1)
	}
	if c := cellCoord(-10); c != 0 {
		t.Fatalf("cellCoord(-10)=%d, want 0", c)
	}
	ents := []protocol.Entity{{ID: 9, X: protocol.MapSize - 1, Y: protocol.MapSize - 1}}
	var g aoiGrid
	g.build(ents)
	if got := queryPrecise(&g, ents, protocol.MapSize-1, protocol.MapSize-1, 32); !slices.Equal(got, []uint16{9}) {
		t.Fatalf("corner entity: got %v, want [9]", got)
	}
}

// TestGridRebuildClearsCells проверяет, что build очищает клетки: сущность из
// прошлого кадра не «залипает».
func TestGridRebuildClearsCells(t *testing.T) {
	var g aoiGrid
	g.build([]protocol.Entity{{ID: 1, X: 100, Y: 100}})
	next := []protocol.Entity{{ID: 2, X: 2000, Y: 2000}}
	g.build(next)
	if got := queryPrecise(&g, next, 100, 100, 50); len(got) != 0 {
		t.Fatalf("stale entity after rebuild: got %v, want empty", got)
	}
	if got := queryPrecise(&g, next, 2000, 2000, 50); !slices.Equal(got, []uint16{2}) {
		t.Fatalf("current entity: got %v, want [2]", got)
	}
}
