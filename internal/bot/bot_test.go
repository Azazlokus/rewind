package bot

import (
	"context"
	"testing"

	"arena/internal/protocol"
	"arena/internal/transport"
)

// fullSnap кодирует полный снапшот (BaseTick=0) с одним игроком — заготовка для теста.
func fullSnap(t *testing.T, tick uint32, x float32) []byte {
	t.Helper()
	b, err := protocol.AppendSnapshot(nil, &protocol.Snapshot{
		Tick: tick, BaseTick: 0,
		Entities: []protocol.Entity{{ID: 1, Kind: protocol.KindPlayer, X: x, Y: 1, HP: 100}},
	})
	if err != nil {
		t.Fatalf("encode snapshot tick %d: %v", tick, err)
	}
	return b
}

// TestBotSkipsStaleSnapshot: когда снапшоты приходят не по порядку (как на unreliable
// WebRTC-канале), бот пропускает устаревший тик и не откатывает ack — зеркалит защиту
// web/game.js. Сервер шлёт тик 10, затем устаревший тик 8, затем свежий тик 11: второй
// ReadSnapshot обязан вернуть 11, а не 8.
func TestBotSkipsStaleSnapshot(t *testing.T) {
	srv, cli := transport.Pipe(8)
	ctx := context.Background()

	ack, err := protocol.AppendJoinAck(nil, protocol.JoinAck{YourID: 1, Tick: 5})
	if err != nil {
		t.Fatalf("encode joinack: %v", err)
	}
	// Сервер: JoinAck, потом снапшоты 10, 8 (устаревший), 11. Буфера Pipe хватает,
	// поэтому пишем последовательно из этой же горутины до Attach/ReadSnapshot.
	for _, msg := range [][]byte{ack, fullSnap(t, 10, 1), fullSnap(t, 8, 2), fullSnap(t, 11, 3)} {
		if err := srv.Write(ctx, msg); err != nil {
			t.Fatalf("server write: %v", err)
		}
	}

	c, err := Attach(ctx, cli)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer func() { _ = c.Close() }()

	snap, err := c.ReadSnapshot(ctx)
	if err != nil {
		t.Fatalf("read snapshot 1: %v", err)
	}
	if snap.Tick != 10 {
		t.Fatalf("first snapshot tick = %d, want 10", snap.Tick)
	}

	// Устаревший тик 8 должен быть пропущен — следующий возвращённый снапшот это 11.
	snap, err = c.ReadSnapshot(ctx)
	if err != nil {
		t.Fatalf("read snapshot 2: %v", err)
	}
	if snap.Tick != 11 {
		t.Fatalf("stale tick 8 was not skipped: got tick %d, want 11", snap.Tick)
	}
	if c.ack.Load() != 11 {
		t.Fatalf("ack = %d, want 11 (must not regress to 8)", c.ack.Load())
	}
}
