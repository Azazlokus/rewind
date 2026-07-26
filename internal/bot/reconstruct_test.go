package bot

import (
	"testing"

	"arena/internal/protocol"
)

// entMap сворачивает набор в id->entity для сравнения без учёта порядка.
func entMap(ents []protocol.Entity) map[uint16]protocol.Entity {
	m := make(map[uint16]protocol.Entity, len(ents))
	for _, e := range ents {
		m[e.ID] = e
	}
	return m
}

// TestReconstructFullThenDelta проверяет базовый путь: полный снапшот, затем
// дельта против него (изменение + удаление + новая сущность).
func TestReconstructFullThenDelta(t *testing.T) {
	rc := newReconstructor()

	full := protocol.Snapshot{
		Tick: 5, BaseTick: 0,
		Entities: []protocol.Entity{
			{ID: 1, Kind: protocol.KindPlayer, X: 10, Y: 10, HP: 100},
			{ID: 2, Kind: protocol.KindPlayer, X: 20, Y: 20, HP: 80},
		},
	}
	got, ok := rc.apply(&full)
	if !ok {
		t.Fatal("full snapshot must reconstruct")
	}
	if len(got) != 2 {
		t.Fatalf("full: got %d entities, want 2", len(got))
	}
	if rc.ents == nil || rc.storeLen() != 1 {
		t.Fatalf("store should hold tick 5, len=%d", rc.storeLen())
	}

	// Дельта против тика 5 (field-level, итерация 9): id1 сдвинулся только по X
	// (mask=FieldX), id2 ушёл, id3 появился целиком (FieldAll). Отсутствующие в маске
	// поля id1 в записи намеренно испорчены (HP:7) — реконструкция обязана взять их
	// из базы (HP:100), а не с провода.
	delta := protocol.Snapshot{
		Tick: 7, BaseTick: 5,
		Entities: []protocol.Entity{
			{ID: 1, X: 15, HP: 7},
			{ID: 3, Kind: protocol.KindProjectile, X: 99, Y: 99, HP: 1},
		},
		Masks:   []uint8{protocol.FieldX, protocol.FieldAll},
		Removed: []uint16{2},
	}
	got, ok = rc.apply(&delta)
	if !ok {
		t.Fatal("delta must reconstruct against known base")
	}
	m := entMap(got)
	if len(m) != 2 {
		t.Fatalf("delta: got %d entities, want 2 (id1,id3)", len(m))
	}
	if e := m[1]; e.X != 15 {
		t.Fatalf("id1 X: got %.1f, want 15", e.X)
	}
	// Поля вне маски (HP, Kind) взяты из базы, не с провода.
	if e := m[1]; e.HP != 100 {
		t.Fatalf("id1 HP (absent in delta) must come from base: got %d, want 100", e.HP)
	}
	if e := m[1]; e.Kind != protocol.KindPlayer {
		t.Fatalf("id1 Kind (absent in delta) must come from base: got %d, want Player", e.Kind)
	}
	if _, has := m[2]; has {
		t.Fatal("id2 must be removed")
	}
	if _, has := m[3]; !has {
		t.Fatal("id3 must be added")
	}
	// База (тик 5) не должна быть испорчена дельтой — id2 всё ещё в ней.
	if base := rc.store[5]; len(base) != 2 {
		t.Fatalf("base tick 5 mutated: len=%d, want 2", len(base))
	}
}

// TestReconstructMissingBase: дельта против неизвестной базы не реконструируется.
func TestReconstructMissingBase(t *testing.T) {
	rc := newReconstructor()
	delta := protocol.Snapshot{Tick: 9, BaseTick: 4, Entities: []protocol.Entity{{ID: 1}}}
	if _, ok := rc.apply(&delta); ok {
		t.Fatal("delta against unknown base must fail to reconstruct")
	}
}

// TestReconstructEvictsOldBases проверяет, что store не растёт без предела и
// вытесняет старейшие базы (кольцо reconKeep).
func TestReconstructEvictsOldBases(t *testing.T) {
	rc := newReconstructor()
	for tick := uint32(1); tick <= reconKeep+10; tick++ {
		s := protocol.Snapshot{Tick: tick, BaseTick: 0, Entities: []protocol.Entity{{ID: 1, X: float32(tick)}}}
		if _, ok := rc.apply(&s); !ok {
			t.Fatalf("tick %d must reconstruct", tick)
		}
	}
	if rc.storeLen() > reconKeep {
		t.Fatalf("store grew to %d, want <= %d", rc.storeLen(), reconKeep)
	}
	// Старейшие вытеснены, свежие — на месте.
	if _, has := rc.store[1]; has {
		t.Fatal("oldest base (tick 1) should be evicted")
	}
	if _, has := rc.store[reconKeep+10]; !has {
		t.Fatal("newest base must be retained")
	}
}

// storeLen — размер хранилища баз (тест-хелпер).
func (rc *reconstructor) storeLen() int { return len(rc.store) }
