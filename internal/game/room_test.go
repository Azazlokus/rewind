package game

import (
	"context"
	"errors"
	"testing"
	"time"

	"arena/internal/protocol"
	"arena/internal/transport"
)

// testRoom is a headless room: real sessions over in-memory pipes, driven by a
// manual clock. No network, no sleeps, fully deterministic.
type testRoom struct {
	t        *testing.T
	room     *Room
	clock    *ManualClock
	interval time.Duration
	cancel   context.CancelFunc
	ctx      context.Context
}

func newTestRoom(t *testing.T, cfg Config) *testRoom {
	t.Helper()
	clock := NewManualClock(time.Time{})
	cfg.Clock = clock
	if cfg.TickRate == 0 {
		cfg.TickRate = 30
	}
	room := NewRoom("test", cfg)

	ctx, cancel := context.WithCancel(context.Background())
	go room.Run(ctx)

	tr := &testRoom{
		t: t, room: room, clock: clock,
		interval: room.cfg.TickInterval(), cancel: cancel, ctx: ctx,
	}
	t.Cleanup(tr.stop)
	return tr
}

func (tr *testRoom) stop() {
	tr.cancel()
	select {
	case <-tr.room.Done():
	case <-time.After(2 * time.Second):
		tr.t.Fatal("room did not stop within 2s")
	}
}

// tick advances the simulation by n ticks.
func (tr *testRoom) tick(n int) {
	tr.clock.AdvanceTicks(n, tr.interval)
}

// pump runs a blocking room call (Join, State) while stepping the clock so the
// loop can process it. It fails the test rather than hanging.
func pump[T any](tr *testRoom, call func() (T, error)) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := call()
		ch <- result{v, err}
	}()
	for range 10000 {
		select {
		case r := <-ch:
			return r.v, r.err
		default:
			tr.clock.Advance(tr.interval)
		}
	}
	tr.t.Fatal("pump: room call did not return within 10000 ticks")
	var zero T
	return zero, nil
}

// tickUntil advances the clock in the background while reading snapshots from c,
// and returns the first snapshot that satisfies pred. This is how tests observe
// the world without racing the asynchronous write pump: snapshots are a stream,
// and the test waits for the one it expects rather than a specific buffered slot.
func (tr *testRoom) tickUntil(c *client, pred func(protocol.Snapshot) bool) protocol.Snapshot {
	tr.t.Helper()
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				tr.clock.Advance(tr.interval)
			}
		}
	}()
	defer close(stop)

	for range 5000 {
		msg := c.read()
		if msg.Type == protocol.MsgSnapshot && pred(msg.Snapshot) {
			return msg.Snapshot
		}
	}
	tr.t.Fatal("tickUntil: predicate never satisfied")
	return protocol.Snapshot{}
}

// client is the client end of a joined session.
type client struct {
	t    *testing.T
	conn transport.Conn
	ctx  context.Context
	id   PlayerID
}

// join connects a new client and completes the handshake, returning the client
// end. The session pumps run in the background for the test's lifetime.
func (tr *testRoom) join(name string) *client {
	tr.t.Helper()
	server, clientConn := transport.Pipe(64)
	sess, err := pump(tr, func() (*Session, error) {
		return tr.room.Join(tr.ctx, server, name)
	})
	if err != nil {
		tr.t.Fatalf("join %q: %v", name, err)
	}
	go func() { _ = sess.Run(tr.ctx) }()

	c := &client{t: tr.t, conn: clientConn, ctx: tr.ctx, id: sess.ID()}
	// The first server message is the JoinAck.
	msg := c.read()
	if msg.Type != protocol.MsgJoinAck {
		tr.t.Fatalf("first message was %v, want JoinAck", msg.Type)
	}
	if PlayerID(msg.JoinAck.YourID) != sess.ID() {
		tr.t.Fatalf("ack id %d != session id %d", msg.JoinAck.YourID, sess.ID())
	}
	return c
}

func (c *client) read() protocol.ServerMessage {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.ctx, time.Second)
	defer cancel()
	data, err := c.conn.Read(ctx)
	if err != nil {
		c.t.Fatalf("client %d read: %v", c.id, err)
	}
	var msg protocol.ServerMessage
	if err := protocol.DecodeServer(data, &msg); err != nil {
		c.t.Fatalf("decode server message: %v", err)
	}
	return msg
}

func (c *client) send(msg []byte) {
	c.t.Helper()
	if err := c.conn.Write(c.ctx, msg); err != nil {
		c.t.Fatalf("client %d write: %v", c.id, err)
	}
}

func (c *client) sendInput(in protocol.Input) {
	buf, err := protocol.AppendInput(nil, in)
	if err != nil {
		c.t.Fatal(err)
	}
	c.send(buf)
}

// TestRoomJoinLeave checks players appear and disappear from the world through
// the inbox, with no direct world access.
func TestRoomJoinLeave(t *testing.T) {
	tr := newTestRoom(t, Config{})
	a := tr.join("alice")
	b := tr.join("bob")

	// Both players are visible.
	tr.tickUntil(b, func(s protocol.Snapshot) bool { return len(s.Entities) == 2 })

	// Alice disconnects; the room must release her.
	if err := a.conn.Close("bye"); err != nil {
		t.Fatal(err)
	}
	snap := tr.tickUntil(b, func(s protocol.Snapshot) bool { return len(s.Entities) == 1 })
	if PlayerID(snap.Entities[0].ID) != b.id {
		t.Fatalf("remaining entity is %d, want bob %d", snap.Entities[0].ID, b.id)
	}
	if tr.room.Players() != 1 {
		t.Fatalf("room reports %d players, want 1", tr.room.Players())
	}
}

// TestRoomInputMovesPlayer checks input flows inbox -> world -> snapshot and that
// acknowledgement advances.
func TestRoomInputMovesPlayer(t *testing.T) {
	tr := newTestRoom(t, Config{})
	c := tr.join("mover")

	before := tr.tickUntil(c, func(s protocol.Snapshot) bool { return hasEntity(s, c.id) })
	startX := entityByID(t, before, c.id).X

	// The world keeps applying the latest input each tick, so a single held
	// "right" command is enough to observe motion and acknowledgement.
	c.sendInput(protocol.Input{Seq: 7, Buttons: protocol.BtnRight})

	after := tr.tickUntil(c, func(s protocol.Snapshot) bool {
		e, ok := lookup(s, c.id)
		return ok && s.LastProcessedSeq >= 7 && e.X > startX
	})
	e := entityByID(t, after, c.id)
	if e.VX <= 0 {
		t.Fatalf("expected positive VX while moving right, got %.2f", e.VX)
	}
}

// TestRoomDropsSlowClient checks the loop disconnects a client that never reads
// its snapshots, instead of blocking on it.
func TestRoomDropsSlowClient(t *testing.T) {
	tr := newTestRoom(t, Config{SessionQueue: 2, MaxBacklog: 3})

	// A client that connects but never reads: its pipe fills, the write pump
	// wedges, its outbound queue fills, and the room must give up on it.
	server, _ := transport.Pipe(1)
	sess, err := pump(tr, func() (*Session, error) {
		return tr.room.Join(tr.ctx, server, "slowpoke")
	})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	go func() { _ = sess.Run(tr.ctx) }()
	waitForPlayers(t, tr, 1)

	// Enough ticks to overflow SessionQueue + MaxBacklog several times over.
	tr.tick(40)
	waitForPlayers(t, tr, 0)
}

// TestRoomFull checks capacity is enforced.
func TestRoomFull(t *testing.T) {
	tr := newTestRoom(t, Config{MaxPlayers: 1})
	tr.join("first")

	server, _ := transport.Pipe(8)
	_, err := pump(tr, func() (*Session, error) {
		return tr.room.Join(tr.ctx, server, "second")
	})
	if !errors.Is(err, ErrRoomFull) {
		t.Fatalf("expected ErrRoomFull, got %v", err)
	}
}

// TestGracefulShutdown checks cancelling the context stops the room after its
// current tick and releases sessions.
func TestGracefulShutdown(t *testing.T) {
	tr := newTestRoom(t, Config{})
	tr.join("a")
	tr.join("b")
	waitForPlayers(t, tr, 2)

	tr.cancel()
	select {
	case <-tr.room.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("room did not stop after context cancel")
	}
	if tr.room.Players() != 0 {
		t.Fatalf("players remained after shutdown: %d", tr.room.Players())
	}
}

// --- helpers ---------------------------------------------------------------

// waitForPlayers advances ticks until the room reports want players. Advancing
// the clock yields to the read-pump goroutines, so a just-closed connection's
// leave event is processed without any sleep.
func waitForPlayers(t *testing.T, tr *testRoom, want int) {
	t.Helper()
	for range 500 {
		if tr.room.Players() == want {
			return
		}
		tr.tick(1)
	}
	t.Fatalf("player count did not reach %d (still %d)", want, tr.room.Players())
}

func lookup(s protocol.Snapshot, id PlayerID) (protocol.Entity, bool) {
	for _, e := range s.Entities {
		if PlayerID(e.ID) == id {
			return e, true
		}
	}
	return protocol.Entity{}, false
}

func hasEntity(s protocol.Snapshot, id PlayerID) bool {
	_, ok := lookup(s, id)
	return ok
}

func entityByID(t *testing.T, s protocol.Snapshot, id PlayerID) protocol.Entity {
	t.Helper()
	e, ok := lookup(s, id)
	if !ok {
		t.Fatalf("entity %d not in snapshot", id)
	}
	return e
}
