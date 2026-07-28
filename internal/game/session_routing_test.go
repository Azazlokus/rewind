package game

import (
	"context"
	"testing"

	"arena/internal/transport"
)

// recordingConn — фейковый transport.Conn, запоминающий, каким путём (reliable
// Write или best-effort WriteUnreliable) ушло каждое сообщение. Read блокируется:
// эти тесты гоняют только исходящий путь.
type recordingConn struct {
	reliable   [][]byte
	unreliable [][]byte
}

func (c *recordingConn) Read(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *recordingConn) Write(ctx context.Context, msg []byte) error {
	c.reliable = append(c.reliable, append([]byte(nil), msg...))
	return nil
}

func (c *recordingConn) Close(string) error { return nil }
func (c *recordingConn) RemoteAddr() string { return "test" }

// unreliableConn добавляет best-effort путь — реализует transport.UnreliableWriter.
type unreliableConn struct {
	recordingConn
}

func (c *unreliableConn) WriteUnreliable(ctx context.Context, msg []byte) error {
	c.unreliable = append(c.unreliable, append([]byte(nil), msg...))
	return nil
}

var (
	_ transport.Conn             = (*recordingConn)(nil)
	_ transport.UnreliableWriter = (*unreliableConn)(nil)
)

// TestSnapshotRoutedUnreliably: когда транспорт умеет best-effort доставку, снапшот
// уходит через WriteUnreliable, а не reliable Write (итерация 12).
func TestSnapshotRoutedUnreliably(t *testing.T) {
	r := NewRoom("t", Config{SessionQueue: 4})
	conn := &unreliableConn{}
	s := newSession(r, 1, "x", conn)

	if err := s.sendSnapshot(context.Background(), []byte("snap")); err != nil {
		t.Fatalf("send snapshot: %v", err)
	}
	if len(conn.unreliable) != 1 || string(conn.unreliable[0]) != "snap" {
		t.Fatalf("snapshot must go via WriteUnreliable, got unreliable=%v reliable=%v", conn.unreliable, conn.reliable)
	}
	if len(conn.reliable) != 0 {
		t.Fatalf("snapshot must not go via reliable Write, got %v", conn.reliable)
	}
}

// TestSnapshotFallsBackToReliable: транспорт без best-effort режима (WebSocket/Pipe)
// шлёт снапшот обычным Write — прежнее поведение, без регресса.
func TestSnapshotFallsBackToReliable(t *testing.T) {
	r := NewRoom("t", Config{SessionQueue: 4})
	conn := &recordingConn{}
	s := newSession(r, 2, "y", conn)

	if err := s.sendSnapshot(context.Background(), []byte("snap")); err != nil {
		t.Fatalf("send snapshot: %v", err)
	}
	if len(conn.reliable) != 1 || string(conn.reliable[0]) != "snap" {
		t.Fatalf("snapshot must fall back to reliable Write, got %v", conn.reliable)
	}
}
