package game

import "testing"

// newTestSession строит сессию без сети и без запущенных pump'ов — достаточно для
// прямой проверки логики очередей. conn не нужен: enqueue-методы его не трогают.
func newTestSession(t *testing.T, sessionQueue int) *Session {
	t.Helper()
	r := NewRoom("t", Config{SessionQueue: sessionQueue})
	return newSession(r, 1, "x", nil)
}

// snap оборачивает строку в *[]byte — очередь снапшотов держит буферы из пула по
// указателю (итерация 6C).
func snap(str string) *[]byte {
	b := []byte(str)
	return &b
}

// TestReliableNotDroppedBySnapshots — суть техдолга, который чинит двухканальная
// очередь: давление снапшотов не должно вытеснять reliable-сообщение (JoinAck).
func TestReliableNotDroppedBySnapshots(t *testing.T) {
	s := newTestSession(t, 2)
	s.enqueueReliable([]byte("ack"))

	// Переполняем дропаемую очередь снапшотов много раз подряд.
	for range 10 {
		s.enqueueSnapshot(snap("snap"))
	}

	select {
	case m := <-s.reliable:
		if string(m) != "ack" {
			t.Fatalf("got reliable %q, want ack", m)
		}
	default:
		t.Fatal("reliable ack was dropped under snapshot pressure")
	}
}

// TestEnqueueSnapshotDropsOldest проверяет, что при полной очереди выкидывается
// самый старый снапшот, растёт backlog, а успешная постановка его сбрасывает.
func TestEnqueueSnapshotDropsOldest(t *testing.T) {
	s := newTestSession(t, 2)
	s.enqueueSnapshot(snap("a"))
	s.enqueueSnapshot(snap("b")) // очередь полна: [a, b]
	if s.dropped != 0 || s.backlog != 0 {
		t.Fatalf("unexpected early drop: dropped=%d backlog=%d", s.dropped, s.backlog)
	}

	s.enqueueSnapshot(snap("c")) // выкидывает a -> [b, c]
	if s.dropped != 1 || s.backlog != 1 {
		t.Fatalf("expected one drop: dropped=%d backlog=%d", s.dropped, s.backlog)
	}
	if got := string(*(<-s.snapshots)); got != "b" {
		t.Fatalf("oldest survivor is %q, want b", got)
	}

	// После освобождения места постановка снова удаётся и сбрасывает backlog.
	s.enqueueSnapshot(snap("d"))
	if s.backlog != 0 {
		t.Fatalf("backlog not reset after successful enqueue: %d", s.backlog)
	}
}

// TestReliableOverflowKicks проверяет, что переполнение reliable-очереди помечает
// сессию на удаление, а не роняет сообщение.
func TestReliableOverflowKicks(t *testing.T) {
	s := newTestSession(t, 2)
	for i := range reliableQueueSize {
		if !s.enqueueReliable([]byte("r")) {
			t.Fatalf("unexpected drop at %d within capacity %d", i, reliableQueueSize)
		}
	}
	if s.lagging(30) {
		t.Fatal("session flagged before reliable queue overflowed")
	}
	if s.enqueueReliable([]byte("overflow")) {
		t.Fatal("expected reliable overflow to fail rather than drop")
	}
	if !s.lagging(30) {
		t.Fatal("session not flagged after reliable overflow")
	}
}
