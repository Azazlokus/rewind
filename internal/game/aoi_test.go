package game

import (
	"testing"

	"arena/internal/protocol"
)

func maxF(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

// TestBroadcastAOICullsFarEntities: при включённом interest management зритель
// получает себя, но не далёкого игрока; при широком радиусе — обоих. Точки спавна
// детерминированы seed'ом, поэтому радиус подбираем, посчитав их в отдельном
// headless-мире с тем же seed и тем же порядком AddPlayer.
func TestBroadcastAOICullsFarEntities(t *testing.T) {
	const seed = 1
	probe := NewWorld(seed)
	pa, err := probe.AddPlayer("viewer")
	if err != nil {
		t.Fatal(err)
	}
	pb, err := probe.AddPlayer("other")
	if err != nil {
		t.Fatal(err)
	}
	// AOI-коробка — по метрике Чебышёва (max по осям); её и считаем.
	cheb := maxF(abs32(pa.X-pb.X), abs32(pa.Y-pb.Y))
	if cheb < 2 {
		t.Skipf("spawns too close for seed %d: cheb=%.2f", seed, cheb)
	}

	t.Run("narrow radius culls far player", func(t *testing.T) {
		tr := newTestRoom(t, Config{Seed: seed, AOIRadius: cheb - 1})
		viewer := tr.join("viewer")
		other := tr.join("other")
		waitForPlayers(t, tr, 2)

		// Позиции стабильны (никто не двигается), поэтому первый же снапшот зрителя
		// уже отражает отсечение по AOI.
		snap := tr.tickUntil(viewer, func(s protocol.Snapshot) bool { return hasEntity(s, viewer.id) })
		if hasEntity(snap, other.id) {
			t.Fatalf("far player %d must be culled from viewer AOI (cheb=%.1f, r=%.1f)",
				other.id, cheb, cheb-1)
		}
		if len(snap.Entities) != 1 {
			t.Fatalf("viewer AOI has %d entities, want 1 (self only)", len(snap.Entities))
		}
		if tr.room.Players() != 2 {
			t.Fatalf("room has %d players, want 2 (other alive, just out of view)", tr.room.Players())
		}
	})

	t.Run("wide radius includes both", func(t *testing.T) {
		tr := newTestRoom(t, Config{Seed: seed, AOIRadius: cheb + 1})
		viewer := tr.join("viewer")
		other := tr.join("other")
		waitForPlayers(t, tr, 2)

		snap := tr.tickUntil(viewer, func(s protocol.Snapshot) bool { return len(s.Entities) == 2 })
		if !hasEntity(snap, viewer.id) || !hasEntity(snap, other.id) {
			t.Fatalf("wide AOI must include both players, got %d entities", len(snap.Entities))
		}
	})
}
